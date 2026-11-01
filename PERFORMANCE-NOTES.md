# PERFORMANCE-NOTES — translate-wails

State: branch `experimental/performance`, backup tag `backup-pre-improvements`.
**Nothing on this branch has been compiled or run** — there is no Go toolchain on the
machine it was edited on. Treat it as a reviewed patch set, not a validated build.

Estimates are tagged **(measured)**, **(derived)**, **(guessed)**.

## 1. What changed in this branch (and why)

| Area | Before | After | Expected win |
|---|---|---|---|
| WinHTTP transport | `WinHttpOpen` + `WinHttpConnect` + request for every call: fresh TCP + TLS handshake per translation/TTS request | package-level session + one connect handle per (host,port,secure), refcounted so handles are never closed while in use; only the request handle is per-call and WinHTTP reuses pooled keep-alive connections | removes ~120-350 ms (guessed) handshake from every request after the first |
| Long queries | always GET, `q` percent-encoded into the URL (a 1200-char Turkish chunk is ~2.1 KB of URL, (measured)) | same params, but `POST application/x-www-form-urlencoded` when the encoded URL would exceed 1800 bytes | no URL-length ceiling, fewer proxy/log artefacts; POST verified 200 + identical JSON (measured) |
| Chunk translation | serial, `time.Sleep(250ms)` between chunks (a 5000-char text paid 1 s of pure sleep) | first chunk alone (so `auto` detection is stable), then remaining chunks with concurrency 3, 40 ms per-wave stagger, in-order assembly, cancel on first failure | 5 chunks: ~1.3 s -> ~0.4 s (derived); 20 chunks: ~5 s -> ~1.6 s (derived) |
| 429 handling | whole translation failed | per-chunk retry (500 ms, 1200 ms) then the same English rate-limit error | long texts survive one throttled chunk |
| Chunk splitting | `utf8.RuneCountInString` + `[]rune(rest)` per iteration: O(n²) with repeated allocation | one `[]rune` conversion, rune-indexed cutting, same delimiter rules | 50k-char paste: tens of ms -> <1 ms (derived) |
| Translation cache (Go) | none | LRU, 64 entries / ~2 MB, key `source|target|text`, returns by value | repeat translations cost microseconds |
| Translation cache (JS) | none | Map, 40 entries, checked *before* the throttle, paints without a network call | edit-and-revert typing is instant |
| Throttle | `MIN_INTERVAL = 800` flat floor on every call | soft floor 250 ms between network calls, escalates to 1200 ms only after a 429 and decays back; cache hits bypass it | removes up to 550 ms from ordinary typing (derived) |
| HWND lookup | `EnumWindows` scan (all top-level windows, pid + title per window) twice per hotkey | cached HWND with a liveness re-check, one fresh enumeration only if the cached handle is stale | ~1-4 ms per hotkey (guessed, machine-dependent) |
| Selection capture | 4 x 30 ms sleeps = 120 ms before the copy is even injected, then 25 ms poll ticks | modifier releases as one `SendInput` batch, 20 ms settle, Ctrl+C as one atomic batch, poll 2/4/8/16 ms with immediate first check | ~100 ms + ~15 ms average removed (derived) |
| Window counter | whitespace `split()` of the whole text on every keystroke | rAF-coalesced single pass, DOM write only when the string changed | no per-keystroke array allocation |
| TTS blobs | prefetched blob URLs leaked when speech was stopped mid-flight | pending-URL set revoked on stop and on every early return | bounded memory during long reads |

TTS now rides the pooled transport too, so its first chunk also skips a handshake.

## 2. Network facts measured from this machine

- `POST https://translate.googleapis.com/translate_a/single` with the form params
  (`client=gtx, sl=auto, tl=tr, dt=t, ie=UTF-8, oe=UTF-8, q=...`) returns **HTTP 200**
  and the same `[[[translated, original, ...]],null,"en",...]` shape as GET — `auto`
  detection still arrives at index 2. (measured)
- A 1200-char Turkish chunk: escaped query length 2000 bytes, full URL 2102 bytes;
  GET 200 (441 ms), POST 200 with byte-identical body length (120 ms) — both fine,
  the difference is handshake/connection variance, not the verb. (measured)
- Consequence: the 1800-byte threshold makes POST the common path for non-ASCII text
  over ~600 chars. That is intentional and verified.

## 3. Still on the table (ranked by impact / risk)

