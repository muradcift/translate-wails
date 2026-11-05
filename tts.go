package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"unicode/utf8"
)

// TTS via the same keyless Google endpoint family as translation, fetched
// through the same transport (winhttp.go: WinHTTP/SChannel primary,
// net/http fallback). The WebView2 network layer blocks this endpoint from
// inside the page, but the Go transport talks to translate.googleapis.com
// all day for translation, so TTS rides the same wire. The endpoint serves
// ~200 characters per request; the frontend chunks and plays the returned
// base64 MP3 pieces sequentially.
const ttsEndpoint = "https://translate.googleapis.com/translate_tts"

// SpeakTTS returns one TTS chunk as base64-encoded MP3 audio.
// Text is capped at 1000 runes to avoid oversized requests.
func (a *App) SpeakTTS(text, lang string) (string, error) {
	if text == "" || lang == "" {
		return "", fmt.Errorf("TTS: empty text or language")
	}
	if utf8.RuneCountInString(text) > 1000 {
		return "", fmt.Errorf("TTS: text too long")
	}
	q := url.Values{}
	q.Set("ie", "UTF-8")
	q.Set("client", "gtx")
	q.Set("tl", lang)
	q.Set("q", text)

	// Share the translation cooldown gate: a 429 here means the whole endpoint
	// family is throttled, so TTS must not keep hammering it either. If the
	// gate actually made us wait, jitter the wake-up so TTS does not fire in
	// lockstep with translation chunks resuming after the same cooldown.
	waited, _ := sharedRateGate.wait(context.Background())
	if waited {
		_ = sleepJitter(context.Background(), rateGateJitterMax)
	}

	status, body, retryAfter, err := httpGet(ttsEndpoint + "?" + q.Encode())
	if err != nil {
		return "", fmt.Errorf("TTS network error")
	}
	if status == http.StatusTooManyRequests {
		sharedRateGate.pause(rateGateCooldown(retryAfter))
	}
	if status != 200 {
		return "", fmt.Errorf("TTS HTTP %d", status)
	}
	if len(body) < 512 {
		return "", fmt.Errorf("TTS payload too small")
	}
	return base64.StdEncoding.EncodeToString(body), nil
}
