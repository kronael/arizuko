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

Edit `/srv/data/kanipi_foo/.env` with channel tokens, then start:

```bash
./kanipi foo
```

## Architecture

Gateway polls channels for messages, routes to containerized
Claude agents via docker. Each agent runs in an ephemeral
container with group folder + state mounted.

```
Channel -> DB (store message) -> message loop
  -> GroupQueue -> runContainerAgent (docker exec)
  -> stream output back to channel
```

Vite dev server runs alongside the gateway for serving web
apps built by agents. Managed in the bash entrypoint, not Node.

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

| Key                     | Purpose                    |
| ----------------------- | -------------------------- |
| ASSISTANT_NAME          | instance name              |
| TELEGRAM_BOT_TOKEN      | enables telegram channel   |
| DISCORD_BOT_TOKEN       | enables discord channel    |
| CONTAINER_IMAGE         | agent docker image         |
| CLAUDE_CODE_OAUTH_TOKEN | passed to agent containers |
| VITE_PORT               | enables vite web serving   |
| WEB_HOST                | vite host binding          |

Channels enabled by token presence (telegram/discord) or
auth dir existence (whatsapp).

## Development

```bash
npm run build       # tsc compile (src/ -> dist/)
npm run dev         # tsx dev mode
npx tsc --noEmit    # typecheck
```

Pre-commit hooks: prettier (single quotes), typecheck, hygiene.