1. **Progressive output.** `TranslateText` returns only when every chunk is done, so a
   long paste shows nothing for the whole duration. Emit a Wails event per finished
   chunk (`translation-chunk` with index/final) and append in the frontend; keep the
   existing promise for compatibility and ignore it when events already painted.
   Risk: ordering/partial state on error; needs the same `seq` discipline as the cache.
2. **Startup deferral.** `buildDropdown` builds ~2 x 100 DOM items plus search inputs at
   startup, settings UI and voice enumeration too. Build dropdown items on first open.
   Risk: first-open hitch moves to interaction time — prebuild on `pointerenter`.
3. **`applyRoundedCorners`** retries `findTranslatorWindow` 20x100 ms (up to 2 s) at
   startup for a cosmetic Win11 call. Reduce to ~5 tries / 50 ms with the cached HWND.
4. **HTTP/2 + TLS resumption.** `WinHttpSetOption(WINHTTP_OPTION_ENABLE_HTTP_PROTOCOL,
   WINHTTP_PROTOCOL_FLAG_HTTP2)` on the request handle. Less relevant now that
   connections are pooled and chunk concurrency is 3; measure before doing it.
5. **gzip.** `WINHTTP_OPTION_DECOMPRESSION` (=118) with `WINHTTP_DECOMPRESSION_FLAG_ALL`
   plus `Accept-Encoding: gzip`. JSON bodies are 1-3 KB; expected win is small but the
   code change is 5 lines. Verify the responses really decompress before trusting it —
   a wrong flag value silently breaks every translation.
6. **Spellcheck on large inputs.** Chromium spellcheck over a 5000-char textarea can
   cause input jank; disable spellcheck above ~1000 chars.
7. **Clipboard allocations.** `getClipboardText` allocates the whole clipboard (up to
   64 MB cap) before `capSelection` truncates to 5000 runes. Truncate during decode.
8. **`sendKeys` batching for the mask key** (kbdhook.go): two separate `SendInput` calls
   per swallowed combo; negligible vs. the capture path, skip unless touching that file.
9. **Single-flight translation.** Two rapid requests for the same key can both hit the
   network before either stores a cache entry; a `singleflight`-style map would dedupe.
   Rare with debounce + 250 ms floor.

## 4. How to verify after building (no instrumentation shipped)

Small, ordering-safe probes; add, measure, remove.

1. Hotkey latency: `time.Now()` at `handleHotkey` entry, after `captureSelection`,
   after `EventsEmit("prefill")`; log the deltas. Expect capture ≈ 25-45 ms now
   (was ≈ 150-220 ms). If capture is still >100 ms, the settle time is too short for
   the foreground app — raise `time.Sleep` in `sendCtrlC` to 35 ms and re-measure.
2. Transport reuse: log elapsed around `httpGet` for two consecutive translations of
   the same text with the cache disabled (or two different texts). Second call should
   drop by the handshake cost.
3. Chunk concurrency: type/paste 5000 chars, log elapsed per chunk; expect ~3 in flight
   and total ≈ slowest wave, not the sum.
4. Rate limiting: type continuously for a minute with the 250 ms floor and watch for
   "Rate limited" in the output pane. If it appears, raise `SOFT_INTERVAL` to 400-500 ms
   in `frontend/src/main.js` rather than reintroducing a fixed 800 ms floor.
5. Clipboard integrity: copy an image, press Win+T in a text app, confirm the image is
   still on the clipboard (this path still drops non-text formats — unchanged).

## 5. Do NOT do this (already tried and failed here)

- **No UIA on the hotkey path.** Untimed cross-process COM wedges the hotkey thread and
  drags the Wails UI thread into cross-apartment servicing → "not responding" →
  WebView2 browser process exit → `os.Exit(-1)`. Observed live.
- **Keep WinHTTP/SChannel primary.** Go `net/http`'s TLS fingerprint is 429'd on this
  network; it stays a fallback only.
- **Do not rely on `RegisterHotKey(MOD_WIN, T)`.** The shell owns the combo; the LL hook
  is the only reliable trigger.
- **Do not drop the Start-menu mask** after swallowing a Win combo (VK 0xE8 + re-injected
  Win-up). Swallowing alone desyncs the shell's Win state.
- **Do not foreground before capture.** Order is sacred: capture (previous app focused)
  → show/foreground → prefill.
- **Do not remove the foreground waivers** (`AttachThreadInput`, synthetic Alt): the hook
  swallows the key before any input queue sees it.
- **Do not remove the 1.2 s capture deadline** or allow late prefill.
- **Do not replace sequence-number polling with a blind sleep** — identical-to-previous
  copies were misread as "nothing copied".
