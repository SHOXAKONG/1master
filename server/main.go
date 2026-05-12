package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"mytunnel/protocol"
)

// Tunnel represents an active client tunnel
type Tunnel struct {
	Subdomain string
	UserID    string
	UserEmail string
	Conn      net.Conn
	Pending   map[string]chan *protocol.Message // waiting for responses by request ID
	mu        sync.Mutex
}

// Server manages all active tunnels
type Server struct {
	tunnels  map[string]*Tunnel // subdomain -> tunnel
	mu       sync.RWMutex
	domain   string
	authURL  string
	authHTTP *http.Client
}

func NewServer(domain, authURL string) *Server {
	return &Server{
		tunnels: make(map[string]*Tunnel),
		domain:  domain,
		authURL: authURL,
		authHTTP: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// authResponse mirrors backend's TunnelAuthResponse
type authResponse struct {
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// verifyToken calls the backend to validate a service token.
// Returns (user info, nil) on success, or (nil, error) with a human-readable reason.
func (s *Server) verifyToken(token string) (*authResponse, error) {
	if s.authURL == "" {
		return nil, fmt.Errorf("server misconfigured: BACKEND_AUTH_URL not set")
	}
	if token == "" {
		return nil, fmt.Errorf("missing token")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.authURL, bytes.NewReader(nil))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := s.authHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth backend unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("invalid or expired token")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth backend returned %d", resp.StatusCode)
	}

	var out authResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode auth response: %w", err)
	}
	return &out, nil
}

// generateRequestID creates a unique request ID
func generateRequestID() string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), r.Intn(100000))
}

// handleTunnelConn handles a new tunnel client connection
func (s *Server) handleTunnelConn(conn net.Conn) {
	defer conn.Close()

	// Wait for registration message
	msg, err := protocol.Recv(conn)
	if err != nil {
		log.Printf("[tunnel] failed to read registration: %v", err)
		return
	}

	if msg.Type != protocol.TypeRegister {
		log.Printf("[tunnel] expected register, got type %d", msg.Type)
		return
	}

	// Verify the client's service token against the backend
	auth, err := s.verifyToken(msg.Token)
	if err != nil {
		log.Printf("[tunnel] auth failed (%s): %v", conn.RemoteAddr(), err)
		protocol.Send(conn, &protocol.Message{
			Type:  protocol.TypeRegisterResp,
			Error: "unauthorized: " + err.Error(),
		})
		return
	}

	// Subdomain is ALWAYS the authenticated user's username.
	// If the client asked for a different one, log and override.
	if auth.Username == "" {
		log.Printf("[tunnel] backend returned empty username for user=%s", auth.Email)
		protocol.Send(conn, &protocol.Message{
			Type:  protocol.TypeRegisterResp,
			Error: "server misconfigured: no username assigned to your account",
		})
		return
	}
	subdomain := auth.Username
	if msg.Subdomain != "" && msg.Subdomain != subdomain {
		log.Printf("[tunnel] client requested '%s' but using username '%s'", msg.Subdomain, subdomain)
	}

	tunnel := &Tunnel{
		Subdomain: subdomain,
		UserID:    auth.UserID,
		UserEmail: auth.Email,
		Conn:      conn,
		Pending:   make(map[string]chan *protocol.Message),
	}

	// Register tunnel
	s.mu.Lock()
	if _, exists := s.tunnels[subdomain]; exists {
		s.mu.Unlock()
		protocol.Send(conn, &protocol.Message{
			Type:  protocol.TypeRegisterResp,
			Error: fmt.Sprintf("subdomain '%s' already in use", subdomain),
		})
		return
	}
	s.tunnels[subdomain] = tunnel
	s.mu.Unlock()

	url := fmt.Sprintf("http://%s.%s", subdomain, s.domain)
	log.Printf("[tunnel] registered: %s (user=%s)", url, auth.Email)

	// Send success response
	protocol.Send(conn, &protocol.Message{
		Type:      protocol.TypeRegisterResp,
		Subdomain: subdomain,
	})

	// Read responses from client
	defer func() {
		s.mu.Lock()
		delete(s.tunnels, subdomain)
		s.mu.Unlock()
		log.Printf("[tunnel] disconnected: %s (user=%s)", subdomain, auth.Email)
	}()

	for {
		msg, err := protocol.Recv(conn)
		if err != nil {
			if err != io.EOF {
				log.Printf("[tunnel] read error (%s): %v", subdomain, err)
			}
			return
		}

		if msg.Type == protocol.TypeProxyResp {
			tunnel.mu.Lock()
			ch, ok := tunnel.Pending[msg.ID]
			if ok {
				ch <- msg
				delete(tunnel.Pending, msg.ID)
			}
			tunnel.mu.Unlock()
		}
	}
}

