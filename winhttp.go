package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// HTTP transport for the translation endpoint.
//
// Primary: WinHTTP (winhttp.dll) with the OS TLS stack (SChannel) — the same
// class of client as browsers/PowerShell on this machine. Go's net/http TLS
// fingerprint is throttled (HTTP 429 "Sorry" page) by the keyless endpoint on
// some networks while OS-stack clients pass with identical URL and headers.
// Fallback: net/http, if WinHTTP itself fails at the API level.
//
// Lifecycle / connection reuse:
// A single WinHttp session handle (WinHttpOpen, with the Chrome UA as the
// session default) and one connect handle per (host, port, secure) are cached
// at package level behind winhttpCache (sync.Mutex). Only the request handle is
// opened and closed per call; WinHTTP pools the underlying keep-alive
// connections per session/connect handle, so repeated keystroke-triggered
// translations no longer pay a fresh TCP connect + TLS handshake each time.
//
// Thread-safety: translation and TTS requests can be in flight concurrently
// from different goroutines. winhttpCache.mu guards only the cached handles;
// handle acquisition is a short critical section and the mutex is released
// before any network I/O, so unrelated requests are not serialized on the
// cache lock. WinHTTP API failures mark the cache close-pending; the cached
// handles are closed only once every in-flight borrower has released them
// (refcounted), so a handle is never closed while a borrower is using it.
// Borrowers that arrive while close is pending reuse/create handles that are
// themselves closed when the last borrower releases.
//
// Either way the caller sees (status, body); HTTP error statuses are handled
// by the caller, not here.

type winHTTPConnectKey struct {
	host   string
	port   int
	secure bool
}

type winHTTPCache struct {
	mu           sync.Mutex
	session      uintptr
	connects     map[winHTTPConnectKey]uintptr
	inflight     int
	closePending bool
}

var winhttpCache winHTTPCache

func httpGet(rawurl string) (int, []byte, time.Duration, error) {
	status, body, retryAfter, err := httpGetWinHTTP(rawurl)
	if err == nil {
		return status, body, retryAfter, nil
	}
	return httpGetNet(rawurl)
}

func httpGetNet(rawurl string) (int, []byte, time.Duration, error) {
	req, err := http.NewRequest(http.MethodGet, rawurl, nil)
	if err != nil {
		return 0, nil, 0, err
	}
	req.Header.Set("User-Agent", chromeUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := translateHTTPClient.Do(req)
	if err != nil {
		return 0, nil, 0, err
	}
	defer resp.Body.Close()
	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody))
	if err != nil {
		return 0, nil, 0, err
	}
	return resp.StatusCode, body, retryAfter, nil
}

func httpPostForm(rawurl string, form url.Values) (int, []byte, time.Duration, error) {
	body := []byte(form.Encode())
	status, respBody, retryAfter, err := httpWinHTTP("POST", rawurl, body)
	if err == nil {
		return status, respBody, retryAfter, nil
	}
	return httpPostFormNet(rawurl, form)
}

func httpPostFormNet(rawurl string, form url.Values) (int, []byte, time.Duration, error) {
	req, err := http.NewRequest(http.MethodPost, rawurl, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, 0, err
	}
	req.Header.Set("User-Agent", chromeUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := translateHTTPClient.Do(req)
	if err != nil {
		return 0, nil, 0, err
	}
	defer resp.Body.Close()
	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody))
	if err != nil {
		return 0, nil, 0, err
	}
	return resp.StatusCode, body, retryAfter, nil
}

func httpGetWinHTTP(rawurl string) (int, []byte, time.Duration, error) {
	return httpWinHTTP("GET", rawurl, nil)
}

