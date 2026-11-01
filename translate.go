package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Keyless Google Translate endpoint, same environment as the reference
// Electron app (Node fetch with a real Chrome UA).
const (
	translateEndpoint    = "https://translate.googleapis.com/translate_a/single"
	chromeUA             = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
	maxChunk             = 1200
	maxChunks            = 60
	maxURLLen            = 1800
	translateConcurrency = 3
)

// errRateLimited is returned for HTTP 429 so callers can distinguish a rate
// limit (retryable) from any other failure.
var errRateLimited = errors.New("Rate limited by Google. Wait a moment and try again.")

var translateHTTPClient = &http.Client{Timeout: 15 * time.Second}

// splitIntoChunks mirrors the reference splitIntoChunks: cut at a sentence
// boundary inside the first max runes, fall back to a space, then to a hard
// cut. Lengths are counted in runes so multibyte text is never split
// mid-rune.
//
// The input is converted to []rune exactly once and the loop indexes into
// that slice, so the cost is O(n) instead of the previous O(n^2) re-scan.
func splitIntoChunks(text string, limit int) []string {
	runes := []rune(text)
	n := len(runes)
	if n <= limit {
		return []string{text}
	}
	var chunks []string
	start := 0
	for n-start > limit {
		// window holds exactly `limit` runes; search delimiters as byte
		// offsets within that window (delimiters are ASCII, so offsets land
		// on rune boundaries).
		window := string(runes[start : start+limit])
		cut := max(
			strings.LastIndex(window, ". "),
			strings.LastIndex(window, "! "),
			strings.LastIndex(window, "? "),
			strings.LastIndex(window, "\n"),
		)
		if cut < limit/2 {
			cut = strings.LastIndex(window, " ")
		}
		end := limit // hard cut
		if cut > 0 {
			// cut points at the delimiter's first byte; +1 includes that
			// first char (e.g. "." or the newline) in this chunk.
			end = utf8.RuneCountInString(window[:cut]) + 1
		}
		if end <= 0 || end > limit {
			end = limit
		}
		if start+end > n {
			end = n - start
		}
		chunks = append(chunks, string(runes[start:start+end]))
		start += end
	}
	if start < n {
		chunks = append(chunks, string(runes[start:]))
	}
	return chunks
}

type chunkResult struct {
	translated string
	detected   string
}

// translateChunk fetches one chunk from the gtx endpoint. Short texts go out
// as GET (closest to the original behaviour); very long encoded URLs go out
// as a POST with the same parameters in the form body, because the endpoint
// accepts both and a huge query string is the failure mode we are avoiding.
func translateChunk(chunk, source, target string) (chunkResult, error) {
	var zero chunkResult

	q := url.Values{}
	q.Set("client", "gtx")
	if source == "auto" {
		q.Set("sl", "auto")
	} else {
		q.Set("sl", source)
	}
	q.Set("tl", target)
	q.Set("dt", "t")
	q.Set("ie", "UTF-8")
	q.Set("oe", "UTF-8")
	q.Set("q", chunk)

	fullURL := translateEndpoint + "?" + q.Encode()

	var status int
	var body []byte
	var err error
	if len(fullURL) > maxURLLen {
		status, body, err = httpPostForm(translateEndpoint, q)
	} else {
		status, body, err = httpGet(fullURL)
	}
	if err != nil {
		return zero, errors.New("Network error: could not reach the translation service.")
	}

	if status == http.StatusTooManyRequests {
		return zero, errRateLimited
	}
	if status != http.StatusOK {
		return zero, fmt.Errorf("Translation failed (HTTP %d).", status)
	}

	var payload []interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return zero, errors.New("Unexpected server response. Try again shortly.")
	}
	if len(payload) == 0 {
		return zero, errors.New("The server response did not contain a translation.")
	}
	segments, ok := payload[0].([]interface{})
	if !ok || len(segments) == 0 {
		return zero, errors.New("The server response did not contain a translation.")
	}
	var sb strings.Builder
	for _, seg := range segments {
		arr, ok := seg.([]interface{})
		if !ok || len(arr) == 0 {
			continue
		}
		if s, ok := arr[0].(string); ok {
			sb.WriteString(s)
		}
	}
	translated := sb.String()
	if translated == "" {
		return zero, errors.New("The server response did not contain a translation.")
	}
	var detected string
	if source == "auto" && len(payload) > 2 {
		if d, ok := payload[2].(string); ok {
			detected = d
		}
	}
	return chunkResult{translated: translated, detected: detected}, nil
}

// translateChunkRetry wraps translateChunk with up to two retries on a rate
// limit, with growing delays. Any other error is returned immediately. The
// context is honoured while waiting between retries.
func translateChunkRetry(ctx context.Context, chunk, source, target string) (chunkResult, error) {
	delays := []time.Duration{500 * time.Millisecond, 1200 * time.Millisecond}
	res, err := translateChunk(chunk, source, target)
	if err == nil {
		return res, nil
	}
	if !errors.Is(err, errRateLimited) {
		return res, err
	}
	for _, d := range delays {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return chunkResult{}, ctx.Err()
		}
		res, err = translateChunk(chunk, source, target)
		if err == nil {
			return res, nil
		}
		if !errors.Is(err, errRateLimited) {
			return res, err
		}
	}
	return chunkResult{}, errRateLimited
}

func translateText(text, source, target string) (TranslationResult, error) {
	if strings.TrimSpace(text) == "" {
		return TranslationResult{}, nil
	}

	cacheKey := source + "|" + target + "|" + text
	if r, ok := cachedTranslate(cacheKey); ok {
		return r, nil
	}

	chunks := splitIntoChunks(text, maxChunk)
	if len(chunks) > maxChunks {
		return TranslationResult{}, errors.New("Text is too long. Shorten it to continue.")
	}

	results := make([]chunkResult, len(chunks))

	// Translate the first chunk on its own so `auto` detection is established
	// from the head of the text and the frontend sees one consistent
	// detectedSource.
	r0, err := translateChunkRetry(context.Background(), chunks[0], source, target)
	if err != nil {
		return TranslationResult{}, err
	}
	results[0] = r0

	// Remaining chunks run with bounded concurrency. A done/cancel channel
	// stops launching further work as soon as one chunk fails.
	if len(chunks) > 1 {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sem := make(chan struct{}, translateConcurrency)
		var wg sync.WaitGroup
		errCh := make(chan error, len(chunks)-1)

		for i := 1; i < len(chunks); i++ {
			wg.Add(1)
			idx := i
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				// Tiny stagger after acquiring a slot so concurrent launches
				// don't look like a burst of abuse. Per-wave (concurrency 3),
				// not per-chunk.
				select {
				case <-time.After(40 * time.Millisecond):
				case <-ctx.Done():
					<-sem
					return
				}
				res, err := translateChunkRetry(ctx, chunks[idx], source, target)
				<-sem
				if err != nil {
					// Only the first failure reports; later ones see the
					// cancelled context and stay silent.
					if ctx.Err() == nil {
						cancel()
						select {
						case errCh <- err:
						default:
						}
					}
					return
				}
				results[idx] = res
			}()
		}

		wg.Wait()

		select {
		case e := <-errCh:
			return TranslationResult{}, e
		default:
		}
	}

	var sb strings.Builder
	var detected string
	for _, r := range results {
		sb.WriteString(r.translated)
		if r.detected != "" {
			detected = r.detected
		}
	}

	final := TranslationResult{TranslatedText: sb.String(), DetectedSource: detected}
	storeTranslate(cacheKey, final)
	return final, nil
}
