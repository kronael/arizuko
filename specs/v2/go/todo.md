# Go port — status and open questions

## Ported packages (complete, compiling, tests passing)

| Package      | Lines | Status | Notes                            |
| ------------ | ----- | ------ | -------------------------------- |
| core/        | ~210  | done   | types, config, Channel iface     |
| store/       | ~530  | done   | SQLite CRUD, schema, migrations  |
| logger/      | ~23   | done   | slog JSON to stderr              |
| groupfolder/ | ~70   | done   | path resolution + validation     |
| router/      | ~120  | done   | XML format, routing rules        |
| queue/       | ~415  | done   | concurrency, circuit breaker     |
| ipc/         | ~300  | done   | file-based request/response      |
| mountsec/    | ~210  | done   | mount validation + allowlist     |
| runtime/     | ~63   | done   | docker lifecycle, orphan cleanup |
| scheduler/   | ~195  | done   | cron via robfig/cron, tasks      |
| diary/       | ~100  | done   | YAML frontmatter diary reader    |
| container/   | ~930  | done   | docker run, mounts, sidecars     |
| gateway/     | ~760  | done   | orchestration, commands, errors  |
| cmd/arizuko/ | ~187  | done   | run, create, group CLI           |

Total: ~4,910 LOC Go (vs ~9,400 LOC TS)

## Recently incorporated from kanipi

- Forward/reply message metadata (forwarded_from, reply_to_text, reply_to_sender)
- Per-chat error tracking (errored flag on chats, mark/clear/check)
- DB migration system (PRAGMA user_version, v1 JID standardization)
- Gateway capabilities manifest (.gateway-caps TOML)
- Diary system (YAML frontmatter summaries as session annotations)
- Per-channel output styling (outputStyle in settings.json)
- Media/video/voice config plumbing to container runner
- Sidecar IPC directory creation
- Sidecar lifecycle management (start/stop/settings wiring)
- Gateway commands (/new, /ping, /chatid, /stop)

## Not ported (functionality gaps)

- **Channel adapters** — telegram, discord, whatsapp, email, web.
  The Channel interface exists but no implementations.

- **Action registry** — unified action system.
  Go version would use JSON Schema or struct tags.

- **File commands** — /put, /get, /ls (agent-side).

- **Web proxy** — vite proxy + auth.

- **MIME enricher** — attachment download + transcription pipeline.

- **Slink** — share link system for web access.

## Open questions

### Q1: Should channels be Go or stay TS?

Channel SDKs are strongest in TS/JS:

- grammy (telegram) — mature, good types
- discord.js — dominant
- baileys (whatsapp) — no Go equivalent
- go-telegram-bot-api exists but less featured

Option A: Port channels to Go (harder for whatsapp)
Option B: Keep channels as TS processes, communicate via SQLite
Option C: Keep channels as TS, embed in Go via wasm/subprocess

### Q2: Is the monolith port valuable without channels?

The gateway can load state, poll SQLite, spawn containers, manage
queues — but without channel adapters it can't receive messages.
Value depends on whether channels stay in-process or become
separate processes (see micro spec).

### Q3: Docker-in-docker path translation

hostPath() translates local → host paths. Need to verify:

- Does the Go version handle all edge cases from TS?
- Is HostProjectRoot correctly detected from /proc/self/mountinfo?
  (currently just reads env vars, no mountinfo parsing)

### Q4: Container output format compatibility

The Go runner parses NANOCLAW_OUTPUT_START/END markers.
Need to verify:

- Agent container still emits these markers
- JSON structure matches Output struct
- Session ID propagation works end-to-end

### Q5: Session management correctness

Verify session eviction logic:

- Error with no output → evict session (force new session next time)
- Error with prior output → keep session (prevent duplicates)
- Does the Go code match the TS cursor rollback behavior exactly?

### Q6: IPC watcher file format

The Go IPC watcher handles requests, legacy messages, legacy tasks.
Verify JSON formats match what the agent container writes.

### Q7: Store schema compatibility

The Go schema uses TEXT timestamps (RFC3339). The TS code also uses
TEXT timestamps. Verify:

- Existing DBs from TS instances can be read by Go
- Timestamp comparison (string ordering) works correctly
- busy_timeout and WAL mode are set identically

## Things to verify before deployment

1. Build the Go binary and agent image
2. Point it at an existing TS instance's data dir
3. Verify it reads groups, sessions, state correctly
4. Test container spawning with a real agent image
5. Test IPC round-trip (agent sends message → gateway delivers)
6. Test scheduler picks up and runs due tasks
7. Test graceful shutdown (SIGTERM → drain containers)
