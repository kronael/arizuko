# arizuko v2m2 — Go Architecture

**Status**: shipped

Go rewrite of kanipi TypeScript gateway. Single static binary,
~4,900 LOC replacing ~9,400 LOC TS.

## Packages (shipped)

| Package      | Lines | Role                                  |
| ------------ | ----- | ------------------------------------- |
| cmd/arizuko/ | ~190  | CLI entrypoint (run, create, group)   |
| core/        | ~210  | Config, types, Channel interface      |
| store/       | ~530  | SQLite CRUD, schema, migrations       |
| gateway/     | ~760  | Orchestration, commands, message loop |
| container/   | ~930  | Docker run, mounts, sidecars, skills  |
| queue/       | ~415  | Per-group concurrency, circuit break  |
| ipc/         | ~300  | File-based IPC, request/response      |
| router/      | ~120  | XML format, routing rules             |
| scheduler/   | ~195  | Cron tasks via robfig/cron            |
| diary/       | ~100  | YAML frontmatter context annotations  |
| groupfolder/ | ~70   | Path resolution and validation        |
| mountsec/    | ~210  | Mount allowlist enforcement           |
| runtime/     | ~63   | Docker lifecycle, orphan cleanup      |
| logger/      | ~23   | slog JSON handler init                |

## Channel Adapters (shipped)

| Channel  | Status  | Library         |
| -------- | ------- | --------------- |
| Telegram | shipped | gotgbot         |
| Discord  | planned | discordgo       |
| WhatsApp | planned | whatsmeow       |
| Email    | planned | go-imap         |
| Web      | shipped | net/http static |

## IPC Actions (shipped)

From kanipi parity work:

- send_message, send_file, reset_session
- inject_message (tier 0-1)
- register_group, escalate_group, delegate_group (tier-restricted)
- set_routing_rules (tier 0-2)
- schedule_task, pause_task, resume_task, cancel_task

## Permission Tiers (shipped)

- Tier 0 (root): no slash in folder — full access
- Tier 1 (world): one slash — can manage own world
- Tier 2 (agent): two slashes — limited access
- Tier 3 (worker): three+ slashes — most restricted

## Data Layout

Per-instance directory: `/srv/data/arizuko_<name>/`

```
.env              config (read from cwd)
store/            SQLite DB (messages.db)
groups/<folder>/  group workspace
  logs/           conversation logs
  CLAUDE.md       agent instructions
data/
  ipc/<folder>/   file-based IPC dirs
  sessions/       agent session state
```

## Container Protocol

Unchanged from kanipi. Docker spawned via `docker run -i --rm`:

- Stdin: JSON `{prompt, sessionId, chatJid, secrets, ...}`
- Stdout: markers `---NANOCLAW_OUTPUT_START---` / `---NANOCLAW_OUTPUT_END---`
- IPC: JSON files + SIGUSR1 signal

## Libraries

| Concern    | Library            | Notes           |
| ---------- | ------------------ | --------------- |
| sqlite     | modernc.org/sqlite | pure Go, no CGO |
| telegram   | gotgbot/v2         | long-polling    |
| file watch | fsnotify           | for IPC         |
| cron       | robfig/cron/v3     | task scheduling |
| env        | joho/godotenv      | .env parsing    |
| logging    | log/slog           | stdlib JSON     |

## Migration from TS

- Same SQLite schema — drop-in replacement
- Same .env format — no config changes
- Same IPC protocol — existing agent containers work
- Same data dir layout (`/srv/data/arizuko_<name>/`)

## Open Work (see other specs)

- **v2m3/extensions.md** — extension points, plugin architecture
- **v2m3/action-registry.md** — unify actions/ with IPC dispatch
- **v2m3/micro-architecture.md** — microservice decomposition
- **v4/isolation.md** — VM-backed execution (crackbox)
- **v4/executor-interface.md** — abstract executor backend
