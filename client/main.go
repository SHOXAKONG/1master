package main

import (
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"mytunnel/protocol"
)

// errKicked means a newer connection took over this subdomain (most likely
// this same client reconnecting from another terminal or after a crash).
// It is terminal: reconnecting would just race the new connection to
// preempt it right back.
var errKicked = errors.New("kicked: another connection took over this subdomain")

// tlsOptions controls whether the client dials the tunnel server over TLS.
type tlsOptions struct {
	enabled    bool
	skipVerify bool
}

// defaultServer is overridable at build time via:
//
//	go build -ldflags="-X main.defaultServer=1master.uz:9000"
//
// so dev builds can stay pointed at localhost while release builds ship
// pointed at production.
var defaultServer = "1master.uz:9000"

const defaultConfigPath = "" // empty == don't read

// Config is the persistent client configuration.
type Config struct {
	Server string `json:"server,omitempty"`
	Token  string `json:"token,omitempty"`
	TLS    bool   `json:"tls,omitempty"`
}

// tunnelSpec is one local port to forward, and the reserved subdomain label
// it should publish on ("" means "let the server pick my default").
type tunnelSpec struct {
	Label string
	Port  int
}

func main() {
	log.SetFlags(log.Ltime)

	if len(os.Args) < 2 {
		printUsageAndExit()
	}

	switch os.Args[1] {
	case "http":
		runHTTP(os.Args[2:])
	case "auth":
		runAuth(os.Args[2:])
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		os.Exit(0)
	case "version", "-v", "--version":
		fmt.Println("1master client v0.1.0")
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %q\n\n", os.Args[1])
		printUsageAndExit()
	}
}

func runAuth(args []string) {
	if len(args) == 0 {
		cfg := loadConfigFile("")
		if cfg.Token == "" {
			fmt.Println("Not authenticated.")
			fmt.Println("Run: 1master auth <your-token>")
			os.Exit(1)
		}
		fmt.Printf("✅ Authenticated (token: %s…%s)\n", cfg.Token[:6], cfg.Token[len(cfg.Token)-4:])
		return
	}
	if len(args) > 1 {
		fmt.Fprintln(os.Stderr, "Usage: 1master auth <token>")
		os.Exit(2)
	}
	token := strings.TrimSpace(args[0])
	if token == "" {
		fmt.Fprintln(os.Stderr, "Empty token.")
		os.Exit(2)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Could not resolve home directory: %v", err)
	}
	dir := home + "/.1master"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Fatalf("Could not create %s: %v", dir, err)
	}
	path := dir + "/config.json"

	// Preserve any existing server setting; just update the token.
	existing := loadConfigFile(path)
	existing.Token = token

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		log.Fatalf("Could not encode config: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		log.Fatalf("Could not write %s: %v", path, err)
	}
	fmt.Printf("✅ Auth token saved to %s\n", path)
	fmt.Println("Now run: 1master http <port>")
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `1master — expose local ports at subdomains you reserve in the dashboard

USAGE:
  1master http <port>                       # your default reserved subdomain
  1master http <label>=<port>               # a specific reserved subdomain
  1master http <label>=<port> <label>=<port> ...   # several at once, one process

EXAMPLES:
  1master http 8000                       # -> https://<your default subdomain>.1master.uz
  1master http web=3000                   # -> https://web.1master.uz (must be reserved first)
  1master http web=3000 api=8000          # two tunnels at once, one auth
  1master http 8080 --token YOUR_TOKEN
  1master http 8080 --server tunnel.1master.uz:9000

  Reserve subdomains (up to 5, unique) at https://1master.uz/dashboard before
  using them here — the server rejects any label you haven't reserved.

FLAGS:
  --token     <token>   Service token. Falls back to MYTUNNEL_TOKEN env var
                        or ~/.1master/config.json.
  --server    <addr>    Tunnel server address (default: 1master.uz:9000).
  --subdomain <label>   Reserved subdomain to use (single-tunnel form only;
                        for several at once use "<label>=<port>" instead).
  --config    <path>    Path to JSON config file (default: ~/.1master/config.json).
  --tls                 Dial the tunnel server over TLS (recommended in prod;
                        can also be set with "tls": true in config.json).
  --tls-skip-verify     Skip TLS certificate verification (self-signed servers).

  Every request that arrives is printed live to the terminal (method + path)
  as a lightweight event stream, per tunnel — no extra flag needed.

COMMANDS:
  http       Start one or more HTTP tunnels for local ports.
  auth       Save your service token to ~/.1master/config.json.
  version    Print client version.
  help       Show this message.
`)
}

func printUsageAndExit() {
	printUsage(os.Stderr)
	os.Exit(2)
}

