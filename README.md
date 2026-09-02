# 🚇 1master

Expose your local server to the internet at a permanent
`<username>.1master.uz` URL — no Go, no Docker, no config files.

## What's 1master?

- 1master is a free, self-hosted tunnel for exposing local servers to the
  public internet.
- Reserve up to 5 subdomains on the [dashboard](https://1master.uz/dashboard)
  — each name is globally unique and yours forever once claimed. Your
  username is reserved automatically on signup.
- Run several tunnels at once, from a single process and a single service
  token — no `-<username>` suffix, the name you reserve is the name you get.
- Every incoming request is printed live to your terminal (method + path) as
  a lightweight built-in request log.
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
  Server:   1master.uz:9000
  Forward:  localhost:3000 -> (default)

[:3000] ✅ online: https://shohruh.1master.uz -> localhost:3000
```

Your local service is now reachable at `https://<your-subdomain>.1master.uz`.
This only works for a subdomain you've already reserved on the dashboard —
signing up reserves your username automatically.

### 4. Run several tunnels at once

One process, one service token, several reserved subdomains — no need for a
terminal per tunnel:

```bash
1master http web=3000 api=8000   # https://web.1master.uz + https://api.1master.uz
```

Prefer one at a time? `1master http <port> --subdomain <label>` still works —
`<label>` must already be reserved. A subdomain label is 1–63 lowercase
letters, digits or hyphens.

Lost your token, or want to invalidate the running session? Rotate from the
dashboard — old tokens stop working immediately.

## Commands

| Command                          | Description                                             |
|----------------------------------|-----------------------------------------------------------|
| `1master http <port>`            | Expose a local HTTP port on your default reserved subdomain. |
| `1master http <label>=<port> …`  | Expose one or more ports on specific reserved subdomains, at once. |
| `1master auth <token>` | Save your service token to `~/.1master/config.json`.    |
| `1master auth`         | Print whether you're authenticated (token is masked).   |
| `1master version`      | Print the CLI version.                                  |
| `1master help`         | Show usage.                                             |

## Flags

| Flag       | Env              | Description                                                            |
|------------|------------------|------------------------------------------------------------------------|
| `--token`     | `MYTUNNEL_TOKEN` | Service token. Falls back to `~/.1master/config.json`.              |
| `--server`    | —                | Tunnel server address. Default: `1master.uz:9000`.                  |
| `--subdomain` | —                | Reserved subdomain to use (single-tunnel form only).                |
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
   under one of the authenticated user's reserved subdomains.
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
