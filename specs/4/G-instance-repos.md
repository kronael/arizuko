---
status: unshipped
---

# Instance Config as Git Repos

Ship arizuko instance configs as bare git repos. Like Helm charts for
agent deployments.

## Repo structure

```
arizuko-<name>/
├── .env.example          # config template (tokens as placeholders)
├── character.json        # agent identity
├── groups/
│   └── root/
│       ├── CLAUDE.md
│       ├── character.json
│       └── facts/
└── README.md
```

## CLI

```bash
arizuko create <name> --from <repo-url-or-path>
arizuko update <name> --from <repo-url>   # merge groups/, keep .env
```

Clone → `/srv/data/arizuko_<name>/` → copy `.env.example` → `.env` →
copy groups/ → generate systemd unit → register groups.