// parseRetryAfter converts a Retry-After header into a duration. Both the
// delta-seconds and the HTTP-date forms are accepted; absent or invalid
// values (and dates in the past) yield 0, letting callers use their default.
func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if t, err := http.ParseTime(value); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func httpWinHTTP(method, rawurl string, body []byte) (int, []byte, time.Duration, error) {
	var zero []byte
	u, err := url.Parse(rawurl)
	if err != nil || u.Host == "" {
		return 0, zero, 0, errors.New("bad url")
	}
	objectName := u.RequestURI()
	secure := u.Scheme == "https"

	host := u.Hostname()
	port := winhttpDefaultHTTPSPort
	if u.Port() != "" {
		if p, perr := parsePort(u.Port()); perr == nil {
			port = p
		}
	}

	obj, err := windows.UTF16PtrFromString(objectName)
	if err != nil {
		return 0, zero, 0, err
	}
	verb, err := windows.UTF16PtrFromString(method)
	if err != nil {
		return 0, zero, 0, err
	}
	acceptHeader, _ := windows.UTF16PtrFromString("Accept: application/json, text/plain, */*")
	contentTypeHeader, _ := windows.UTF16PtrFromString("Content-Type: application/x-www-form-urlencoded")

	_, connect, release, err := winHTTPBorrow(host, port, secure)
	if err != nil {
		winHTTPInvalidate()
		return 0, zero, 0, err
	}
	// defer release() must be registered BEFORE defer procWinHttpCloseHandle.Call(hRequest): defers are LIFO, so release runs after hRequest is closed.
	defer release()

	var flags uintptr
	if secure {
		flags = winhttpFlagSecure
	}
	hRequest, _, _ := procWinHttpOpenRequest.Call(
		connect, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(obj)),
		0, 0, 0, flags)
	if hRequest == 0 {
		winHTTPInvalidate()
		return 0, zero, 0, errors.New("WinHttpOpenRequest failed")
	}
	defer procWinHttpCloseHandle.Call(hRequest)

	r, _, callErr := procWinHttpAddRequestHeaders.Call(
		hRequest, uintptr(unsafe.Pointer(acceptHeader)), uintptr(0xFFFFFFFF), winhttpAddreqAddIfNew)
	if r == 0 {
		winHTTPInvalidate()
		return 0, zero, 0, fmt.Errorf("WinHttpAddRequestHeaders failed (%v)", callErr)
	}
	if method == "POST" {
		r, _, callErr = procWinHttpAddRequestHeaders.Call(
			hRequest, uintptr(unsafe.Pointer(contentTypeHeader)), uintptr(0xFFFFFFFF), winhttpAddreqAddIfNew)
		if r == 0 {
			winHTTPInvalidate()
			return 0, zero, 0, fmt.Errorf("WinHttpAddRequestHeaders failed (%v)", callErr)
		}
	}

	var bodyPtr uintptr
	if len(body) > 0 {
		bodyPtr = uintptr(unsafe.Pointer(&body[0]))
	}
	r, _, _ = procWinHttpSendRequest.Call(
		hRequest, 0, 0, bodyPtr, uintptr(len(body)), uintptr(len(body)), 0)
	if r == 0 {
		winHTTPInvalidate()
		return 0, zero, 0, errors.New("WinHttpSendRequest failed")
	}
	r, _, _ = procWinHttpReceiveResponse.Call(hRequest, 0)
	if r == 0 {
		winHTTPInvalidate()
		return 0, zero, 0, errors.New("WinHttpReceiveResponse failed")
	}

	var status uint32
	statusLen := uint32(4)
	r, _, _ = procWinHttpQueryHeaders.Call(
		hRequest,
		winhttpQueryStatusCode|winhttpQueryFlagNumber,
		0,
		uintptr(unsafe.Pointer(&status)),
		uintptr(unsafe.Pointer(&statusLen)),
		0)
	if r == 0 {
		winHTTPInvalidate()
		return 0, zero, 0, errors.New("WinHttpQueryHeaders failed")
	}

	// Retry-After is optional: an absent header (or a buffer too small for it)
	// simply yields 0, and callers fall back to their default cooldown. The
	// buffer lives on the stack and parsing happens before the body is read.
	var retryAfter time.Duration
	var retryAfterBuf [64]uint16
	retryAfterLen := uint32(len(retryAfterBuf) * 2)
	r, _, _ = procWinHttpQueryHeaders.Call(
		hRequest,
		winhttpQueryRetryAfter,
		0,
		uintptr(unsafe.Pointer(&retryAfterBuf[0])),
		uintptr(unsafe.Pointer(&retryAfterLen)),
		0)
	if r != 0 && retryAfterLen <= uint32(len(retryAfterBuf)*2) {
		retryAfter = parseRetryAfter(syscall.UTF16ToString(retryAfterBuf[:]))
	}

	var respBody []byte
	for {
		var avail uint32
		r, _, _ := procWinHttpQueryDataAvailable.Call(hRequest, uintptr(unsafe.Pointer(&avail)))
		if r == 0 {
			winHTTPInvalidate()
			return 0, zero, 0, errors.New("WinHttpQueryDataAvailable failed")
		}
		if avail == 0 {
			break
		}
		if uint64(len(respBody))+uint64(avail) > maxHTTPBody {
			return 0, zero, 0, errors.New("response too large")
		}
		chunk := make([]byte, avail)
		var got uint32
		r, _, _ = procWinHttpReadData.Call(
			hRequest, uintptr(unsafe.Pointer(&chunk[0])), uintptr(avail), uintptr(unsafe.Pointer(&got)))
		if r == 0 {
			winHTTPInvalidate()
			return 0, zero, 0, errors.New("WinHttpReadData failed")
		}
		if got == 0 {
			break
		}
		respBody = append(respBody, chunk[:got]...)
		if got < avail {
			break
		}
	}
	return int(status), respBody, retryAfter, nil
}

