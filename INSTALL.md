# Installing arizuko

LLM-friendly installation guide. An AI assistant reading this should have
everything needed to guide a user through setup.

## Prerequisites checklist

Run these checks and note which fail:

```bash
# 1. OS (Linux x86_64 required)
uname -s -m   # expect: Linux x86_64

# 2. Go 1.21+ (any version that understands toolchain switching)
go version    # missing = blocker; <1.21 = blocker
              # go.mod pins `toolchain go1.27rc2`. GOTOOLCHAIN=auto (the
              # default) fetches that toolchain on first build, so an older
              # Go is fine. GOTOOLCHAIN=local or an offline box is NOT —
              # install go1.27rc2 by hand (see "Go missing or too old")

# 3. Docker daemon running
docker info >/dev/null 2>&1 && echo "docker ok" || echo "docker MISSING"

# 4. Docker Compose v2.20+
docker compose version   # need v2.20+ (generated compose uses top-level `include:`);
                         # "command not found" = blocker, < 2.20 = upgrade first

# 5. Docker socket access (for arizuko run)
# User must either be in docker group OR run with sudo
groups | grep -q docker && echo "docker group ok" || echo "not in docker group (will need sudo)"

# 6. git + make
git --version && make --version | head -1

# 7. Data directory writable
# Default: /srv/data — needs write access for arizuko create
# Alternative: set PREFIX env to use a different path
ls -ld /srv/data 2>/dev/null || echo "/srv/data does not exist"
```

## Fixing blockers

### Go missing or too old

Use **`g`** — a minimal Go version manager that installs without root and
manages multiple versions cleanly:

```bash
curl -sSL https://raw.githubusercontent.com/stefanmaric/g/main/bin/g-install | bash
# Re-source your shell profile (path the installer just printed), then:
g install 1.26.5
g set 1.26.5
go version   # verify: go1.26.5 linux/amd64
```

`g` adds itself and Go to `~/.local/bin` (or `~/go` by default). No sudo
needed, no system Go touched.

