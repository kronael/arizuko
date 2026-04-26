# CLAUDE.md

## Response Style

Be terse. Lead with the answer, skip preamble, skip trailing summaries
of what you just did. One-sentence replies are fine. Exceptions only
when explicitly asked or the task requires it: generating content
(specs, docs, prose), multi-step plans, root-cause walkthroughs.

## What is arizuko

Multitenant Claude agent router. External channel adapters register via
HTTP; router routes messages to containerized Claude agents. Docker compose
orchestration.

## Build & Test

```bash
make build    # go build → ./arizuko + all daemon binaries
make lint     # go vet ./...
make test     # go test ./... -count=1
make images   # all docker images (router + adapters + agent)
make agent    # agent docker image (make -C ant image)

# Run a single test package
go test ./gateway/... -count=1 -run TestName
```

Tests use `modernc.org/sqlite` (pure Go, no CGO).
Exception: `gated` requires `CGO_ENABLED=1` (see Makefile).
Pre-commit hooks configured via `.pre-commit-config.yaml`.

## Architecture

See ARCHITECTURE.md for package graph, message flow, container model.

## Docs layout

Root UPPERCASE files: `ARCHITECTURE.md`, `SECURITY.md`, `ROUTING.md`,
`EXTENDING.md`, `CHANGELOG.md`, `CLAUDE.md`. Per-daemon detail lives
next to the source (e.g. `ipc/SECURITY.md`). No `docs/` directory —
add a per-daemon `SECURITY.md` when its threat model outgrows a row
in the root table.

Keep `EXTENDING.md` current as extension points evolve (channels,
actions, routing, mounts, skills, tasks, diary; skill scopes;
permission tiers).

## Layout

```
cmd/arizuko/       CLI entrypoint (generate, run, create, group, status, pair)
core/              Config, types, Channel interface
store/             SQLite (messages.db), migrations
gateway/           Main loop + commands
container/         Docker runner + runtime
ant/               In-container agent (TypeScript, skills, Dockerfile)
queue/             Per-group concurrency
router/            Message formatting + routing
chanreg/           Channel registry + HTTP proxy
api/               Router HTTP API server
compose/           Docker-compose generation
ipc/               MCP server (unix socket, runtime auth)
auth/              Identity, authorization, JWT, OAuth, middleware
diary/             Diary annotations
groupfolder/       Path validation
mountsec/          Mount security
template/          Instance seed files; services/ has adapter TOMLs, web/ has Vite scaffold
sidecar/           whisper transcription service image
gated/             Gateway daemon
timed/             Scheduler daemon
onbod/             Onboarding daemon (gated admission queue + OAuth link)
dashd/             Operator dashboards (HTMX)
webd/              Web channel daemon (websocket hub, slink, MCP bridge)
proxyd/            Web proxy (/pub/ public, /* auth-gated)
grants/            Grant rule engine (library)
chanlib/           Shared HTTP + auth primitives, URLCache, fsutil, env
                   helpers, ShortHash (library; used beyond adapters)
db_utils/          SQL migration runner (library)
theme/             Shared CSS/HTML helpers (library)
teled/             Telegram adapter (Go)
discd/             Discord adapter (Go)
mastd/             Mastodon adapter (Go)
bskyd/             Bluesky adapter (Go)
reditd/            Reddit adapter (Go)
whapd/             WhatsApp adapter (TypeScript)
twitd/             X/Twitter adapter (TypeScript, browser emulation)
emaid/             Email adapter (IMAP/SMTP, Go)
linkd/             LinkedIn adapter (Go)
cfg/               Instance config files (per-deploy .env snapshots)
```

## Conventions

- JSONL files use `.jl` extension (not `.jsonl`)
- XML tags for prompt structure, JSON for IPC/MCP/structured output
- Container output delimited by `---ARIZUKO_OUTPUT_START---` / `---ARIZUKO_OUTPUT_END---`
- IPC: MCP over unix socket, socat bridge into container
- Business features (gates, grants, onboarding) are DB-backed with CLI +
  chat command for management. Infra (ports, timeouts, images, paths) stays
  as env vars in `.env`.

### Trust boundaries

`proxyd` is the sole signer of identity headers (`auth.SignHMAC`).
Every other HTTP-receiving backend (`webd`, `onbod`, future) MUST
verify via `auth/middleware.go` — `auth.RequireSigned` for
always-authed routes, `auth.StripUnsigned` for backends mixing
public + authed flows. Never inline an `auth.VerifyUserSig` call
in handler code; never trust `X-User-Sub` without a sig check.

### Subagent worktrees

When spawning Agent subagents that make non-trivial changes
(touches >5 files, schema migrations, new specs, cross-package
refactors), pass `isolation: "worktree"` so the agent works in an
isolated git checkout. This prevents conflicts if multiple subs
run in parallel or if you're editing the main tree alongside.