// parseTunnelSpecs turns the positional args after "http" into tunnelSpecs.
// A single bare port is the legacy single-tunnel form (subdomain comes from
// --subdomain / the account default). Two or more args, or any arg
// containing "=", switches to multi-tunnel mode where every arg must be
// "<label>=<port>" so each tunnel's subdomain is unambiguous.
func parseTunnelSpecs(args []string, flagSubdomain string) ([]tunnelSpec, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("missing port. Usage: 1master http <port>")
	}

	multi := len(args) > 1
	for _, a := range args {
		if strings.Contains(a, "=") {
			multi = true
		}
	}

	if !multi {
		port, err := parsePort(args[0])
		if err != nil {
			return nil, err
		}
		return []tunnelSpec{{Label: strings.TrimSpace(flagSubdomain), Port: port}}, nil
	}

	if flagSubdomain != "" {
		return nil, fmt.Errorf("--subdomain can't be combined with multiple tunnels; use \"<label>=<port>\" for each instead")
	}

	specs := make([]tunnelSpec, 0, len(args))
	seen := make(map[string]bool, len(args))
	for _, a := range args {
		label, portStr, ok := strings.Cut(a, "=")
		if !ok || label == "" || portStr == "" {
			return nil, fmt.Errorf("invalid tunnel %q — multi-tunnel args must look like \"<label>=<port>\", e.g. \"web=3000\"", a)
		}
		port, err := parsePort(portStr)
		if err != nil {
			return nil, err
		}
		label = strings.ToLower(strings.TrimSpace(label))
		if seen[label] {
			return nil, fmt.Errorf("duplicate subdomain %q", label)
		}
		seen[label] = true
		specs = append(specs, tunnelSpec{Label: label, Port: port})
	}
	return specs, nil
}

func parsePort(s string) (int, error) {
	port, err := strconv.Atoi(s)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("invalid port %q — must be 1–65535", s)
	}
	return port, nil
}

func runHTTP(args []string) {
	fs := flag.NewFlagSet("http", flag.ExitOnError)
	fs.Usage = func() { printUsage(os.Stderr) }
	token := fs.String("token", "", "Service token (or set MYTUNNEL_TOKEN)")
	serverAddr := fs.String("server", "", "Tunnel server address")
	subdomain := fs.String("subdomain", "", "Reserved subdomain to use (single-tunnel form only)")
	configPath := fs.String("config", "", "Config file path (default ~/.1master/config.json)")
	useTLS := fs.Bool("tls", false, "Dial the tunnel server over TLS")
	tlsSkipVerify := fs.Bool("tls-skip-verify", false, "Skip TLS certificate verification (self-signed servers)")

	// Positional ports/specs must come before flags (ngrok-style):
	// `1master http 8000 --token X` or `1master http web=3000 api=8000 --token X`.
	splitAt := len(args)
	for i, a := range args {
		if strings.HasPrefix(a, "-") {
			splitAt = i
			break
		}
	}
	positional, flagArgs := args[:splitAt], args[splitAt:]
	if err := fs.Parse(flagArgs); err != nil {
		os.Exit(2)
	}

	specs, err := parseTunnelSpecs(positional, *subdomain)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(2)
	}

	// Resolve config: flag > env > config-file > default.
	fileCfg := loadConfigFile(*configPath)

	resolvedServer := firstNonEmpty(*serverAddr, fileCfg.Server, defaultServer)
	resolvedToken := firstNonEmpty(*token, os.Getenv("MYTUNNEL_TOKEN"), fileCfg.Token)
	resolvedTLS := &tlsOptions{
		enabled:    *useTLS || fileCfg.TLS,
		skipVerify: *tlsSkipVerify,
	}

	if resolvedToken == "" {
		fmt.Fprint(os.Stderr, `❌ Missing service token.

Provide it via one of:
  --token <token>
  MYTUNNEL_TOKEN environment variable
  ~/.1master/config.json   ({"token": "..."})
`)
		os.Exit(2)
	}

	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════╗")
	fmt.Println("║              🚇 1master Client                  ║")
	fmt.Println("╠══════════════════════════════════════════════════╣")
	fmt.Printf("║  Server:  %s\n", resolvedServer)
	for _, spec := range specs {
		label := spec.Label
		if label == "" {
			label = "(default)"
		}
		fmt.Printf("║  Forward: localhost:%-6d -> %s\n", spec.Port, label)
	}
	fmt.Println("╚══════════════════════════════════════════════════╝")
	fmt.Println()

	var wg sync.WaitGroup
	for _, spec := range specs {
		wg.Add(1)
		go func(spec tunnelSpec) {
			defer wg.Done()
			runTunnelWithRetry(resolvedServer, spec, resolvedToken, resolvedTLS)
		}(spec)
	}
	wg.Wait()
}

