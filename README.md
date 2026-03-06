# kanipi

Multitenant Claude agent gateway with multi-channel support
(telegram, whatsapp, discord, email). Nanoclaw fork with
systemd-managed instances and MCP sidecar extensibility.

## Quick Start

```bash
make image                     # build gateway docker image
make -C container image        # build agent docker image
make -C sidecar/whisper image  # build whisper sidecar image
./kanipi create foo            # seed instance at /srv/data/kanipi_foo/
```

Edit `/srv/data/kanipi_foo/.env` with channel tokens,
register the main group, and start:

```bash
./kanipi config foo group add tg:-123456789  # register main group
./kanipi foo                                 # start gateway
```

## Group Management

Groups must be registered before the bot processes messages.

```bash
kanipi config <instance> group list               # registered + discovered
kanipi config <instance> group add <jid> [folder] # register group
kanipi config <instance> group rm  <jid>          # unregister (not main)
```

First group added defaults to folder `main` with direct mode
(no trigger required). Subsequent groups require a folder name
and use trigger mode (`@assistant_name` to activate).

## Web Channel (slink)

Groups with a slink token accept messages via HTTP:

```bash
POST /pub/s/<token>
Content-Type: application/json

{"text": "hello", "media_url": "https://example.com/file.mp3"}
```

Optional `Authorization: Bearer <jwt>` header. When `AUTH_SECRET` is set,
JWTs are verified (HMAC-SHA256). Authenticated requests get a higher rate
limit (`SLINK_AUTH_RPM`, default 60/min vs `SLINK_ANON_RPM` default 10/min).

`/pub/REDACTED.js` is a client-side script for embedding in public web pages
(reads `data-token` from script tag). The authenticated `/_REDACTED/REDACTED.js`
is injected into all proxied HTML pages and adds a `POST /_REDACTED/message`
handler plus a SSE stream at `/_REDACTED/stream?group=<name>` for receiving
agent responses in-page.

## System Messages and Sessions

The gateway injects system messages (new-session, new-day) as XML
prepended to agent stdin. Messages are enqueued in the `system_messages`
DB table and flushed on the next agent invocation for the group.

Every container spawn/exit is recorded in the `sessions` table. On agent
error, the user receives a retry prompt and the session cursor rolls back
so the next run starts fresh.

## Commands

Slash commands (`/new`, `/ping`, `/chatid`) are handled by a pluggable
registry in `src/commands/`. Commands are intercepted before the message
reaches the agent. The registry is exported as `commands.xml` into each
group's IPC directory so agents can discover available commands.

## Instance Layout

`/srv/data/kanipi_<name>/`:

```
.env                    config (tokens, ports)
store/                  SQLite DB, whatsapp auth
groups/main/logs/       conversation logs
data/sessions/          per-session state dirs
data/ipc/               agent IPC files
web/pub/                public web files (no auth)
web/priv/               private web files (auth required)
```

The `/pub/` URL prefix is the auth boundary: files under `web/pub/`
are served without authentication, `web/priv/` requires auth.
Agent skills are seeded from `container/skills/` to
`~/.claude/skills/` inside each container on first spawn.

## Config

All via `.env` (seeded from `template/env.example`):

| Key                       | Purpose                                  |
| ------------------------- | ---------------------------------------- |
| ASSISTANT_NAME            | instance name                            |
| TELEGRAM_BOT_TOKEN        | enables telegram channel                 |
| DISCORD_BOT_TOKEN         | enables discord channel                  |
| CONTAINER_IMAGE           | agent docker image                       |
| CLAUDE_CODE_OAUTH_TOKEN   | passed to agent containers               |
| IDLE_TIMEOUT              | container idle shutdown (ms)             |
| MAX_CONCURRENT_CONTAINERS | concurrent agent limit                   |
| VITE_PORT                 | enables vite web serving                 |
| WEB_HOST                  | vite host binding                        |
| REDACTED_USERS               | basic auth users (alice:pw,bob:pw2)      |
| AUTH_SECRET               | HMAC secret for JWT verification (slink) |
| WHISPER_BASE_URL          | whisper sidecar URL for transcription    |

Channels enabled by token presence (telegram/discord),
auth dir existence (whatsapp: `store/auth/creds.json`),
or `EMAIL_IMAP_HOST` presence (email).

Per-group whisper language hints: create `.whisper-language` in the group
folder with one BCP-47 language code per line (e.g. `cs`, `de`). The whisper
handler runs one additional forced-decode pass per language and labels each
result separately alongside the auto-detected pass.

## Deployment

Run directly with docker:

```bash
docker run -d -i --name kanipi_foo \
    --network=host \
    -v /srv/data/kanipi_foo:/srv/app/home \
    -v /srv/run/kanipi_foo:/srv/run/kanipi_foo \
    -v /var/run/docker.sock:/var/run/docker.sock \
    kanipi foo
```

Or with systemd — create `/etc/systemd/system/kanipi_foo.service`:

```ini
[Unit]
Description=kanipi foo
After=docker.service
Requires=docker.service

[Service]
Restart=always
RestartSec=1
ExecStartPre=-/usr/bin/docker stop %n
ExecStartPre=-/usr/bin/docker rm -f %n
ExecStop=/usr/bin/docker rm -f %n
ExecStart=/usr/bin/docker run -i --rm --name %n \
    --network=host \
    -v /srv/data/kanipi_foo:/srv/app/home \
    -v /srv/run/kanipi_foo:/srv/run/kanipi_foo \
    -v /var/run/docker.sock:/var/run/docker.sock \
    kanipi foo

[Install]
WantedBy=default.target
```

Then `systemctl enable --now kanipi_foo`.

## Development

```bash
make build          # tsc compile (src/ -> dist/)
make lint           # typecheck without emitting
make test           # unit tests (vitest run src/)
npm run dev         # tsx dev mode
vitest run          # run all tests (some require docker)
```

Pre-commit hooks: prettier (single quotes), typecheck, hygiene.
