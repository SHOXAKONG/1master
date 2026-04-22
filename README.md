# 🚇 MyTunnel — My Own ngrok

A minimal self-hosted tunnel that exposes your local services to the internet.

## Architecture

```
Internet User                    Your VPS                     Your Laptop
     |                              |                              |
     |   GET foo.example.com   +---------+   TCP tunnel     +-----------+
     |  ---------------------->| Server  |<================>|  Client   |
     |  <----------------------| :8080   |   (port 9000)    |           |
     |    HTML response        +---------+                  +-----+-----+
                                                                  |
                                                           localhost:3000
```

## Quick Start

### 1. Build

```bash
# Build the server (deploy this to your VPS)
cd server && go build -o mytunnel-server .

# Build the client (run this on your laptop)
cd client && go build -o mytunnel-client .
```

### 2. Run the Server (on your VPS)

```bash
# Set your domain and start
export TUNNEL_DOMAIN=tunnel.yourdomain.com
export TUNNEL_PORT=9000    # client connections
export HTTP_PORT=8080      # public HTTP traffic

./mytunnel-server
```

**DNS Setup:** Point `*.tunnel.yourdomain.com` to your VPS IP with a wildcard A record.

### 3. Run the Client (on your laptop)

```bash
# Expose local port 3000
./mytunnel-client -server your-vps-ip:9000 -port 3000

# Or request a specific subdomain
./mytunnel-client -server your-vps-ip:9000 -port 3000 -subdomain myapp
```

### 4. Test Locally (no VPS needed)

```bash
# Terminal 1: Start a simple local web server
python3 -m http.server 3000

# Terminal 2: Start the tunnel server
cd server && go run .

# Terminal 3: Start the tunnel client
cd client && go run . -port 3000

# Terminal 4: Test it (use the subdomain from client output)
curl -H "Host: <subdomain>.localhost" http://localhost:8080
```

## Configuration

### Server Environment Variables

| Variable        | Default     | Description                     |
|----------------|-------------|---------------------------------|
| `TUNNEL_DOMAIN`| `localhost` | Your tunnel domain              |
| `TUNNEL_PORT`  | `9000`      | Port for client TCP connections |
| `HTTP_PORT`    | `8080`      | Port for public HTTP traffic    |

### Client Flags

| Flag         | Default          | Description                    |
|-------------|------------------|--------------------------------|
| `-server`   | `localhost:9000` | Tunnel server address          |
| `-port`     | `3000`           | Local port to expose           |
| `-subdomain`| *(random)*       | Request a specific subdomain   |

## How It Works

1. **Client** connects to the **Server** via TCP and registers a tunnel
2. **Server** assigns a subdomain (e.g., `cool-fox-1234`)
3. When someone visits `cool-fox-1234.yourdomain.com`:
    - Server receives the HTTP request
    - Serializes it and sends through the TCP tunnel to the Client
    - Client forwards the request to your local service (e.g., `localhost:3000`)
    - Client reads the response and sends it back through the tunnel
    - Server writes the response back to the visitor

## What's Next?

To make this production-ready, you could add:

- **TLS/HTTPS** — use Let's Encrypt with wildcard certs
- **Authentication** — tokens so only authorized clients can create tunnels
- **WebSocket support** — upgrade tunnel to WebSocket for better reliability
- **Connection multiplexing** — use yamux/smux for multiple streams over one conn
- **Dashboard** — web UI to see active tunnels
- **Rate limiting** — protect against abuse
- **TCP tunnels** — support raw TCP, not just HTTP