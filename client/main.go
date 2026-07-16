package main

import (
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"mytunnel/protocol"
)

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

// Config is the persistent client configuration (single tunnel per token).
type Config struct {
	Server string `json:"server,omitempty"`
	Token  string `json:"token,omitempty"`
	TLS    bool   `json:"tls,omitempty"`
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
	fmt.Fprint(w, `1master — expose a local port at <username>.1master.uz

USAGE:
  1master http <port> [flags]

EXAMPLES:
  1master http 8000                       # -> https://<username>.1master.uz
  1master http 3000 --subdomain web       # -> https://web-<username>.1master.uz
  1master http 8080 --token YOUR_TOKEN
  1master http 8080 --server tunnel.1master.uz:9000

  # Run several tunnels at once (each in its own terminal, one per port):
  1master http 8080                        # api on <username>.1master.uz
  1master http 3000 --subdomain web        # web on web-<username>.1master.uz

FLAGS:
  --token     <token>   Service token. Falls back to MYTUNNEL_TOKEN env var
                        or ~/.1master/config.json.
  --server    <addr>    Tunnel server address (default: 1master.uz:9000).
  --subdomain <label>   Custom subdomain label, published as <label>-<username>.
                        Omit it for the first tunnel (uses <username>); required
                        to distinguish additional tunnels running at the same time.
  --config    <path>    Path to JSON config file (default: ~/.1master/config.json).
  --tls                 Dial the tunnel server over TLS (recommended in prod;
                        can also be set with "tls": true in config.json).
  --tls-skip-verify     Skip TLS certificate verification (self-signed servers).

COMMANDS:
  http       Start an HTTP tunnel for a local port.
  auth       Save your service token to ~/.1master/config.json.
  version    Print client version.
  help       Show this message.
`)
}

func printUsageAndExit() {
	printUsage(os.Stderr)
	os.Exit(2)
}

func runHTTP(args []string) {
	fs := flag.NewFlagSet("http", flag.ExitOnError)
	fs.Usage = func() { printUsage(os.Stderr) }
	token := fs.String("token", "", "Service token (or set MYTUNNEL_TOKEN)")
	serverAddr := fs.String("server", "", "Tunnel server address")
	subdomain := fs.String("subdomain", "", "Requested subdomain label (published as <label>-<username>)")
	configPath := fs.String("config", "", "Config file path (default ~/.1master/config.json)")
	useTLS := fs.Bool("tls", false, "Dial the tunnel server over TLS")
	tlsSkipVerify := fs.Bool("tls-skip-verify", false, "Skip TLS certificate verification (self-signed servers)")

	// Positional port must come before flags (ngrok-style): `1master http 8000 --token X`.
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "❌ Missing port. Usage: 1master http <port>")
		os.Exit(2)
	}
	portArg := args[0]
	port, err := strconv.Atoi(portArg)
	if err != nil || port <= 0 || port > 65535 {
		fmt.Fprintf(os.Stderr, "❌ Invalid port %q. Must be 1–65535.\n", portArg)
		os.Exit(2)
	}
	if err := fs.Parse(args[1:]); err != nil {
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

	localAddr := fmt.Sprintf("localhost:%d", port)
	resolvedSubdomain := strings.TrimSpace(*subdomain)

	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════╗")
	fmt.Println("║              🚇 1master Client                  ║")
	fmt.Println("╠══════════════════════════════════════════════════╣")
	fmt.Printf("║  Server:    %s\n", resolvedServer)
	fmt.Printf("║  Forwards:  %s\n", localAddr)
	if resolvedSubdomain != "" {
		fmt.Printf("║  Subdomain: %s-<username>.1master.uz\n", resolvedSubdomain)
	} else {
		fmt.Println("║  Subdomain: <username>.1master.uz")
	}
	fmt.Println("╚══════════════════════════════════════════════════╝")
	fmt.Println()

	for {
		if err := runTunnel(resolvedServer, localAddr, resolvedToken, resolvedSubdomain, resolvedTLS); err != nil {
			log.Printf("Connection lost: %v", err)
			if isFatalAuthError(err) {
				log.Printf("Auth error — not reconnecting.")
				return
			}
			log.Printf("Reconnecting in 3 seconds...")
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

func runTunnel(serverAddr, localAddr, token, subdomain string, tlsCfg *tlsOptions) error {
	log.Printf("Connecting to %s...", serverAddr)

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

	log.Printf("✅ Online: https://%s -> %s", resp.Hostname, localAddr)

	for {
		msg, err := protocol.Recv(conn)
		if err != nil {
			return fmt.Errorf("recv: %w", err)
		}
		if msg.Type == protocol.TypeNewConn {
			go handleConn(dataAddr, localAddr, msg.ConnID, tlsCfg)
		}
	}
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
