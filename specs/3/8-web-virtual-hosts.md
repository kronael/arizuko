---
status: shipped
---

# Web Virtual Hosts

One DNS hostname per world. Root controls routing. World-level web
management is deferred — defined by the world itself when needed.

## Problem

A single kanipi instance hosts multiple worlds. Today all groups share
one web root (`DATA_DIR/web/`). Each world needs its own hostname so
it can be addressed independently.

## Current state (shipped)

Tier 0-1 groups get `/workspace/web/` mounted rw, pointing to the
instance web root. Groups use subdirectories:

```
/srv/data/REDACTED/web/
  index.html              ← instance landing
  happy/index.html        ← REDACTED/happy/
  REDACTED/index.html        ← REDACTED/REDACTED/
```

## Design

### One hostname per world

Each tier 1 world can have one DNS hostname assigned to it. Requests
to that hostname are routed to `groups/<world>/web/`.

```sql
ALTER TABLE groups ADD COLUMN web_host TEXT;
```

Proxy logic:

1. Match `Host` header against `groups.web_host`
2. If matched → serve from `groups/<folder>/web/`
3. If no match → serve from instance `web/`

### Permissions

Root (tier 0) controls hostname assignment. World agents (tier 1) read
their own hostname but cannot change it.

| Action       | Tier 0 | Tier 1 | Tier 2+ |
| ------------ | ------ | ------ | ------- |
| set_web_host | any    | no     | no      |
| get_web_host | any    | self   | no      |

New IPC actions:

```
set_web_host   { folder, host }     tier 0 only
get_web_host   { folder }           tier 0-1
```

CLI equivalent:

```bash
kanipi config <instance> group set-web-host atlas atlas.example.com
```

Validation: `host` must be a valid hostname (no scheme, no path), no
duplicates across groups, folder must exist.

### Serving

One vite process per world web dir, same pattern as the instance web dir.
The bash entrypoint starts vite for `DATA_DIR/web/` on an internal port;
world vhosts start additional vite processes for `groups/<folder>/web/`.
The proxy routes by `Host` header to the correct vite port.

### World-level web management

How a world agent manages its own web content (deploy flows, tooling,
vite if needed) is left to the world itself. Not specified here.

## Implementation order

1. Add `web_host` column to `groups`
2. Web proxy routes by `Host` header to `groups/<folder>/web/`
3. Static file serving for matched hosts
4. CLI: `group set-web-host <folder> <host>`
5. IPC actions: `set_web_host` / `get_web_host`

## Related

- `specs/3/5-permissions.md` — tier model, `/workspace/web` mount enforcement
