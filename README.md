# kanipi

Nanoclaw fork with telegram support. Strips to gateway +
agent + telegram.

## Build

```bash
make image          # gateway container
make agent-image    # agent container
```

## Usage

```bash
kanipi create mybot
# edit /srv/data/kanipi_mybot/.env (TELEGRAM_BOT_TOKEN)
# symlink systemd service, start
```

## Config

`.env` in data dir, nanoclaw reads it from cwd:

```
ASSISTANT_NAME=mybot
TELEGRAM_BOT_TOKEN=...
TELEGRAM_ONLY=true
CONTAINER_IMAGE=kanipi-agent:latest
```

## Extending

Drop MCP server binaries in `sidecar/`, register in
`template/workspace/mcporter.json`. Agent discovers and
calls their tools.

See ARCHITECTURE.md for design.
