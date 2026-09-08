package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// Keyless Google Translate endpoint, same environment as the reference
// Electron app (Node fetch with a real Chrome UA).
const (
	translateEndpoint = "https://translate.googleapis.com/translate_a/single"
	chromeUA          = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
	maxChunk          = 1200
	chunkDelay        = 250 * time.Millisecond
)

var translateHTTPClient = &http.Client{Timeout: 15 * time.Second}

// splitIntoChunks mirrors the reference splitIntoChunks: cut at a sentence
// boundary inside the first max runes, fall back to a space, then to a hard
// cut. Lengths are counted in runes so multibyte text is never split
// mid-rune.
func splitIntoChunks(text string, limit int) []string {
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}
	var chunks []string
	rest := text
	for utf8.RuneCountInString(rest) > limit {
		r := []rune(rest)
		window := string(r[:limit])
		cut := max(
			strings.LastIndex(window, ". "),
			strings.LastIndex(window, "! "),
			strings.LastIndex(window, "? "),
			strings.LastIndex(window, "\n"),
		)
		if cut < limit/2 {
			cut = strings.LastIndex(window, " ")
		}
		end := limit // hard cut (JS: cut = max-1 -> slice(0, max))
		if cut > 0 {
			// cut is a byte index at an ASCII delimiter start, which is
			// always rune-aligned; include the delimiter's first char.
			end = utf8.RuneCountInString(window[:cut]) + 1
		}
		if end <= 0 || end > len(r) {
			end = limit
		}
		chunks = append(chunks, string(r[:end]))
		rest = string(r[end:])
	}
	if rest != "" {
		chunks = append(chunks, rest)
	}
	return chunks
}

type chunkResult struct {
	translated string
	detected   string
}

// translateChunk mirrors the reference translateChunk: single GET against
// the gtx endpoint, English error messages for the UI.
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

	req, err := http.NewRequest(http.MethodGet, translateEndpoint+"?"+q.Encode(), nil)
	if err != nil {
		return zero, errors.New("Network error: could not reach the translation service.")
	}
	req.Header.Set("User-Agent", chromeUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")

	status, body, err := httpGet(req.URL.String())
	if err != nil {
		return zero, errors.New("Network error: could not reach the translation service.")
	}

	if status == http.StatusTooManyRequests {
		return zero, errors.New("Rate limited by Google. Wait a moment and try again.")
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

func translateText(text, source, target string) (TranslationResult, error) {
	if strings.TrimSpace(text) == "" {
		return TranslationResult{}, nil
	}
	chunks := splitIntoChunks(text, maxChunk)
	var sb strings.Builder
	var detected string
	for i, c := range chunks {
		if i > 0 {
			time.Sleep(chunkDelay)
		}
		r, err := translateChunk(c, source, target)
		if err != nil {
			return TranslationResult{}, err
		}
		sb.WriteString(r.translated)
		if r.detected != "" {
			detected = r.detected
		}
	}
	return TranslationResult{TranslatedText: sb.String(), DetectedSource: detected}, nil
}
