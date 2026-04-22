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
	"strings"
	"sync"
	"time"

	"mytunnel/protocol"
)

// TunnelConfig represents a single tunnel mapping
type TunnelConfig struct {
	Subdomain string `json:"subdomain"`
	Port      int    `json:"port"`
}

// Config is the client configuration
type Config struct {
	Server  string         `json:"server"`
	Tunnels []TunnelConfig `json:"tunnels"`
}

func main() {
	configFile := flag.String("config", "", "Path to config file (JSON)")
	serverAddr := flag.String("server", "localhost:9000", "Tunnel server address")
	localPort := flag.Int("port", 3000, "Local port to expose")
	subdomain := flag.String("subdomain", "", "Static subdomain (required)")
	flag.Parse()

	// If config file provided, use it for multiple tunnels
	if *configFile != "" {
		runFromConfig(*configFile)
		return
	}

	// Single tunnel mode
	if *subdomain == "" {
		fmt.Println("⚠️  No subdomain specified. Use -subdomain to set a static one.")
		fmt.Println("   Example: go run . -subdomain myapp -port 3000")
		fmt.Println("   Or use a config file for multiple tunnels:")
		fmt.Println("   go run . -config tunnels.json")
		fmt.Println()
	}

	localAddr := fmt.Sprintf("localhost:%d", *localPort)

	for {
		if err := runTunnel(*serverAddr, localAddr, *subdomain); err != nil {
			log.Printf("[%s] Connection lost: %v", *subdomain, err)
			log.Printf("[%s] Reconnecting in 3 seconds...", *subdomain)
			time.Sleep(3 * time.Second)
		}
	}
}

func runFromConfig(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("Failed to read config: %v", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		log.Fatalf("Failed to parse config: %v", err)
	}

	if len(config.Tunnels) == 0 {
		log.Fatal("No tunnels defined in config")
	}

	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════╗")
	fmt.Println("║              🚇 MyTunnel Client                 ║")
	fmt.Println("╠══════════════════════════════════════════════════╣")
	for _, t := range config.Tunnels {
		fmt.Printf("║  %s -> localhost:%d\n", t.Subdomain, t.Port)
	}
	fmt.Println("╚══════════════════════════════════════════════════╝")
	fmt.Println()

	var wg sync.WaitGroup
	for _, t := range config.Tunnels {
		wg.Add(1)
		go func(tunnel TunnelConfig) {
			defer wg.Done()
			localAddr := fmt.Sprintf("localhost:%d", tunnel.Port)
			for {
				if err := runTunnel(config.Server, localAddr, tunnel.Subdomain); err != nil {
					log.Printf("[%s] Connection lost: %v", tunnel.Subdomain, err)
					log.Printf("[%s] Reconnecting in 3 seconds...", tunnel.Subdomain)
					time.Sleep(3 * time.Second)
				}
			}
		}(t)
	}
	wg.Wait()
}

func runTunnel(serverAddr, localAddr, subdomain string) error {
	log.Printf("[%s] Connecting to %s...", subdomain, serverAddr)
	conn, err := net.DialTimeout("tcp", serverAddr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	err = protocol.Send(conn, &protocol.Message{
		Type:      protocol.TypeRegister,
		Subdomain: subdomain,
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

	log.Printf("[%s] ✅ Online -> %s", resp.Subdomain, localAddr)

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
