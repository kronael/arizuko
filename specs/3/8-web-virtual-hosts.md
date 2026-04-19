---
status: shipped
---

# Web Virtual Hosts

One DNS hostname per world. Root controls routing. World-level web
management is deferred to the world.

## Model

Each tier 1 world may have one DNS hostname. Requests to that hostname
route to `groups/<world>/web/`.

```sql
ALTER TABLE groups ADD COLUMN web_host TEXT;
```

Proxy:

1. Match `Host` header against `groups.web_host`.
2. Matched → serve from `groups/<folder>/web/`.
3. No match → serve from instance `web/`.

## Permissions

| Action       | Tier 0 | Tier 1 | Tier 2+ |
| ------------ | ------ | ------ | ------- |
| set_web_host | any    | no     | no      |
| get_web_host | any    | self   | no      |

IPC: `set_web_host { folder, host }` (tier 0), `get_web_host { folder }`
(tier 0-1).

CLI: `arizuko config <instance> group set-web-host <folder> <host>`.

Validation: valid hostname (no scheme, no path); no duplicates; folder
must exist.

## Serving

One vite process per world web dir. Proxy routes by `Host` header.

## Files

- `proxyd/` — Host header routing
- `cmd/arizuko/` — CLI
- `ipc/` — set/get_web_host actions
- `store/migrations/` — `web_host` column

## Related

- `5-permissions.md` — `/workspace/web` mount enforcement
