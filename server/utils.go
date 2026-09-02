package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// newConnID returns an unguessable hex connection id. It is unguessable on
// purpose: the data port is shared by all tunnels, so a random id prevents one
// client from hijacking another's pending public connection.
func newConnID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// parseHost reads the start of an incoming HTTP request, extracts the Host
// header plus the request line's method and path (for the client's live
// request log), and returns them along with the bytes already consumed (so
// they can be replayed to the client's local service). Adapted from jprq.
func parseHost(r io.Reader) (host, method, path string, initial []byte, err error) {
	buffer := make([]byte, 2048)
	size, readErr := r.Read(buffer)
	buffer = buffer[:size]
	if readErr != nil && size == 0 {
		return "", "", "", buffer, readErr
	}

	text := string(buffer)
	method, path = parseRequestLine(text)

	left := strings.Index(text, "Host: ")
	if left < 0 {
		left = strings.Index(text, "host: ")
	}
	if left < 0 {
		return "", "", "", buffer, fmt.Errorf("no host header found")
	}
	rest := text[left+len("Host: "):]
	right := strings.Index(rest, "\n")
	if right < 0 {
		return "", "", "", buffer, fmt.Errorf("malformed host header")
	}
	host = strings.TrimSpace(rest[:right])
	if colon := strings.Index(host, ":"); colon != -1 {
		host = host[:colon]
	}
	return strings.ToLower(host), method, path, buffer, nil
}

// parseRequestLine extracts the method and path from the first line of an
// HTTP request (e.g. "GET /api/health HTTP/1.1"). Best-effort: used only for
// the client's terminal request log, so a malformed line just yields "".
func parseRequestLine(text string) (method, path string) {
	end := strings.IndexAny(text, "\r\n")
	if end < 0 {
		end = len(text)
	}
	fields := strings.Fields(text[:end])
	if len(fields) < 2 {
		return "", ""
	}
	return fields[0], fields[1]
}

// subdomainOf returns everything before the first dot of host, or "".
func subdomainOf(host string) string {
	if dot := strings.Index(host, "."); dot != -1 {
		return host[:dot]
	}
	return ""
}

// writeHTTPError writes a minimal HTTP error response and closes the connection.
func writeHTTPError(w io.WriteCloser, statusCode int, status, message string) {
	fmt.Fprintf(w,
		"HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		statusCode, status, len(message), message)
	w.Close()
}