// runTunnelWithRetry keeps one tunnel connected, reconnecting on transient
// errors, until a fatal auth error ends it for good. Each tunnel spec gets
// its own independent retry loop, so one dying doesn't affect the others.
func runTunnelWithRetry(serverAddr string, spec tunnelSpec, token string, tlsCfg *tlsOptions) {
	localAddr := fmt.Sprintf("localhost:%d", spec.Port)
	logLabel := spec.Label
	if logLabel == "" {
		logLabel = fmt.Sprintf(":%d", spec.Port)
	}

	for {
		if err := runTunnel(serverAddr, localAddr, token, spec.Label, logLabel, tlsCfg); err != nil {
			log.Printf("[%s] connection lost: %v", logLabel, err)
			if isFatalAuthError(err) {
				log.Printf("[%s] auth error — not reconnecting.", logLabel)
				return
			}
			if errors.Is(err, errKicked) {
				log.Printf("[%s] another connection took over this subdomain — not reconnecting.", logLabel)
				return
			}
			log.Printf("[%s] reconnecting in 3 seconds...", logLabel)
			time.Sleep(3 * time.Second)
		}
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// loadConfigFile reads JSON config from `path` if given, else from
// ~/.1master/config.json. Missing file is not an error.
func loadConfigFile(path string) Config {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Config{}
		}
		path = home + "/.1master/config.json"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("Warning: ignoring malformed config %s: %v", path, err)
		return Config{}
	}
	return cfg
}

func isFatalAuthError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unauthorized")
}

// dial opens a TCP (or TLS) connection to addr.
func dial(addr string, tlsCfg *tlsOptions) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if !tlsCfg.enabled {
		return dialer.Dial("tcp", addr)
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: tlsCfg.skipVerify, //nolint:gosec // opt-in for self-signed servers
	})
}

func runTunnel(serverAddr, localAddr, token, subdomain, logLabel string, tlsCfg *tlsOptions) error {
	log.Printf("[%s] connecting to %s...", logLabel, serverAddr)

	rawConn, err := dial(serverAddr, tlsCfg)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer rawConn.Close()

	conn := protocol.NewSafeConn(rawConn)
	if err := conn.Send(&protocol.Message{Type: protocol.TypeRegister, Token: token, Subdomain: subdomain}); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	resp, err := protocol.Recv(conn)
	if err != nil {
		return fmt.Errorf("register response: %w", err)
	}
	if resp.Error != "" {
		return fmt.Errorf("registration failed: %s", resp.Error)
	}

	// The client dials this address once per public request.
	host, _, err := net.SplitHostPort(serverAddr)
	if err != nil {
		host = serverAddr
	}
	dataAddr := net.JoinHostPort(host, resp.DataPort)

	log.Printf("[%s] ✅ online: https://%s -> %s", logLabel, resp.Hostname, localAddr)

	stopPing := make(chan struct{})
	defer close(stopPing)
	go sendHeartbeats(conn, stopPing)

	for {
		msg, err := protocol.Recv(conn)
		if err != nil {
			return fmt.Errorf("recv: %w", err)
		}
		switch msg.Type {
		case protocol.TypeNewConn:
			// Live request event stream: print the instant a request arrives,
			// before we even dial the local service.
			log.Printf("[%s] %s %s", logLabel, orDash(msg.Method), orDash(msg.Path))
			go handleConn(dataAddr, localAddr, msg.ConnID, tlsCfg)
		case protocol.TypeKicked:
			return errKicked
		}
	}
}

// sendHeartbeats periodically pings the control connection so the server
// knows this client is still alive; see protocol.ControlIdleTimeout. It
// self-terminates the moment a send fails (the connection is dead anyway,
// runTunnel's own Recv will notice next) or stop is closed.
func sendHeartbeats(conn *protocol.SafeConn, stop <-chan struct{}) {
	ticker := time.NewTicker(protocol.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := conn.Send(&protocol.Message{Type: protocol.TypePing}); err != nil {
				return
			}
		}
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// handleConn services one public request: it dials the local service and a
// fresh data connection to the server, identifies itself, then pipes bytes
// raw in both directions. No HTTP parsing happens here, so streaming and
// WebSocket upgrades pass through untouched.
func handleConn(dataAddr, localAddr, connID string, tlsCfg *tlsOptions) {
	idBytes, err := hex.DecodeString(connID)
	if err != nil || len(idBytes) != protocol.ConnIDSize {
		log.Printf("[%s] invalid connection id", connID)
		return
	}

	localConn, err := dialLocal(localAddr)
	if err != nil {
		log.Printf("[%s] local service unreachable: %v", connID, err)
		return
	}
	defer localConn.Close()

	dataConn, err := dial(dataAddr, tlsCfg)
	if err != nil {
		log.Printf("[%s] data connect failed: %v", connID, err)
		return
	}
	defer dataConn.Close()

	if _, err := dataConn.Write(idBytes); err != nil {
		log.Printf("[%s] failed to pair data connection: %v", connID, err)
		return
	}

	protocol.Bind(localConn, dataConn)
}

// dialLocal connects to the user's local service, trying the 127.0.0.1 /
// localhost counterpart as a fallback.
func dialLocal(localAddr string) (net.Conn, error) {
	addrs := []string{localAddr}
	if port := localAddr[strings.LastIndex(localAddr, ":")+1:]; port != localAddr {
		if strings.HasPrefix(localAddr, "localhost:") {
			addrs = append(addrs, "127.0.0.1:"+port)
		} else if strings.HasPrefix(localAddr, "127.0.0.1:") {
			addrs = append(addrs, "localhost:"+port)
		}
	}
	var lastErr error
	for _, addr := range addrs {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
