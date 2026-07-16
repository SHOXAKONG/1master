package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"mytunnel/protocol"
)

// pendingConnTTL bounds how long a public connection waits for the client to
// dial the data port and pair with it before we give up.
const pendingConnTTL = 10 * time.Second

// Tunnel is one client's live control connection, addressed by subdomain.
type Tunnel struct {
	Subdomain string
	UserEmail string
	control   *protocol.SafeConn
}

// pendingConn is a public connection waiting to be paired with a client-dialed
// data connection.
type pendingConn struct {
	pub     net.Conn
	initial []byte  // bytes already read from pub while sniffing the Host header
	tunnel  *Tunnel // owning tunnel, so we can drop it if the client disconnects
}

// Server owns the three planes: control, public, and data.
type Server struct {
	domain   string
	dataPort string
	authURL  string
	authHTTP *http.Client

	mu      sync.RWMutex
	tunnels map[string]*Tunnel // subdomain -> tunnel

	pmu     sync.Mutex
	pending map[string]*pendingConn // conn id -> waiting public connection
}

func NewServer(domain, dataPort, authURL string) *Server {
	return &Server{
		domain:   domain,
		dataPort: dataPort,
		authURL:  authURL,
		authHTTP: &http.Client{Timeout: 5 * time.Second},
		tunnels:  make(map[string]*Tunnel),
		pending:  make(map[string]*pendingConn),
	}
}

