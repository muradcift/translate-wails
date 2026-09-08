package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
// Either way the caller sees (status, body); HTTP error statuses are handled
// by the caller, not here.

func httpGet(rawurl string) (int, []byte, error) {
	status, body, err := httpGetWinHTTP(rawurl)
	if err == nil {
		return status, body, nil
	}
	return httpGetNet(rawurl)
}

func httpGetNet(rawurl string) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, rawurl, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", chromeUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := translateHTTPClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

func httpGetWinHTTP(rawurl string) (int, []byte, error) {
	var zero []byte
	u, err := url.Parse(rawurl)
	if err != nil || u.Host == "" {
		return 0, zero, errors.New("bad url")
	}
	objectName := u.RequestURI()
	secure := u.Scheme == "https"

	ua, err := windows.UTF16PtrFromString(chromeUA)
	if err != nil {
		return 0, zero, err
	}
	host, err := windows.UTF16PtrFromString(u.Hostname())
	if err != nil {
		return 0, zero, err
	}
	obj, err := windows.UTF16PtrFromString(objectName)
	if err != nil {
		return 0, zero, err
	}
	// The User-Agent travels as the session default from WinHttpOpen (the
	// supported way — REPLACE fails here with 12150 since request headers
	// are not materialized yet). Accept has no default: ADD it.
	verb, _ := windows.UTF16PtrFromString("GET")
	acceptHeader, _ := windows.UTF16PtrFromString("Accept: application/json, text/plain, */*")

	hSession, _, _ := procWinHttpOpen.Call(
		uintptr(unsafe.Pointer(ua)),
		winhttpAccessAutomaticProxy, 0, 0, 0)
	if hSession == 0 {
		return 0, zero, errors.New("WinHttpOpen failed")
	}
	defer procWinHttpCloseHandle.Call(hSession)
	procWinHttpSetTimeouts.Call(hSession, 10000, 10000, 10000, 15000)

	port := winhttpDefaultHTTPSPort
	if u.Port() != "" {
		if p, perr := parsePort(u.Port()); perr == nil {
			port = p
		}
	}
	hConnect, _, _ := procWinHttpConnect.Call(
		hSession, uintptr(unsafe.Pointer(host)), uintptr(port), 0)
	if hConnect == 0 {
		return 0, zero, errors.New("WinHttpConnect failed")
	}
	defer procWinHttpCloseHandle.Call(hConnect)

	var flags uintptr
	if secure {
		flags = winhttpFlagSecure
	}
	hRequest, _, _ := procWinHttpOpenRequest.Call(
		hConnect, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(obj)),
		0, 0, 0, flags)
	if hRequest == 0 {
		return 0, zero, errors.New("WinHttpOpenRequest failed")
	}
	defer procWinHttpCloseHandle.Call(hRequest)

	// User-Agent has a session default (set at WinHttpOpen): replace it.
	// Accept has no default: add it only if new (REPLACE would fail 12150).
	r, _, callErr := procWinHttpAddRequestHeaders.Call(
		hRequest, uintptr(unsafe.Pointer(acceptHeader)), uintptr(0xFFFFFFFF), winhttpAddreqAddIfNew)
	if r == 0 {
		return 0, zero, fmt.Errorf("WinHttpAddRequestHeaders failed (%v)", callErr)
	}
	r, _, _ = procWinHttpSendRequest.Call(hRequest, 0, 0, 0, 0, 0, 0)
	if r == 0 {
		return 0, zero, errors.New("WinHttpSendRequest failed")
	}
	r, _, _ = procWinHttpReceiveResponse.Call(hRequest, 0)
	if r == 0 {
		return 0, zero, errors.New("WinHttpReceiveResponse failed")
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
		return 0, zero, errors.New("WinHttpQueryHeaders failed")
	}

	var body []byte
	for {
		var avail uint32
		r, _, _ := procWinHttpQueryDataAvailable.Call(hRequest, uintptr(unsafe.Pointer(&avail)))
		if r == 0 {
			return 0, zero, errors.New("WinHttpQueryDataAvailable failed")
		}
		if avail == 0 {
			break
		}
		if uint64(len(body))+uint64(avail) > maxHTTPBody {
			return 0, zero, errors.New("response too large")
		}
		chunk := make([]byte, avail)
		var got uint32
		r, _, _ = procWinHttpReadData.Call(
			hRequest, uintptr(unsafe.Pointer(&chunk[0])), uintptr(avail), uintptr(unsafe.Pointer(&got)))
		if r == 0 {
			return 0, zero, errors.New("WinHttpReadData failed")
		}
		if got == 0 {
			break
		}
		body = append(body, chunk[:got]...)
		if got < avail {
			break
		}
	}
	return int(status), body, nil
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