func winHTTPBorrow(host string, port int, secure bool) (uintptr, uintptr, func(), error) {
	c := &winhttpCache
	c.mu.Lock()

	if c.closePending && c.inflight == 0 {
		closeCachedHandlesLocked()
	}

	if c.session == 0 {
		ua, err := windows.UTF16PtrFromString(chromeUA)
		if err != nil {
			c.mu.Unlock()
			return 0, 0, nil, err
		}
		h, _, _ := procWinHttpOpen.Call(
			uintptr(unsafe.Pointer(ua)),
			winhttpAccessAutomaticProxy, 0, 0, 0)
		if h == 0 {
			c.mu.Unlock()
			return 0, 0, nil, errors.New("WinHttpOpen failed")
		}
		procWinHttpSetTimeouts.Call(h, 10000, 10000, 10000, 15000)
		c.session = h
	}
	if c.connects == nil {
		c.connects = make(map[winHTTPConnectKey]uintptr)
	}
	key := winHTTPConnectKey{host: host, port: port, secure: secure}
	if hConn, ok := c.connects[key]; !ok || hConn == 0 {
		hostPtr, err := windows.UTF16PtrFromString(host)
		if err != nil {
			c.mu.Unlock()
			return 0, 0, nil, err
		}
		hConn, _, _ = procWinHttpConnect.Call(c.session, uintptr(unsafe.Pointer(hostPtr)), uintptr(port), 0)
		if hConn == 0 {
			c.closePending = true
			if c.inflight == 0 {
				closeCachedHandlesLocked()
			}
			c.mu.Unlock()
			return 0, 0, nil, errors.New("WinHttpConnect failed")
		}
		c.connects[key] = hConn
	}

	session := c.session
	connect := c.connects[key]
	c.inflight++
	c.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.inflight > 0 {
				c.inflight--
			}
			if c.closePending && c.inflight == 0 {
				closeCachedHandlesLocked()
			}
		})
	}
	return session, connect, release, nil
}

func winHTTPInvalidate() {
	c := &winhttpCache
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closePending = true
	if c.inflight == 0 {
		closeCachedHandlesLocked()
	}
}

func closeCachedHandlesLocked() {
	c := &winhttpCache
	for _, v := range c.connects {
		procWinHttpCloseHandle.Call(v)
	}
	c.connects = nil
	if c.session != 0 {
		procWinHttpCloseHandle.Call(c.session)
		c.session = 0
	}
	c.closePending = false
}

func parsePort(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, errors.New("empty port")
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, errors.New("bad port")
		}
		n = n*10 + int(s[i]-'0')
		if n > 65535 {
			return 0, errors.New("bad port")
		}
	}
	return n, nil
}
