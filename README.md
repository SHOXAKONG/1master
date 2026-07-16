# 🚇 1master

Expose your local server to the internet at a permanent
`<username>.1master.uz` URL — no Go, no Docker, no config files.

## What's 1master?

- 1master is a free, self-hosted tunnel for exposing local servers to the
  public internet.
- Your username is your default subdomain (`<username>.1master.uz`) — forever
  yours, never changes across restarts, reboots, or network swaps.
- Run many tunnels at once. Give each a custom subdomain with `--subdomain`;
  it's published as `<label>-<username>.1master.uz`, so no two users collide.
- Authenticated. Service tokens issued from the dashboard, validated on every
  registration.

## How to install

### macOS and Linux

```bash
curl -fsSL https://1master.uz/install.sh | sh
```

Installs `/usr/local/bin/1master`. Detects `darwin`/`linux` × `amd64`/`arm64`
automatically. Uses `sudo` only if `/usr/local/bin` isn't writable.

Custom install location:

```bash
ONEMASTER_PREFIX=$HOME/.local/bin sh -c "curl -fsSL https://1master.uz/install.sh | sh"
```

### Windows

Not packaged yet. Build from source:

```powershell
git clone https://github.com/SHOXAKONG/1master
cd 1master
go build -o 1master.exe ./client
```

## How to use

### 1. Register and grab your service token

Sign up at [https://1master.uz/register](https://1master.uz/register). Verify
your email. The verify page shows your **service token once** — copy it.

### 2. Authenticate the CLI

```bash
1master auth <your-service-token>
```

Stored at `~/.1master/config.json` with `chmod 600`.

### 3. Expose a local port

```bash
1master http 3000
```

```text
🚇 1master Client
  Server:    1master.uz:9000
  Forwards:  localhost:3000
  Subdomain: <your-username>.1master.uz

✅ Online: shohruh -> localhost:3000
```

Your local service is now reachable at `https://<your-username>.1master.uz`.

### 4. Run several tunnels at once

Each tunnel is its own process (one per terminal). The first can use your bare
username; give the others a `--subdomain` so they get distinct URLs:

```bash
1master http 8080                   # https://<username>.1master.uz
1master http 3000 --subdomain web   # https://web-<username>.1master.uz
1master http 9000 --subdomain api   # https://api-<username>.1master.uz
```

A subdomain label is 1–30 lowercase letters, digits or hyphens. They all share
the same service token.

Lost your token, or want to invalidate the running session? Rotate from the
dashboard — old tokens stop working immediately.

## Commands

| Command                | Description                                             |
|------------------------|---------------------------------------------------------|
| `1master http <port>`  | Expose a local HTTP port at `<username>.1master.uz`.    |
| `1master auth <token>` | Save your service token to `~/.1master/config.json`.    |
| `1master auth`         | Print whether you're authenticated (token is masked).   |
| `1master version`      | Print the CLI version.                                  |
| `1master help`         | Show usage.                                             |

## Flags

| Flag       | Env              | Description                                                            |
|------------|------------------|------------------------------------------------------------------------|
| `--token`     | `MYTUNNEL_TOKEN` | Service token. Falls back to `~/.1master/config.json`.              |
| `--server`    | —                | Tunnel server address. Default: `1master.uz:9000`.                  |
| `--subdomain` | —                | Custom subdomain label, published as `<label>-<username>`.          |
| `--config`    | —                | Path to JSON config file. Default: `~/.1master/config.json`.        |

Precedence for every value: **CLI flag → env var → config file → default**.

## Self-host

Want to run your own instance? The full deployment guide is at
[DEPLOYMENT.md](DEPLOYMENT.md) — covers the FastAPI backend, Vue dashboard,
Go tunnel server, Caddy with auto-TLS, and full CI/CD via GitHub Actions.

## How it works

```
Internet user                  VPS (1master.uz)               Your laptop
     │                                │                              │
     │ GET shohruh.1master.uz   ┌─────┴──────┐  raw TCP    ┌────────┴────┐
     │ ─────────────────────►  │ Caddy :443 │             │ 1master CLI │
     │                          │  + Tunnel  │ ◄═════════ │             │
     │ ◄─────────────────────  │  server    │ persistent  │             │
     │      HTTP response       └────────────┘    9000     └──────┬──────┘
                                                                  │
                                                            localhost:3000
```

1. **CLI** opens a long-lived TCP connection to the tunnel server, registers
   under the authenticated user's username.
2. **Caddy** receives an HTTP request for `<username>.1master.uz`, forwards to
   the tunnel server.
3. **Tunnel server** looks up the active subscription, serializes the request,
   forwards it through the TCP tunnel.
4. **CLI** dials your local port, replays the request, returns the response
   back through the tunnel.
5. **Tunnel server** writes the response back to the original visitor.

## Architecture

This repo is the Go tunnel server + CLI client. It pairs with two sibling
repos for the dashboard and API:

| Repo                                                                                                | What's in it                  |
|-----------------------------------------------------------------------------------------------------|-------------------------------|
| [`SHOXAKONG/1master`](https://github.com/SHOXAKONG/1master)                                          | Go tunnel server + CLI client |
| [`SHOXAKONG/1master-project-backend`](https://github.com/SHOXAKONG/1master-project-backend)          | FastAPI auth + token issuing  |
| [`SHOXAKONG/1master-project-frontend`](https://github.com/SHOXAKONG/1master-project-frontend)       | Vue dashboard + docs site     |

## License

MIT.
