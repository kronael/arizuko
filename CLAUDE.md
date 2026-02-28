# kanipi

Nanoclaw fork with telegram support. Telegram-only gateway,
systemd-managed instances, MCP sidecar extensibility.

## Layout

```
template/             seed for new instances
  workspace/
    skills/           curated skills (ship, reload, info)
    mcporter.json     MCP sidecar configs
container/            agent container build
  Dockerfile          agent image
  build.sh            agent image builder
  agent-runner        agent entrypoint
  skills/             agent-side skills
sidecar/              adjacent MCP servers
kanipi                container entrypoint
Dockerfile            nanoclaw gateway build
Makefile              build
```

## Data Dir

`/srv/data/kanipi_<name>/` per instance:
- `.env` - config (nanoclaw reads from cwd)
- `.claude/skills/` - seeded from template
- `groups/main/logs/` - conversation logs
- `store/` - persistent state
- `data/` - instance data

## Entrypoint

Two modes:
- `kanipi create <name>` - seed data dir, .env, systemd
- `kanipi <instance>` - cd to home, exec nanoclaw

## Extensibility

MCP sidecars in `sidecar/` registered via mcporter.
Write a binary, expose as MCP server, agent calls tools
natively.
