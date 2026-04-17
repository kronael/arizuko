---
status: planned
---

# Instance Config as Git Repos

Arizuko instance configs should ship as bare git repos with a known structure.
Like Helm charts for agent deployments.

## Repo structure

```
arizuko-<name>/
├── .env.example          # config template (tokens replaced with placeholders)
├── character.json        # agent identity (bio, topics, style)
├── groups/
│   └── root/
│       ├── CLAUDE.md     # agent instructions
│       ├── character.json  # per-group override (optional)
│       └── facts/        # knowledge files (YAML markdown)
└── README.md             # what this agent does, how to deploy
```

## CLI

```bash
# Create from repo
arizuko create <name> --from <repo-url>
arizuko create <name> --from /path/to/local/repo

# What it does:
# 1. Clone repo to tmp
# 2. mkdir /srv/data/arizuko_<name>/
# 3. Copy .env.example → .env (user fills secrets)
# 4. Copy groups/ → groups/
# 5. Copy character.json → groups/root/character.json (if not per-group)
# 6. Generate systemd unit
# 7. Register groups from repo structure

# Update from repo (pull new facts, CLAUDE.md changes)
arizuko update <name> --from <repo-url>
# Merges groups/ content, preserves .env and local state
```

## Scope

This is a v2 feature. For now, instance setup is manual.
The repo structure is the target format — we can extract
arizuko-REDACTED as the first instance repo once the CLI
supports `--from`.