Trivial changes (single-file edits, doc tweaks, one-line fixes,
typo runs) can run on the shared tree — worktree creation
overhead isn't worth it.

The Agent tool cleans up the worktree automatically if the agent
made no changes; otherwise the worktree path + branch are
returned in the result for review.

## Design principles

### Simple stays simple, complex goes deeper

arizuko's primitives scale with need. A solo user runs `solo/inbox`
with one group; a corporation runs `corp/eng/sre/oncall/launch-q3`
with five-deep paths. Same code for both. Don't force structure
where it isn't needed; don't fight structure where it is.

Applies throughout:

- **Group hierarchy** — arbitrary path depth. Suggested segment
  labels (`world/org/branch/unit/thread`) are advisory. The system
  reads paths, not labels.
- **Topic kinds** — default is just a chat thread. Add `task` /
  `meeting` / `project` / `question` metadata when the work needs
  tracking, not before.
- **Grants** — tier-derived defaults out of the box; per-folder
  custom rules when ops need them.
- **Channels** — env-var setup for trivial, dashd UI for managed,
  auth-tunnel for browser-side challenges. Same `chats` table backs all.
- **Secrets** — folder-scoped by default; per-user only in
  provably-single-user contexts. No required scope ceremony for
  small deployments.

The principle: every primitive has a one-line setup AND a deep-config
path. Pick the depth that matches your org's actual complexity.

## Data Dir

`/srv/data/arizuko_<name>/` per instance:

- `.env` — config (gateway reads from cwd)
- `store/` — SQLite DB (`messages.db`)
- `groups/<folder>/` — group files, logs, diary
- `groups/<folder>/media/<YYYYMMDD>/` — downloaded inbound attachments
- `ipc/<folder>/` — MCP unix sockets
- `groups/<folder>/.claude/` — agent session state

## Config

All config via `.env` in data dir or env vars (`core.LoadConfig`).

Infra: `ASSISTANT_NAME`, `CONTAINER_IMAGE`, `CONTAINER_TIMEOUT`,
`IDLE_TIMEOUT`, `MAX_CONCURRENT_CONTAINERS`, `API_PORT`, `CHANNEL_SECRET`,
`HOST_DATA_DIR`, `HOST_APP_DIR`, `WEB_HOST`, `AUTH_SECRET`, `AUTH_BASE_URL`,
`TZ`, `ARIZUKO_DEV`.
Media: `MEDIA_ENABLED`, `MEDIA_MAX_FILE_BYTES`, `WHISPER_BASE_URL`,
`VOICE_TRANSCRIPTION_ENABLED`, `VIDEO_TRANSCRIPTION_ENABLED`, `WHISPER_MODEL`.
OAuth: `GITHUB_CLIENT_ID/SECRET`, `GITHUB_ALLOWED_ORG`,
`DISCORD_CLIENT_ID/SECRET`, `GOOGLE_CLIENT_ID/SECRET`, `GOOGLE_ALLOWED_EMAILS`.
Flags: `ONBOARDING_ENABLED` (true/false), `IMPULSE_ENABLED`,
`SEND_DISABLED_CHANNELS`, `SEND_DISABLED_GROUPS`, `ONBOARDING_PLATFORMS`.
Onboarding (onbod): `ONBOARDING_PROTOTYPE`, `ONBOARDING_GREETING`,
`ONBOARDING_GATES` (format `*:50/day` or `github:org=X:10/day,google:domain=Y:20/day`).
Gates write to `onboarding.gate` + `onboarding.queued_at` columns (migration 0027);
per-gate state lives in `onboarding_gates` (migration 0029).
Daemon-specific: `DATA_DIR`, `DATABASE`, `DB_PATH`, `DASH_PORT`,
`ROUTER_URL`, `ONBOD_LISTEN_ADDR`, `ONBOARD_POLL_INTERVAL`.

## Entrypoint

```
arizuko generate <instance>    write docker-compose.yml to data dir
arizuko run <instance>         generate + docker compose up
arizuko create <name>          seed data dir, .env, default group
arizuko group <inst> list|add|rm   manage groups
arizuko group <inst> grant <sub> <pattern>   add user_groups ACL row
arizuko group <inst> ungrant <sub> <pattern>
arizuko group <inst> grants [<sub>]
arizuko gate <inst> list|add|rm|enable|disable   manage onboarding_gates rows
arizuko status <instance>      show compose services + channels
arizuko pair <instance> <svc>  docker compose run --rm a service
```

Daemons are standalone binaries: `gated`, `timed`, `teled`, `discd`,
`mastd`, `bskyd`, `reditd`, `emaid`, `linkd`, `whapd`, `twitd`, `onbod`, `dashd`,
`webd`, `proxyd`. Go daemons: `<name>/main.go`. TS daemons: `<name>/src/main.ts`.

