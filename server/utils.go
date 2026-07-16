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
// header, and returns it along with the bytes already consumed (so they can be
// replayed to the client's local service). Adapted from jprq.
func parseHost(r io.Reader) (string, []byte, error) {
	buffer := make([]byte, 2048)
	size, err := r.Read(buffer)
	buffer = buffer[:size]
	if err != nil && size == 0 {
		return "", buffer, err
	}

	text := string(buffer)
	left := strings.Index(text, "Host: ")
	if left < 0 {
		left = strings.Index(text, "host: ")
	}
	if left < 0 {
		return "", buffer, fmt.Errorf("no host header found")
	}
	text = text[left+len("Host: "):]
	right := strings.Index(text, "\n")
	if right < 0 {
		return "", buffer, fmt.Errorf("malformed host header")
	}
	host := strings.TrimSpace(text[:right])
	if colon := strings.Index(host, ":"); colon != -1 {
		host = host[:colon]
	}
	return strings.ToLower(host), buffer, nil
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
