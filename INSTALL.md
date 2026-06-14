# Installing arizuko

LLM-friendly installation guide. An AI assistant reading this should have
everything needed to guide a user through setup.

## Prerequisites checklist

Run these checks and note which fail:

```bash
# 1. OS (Linux x86_64 required)
uname -s -m   # expect: Linux x86_64

# 2. Go 1.22+ (build requirement)
go version    # need 1.22+; missing or <1.22 = blocker

# 3. Docker daemon running
docker info >/dev/null 2>&1 && echo "docker ok" || echo "docker MISSING"

# 4. Docker Compose v2 (plugin, not standalone)
docker compose version   # need v2+; "command not found" = blocker

# 5. gcc/build-essential (CGO requirement)
gcc --version >/dev/null 2>&1 && echo "gcc ok" || echo "gcc MISSING"

# 6. git + make
git --version && make --version | head -1

# 7. Data directory writable
# Default: /srv/data — needs write access for arizuko create
# Alternative: set PREFIX env to use a different path
ls -ld /srv/data 2>/dev/null || echo "/srv/data does not exist"
```

## Fixing blockers

### Go missing or too old

Option A — **g (Go version manager)**, recommended:

```bash
curl -sSL https://git.io/g-install | sh -s
source ~/.bashrc   # or restart shell
g install 1.22.5
```

Option B — **tarball** (if you prefer not to use g):

```bash
curl -LO https://go.dev/dl/go1.22.5.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.22.5.linux-amd64.tar.gz
echo 'export PATH=/usr/local/go/bin:$PATH' >> ~/.bashrc
source ~/.bashrc
```

### Docker Compose v2 missing

```bash
mkdir -p ~/.docker/cli-plugins
curl -SL https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64 \
  -o ~/.docker/cli-plugins/docker-compose
chmod +x ~/.docker/cli-plugins/docker-compose
docker compose version   # verify
```

### gcc/build-essential missing

```bash
sudo apt-get update && sudo apt-get install -y build-essential
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

## Build Docker images

**Minimal** (just core + one adapter):

```bash
sudo make images            # builds arizuko:latest (all adapters bundled)
sudo make agent             # builds arizuko-ant:latest (Claude Code agent)
```

**Additional images** (only if using these features):

```bash
sudo make -C whapd image    # WhatsApp (whapd)
sudo make -C crackbox image # egress sandbox
sudo make -C ttsd image     # TTS
sudo make -C davd image     # WebDAV
```

## Create instance

```bash
./arizuko create <name>     # e.g., ./arizuko create mybot
# Creates /srv/data/arizuko_<name>/ with .env template
```

## Configure .env

Edit `/srv/data/arizuko_<name>/.env`:

```bash
# Required
ASSISTANT_NAME=MyBot                    # display name
AUTH_SECRET=$(openssl rand -hex 32)     # generate once
SECRETS_KEY=$(openssl rand -hex 32)     # generate once
CONTAINER_IMAGE=arizuko-ant:latest
WEB_HOST=myhost.example.com             # public hostname (or localhost for local)

# Claude API (pick one)
ANTHROPIC_API_KEY=sk-ant-...            # direct API key
# OR
CLAUDE_CODE_OAUTH_TOKEN=...             # from claude.ai subscription
```

## Channel adapter credentials

Add credentials for your chosen adapter(s):

| Adapter              | Env vars                                     | How to get                           |
| -------------------- | -------------------------------------------- | ------------------------------------ |
| **teled** (Telegram) | `TELEGRAM_BOT_TOKEN`                         | @BotFather → /newbot                 |
| **discd** (Discord)  | `DISCORD_BOT_TOKEN`                          | discord.com/developers → Bot → Token |
| **slakd** (Slack)    | `SLACK_BOT_TOKEN`, `SLACK_SIGNING_SECRET`    | api.slack.com → App → OAuth          |
| **mastd** (Mastodon) | `MASTODON_ACCESS_TOKEN`, `MASTODON_INSTANCE` | Settings → Development → New app     |
| **bskyd** (Bluesky)  | `BLUESKY_HANDLE`, `BLUESKY_APP_PASSWORD`     | Settings → App passwords             |
| **emaid** (Email)    | `IMAP_*`, `SMTP_*`                           | Your email provider                  |
| **whapd** (WhatsApp) | (none — pairs via QR)                        | `./arizuko pair <name> whapd`        |

**Telegram is simplest** — just needs one token from @BotFather.

## Register first group

```bash
# Telegram example (get chat ID by adding @userinfobot to your group)
./arizuko group <name> add tg:-123456789 main

# Discord example
./arizuko group <name> add discord:<guild_id>/<channel_id> main

# Slack example
./arizuko group <name> add slack:<team_id>/channel/<channel_id> main
```

## Run

```bash
./arizuko run <name>        # generates docker-compose.yml + starts containers
```

## Verify

```bash
sudo systemctl status arizuko_<name>
sudo docker ps --filter "name=arizuko-" --format "{{.Names}} {{.Status}}"
make smoke SMOKE_INSTANCE=<name>
```

All core containers (routd, authd, runed, adapter) should show `(healthy)`.

## Troubleshooting

**Agent doesn't reply:**

1. Check image exists: `sudo docker images | grep arizuko-ant`
2. Check adapter health: `sudo docker ps | grep <adapter>`
3. Check logs: `sudo journalctl -u arizuko_<name> --since "5 min ago" | grep -i error`

**Container exit 125:**
Image/compose mismatch. Rebuild: `sudo make images && sudo make agent`

**Adapter shows `(unhealthy)`:**
Platform credentials wrong or expired. Check adapter-specific logs.

**Permission denied on /srv/data:**
Either chown it or set `PREFIX` env var before all commands.

## Quick reference

```bash
# Full install (Telegram example)
git clone https://github.com/kronael/arizuko && cd arizuko
make build && sudo make images && sudo make agent
./arizuko create mybot
# Edit /srv/data/arizuko_mybot/.env (set TELEGRAM_BOT_TOKEN, AUTH_SECRET, etc.)
./arizuko group mybot add tg:-123456789 main
./arizuko run mybot
```