`go.mod` pins `toolchain go1.27rc2` — a **release candidate**, because arizuko
adopted Go 1.27's stdlib ahead of GA. `g` carries only stable releases, so
install the latest stable as above and let `GOTOOLCHAIN=auto` (Go's default)
fetch the RC on the first `go build`. That fetch is the only thing standing
between a stable Go and a working build.

Two cases where it does not apply, and you must install the RC by hand:
`GOTOOLCHAIN=local` is set, or the machine has no network to `go.dev`.

```bash
go install golang.org/dl/go1.27rc2@latest && go1.27rc2 download
```

This pin moves to `go 1.27.0` once Go 1.27 ships GA (expected August 2026);
see [`specs/5/36-go-1.27-adoption.md`](specs/5/36-go-1.27-adoption.md).

Offline/air-gapped builds get no auto-fetch — install the RC toolchain
explicitly before building.

### Docker Compose v2 missing

```bash
mkdir -p ~/.docker/cli-plugins
curl -SL https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64 \
  -o ~/.docker/cli-plugins/docker-compose
chmod +x ~/.docker/cli-plugins/docker-compose
docker compose version   # verify
```

### Docker socket access

`arizuko run` needs to talk to the Docker daemon. Either:

```bash
# Option A: add user to docker group (recommended, requires re-login)
sudo usermod -aG docker $USER
# Log out and back in, then verify:
docker ps

# Option B: run arizuko commands with sudo
sudo ./arizuko run <name>
```

### /srv/data not writable

Option A — chown to your user (simplest):

```bash
sudo mkdir -p /srv/data
sudo chown -R $(whoami):$(whoami) /srv/data
```

Option B — use a different prefix:

```bash
export PREFIX=~/.arizuko
# All arizuko commands will use $PREFIX instead of /srv/data
```

## Build

```bash
git clone https://github.com/kronael/arizuko && cd arizuko
make build                  # ./arizuko CLI + daemon binaries
make test                   # sanity check (go test ./... -short)
```

Note: host builds use `modernc.org/sqlite` (pure Go, no CGO). No gcc needed.

## Build Docker images

```bash
sudo make images   # builds ALL images: arizuko:latest + arizuko-ant:latest + adapters
```

This builds the complete image set. Individual adapter images (whapd, etc.)
are included. Only build separately if you need to rebuild one without
rebuilding all:

```bash
sudo make agent             # just arizuko-ant:latest (Claude Code agent)
sudo make -C whapd image    # just WhatsApp adapter
sudo DOCKER_BUILDKIT=1 docker build -t arizuko-egress -f egress/Dockerfile . # just the egress proxy
```

## Create instance

```bash
./arizuko create <name>     # e.g., ./arizuko create mybot
# Creates /srv/data/arizuko_<name>/ with .env template
```

This auto-generates `AUTH_SECRET` and `SECRETS_KEY`. You don't need to
create these manually.

`create` makes the instance's FILES and nothing else — the directory, the
`.env`, the compose fragments and the default group's own tree — so it runs
before the instance has a store server. Every ROW is its owner daemon's, written
at that daemon's first boot: routd seeds the ACL floor, the egress floor and the
`main` group there, carrying the `--product` choice `create` wrote into
`ARIZUKO_DEFAULT_PRODUCT`.

## The store server

Every owner's tables live on one DynamoDB-protocol server per instance —
ExtendDB over PostgreSQL (`specs/6/42-dynamo-over-postgres.md`). The package
brings both containers; the server's own init run is the operator's, because it
prints an admin password once and mints one access key per daemon.

```bash
./arizuko packages <name> add extenddb   # mints STORE_DB_PASSWORD + STORE_APP_PASSWORD
./arizuko generate <name>
cd /srv/data/arizuko_<name>
sudo docker compose up -d extenddb_db    # the instance's own PostgreSQL role
sudo docker compose run --rm extenddb init \
  --pg-host extenddb_db --pg-port 5432 \
  --pg-user arizuko_<name> --pg-pass "$STORE_DB_PASSWORD" \
  --extenddb-user arizuko_<name>_app --extenddb-pass "$STORE_APP_PASSWORD" \
  --bind-addr 0.0.0.0 --tls-san extenddb \
  --config /var/lib/extenddb/extenddb.toml
```

`init` prints the admin password and the account id once. Put both in `.env`
as `STORE_ADMIN_PASSWORD` and `STORE_ACCOUNT_ID` before you lose them, then
start the server and turn the control-plane delay off, so a table is servable
the moment `CreateTable` returns:

```bash
sudo docker compose up -d extenddb
sudo docker exec -e EXTENDDB_PASSWORD=$STORE_ADMIN_PASSWORD arizuko_extenddb_<name> \
  extenddb settings -c /var/lib/extenddb/extenddb.toml set control_plane_delay_seconds 0
```

Each daemon signs as itself, with a policy bounded to the owners it reaches —
routd on `routd.*`, dashd on `routd.*`, `onbod.*` and `runed.*`, an adapter on
its own prefix. For every daemon that reaches a store, create the user, put the
policy and mint the key with `extenddb manage --user admin`, then write the
pair into `env/<daemon>.env` as `AWS_ACCESS_KEY_ID` and
`AWS_SECRET_ACCESS_KEY`; a later generate carries them forward.

TLS is mandatory — the server refuses to start without it — so every daemon
needs the certificate the init run generated:

```bash
echo "STORE_CA=$(sudo base64 -w0 store/extenddb/.extenddb/tls/cert.pem)" | sudo tee -a .env
echo 'DYNAMODB_URL=https://extenddb:18443' | sudo tee -a .env
./arizuko generate <name>
```

`DYNAMODB_URL` names the server every daemon opens its owner on. A daemon
without it refuses to start and says so: there is no file to fall back to.

## Configure .env

Edit `/srv/data/arizuko_<name>/.env`. The only required additions are:

```bash
# Display name (required)
ASSISTANT_NAME=MyBot

# Claude API credential (required — pick one)
ANTHROPIC_API_KEY=sk-ant-...            # direct API key
# OR
CLAUDE_CODE_OAUTH_TOKEN=...             # from claude.ai subscription

# Channel credentials (at least one required — see next section)
TELEGRAM_BOT_TOKEN=...                  # example: Telegram
```

Optional settings:

```bash
# Only required if using web interface / OAuth login
WEB_HOST=myhost.example.com

# Already auto-generated by arizuko create — don't change unless you know why
# AUTH_SECRET=...
# SECRETS_KEY=...

# Host path of the arizuko checkout (this repo). `arizuko packages add`
# links an adapter's compose fragment straight at this path instead of
# copying it, so a template fix reaches this instance on the next
# `arizuko generate` with no manual step. Required if `arizuko generate`
# ever runs via the containerized systemd pattern (ExecStartPre `docker run
# arizuko:latest arizuko generate <name>`, see Troubleshooting) — that
# container cannot see the checkout to derive it on its own.
HOST_APP_DIR=/path/to/arizuko
```

## Channel adapter credentials

Add credentials for your chosen adapter(s):

| Adapter              | Env vars                                                                                                  | How to get                           |
| -------------------- | --------------------------------------------------------------------------------------------------------- | ------------------------------------ |
| **teled** (Telegram) | `TELEGRAM_BOT_TOKEN`                                                                                      | @BotFather → /newbot                 |
| **discd** (Discord)  | `DISCORD_BOT_TOKEN`                                                                                       | discord.com/developers → Bot → Token |
| **slakd** (Slack)    | `SLACK_BOT_TOKEN`, `SLACK_SIGNING_SECRET`                                                                 | api.slack.com → App → OAuth          |
| **bskyd** (Bluesky)  | `BLUESKY_HANDLE`, `BLUESKY_APP_PASSWORD`                                                                  | Settings → App passwords             |
| **emaid** (Email)    | `EMAIL_IMAP_HOST`, `EMAIL_SMTP_HOST`, `EMAIL_ACCOUNT`, `EMAIL_PASSWORD`                                   | Your email provider                  |
| **whapd** (WhatsApp) | (none — pairs via QR)                                                                                     | `./arizuko pair <name> whapd`        |
| **gitd** (GitHub)    | (none in `.env` — `./arizuko github <name> add …` mints the secret; `GITHUB_TOKEN` optional, for replies) | GitHub → Settings → Webhooks / App   |

**Telegram is simplest** — just needs one token from @BotFather.

## Enable adapter

Add the package for your chosen adapter to the instance:

```bash
./arizuko packages <name> list           # available vs enabled
./arizuko packages <name> add teled      # Telegram
./arizuko packages <name> add discd      # Discord
./arizuko packages <name> add slakd      # Slack
./arizuko packages <name> add gitd       # GitHub webhooks (then `arizuko github <name> add …`)
```

Without this step, no adapter will start. Each adapter needs its package in
`services/`.

## Optional: model gateway

By default every agent container receives the model credential from `.env`.
The `litellm` package moves it into a gateway container instead; the agent
gets a virtual key that only the gateway accepts (`specs/6/23-llm-gateway.md`).

```bash
./arizuko packages <name> add litellm
# generated MODEL_GATEWAY_KEY for litellm into .env
./arizuko generate <name>
```

`add` mints `MODEL_GATEWAY_KEY` and `MODEL_GATEWAY_DB_PASSWORD` into
`/srv/data/arizuko_<name>/.env` (the package's `litellm.yaml` declares both
`generate`), and every generate sets `MODEL_GATEWAY_URL=http://litellm:4000`
unless `.env` names another gateway. To rotate a secret, delete its line; the
next generate mints a new one.

`MODEL_GATEWAY_KEY` is the gateway's MASTER key and goes to routd, which mints
one virtual key per turn with it, bounded by that folder's remaining daily
budget and expiring with the run. runed gets only the URL; the container gets
only the minted key. The package also starts `litellm_db`, the Postgres litellm
keeps those keys and its spend log in — that database is the gateway's
bookkeeping, in a named volume, and separate from the instance's own store
server.

The gateway reads `ANTHROPIC_API_KEY`, or `CLAUDE_CODE_OAUTH_TOKEN` when the
API key is unset, from the same `.env`. Which one you put there matters:

- **API key** — every harness (`claude`, `codex`, `pi`) works through the
  gateway, billed per token.
- **Subscription OAuth token** — only first-party clients draw on plan
  limits. Measured 2026-09-04: `claude` and `codex` complete turns on plan
  quota; `pi` gets HTTP 400 from Anthropic ("Third-party apps now draw from
  your extra usage, not your plan limits") and needs extra-usage credits or
  an API key. arizuko sends no forged client identity to change that.

With the gateway configured, litellm is the ledger and arizuko stores no
spend (`specs/6/39-gateway-owns-spend.md`). An `arizuko budget` cap becomes the
folder's team budget at the gateway, so it binds on the request itself, on a
number the agent cannot choose, and the dashboard reads the same figure back.

Without a gateway there is no meter. `/dash/usage/` says so instead of showing
a total, and a cap set on such an instance is enforced by nothing — routd logs
that at ERROR every time the gate runs. A cap needs a gateway.

The gateway's JSON log lines land in `journalctl -u arizuko_<name>` with every
other daemon, and its per-request token and cost spans go to the OTLP collector
when `OTEL_EXPORTER_OTLP_ENDPOINT` is set.

## Register first group

```bash
# Telegram example (get chat ID by adding @userinfobot to your group)
# Format: telegram:group/<positive_id> (drop the leading minus)
./arizuko group <name> add telegram:group/123456789 main

# Discord example
./arizuko group <name> add discord:<guild_id>/<channel_id> main

# Slack example
./arizuko group <name> add slack:<team_id>/channel/<channel_id> main
```

## Run

```bash
./arizuko run <name>        # generates docker-compose.yml + starts containers (foreground)
```

This runs `docker compose up` in the foreground. Use Ctrl+C to stop.

For background operation:

```bash
./arizuko run <name> &      # background with job control
# OR create a systemd unit (see Troubleshooting)
```

## Verify

```bash
# Check running containers
docker compose -f /srv/data/arizuko_<name>/docker-compose.yml ps

# Check container health
docker ps --filter "name=arizuko-" --format "{{.Names}} {{.Status}}"

# View logs
docker compose -f /srv/data/arizuko_<name>/docker-compose.yml logs --tail=50

# Run smoke test
make smoke SMOKE_INSTANCE=<name>
```

All core containers (routd, authd, runed, adapter) should show `(healthy)`.

## Troubleshooting

**Agent doesn't reply (healthchecks green):** usually one of

1. **`arizuko-ant` image missing.** journalctl shows
   `pull access denied for arizuko-ant`. Fix: `sudo make -C ant image`.
2. **Adapter disconnected.** `docker ps` shows `(unhealthy)`, or its
   `/health` returns 503 `{status:"disconnected"}` while the process is up
   (whapd waiting for a QR scan, a stream dropped). Check the adapter's
   logs, not routd's:
   `sudo curl -s -o /dev/null -w '%{http_code}\n' http://localhost:<port>/health`
3. **Adapter silent.**
   `sudo journalctl -u arizuko_<name> --since "10 min ago" | grep -viE health`
4. **Container exit 125** in runed logs — see below.

The docker log driver is `none`: read `journalctl -u arizuko_<name>`, never
`docker logs`.

**Container exit 125:**
Image/compose mismatch. Rebuild: `sudo make images`

**Adapter shows `(unhealthy)`:**
Platform credentials wrong or expired. Check adapter-specific logs.

**Permission denied on /srv/data:**
Either chown it or set `PREFIX` env var before all commands.

**Permission denied on docker socket:**
Add user to docker group or run with sudo (see Docker socket access above).

**Running as a systemd service:**

If you want `arizuko run` managed by systemd instead of foreground:

```bash
sudo tee /etc/systemd/system/arizuko_<name>.service <<EOF
[Unit]
Description=arizuko <name>
After=docker.service
Requires=docker.service

[Service]
Type=simple
WorkingDirectory=/srv/data/arizuko_<name>
ExecStart=/path/to/arizuko run <name>
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now arizuko_<name>
sudo systemctl status arizuko_<name>
sudo journalctl -u arizuko_<name> -f
```

## Quick reference

```bash
# Full install (Telegram example)
git clone https://github.com/kronael/arizuko && cd arizuko
make build && sudo make images
./arizuko create mybot
# Edit /srv/data/arizuko_mybot/.env (set TELEGRAM_BOT_TOKEN, ANTHROPIC_API_KEY, ASSISTANT_NAME)
./arizuko packages mybot add teled
./arizuko group mybot add telegram:group/123456789 main
./arizuko run mybot
```
