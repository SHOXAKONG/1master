# 1master — Production Deployment Guide

End-to-end deployment of the 1master tunnel service across three GitHub repos
and a single VPS. Every command you need, in the order you run them.

---

## Table of contents

1. [Architecture overview](#1-architecture-overview)
2. [Prerequisites](#2-prerequisites)
3. [DNS records](#3-dns-records)
4. [Generate the deploy SSH key](#4-generate-the-deploy-ssh-key)
5. [Configure GitHub secrets in all three repos](#5-configure-github-secrets-in-all-three-repos)
6. [VPS one-time bootstrap](#6-vps-one-time-bootstrap)
7. [Stage compose files on the VPS](#7-stage-compose-files-on-the-vps)
8. [Fill in `.env` files on the VPS](#8-fill-in-env-files-on-the-vps)
9. [Make GHCR packages pullable](#9-make-ghcr-packages-pullable)
10. [First deploy — push each repo](#10-first-deploy--push-each-repo)
11. [First boot on the VPS](#11-first-boot-on-the-vps)
12. [Cut the CLI release](#12-cut-the-cli-release)
13. [Smoke test end-to-end](#13-smoke-test-end-to-end)
14. [Daily operations](#14-daily-operations)
15. [Troubleshooting](#15-troubleshooting)
16. [Wildcard HTTPS for tunnels](#16-wildcard-https-for-tunnels)

---

## 1. Architecture overview

```
                    DNS (eskiz.uz or Cloudflare)
                              │
                              ▼
                    Caddy :80 / :443 (TLS auto)
        ┌─────────────────────┼─────────────────────┐
        │                     │                     │
   1master.uz           api.1master.uz         *.1master.uz
        ▼                     ▼                     ▼
    frontend             api + worker          tunnel-server
                              │                  (+ TCP :9000
                          postgres                publicly exposed)
                            redis
```

**Three repos**, each with its own `.github/workflows/`:

| Repo                                                              | Image                                | Owns                |
|-------------------------------------------------------------------|--------------------------------------|---------------------|
| [SHOXAKONG/1master-project-backend](https://github.com/SHOXAKONG/1master-project-backend)   | `ghcr.io/shoxakong/1master-api`           | `api`, `worker`     |
| [SHOXAKONG/1master-project-frontend](https://github.com/SHOXAKONG/1master-project-frontend) | `ghcr.io/shoxakong/1master-frontend`      | `frontend`          |
| [SHOXAKONG/1master](https://github.com/SHOXAKONG/1master)                                   | `ghcr.io/shoxakong/1master-tunnel-server` | `tunnel-server`     |

**Orchestration files** (`docker-compose.yml`, `Caddyfile`, `.env`, `backend/.env`)
live at `/opt/1master/` on the VPS — not in any git repo.

---

## 2. Prerequisites

- A VPS with Ubuntu 22.04 / 24.04 (any provider — DigitalOcean, Hetzner, Vultr).
- A domain you control (`1master.uz` in this guide).
- A GitHub account that owns the three repos (`SHOXAKONG`).
- The `gh` CLI installed on your laptop (`brew install gh`) and authenticated
  (`gh auth login`).
- Local clones of all three repos side by side:
  ```
  ~/code/
  ├── 1master-project-backend/
  ├── 1master-project-frontend/
  └── 1master/                    (this repo)
  ```

---

## 3. DNS records

Point three records at your VPS public IP (`95.216.199.114` in this guide):

| Type | Name              | Value             |
|------|-------------------|-------------------|
| A    | `1master.uz`      | `95.216.199.114`  |
| A    | `api.1master.uz`  | `95.216.199.114`  |
| A    | `*.1master.uz`    | `95.216.199.114`  |

Verify after ~5 min:

```bash
dig +short A 1master.uz
dig +short A api.1master.uz
dig +short A foo.1master.uz   # wildcard test
```

All three should return your VPS IP.

---

## 4. Generate the deploy SSH key

On your **laptop**:

```bash
# Generate a dedicated keypair for CI deploys (no passphrase)
ssh-keygen -t ed25519 -C "1master-ci" -f ~/.ssh/1master_deploy -N ""

# Install the public key on the VPS
ssh-copy-id -i ~/.ssh/1master_deploy.pub root@95.216.199.114

# Verify it works (no password prompt)
ssh -i ~/.ssh/1master_deploy root@95.216.199.114 'echo OK'
```

---

## 5. Configure GitHub secrets in all three repos

Same five secrets per repo. Easiest via `gh` CLI:

```bash
for REPO in \
    SHOXAKONG/1master \
    SHOXAKONG/1master-project-backend \
    SHOXAKONG/1master-project-frontend; do
  gh secret set DEPLOY_HOST    --repo "$REPO" --body "95.216.199.114"
  gh secret set DEPLOY_USER    --repo "$REPO" --body "root"
  gh secret set DEPLOY_PORT    --repo "$REPO" --body "22"
  gh secret set DEPLOY_PATH    --repo "$REPO" --body "/opt/1master"
  gh secret set DEPLOY_SSH_KEY --repo "$REPO" < ~/.ssh/1master_deploy
done

# Frontend also needs the API URL (variable, not secret)
gh variable set VITE_API_BASE_URL \
  --repo SHOXAKONG/1master-project-frontend \
  --body "https://api.1master.uz/api/v1"
```

Verify:

```bash
gh secret list --repo SHOXAKONG/1master
gh secret list --repo SHOXAKONG/1master-project-backend
gh secret list --repo SHOXAKONG/1master-project-frontend
gh variable list --repo SHOXAKONG/1master-project-frontend
```

You should see `DEPLOY_HOST`, `DEPLOY_USER`, `DEPLOY_PORT`, `DEPLOY_PATH`,
`DEPLOY_SSH_KEY` in each, plus `VITE_API_BASE_URL` only in the frontend.

> `GITHUB_TOKEN` is auto-provided to every workflow with `packages: write` for
> GHCR pushes — never set it manually.

---

## 6. VPS one-time bootstrap

SSH into the VPS and install Docker:

```bash
ssh root@95.216.199.114

# Install Docker + Compose plugin
curl -fsSL https://get.docker.com | sh

# Confirm
docker --version
docker compose version

# Open firewall for inbound 80, 443, 9000 (if you're using ufw)
ufw allow 22/tcp   2>/dev/null
ufw allow 80/tcp   2>/dev/null
ufw allow 443/tcp  2>/dev/null
ufw allow 9000/tcp 2>/dev/null

# Make the deploy directory
mkdir -p /opt/1master/backend
exit
```

---

## 7. Stage compose files on the VPS

From your laptop:

```bash
cd ~/code/1master-project-backend   # or wherever you keep these
# These four files must exist locally — they live alongside the backend repo
# (or in a separate orchestration repo / shared dir):
#   docker-compose.yml
#   docker-compose.dev.yml
#   Caddyfile
#   .env.example

scp docker-compose.yml docker-compose.dev.yml Caddyfile .env.example \
    root@95.216.199.114:/opt/1master/

# Also drop the backend env example
scp backend/.env.example root@95.216.199.114:/opt/1master/backend/
```

---

## 8. Fill in `.env` files on the VPS

SSH back in:

```bash
ssh root@95.216.199.114
cd /opt/1master

# ----- /opt/1master/.env -----
cp .env.example .env
nano .env
```

Required values:

```env
ROOT_DOMAIN=1master.uz
API_DOMAIN=api.1master.uz
TUNNEL_DOMAIN=1master.uz
ACME_EMAIL=you@example.com
POSTGRES_DB=onemaster
POSTGRES_USER=postgres
POSTGRES_PASSWORD=                # ← strong random string, see below
GHCR_OWNER=shoxakong              # lowercased GitHub username
API_IMAGE_TAG=latest
FRONTEND_IMAGE_TAG=latest
TUNNEL_IMAGE_TAG=latest
```

Generate the password:
```bash
openssl rand -base64 24
```

Now the backend's own env:

```bash
cp backend/.env.example backend/.env
nano backend/.env
```

**Critical**: `DATABASE_URL` password **must match** `POSTGRES_PASSWORD` above
exactly. Otherwise the api container won't authenticate to postgres.

```env
DATABASE_URL=postgresql+asyncpg://postgres:<SAME_PASSWORD>@postgres:5432/onemaster
SECRET_KEY=                       # ← generate, see below
PROJECT_NAME=OneMaster API
API_V1_PREFIX=/api/v1
DEBUG=False
ALGORITHM=HS256
ACCESS_TOKEN_EXPIRE_MINUTES=30
EMAIL_CODE_LENGTH=6
EMAIL_CODE_TTL_MINUTES=15
SERVICE_TOKEN_TTL_DAYS=365
SMTP_HOST=smtp.gmail.com
SMTP_PORT=587
SMTP_USERNAME=you@gmail.com
SMTP_PASSWORD=                    # ← 16-char Gmail App Password
SMTP_FROM_EMAIL=you@gmail.com
SMTP_FROM_NAME=1master
SMTP_USE_TLS=True
CELERY_BROKER_URL=redis://redis:6379/0
CELERY_RESULT_BACKEND=redis://redis:6379/1
CELERY_TASK_ALWAYS_EAGER=False
UVICORN_WORKERS=2
CELERY_CONCURRENCY=2
```

Generate `SECRET_KEY`:
```bash
python3 -c 'import secrets; print(secrets.token_hex(32))'
```

Sanity check the passwords match:
```bash
grep POSTGRES_PASSWORD /opt/1master/.env
grep DATABASE_URL /opt/1master/backend/.env
# The substring between "postgres:" and "@postgres:5432" must equal POSTGRES_PASSWORD.
```

---

## 9. Make GHCR packages pullable

The deploy workflows push images to `ghcr.io/shoxakong/<name>`. By default these
are **private** and bound to the repo that pushed them, so the VPS needs auth
to pull. Pick one path.

### 9a. Easiest — make the packages public

After the first build succeeds (see step 10), visit each settings page and
flip visibility to **Public**:

```
https://github.com/users/SHOXAKONG/packages/container/1master-api/settings
https://github.com/users/SHOXAKONG/packages/container/1master-frontend/settings
https://github.com/users/SHOXAKONG/packages/container/1master-tunnel-server/settings
```

Scroll to *Danger Zone* → *Change package visibility* → **Public** → confirm.
Images contain no secrets (config comes from env vars at runtime), so public is
safe.

### 9b. Keep private — log in on the VPS with a PAT

Create a Classic PAT at https://github.com/settings/tokens/new with **only**
`read:packages` scope, no expiration. Then on the VPS:

```bash
docker logout ghcr.io
echo 'ghp_YOUR_TOKEN_HERE' | docker login ghcr.io -u SHOXAKONG --password-stdin
# expected: Login Succeeded
```

---

## 10. First deploy — push each repo

From your laptop:

```bash
# Backend
cd ~/code/1master-project-backend
git add -A
git commit -m "Initial deployable backend" --allow-empty
git push -u origin main
gh run watch --repo SHOXAKONG/1master-project-backend

# Frontend
cd ~/code/1master-project-frontend
git add -A
git commit -m "Initial deployable frontend" --allow-empty
git push -u origin main
gh run watch --repo SHOXAKONG/1master-project-frontend

# Tunnel
cd ~/code/1master
git add -A
git commit -m "Initial deployable tunnel-server" --allow-empty
git push -u origin main
gh run watch --repo SHOXAKONG/1master
```

Each workflow:
1. Builds the Docker image (~30-90 s).
2. Pushes to GHCR (`:latest` + `:sha-XXXX` tags).
3. SSHes into the VPS and runs `docker compose pull <service>` + `up -d <service>`.

The **first** workflow run will fail at the deploy step because the stack
hasn't been brought up at all yet — that's expected. Continue to step 11.

---

## 11. First boot on the VPS

```bash
ssh root@95.216.199.114
cd /opt/1master
docker compose pull
docker compose up -d
docker compose ps
```

Wait ~30 seconds, then re-run `docker compose ps`. All seven services should
read `Up` or `Up (healthy)`:

```
1master-api-1             Up (healthy)
1master-caddy-1           Up
1master-frontend-1        Up
1master-postgres-1        Up (healthy)
1master-redis-1           Up (healthy)
1master-tunnel-server-1   Up
1master-worker-1          Up
```

Watch Caddy obtain Let's Encrypt certs (~30-60s on first boot):

```bash
docker compose logs -f caddy | grep -iE "(certificate|obtain|serving)"
```

You're looking for `certificate obtained successfully  identifier=api.1master.uz`
and the same for `1master.uz`. Press `Ctrl+C` once you see them.

---

## 12. Cut the CLI release

To populate the CLI binaries on the GitHub release page (so `curl … | sh`
works), tag a release in the tunnel repo:

```bash
cd ~/code/1master
git tag v0.1.0
git push origin v0.1.0
gh run watch --repo SHOXAKONG/1master
```

The `release.yml` workflow will:
1. Cross-compile 8 binaries (linux/darwin × amd64/arm64 × client/server).
2. Create a GitHub Release at `releases/tag/v0.1.0` with all assets.
3. Push a multi-arch tunnel-server image tagged `:v0.1.0` and `:latest`.

Refresh `https://github.com/SHOXAKONG/1master/releases/tag/v0.1.0` — you should
see 10 files under Assets (8 binaries + `install.sh` + `SHA256SUMS`).

### Sync `1master.uz/install.sh` with the new release

The api container serves `/install.sh` from a named volume, populated at first
boot from the backend image. Refresh it to the latest release version:

```bash
ssh root@95.216.199.114 'docker run --rm -v 1master_releases:/r alpine:3.19 \
  sh -c "wget -qO /r/install.sh \
    https://github.com/SHOXAKONG/1master/releases/latest/download/install.sh \
    && chmod 644 /r/install.sh"'
```

---

## 13. Smoke test end-to-end

```bash
# From your laptop — TLS, API, install script
curl -fsS https://api.1master.uz/install.sh | head -3
curl -fsS https://1master.uz/ | head -5

# Install the CLI
curl -fsSL https://1master.uz/install.sh | sh
1master version

# Register an account via the dashboard
open https://1master.uz/register
# Fill in email/password/etc., check inbox for 6-digit code, verify.
# The Verify page shows your service token ONCE — copy it.

# Save the token locally
1master auth ghp_PASTE_YOUR_SERVICE_TOKEN

# Start a local service to expose
python3 -m http.server 5000 &

# Tunnel it
1master http 5000
# → ✅ Online: <username> -> localhost:5000
```

From any browser (use Chrome incognito, Safari aggressively upgrades HTTP):

```
http://<your-username>.1master.uz
```

You should see Python's directory listing.

> **Note**: Tunnel URLs are **HTTP-only** by default. See
> [section 16](#16-wildcard-https-for-tunnels) if you want HTTPS.

---

## 14. Daily operations

### Automatic deploys (after first boot)

Every `git push` to any of the three repos' `main` branch triggers its
workflow, which auto-deploys. No manual commands.

### Manual restart

```bash
ssh root@95.216.199.114 'cd /opt/1master && docker compose restart api'
```

### Tail logs

```bash
ssh root@95.216.199.114 'cd /opt/1master && docker compose logs -f api'
# or all services:
ssh root@95.216.199.114 'cd /opt/1master && docker compose logs -f'
```

### Rollback to a previous build

Every successful workflow tags the image as `sha-XXXXXXX`. To roll back:

```bash
ssh root@95.216.199.114
cd /opt/1master
nano .env
# Change API_IMAGE_TAG=latest to API_IMAGE_TAG=sha-abc1234 (an older sha)
docker compose pull api worker
docker compose up -d api worker
```

### Database shell

```bash
ssh root@95.216.199.114 'cd /opt/1master && docker compose exec postgres psql -U postgres onemaster'
```

### Run a one-shot migration

```bash
ssh root@95.216.199.114 'cd /opt/1master && docker compose run --rm -e ROLE=migrate api'
```

### Cut a new CLI release

```bash
cd ~/code/1master
git tag v0.1.1 && git push origin v0.1.1
# Wait for release.yml to finish, then end users auto-update via
# curl -fsSL https://1master.uz/install.sh | sh
```

### Backup

```bash
ssh root@95.216.199.114 'docker run --rm \
  -v 1master_postgres_data:/postgres:ro \
  -v 1master_redis_data:/redis:ro \
  -v 1master_caddy_data:/caddy:ro \
  -v /root/backups:/out \
  alpine sh -c "tar -czf /out/1master-\$(date +%F).tar.gz -C / postgres redis caddy"'

scp root@95.216.199.114:/root/backups/'1master-*.tar.gz' ~/backups/
```

For logical Postgres dumps:
```bash
ssh root@95.216.199.114 'cd /opt/1master && docker compose exec -T postgres pg_dump -U postgres onemaster' > onemaster.sql
```

---

## 15. Troubleshooting

### `password authentication failed for user "postgres"` in api logs
The `DATABASE_URL` password in `backend/.env` doesn't match `POSTGRES_PASSWORD`
in `.env`. They must be byte-for-byte identical. If Postgres was already
initialized with a different password:
```bash
cd /opt/1master
docker compose down
docker volume rm 1master_postgres_data
docker compose up -d
```

### `docker compose pull` returns `denied`
The package is private and the VPS isn't logged in. Either make the package
public (step 9a) or `docker login ghcr.io` with a PAT (step 9b).

### Caddy logs spam `no valid A records found for 1master.uz`
DNS for that domain isn't pointing at the VPS yet. Verify with `dig +short A 1master.uz`.

### Caddy logs spam `no solvers available for remaining challenges (offered=[dns-01])`
You enabled wildcard TLS but didn't add the Cloudflare plugin + token. Either
disable wildcard (use `http://*.{$TUNNEL_DOMAIN}` block) or follow
[section 16](#16-wildcard-https-for-tunnels).

### `1master http <port>` says `connect: dial tcp [::1]:9000: connect: connection refused`
The CLI is pointed at `localhost` instead of your VPS. Edit
`~/.1master/config.json`:
```json
{
  "server": "1master.uz:9000",
  "token": "your-existing-token"
}
```

### Tunnel returns 403 from your local server
Your local dev server (Vite, Next.js, etc.) is rejecting the `Host: <user>.1master.uz`
header. For Vite, add to `vite.config.ts`:
```ts
server: { host: true, allowedHosts: ['.1master.uz'] }
```

Or test with `python3 -m http.server <port>` which doesn't host-check.

### CORS error in browser console
`https://1master.uz` not in `CORSMiddleware` allow_origins in
`backend/main.py`. Add it, commit, push → auto-deploy.

### Workflow: `unable to authenticate, attempted methods [none publickey]`
GitHub secret `DEPLOY_SSH_KEY` is missing or malformed. Re-set with:
```bash
gh secret set DEPLOY_SSH_KEY --repo <owner>/<repo> < ~/.ssh/1master_deploy
```

### Poetry CI errors: `[tool.poetry] section not found`
Backend uses PEP 621 (`[project]` table), needs Poetry 2.x. Pin
`POETRY_VERSION=2.1.4` in `backend/Dockerfile` and the CI workflow.

### Poetry CI errors: `Group(s) not found: dev (via --without)`
Remove `--without dev` from `poetry install` — your `pyproject.toml` has no
`dev` group defined.

---

## 16. Wildcard HTTPS for tunnels

By default tunnels are HTTP-only. Three paths to add HTTPS, in order of effort:

### A. On-demand TLS (recommended — no DNS migration)

Caddy can issue a cert per subdomain the first time anyone visits it. Your
wildcard A record makes any `*.1master.uz` resolve to the VPS, so HTTP-01
just works.

Edit `/opt/1master/Caddyfile`:

```caddyfile
{
    email {$ACME_EMAIL}
    on_demand_tls {
        ask http://api:8000/api/v1/tunnel/cert-allowed
    }
}

# (Other site blocks unchanged…)

*.{$TUNNEL_DOMAIN} {
    reverse_proxy tunnel-server:8080
    tls {
        on_demand
    }
}
```

You also need to add an endpoint `GET /api/v1/tunnel/cert-allowed?domain=foo.1master.uz`
in the backend that returns 200 if the username exists. Trivial FastAPI route:

```python
@router.get("/tunnel/cert-allowed")
async def cert_allowed(domain: str, session: AsyncSession = Depends(get_async_session)):
    username = domain.split(".", 1)[0]
    user = await UserRepository(session).get_by_username(username)
    if not user:
        raise HTTPException(status_code=403)
    return {"ok": True}
```

Reload Caddy and watch the first request to a tunnel hostname acquire a cert.

### B. Cloudflare DNS-01 (proper wildcard cert)

Requires moving DNS to Cloudflare. See `/opt/1master/Caddyfile` comments. Steps:
1. Add `1master.uz` as a zone in Cloudflare, change nameservers at eskiz.uz.
2. Recreate all existing DNS records in Cloudflare (~15 records).
3. Create a Cloudflare API token with `Zone:DNS:Edit`.
4. Add to `/opt/1master/.env`: `CF_API_TOKEN=<token>`.
5. In `docker-compose.yml`, swap `image: caddy:2-alpine` for a Caddy build with
   the Cloudflare plugin (`ghcr.io/caddybuilds/caddy-cloudflare:latest`).
6. In `Caddyfile`, replace the wildcard block with:
   ```caddyfile
   *.{$TUNNEL_DOMAIN} {
       reverse_proxy tunnel-server:8080
       tls {
           dns cloudflare {$CF_API_TOKEN}
       }
   }
   ```
7. `docker compose up -d caddy`. First wildcard cert ~60s.

### C. Cloudflare proxy (HTTPS at the edge, HTTP at origin)

If you moved DNS to Cloudflare anyway, enable the orange-cloud proxy for the
wildcard record. Cloudflare terminates TLS and forwards plain HTTP to your VPS.
No Caddy plugin needed.

Tradeoff: Cloudflare sees all tunneled traffic and may throw CAPTCHA pages on
suspected bots. For a personal tunneling service this is usually fine; for
production API tunnels it can add friction.

---

That's the entire deployment surface. Every command needed to take a fresh
VPS to a fully running 1master stack with CI/CD is above.
