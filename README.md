# 🚇 MyTunnel — My Own ngrok

A minimal self-hosted tunnel that exposes your local services to the internet with **static subdomains** that never change.

## Architecture

```
Internet User                    Your VPS                     Your Laptop
     |                              |                              |
     |   GET myapp.example.com +---------+   TCP tunnel     +-----------+
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

**Single tunnel with a static subdomain:**

```bash
./mytunnel-client -server your-vps-ip:9000 -port 3000 -subdomain myapp
```

Your app is now live at `myapp.tunnel.yourdomain.com` — restart all you want, same URL every time.

**Multiple tunnels at once with a config file:**

```bash
./mytunnel-client -config tunnels.json
```

### 4. Test Locally (no VPS needed)

```bash
# Terminal 1: Start a simple local web server
python3 -m http.server 3000

# Terminal 2: Start the tunnel server
cd server && go run .

# Terminal 3: Start the tunnel client with a static subdomain
cd client && go run . -subdomain myapp -port 3000

# Terminal 4: Test it
curl -H "Host: myapp.localhost" http://localhost:8080
```

## Configuration

### Server Environment Variables

| Variable        | Default     | Description                     |
|----------------|-------------|---------------------------------|
| `TUNNEL_DOMAIN`| `localhost` | Your tunnel domain              |
| `TUNNEL_PORT`  | `9000`      | Port for client TCP connections |
| `HTTP_PORT`    | `8080`      | Port for public HTTP traffic    |

### Client Flags

| Flag         | Default          | Description                          |
|-------------|------------------|--------------------------------------|
| `-server`   | `localhost:9000` | Tunnel server address                |
| `-port`     | `3000`           | Local port to expose                 |
| `-subdomain`| *(random)*       | Static subdomain (same on reconnect) |
| `-config`   | *(none)*         | Path to JSON config for multi-tunnel |

### Multi-Tunnel Config File

Create a `tunnels.json` to expose multiple local services at once:

```json
{
  "server": "your-vps-ip:9000",
  "tunnels": [
    { "subdomain": "frontend", "port": 3000 },
    { "subdomain": "api",      "port": 8000 },
    { "subdomain": "admin",    "port": 5173 }
  ]
}
```

Run with:

```bash
./mytunnel-client -config tunnels.json
```

This creates three permanent tunnels in one command:
- `frontend.tunnel.yourdomain.com` → `localhost:3000`
- `api.tunnel.yourdomain.com` → `localhost:8000`
- `admin.tunnel.yourdomain.com` → `localhost:5173`

All subdomains are static — they stay the same across restarts and reconnects.

## How It Works

1. **Client** connects to the **Server** via TCP and registers a tunnel with a static subdomain
2. **Server** reserves that subdomain (e.g., `myapp`)
3. When someone visits `myapp.tunnel.yourdomain.com`:
   - Server receives the HTTP request
   - Serializes it and sends through the TCP tunnel to the Client
   - Client forwards the request to your local service (e.g., `localhost:3000`)
   - Client reads the response and sends it back through the tunnel
   - Server writes the response back to the visitor
4. If the client disconnects and reconnects, it reclaims the same subdomain

## Production Setup with Caddy

Put Caddy in front of the tunnel server for automatic HTTPS:

```
*.tunnel.yourdomain.com {
    reverse_proxy localhost:8080
    tls {
        dns cloudflare {env.CF_API_TOKEN}
    }
}
```

## What's Next?

To make this even more production-ready, you could add:

- **Authentication** — tokens so only authorized clients can create tunnels
- **WebSocket support** — upgrade tunnel to WebSocket for better reliability
- **Connection multiplexing** — use yamux/smux for multiple streams over one conn
- **Dashboard** — web UI to see active tunnels
- **Rate limiting** — protect against abuse
- **TCP tunnels** — support raw TCP, not just HTTP