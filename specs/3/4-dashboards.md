---
status: planned
---

# Dashboards

Long-running web services for operator/admin interaction with
gateway state. Not ephemeral like agent containers, not static
like web content.

## Problem

Operators need interactive tools to:

- Inspect and curate facts (atlas)
- Review and approve draft responses (evangelist, cheerleader)
- Monitor agent sessions and message queues
- Edit knowledge base entries

These require:

- Read/write access to gateway DB and state
- Long-running process (not per-request spawn)
- Web UI with real-time updates
- Same auth as gateway web (JWT)

## Examples

### Facts inspector (atlas)

Browse, search, edit facts/\*.md files. View embeddings, verify
sources, mark outdated. Trigger re-research on stale facts.

### Curation dashboard (evangelist)

Review draft queue. Approve/reject/edit responses before posting.
See relevance scores, source context, posting history.

### Session monitor

View active containers, message queues, IPC traffic. Kill stuck
containers, replay failed messages, inspect logs.

## Architecture

```
gateway (main process)
  ├── web proxy (vite)
  ├── channel handlers
  ├── container runner
  └── dashboard services ← NEW
        ├── facts-inspector
        ├── curation
        └── session-monitor
```

### Option A: In-process (gateway plugins)

Dashboards run as Express routes inside the gateway process.
Share DB connection, state, auth middleware.

```typescript
// src/dashboards/facts-inspector.ts
export function register(app: Express, db: Database) {
  app.get('/dash/facts', authMiddleware, (req, res) => {
    const facts = listFacts(db);
    res.json(facts);
  });
}
```

Pro: Simple, shared state, no IPC
Con: Couples dashboard code to gateway, restart gateway to update

### Option B: Companion processes

Dashboards run as separate processes, connect to gateway via
HTTP API or shared DB.

```
gateway ──HTTP API──► facts-inspector (port 3001)
                    ► curation (port 3002)
```

Pro: Independent deployment, can use different tech
Con: Need API surface, auth propagation, more moving parts

### Option C: Agent-served dashboards

Dashboard UI served from `/workspace/web/<dash>/`. Interactive
features via agent MCP tools or gateway actions.

Pro: Uses existing infrastructure
Con: No long-running state, limited real-time capability

## Recommendation

**Option A** for v1. Dashboards are gateway plugins with Express
routes. They share the DB connection and auth. Simple to build,
easy to iterate.

Structure:

```
src/
  dashboards/
    index.ts          ← registers all dashboards
    facts-inspector/
      routes.ts       ← Express routes
      views/          ← HTML templates or React components
    curation/
      routes.ts
```

Mount at `/dash/<name>/`. Require auth (same as gateway web).

## Gateway changes

1. Add `src/dashboards/` directory structure
2. Register dashboard routes in `web-server.ts`
3. Auth middleware for `/dash/*` routes
4. Optional: WebSocket for real-time updates

## Dashboard API

Dashboards can use existing gateway internals:

| Need            | Source                      |
| --------------- | --------------------------- |
| Read facts      | `fs.readdir(FACTS_DIR)`     |
| Read messages   | `db.prepare('SELECT...')`   |
| Read drafts     | `db.prepare('SELECT...')`   |
| Write facts     | `fs.writeFile()`            |
| Approve draft   | `db.prepare('UPDATE...')`   |
| Send message    | `sendMessage()` from router |
| List containers | `docker ps` via Bash        |

No new IPC or MCP needed. Dashboards are trusted gateway code.

## Per-group dashboards

Some dashboards are group-specific (facts for atlas, curation
for evangelist). Mount at `/dash/<folder>/<name>/`.

Access control: tier 0-1 can view all dashboards. Tier 2 can
view own group's dashboards only.

## Individual specs

| Dashboard      | Spec                 | Status |
| -------------- | -------------------- | ------ |
| Status/health  | `3/P-dash-status.md` | open   |
| Memory browser | `3/Q-dash-memory.md` | open   |
| WebDAV files   | `3/M-webdav.md`      | open   |

Atlas facts inspector and evangelist curation deferred to
phase 5-6 (depend on those features shipping first).

## Open

- Build system: bundle dashboard frontend separately?
- Real-time: WebSocket or SSE for live updates?
- Mobile: responsive or desktop-only for v1?
- Extensibility: plugin system or hardcoded dashboards?