## Service Architecture

Daemons end in `d` (4+d naming), libraries don't. Shared SQLite DB (WAL mode).

| Name       | Type    | Role                                                  |
| ---------- | ------- | ----------------------------------------------------- |
| `gated`    | daemon  | Message loop, routing, containers                     |
| `timed`    | daemon  | Cron poll, writes to messages                         |
| `onbod`    | daemon  | Onboarding: OAuth link, gated admission queue         |
| `dashd`    | daemon  | Operator dashboards (HTMX)                            |
| `webd`     | daemon  | Web channel: websocket hub, slink, MCP bridge         |
| `proxyd`   | daemon  | Web proxy: /pub/ public, /\* auth-gated               |
| `vited`    | service | Vite dev server (compose-generated, arizuko-vite img) |
| `teled`    | daemon  | Telegram adapter                                      |
| `discd`    | daemon  | Discord adapter                                       |
| `mastd`    | daemon  | Mastodon adapter                                      |
| `bskyd`    | daemon  | Bluesky adapter                                       |
| `reditd`   | daemon  | Reddit adapter                                        |
| `whapd`    | daemon  | WhatsApp adapter (TypeScript)                         |
| `twitd`    | daemon  | X/Twitter adapter (TypeScript, browser emulation)     |
| `emaid`    | daemon  | Email adapter (IMAP/SMTP)                             |
| `linkd`    | daemon  | LinkedIn adapter                                      |
| `ipc`      | library | MCP server, identity stamping                         |
| `auth`     | library | Authorization policy, JWT, OAuth                      |
| `grants`   | library | Grant rule engine                                     |
| `chanlib`  | library | HTTP + auth, URLCache, fsutil, env, ShortHash         |
| `db_utils` | library | SQL migration runner                                  |
| `theme`    | library | Shared CSS/HTML helpers                               |

**Schema ownership**: `gated` (via `store/`) owns `messages.db`. All
migrations in `store/migrations/`. Other daemons connect read/write but
never run migrations. `store.Migrate(db)` for test fixtures.

## Operational check (post-deploy)

```bash
sudo systemctl status arizuko_<instance>
sudo journalctl -u arizuko_<instance> --since "5 min ago" --no-pager | head -30
sudo journalctl -u arizuko_<instance> --since "5 min ago" --no-pager | grep -iE 'error|fatal'
sudo docker ps --filter "name=arizuko-" --format "{{.Names}} {{.Status}}"
```

Red flags: `"error in message loop"`, `"container timeout"`, `"circuit breaker open"`.

Adapter `/health` returns 503 `{status:"disconnected"}` when the
platform side is down even if the process is up (whapd showing QR,
mastd stream dropped, …). Check on the host:

```bash
sudo curl -s -o /dev/null -w '%{http_code}\n' http://localhost:<port>/health
```

## Shipping changes

1. Add entry to `CHANGELOG.md`
2. Add migration file `ant/skills/self/migrations/NNN-desc.md`
3. Update `ant/skills/self/MIGRATION_VERSION`
4. Update `ant/skills/self/SKILL.md`
5. Rebuild agent image

## Tagging a new version

1. Move CHANGELOG.md [Unreleased] to `[vX.Y.Z] — YYYY-MM-DD`
2. `git tag vX.Y.Z`, tag docker images (`arizuko:vX.Y.Z`, `arizuko-ant:vX.Y.Z`)
3. Add `.diary/YYYYMMDD.md` entry

## Deploy policy

- **krons** is the test/deploy target. Always deploy here first.
- **sloth** and **marinade** only on explicit user request.
- Docker requires `sudo`. `make image` / `make agent` will fail without it.

## "Nothing works" checklist

Healthchecks green but the agent doesn't reply — usually one of:

1. **`arizuko-ant` image missing**. Look for `pull access denied for arizuko-ant` in journalctl. Fix: `sudo make -C ant image`.
2. **Adapter disconnected**. `docker ps` shows `(unhealthy)` or `/health`
   returns 503 — platform link is down. whapd waits for QR scan, mastd
   stream dropped, etc. Check adapter logs, not gated's.
3. **Adapter silent**. `sudo journalctl -u arizuko_<inst> --since "10 min ago" | grep -viE health`.
4. **Container exit 125** in gated logs = image/compose mismatch, not a code bug.

Docker log driver is `none` — use `journalctl -u arizuko_<inst>`, not `docker logs`.

## Migrating from kanipi

See `MIGRATION.md`.

## Related projects

- `/home/onvos/app/eliza-atlas` — ElizaOS fork; reference for facts/memory
- `/home/onvos/app/refs/brainpro` — reference for daily notes pattern
