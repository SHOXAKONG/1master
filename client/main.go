package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"mytunnel/protocol"
)

const (
	defaultServer     = "localhost:9000"
	defaultConfigPath = "" // empty == don't read
)

// Config is the persistent client configuration (single tunnel per token).
type Config struct {
	Server string `json:"server,omitempty"`
	Token  string `json:"token,omitempty"`
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
  1master http 8000
  1master http 3000 --token YOUR_TOKEN
  1master http 8080 --server tunnel.1master.uz:9000

FLAGS:
  --token   <token>   Service token. Falls back to MYTUNNEL_TOKEN env var
                      or ~/.1master/config.json.
  --server  <addr>    Tunnel server address (default: localhost:9000).
  --config  <path>    Path to JSON config file (default: ~/.1master/config.json).

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
	configPath := fs.String("config", "", "Config file path (default ~/.1master/config.json)")

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

	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════╗")
	fmt.Println("║              🚇 1master Client                  ║")
	fmt.Println("╠══════════════════════════════════════════════════╣")
	fmt.Printf("║  Server:    %s\n", resolvedServer)
	fmt.Printf("║  Forwards:  %s\n", localAddr)
	fmt.Println("║  Subdomain: <your-username>.1master.uz           ║")
	fmt.Println("╚══════════════════════════════════════════════════╝")
	fmt.Println()

	for {
		if err := runTunnel(resolvedServer, localAddr, resolvedToken); err != nil {
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

func runTunnel(serverAddr, localAddr, token string) error {
	log.Printf("Connecting to %s...", serverAddr)
	conn, err := net.DialTimeout("tcp", serverAddr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	err = protocol.Send(conn, &protocol.Message{
		Type:  protocol.TypeRegister,
		Token: token,
	})
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}

	resp, err := protocol.Recv(conn)
	if err != nil {
		return fmt.Errorf("register response: %w", err)
	}

	if resp.Error != "" {
		return fmt.Errorf("registration failed: %s", resp.Error)
	}

	log.Printf("✅ Online: %s -> %s", resp.Subdomain, localAddr)

	for {
		msg, err := protocol.Recv(conn)
		if err != nil {
			return fmt.Errorf("recv: %w", err)
		}

		if msg.Type == protocol.TypeProxy {
			go handleProxyRequest(conn, msg, localAddr)
		}
	}
}

func handleProxyRequest(tunnelConn net.Conn, msg *protocol.Message, localAddr string) {
	reader := bufio.NewReader(strings.NewReader(string(msg.Data)))
	req, err := http.ReadRequest(reader)
	if err != nil {
		log.Printf("[%s] failed to parse request: %v", msg.ID, err)
		sendError(tunnelConn, msg.ID, "Failed to parse request")
		return
	}

	addrs := []string{localAddr}
	port := localAddr[strings.LastIndex(localAddr, ":")+1:]
	if strings.HasPrefix(localAddr, "localhost:") {
		addrs = append(addrs, "127.0.0.1:"+port)
	} else if strings.HasPrefix(localAddr, "127.0.0.1:") {
		addrs = append(addrs, "localhost:"+port)
	}

	var localConn net.Conn
	var connErr error
	for _, addr := range addrs {
		localConn, connErr = net.DialTimeout("tcp", addr, 5*time.Second)
		if connErr == nil {
			break
		}
	}
	if connErr != nil {
		log.Printf("[%s] local service unavailable on port %s: %v", msg.ID, port, connErr)
		sendError(tunnelConn, msg.ID, fmt.Sprintf("Local service on port %s is not reachable", port))
		return
	}
	defer localConn.Close()

	err = req.Write(localConn)
	if err != nil {
		log.Printf("[%s] failed to write to local: %v", msg.ID, err)
		sendError(tunnelConn, msg.ID, "Failed to forward request")
		return
	}

	localReader := bufio.NewReader(localConn)
	resp, err := http.ReadResponse(localReader, req)
	if err != nil {
		log.Printf("[%s] failed to read local response: %v", msg.ID, err)
		sendError(tunnelConn, msg.ID, "Failed to read response from local service")
		return
	}
	defer resp.Body.Close()

	var respBuf strings.Builder
	fmt.Fprintf(&respBuf, "%s %s\r\n", resp.Proto, resp.Status)
	for key, vals := range resp.Header {
		for _, val := range vals {
			fmt.Fprintf(&respBuf, "%s: %s\r\n", key, val)
		}
	}
	fmt.Fprintf(&respBuf, "\r\n")

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	respBytes := append([]byte(respBuf.String()), bodyBytes...)

	log.Printf("[%s] %s %s -> %s", msg.ID, req.Method, req.URL.Path, resp.Status)

	protocol.Send(tunnelConn, &protocol.Message{
		Type: protocol.TypeProxyResp,
		ID:   msg.ID,
		Data: respBytes,
	})
}

func sendError(conn net.Conn, id, errMsg string) {
	protocol.Send(conn, &protocol.Message{
		Type:  protocol.TypeProxyResp,
		ID:    id,
		Error: errMsg,
	})
}
