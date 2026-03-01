# kanipi

Multitenant Claude agent gateway with multi-channel support
(telegram, whatsapp, discord). Nanoclaw fork with systemd-managed
instances and MCP sidecar extensibility.

## Quick Start

```bash
make image          # build gateway docker image
make agent-image    # build agent docker image
./kanipi create foo # seed instance at /srv/data/kanipi_foo/
```

Edit `/srv/data/kanipi_foo/.env` with channel tokens,
register the main group, and start:

```bash
./kanipi group add foo tg:-123456789  # register main group
./kanipi foo                          # start gateway
```

## Group Management

Groups must be registered before the bot processes messages.

```bash
kanipi group list <instance>               # registered + discovered
kanipi group add <instance> <jid> [folder] # register group
kanipi group rm <instance> <jid>           # unregister (not main)
```

First group added defaults to folder `main` with direct mode
(no trigger required). Subsequent groups require a folder name
and use trigger mode (`@assistant_name` to activate).

## Instance Layout

`/srv/data/kanipi_<name>/`:

```
.env              config (tokens, ports)
store/            SQLite DB, whatsapp auth
groups/main/logs/ conversation logs
data/             IPC, sessions
web/              vite web app (MPA)
```

## Config

All via `.env` (seeded from `template/env.example`):

| Key                       | Purpose                      |
| ------------------------- | ---------------------------- |
| ASSISTANT_NAME            | instance name                |
| TELEGRAM_BOT_TOKEN        | enables telegram channel     |
| DISCORD_BOT_TOKEN         | enables discord channel      |
| CONTAINER_IMAGE           | agent docker image           |
| CLAUDE_CODE_OAUTH_TOKEN   | passed to agent containers   |
| IDLE_TIMEOUT              | container idle shutdown (ms) |
| MAX_CONCURRENT_CONTAINERS | concurrent agent limit       |
| VITE_PORT                 | enables vite web serving     |
| WEB_HOST                  | vite host binding            |

Channels enabled by token presence (telegram/discord) or
auth dir existence (whatsapp: `store/auth/creds.json`).

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
npm run build       # tsc compile (src/ -> dist/)
npm run dev         # tsx dev mode
npx tsc --noEmit    # typecheck
```

Pre-commit hooks: prettier (single quotes), typecheck, hygiene.