// authResponse mirrors the backend's TunnelAuthResponse.
type authResponse struct {
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// verifyToken validates a service token against the backend.
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

// ---------------- Control plane ----------------

// handleControlConn authenticates a client, registers its tunnel, and then
// pushes new-connection events until the control connection closes.
func (s *Server) handleControlConn(rawConn net.Conn) {
	defer rawConn.Close()
	conn := protocol.NewSafeConn(rawConn)

	msg, err := protocol.Recv(conn)
	if err != nil {
		log.Printf("[control] read register failed (%s): %v", rawConn.RemoteAddr(), err)
		return
	}
	if msg.Type != protocol.TypeRegister {
		log.Printf("[control] expected register, got type %d", msg.Type)
		return
	}

	auth, err := s.verifyToken(msg.Token)
	if err != nil {
		log.Printf("[control] auth failed (%s): %v", rawConn.RemoteAddr(), err)
		conn.Send(&protocol.Message{Type: protocol.TypeRegistered, Error: "unauthorized: " + err.Error()})
		return
	}

	username := auth.Username
	if username == "" {
		conn.Send(&protocol.Message{
			Type:  protocol.TypeRegistered,
			Error: "server misconfigured: no username assigned to your account",
		})
		return
	}

	// A user may run several tunnels at once; each gets its own subdomain.
	// No requested label -> the bare username; a label -> "<label>-<username>".
	subdomain, err := effectiveSubdomain(username, msg.Subdomain)
	if err != nil {
		conn.Send(&protocol.Message{
			Type:  protocol.TypeRegistered,
			Error: "invalid subdomain: " + err.Error(),
		})
		return
	}

	tunnel := &Tunnel{Subdomain: subdomain, UserEmail: auth.Email, control: conn}

	s.mu.Lock()
	if _, exists := s.tunnels[subdomain]; exists {
		s.mu.Unlock()
		conn.Send(&protocol.Message{
			Type:  protocol.TypeRegistered,
			Error: fmt.Sprintf("subdomain '%s' already in use — pick another with --subdomain <label>", subdomain),
		})
		return
	}
	s.tunnels[subdomain] = tunnel
	s.mu.Unlock()

	hostname := fmt.Sprintf("%s.%s", subdomain, s.domain)
	if err := conn.Send(&protocol.Message{
		Type:      protocol.TypeRegistered,
		Subdomain: subdomain,
		Hostname:  hostname,
		DataPort:  s.dataPort,
	}); err != nil {
		s.dropTunnel(tunnel)
		return
	}
	log.Printf("[control] registered: http://%s (user=%s)", hostname, auth.Email)

	// Tear everything down when the control connection goes away.
	defer func() {
		s.dropTunnel(tunnel)
		log.Printf("[control] disconnected: %s (user=%s)", subdomain, auth.Email)
	}()

	// The client never sends more control frames; this blocks until the
	// connection closes, acting as a disconnect detector.
	for {
		if _, err := protocol.Recv(conn); err != nil {
			return
		}
	}
}

// dropTunnel removes a tunnel and closes any of its still-pending public
// connections.
func (s *Server) dropTunnel(t *Tunnel) {
	s.mu.Lock()
	if cur, ok := s.tunnels[t.Subdomain]; ok && cur == t {
		delete(s.tunnels, t.Subdomain)
	}
	s.mu.Unlock()

	s.pmu.Lock()
	for id, p := range s.pending {
		if p.tunnel == t {
			p.pub.Close()
			delete(s.pending, id)
		}
	}
	s.pmu.Unlock()
}

// ---------------- Public plane ----------------

// servePublicConn sniffs the Host header of an incoming public request, finds
// the matching tunnel, parks the connection, and asks the client to dial back.
func (s *Server) servePublicConn(pub net.Conn) {
	_ = pub.SetReadDeadline(time.Now().Add(pendingConnTTL))
	host, initial, err := parseHost(pub)
	if err != nil || host == "" {
		writeHTTPError(pub, http.StatusBadRequest, "Bad Request", "could not determine target host")
		return
	}
	_ = pub.SetReadDeadline(time.Time{})

	if host == s.domain {
		s.writeStatusPage(pub)
		return
	}

	subdomain := subdomainOf(host)
	s.mu.RLock()
	tunnel, ok := s.tunnels[subdomain]
	s.mu.RUnlock()
	if !ok {
		writeHTTPError(pub, http.StatusNotFound, "Not Found", fmt.Sprintf("tunnel '%s' not found", subdomain))
		return
	}

	id, err := newConnID()
	if err != nil {
		writeHTTPError(pub, http.StatusInternalServerError, "Internal Server Error", "could not allocate connection")
		return
	}

	s.pmu.Lock()
	s.pending[id] = &pendingConn{pub: pub, initial: initial, tunnel: tunnel}
	s.pmu.Unlock()

	// Reap the pending connection if the client never dials back.
	time.AfterFunc(pendingConnTTL, func() {
		if p := s.takePending(id); p != nil {
			writeHTTPError(p.pub, http.StatusGatewayTimeout, "Gateway Timeout", "tunnel client did not respond")
		}
	})

	if err := tunnel.control.Send(&protocol.Message{Type: protocol.TypeNewConn, ConnID: id}); err != nil {
		if p := s.takePending(id); p != nil {
			writeHTTPError(p.pub, http.StatusBadGateway, "Bad Gateway", "tunnel client unavailable")
		}
	}
}

func (s *Server) writeStatusPage(conn net.Conn) {
	s.mu.RLock()
	count := len(s.tunnels)
	s.mu.RUnlock()
	body := fmt.Sprintf("1master tunnel server\nactive tunnels: %d\n", count)
	fmt.Fprintf(conn,
		"HTTP/1.1 200 OK\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		len(body), body)
	conn.Close()
}

// ---------------- Data plane ----------------

// serveDataConn pairs a client-dialed data connection with its waiting public
// connection and pipes bytes between them.
func (s *Server) serveDataConn(data net.Conn) {
	idBytes := make([]byte, protocol.ConnIDSize)
	_ = data.SetReadDeadline(time.Now().Add(pendingConnTTL))
	if _, err := io.ReadFull(data, idBytes); err != nil {
		data.Close()
		return
	}
	_ = data.SetReadDeadline(time.Time{})

	id := hex.EncodeToString(idBytes)
	p := s.takePending(id)
	if p == nil {
		// Unknown/expired/duplicate id.
		data.Close()
		return
	}

	// Replay the bytes we already read from the public side so the client's
	// local service receives the full, original request.
	if len(p.initial) > 0 {
		if _, err := data.Write(p.initial); err != nil {
			p.pub.Close()
			data.Close()
			return
		}
	}

	protocol.Bind(p.pub, data)
}

// takePending atomically removes and returns a pending connection.
func (s *Server) takePending(id string) *pendingConn {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	p, ok := s.pending[id]
	if !ok {
		return nil
	}
	delete(s.pending, id)
	return p
}

// ---------------- Bootstrap ----------------

func listen(port string, tlsCfg *tls.Config) (net.Listener, error) {
	if tlsCfg != nil {
		return tls.Listen("tcp", ":"+port, tlsCfg)
	}
	return net.Listen("tcp", ":"+port)
}

func acceptLoop(name string, ln net.Listener, handler func(net.Conn)) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("[%s] accept error: %v", name, err)
			return
		}
		go handler(conn)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	domain := envOr("TUNNEL_DOMAIN", "localhost")
	controlPort := envOr("TUNNEL_PORT", "9000")
	dataPort := envOr("TUNNEL_DATA_PORT", "9001")
	httpPort := envOr("HTTP_PORT", "8080")
	authURL := envOr("BACKEND_AUTH_URL", "http://localhost:8000/api/v1/tunnel/auth")

	// Optional TLS for the client<->server control and data links, so the
	// service token and proxied traffic are not sent in cleartext.
	var tlsCfg *tls.Config
	tlsCert, tlsKey := os.Getenv("TUNNEL_TLS_CERT"), os.Getenv("TUNNEL_TLS_KEY")
	if tlsCert != "" && tlsKey != "" {
		cert, err := tls.LoadX509KeyPair(tlsCert, tlsKey)
		if err != nil {
			log.Fatalf("failed to load TLS cert/key: %v", err)
		}
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}

	server := NewServer(domain, dataPort, authURL)

	controlLn, err := listen(controlPort, tlsCfg)
	if err != nil {
		log.Fatalf("control listener on :%s: %v", controlPort, err)
	}
	dataLn, err := listen(dataPort, tlsCfg)
	if err != nil {
		log.Fatalf("data listener on :%s: %v", dataPort, err)
	}
	// Public traffic arrives from Caddy (which terminates TLS), so it is plain.
	publicLn, err := net.Listen("tcp", ":"+httpPort)
	if err != nil {
		log.Fatalf("public listener on :%s: %v", httpPort, err)
	}

	tlsState := "plaintext — set TUNNEL_TLS_CERT/KEY to enable TLS"
	if tlsCfg != nil {
		tlsState = "TLS"
	}
	log.Printf("control :%s (%s)", controlPort, tlsState)
	log.Printf("data    :%s (%s)", dataPort, tlsState)
	log.Printf("public  :%s (plaintext, front with Caddy for TLS)", httpPort)
	log.Printf("domain  %s", domain)
	log.Printf("auth    %s", authURL)

	go acceptLoop("control", controlLn, server.handleControlConn)
	go acceptLoop("data", dataLn, server.serveDataConn)
	acceptLoop("public", publicLn, server.servePublicConn)
}