// ServeHTTP handles incoming public HTTP requests and routes them to tunnels
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract subdomain from Host header
	host := r.Host
	if colonIdx := strings.Index(host, ":"); colonIdx != -1 {
		host = host[:colonIdx]
	}

	// Get subdomain: everything before the first dot
	subdomain := ""
	if dotIdx := strings.Index(host, "."); dotIdx != -1 {
		subdomain = host[:dotIdx]
	}

	if subdomain == "" {
		// Root domain - show status page
		w.Header().Set("Content-Type", "text/html")
		s.mu.RLock()
		count := len(s.tunnels)
		s.mu.RUnlock()
		fmt.Fprintf(w, `<html><body style="font-family:monospace;padding:40px">
			<h1>🚇 MyTunnel Server</h1>
			<p>Active tunnels: %d</p>
			<p>Usage: <code>mytunnel-client -server %s:9000 -port 3000</code></p>
		</body></html>`, count, s.domain)
		return
	}

	// Find the tunnel
	s.mu.RLock()
	tunnel, ok := s.tunnels[subdomain]
	s.mu.RUnlock()

	if !ok {
		http.Error(w, fmt.Sprintf("Tunnel '%s' not found", subdomain), http.StatusNotFound)
		return
	}

	// Serialize the HTTP request
	reqID := generateRequestID()

	// Build raw HTTP request to forward
	var reqBuf strings.Builder
	fmt.Fprintf(&reqBuf, "%s %s %s\r\n", r.Method, r.URL.RequestURI(), r.Proto)
	fmt.Fprintf(&reqBuf, "Host: %s\r\n", r.Host)
	for key, vals := range r.Header {
		for _, val := range vals {
			fmt.Fprintf(&reqBuf, "%s: %s\r\n", key, val)
		}
	}
	fmt.Fprintf(&reqBuf, "X-Forwarded-For: %s\r\n", r.RemoteAddr)
	fmt.Fprintf(&reqBuf, "\r\n")

	// Read request body
	body, _ := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	reqBytes := append([]byte(reqBuf.String()), body...)

	// Create response channel
	respCh := make(chan *protocol.Message, 1)
	tunnel.mu.Lock()
	tunnel.Pending[reqID] = respCh
	tunnel.mu.Unlock()

	// Send to client
	err := protocol.Send(tunnel.Conn, &protocol.Message{
		Type: protocol.TypeProxy,
		ID:   reqID,
		Data: reqBytes,
	})
	if err != nil {
		tunnel.mu.Lock()
		delete(tunnel.Pending, reqID)
		tunnel.mu.Unlock()
		http.Error(w, "Tunnel connection error", http.StatusBadGateway)
		return
	}

	// Wait for response with timeout
	select {
	case resp := <-respCh:
		if resp.Error != "" {
			http.Error(w, resp.Error, http.StatusBadGateway)
			return
		}
		// Parse the HTTP response from client
		respReader := bufio.NewReader(strings.NewReader(string(resp.Data)))
		httpResp, err := http.ReadResponse(respReader, r)
		if err != nil {
			// Fallback: write raw data
			w.Header().Set("Content-Type", "text/html")
			w.Write(resp.Data)
			return
		}
		defer httpResp.Body.Close()

		// Copy response headers
		for key, vals := range httpResp.Header {
			for _, val := range vals {
				w.Header().Add(key, val)
			}
		}

		// Write status code
		w.WriteHeader(httpResp.StatusCode)

		// Copy response body
		io.Copy(w, httpResp.Body)

	case <-time.After(30 * time.Second):
		tunnel.mu.Lock()
		delete(tunnel.Pending, reqID)
		tunnel.mu.Unlock()
		http.Error(w, "Tunnel timeout", http.StatusGatewayTimeout)
	}
}

func main() {
	domain := os.Getenv("TUNNEL_DOMAIN")
	if domain == "" {
		domain = "localhost"
	}

	tunnelPort := os.Getenv("TUNNEL_PORT")
	if tunnelPort == "" {
		tunnelPort = "9000"
	}

	httpPort := os.Getenv("HTTP_PORT")
	if httpPort == "" {
		httpPort = "8080"
	}

	authURL := os.Getenv("BACKEND_AUTH_URL")
	if authURL == "" {
		authURL = "http://localhost:8000/api/v1/tunnel/auth"
	}

	server := NewServer(domain, authURL)

	// Start tunnel listener (TCP for client connections)
	go func() {
		ln, err := net.Listen("tcp", ":"+tunnelPort)
		if err != nil {
			log.Fatalf("failed to listen on tunnel port %s: %v", tunnelPort, err)
		}
		log.Printf("🔌 Tunnel listener on :%s", tunnelPort)

		for {
			conn, err := ln.Accept()
			if err != nil {
				log.Printf("accept error: %v", err)
				continue
			}
			go server.handleTunnelConn(conn)
		}
	}()

	// Start HTTP server (for public traffic)
	log.Printf("🌐 HTTP server on :%s", httpPort)
	log.Printf("📡 Domain: %s", domain)
	log.Printf("🔐 Auth backend: %s", authURL)
	log.Printf("")
	log.Printf("Clients connect to :%s, public traffic comes in on :%s", tunnelPort, httpPort)

	if err := http.ListenAndServe(":"+httpPort, server); err != nil {
		log.Fatalf("HTTP server error: %v", err)
	}
}
