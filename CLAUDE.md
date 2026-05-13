# CLAUDE.md

## Identity is configured, never derived

NEVER `filepath.Base()` a runtime path to discover project name, container name, network name, or instance flavor. Compose generation writes those into env vars; daemons read them, never reverse-engineer them. Cost an outage on krons (2026-04-29): auto-deriving from container's `/srv/app/home` got `home` instead of `arizuko_krons`, every spawn failed `docker network connect`, and the queue replayed the failure forever.

## Canonical paths

- GitHub: `github.com/kronael/arizuko` — the home of this project.
- `go.mod` module: `github.com/kronael/arizuko`. All imports
  `github.com/kronael/arizuko/<pkg>`. Renamed 2026-05-13 (was
  `onvos/arizuko` historically — see CHANGELOG).
- Shippable sibling components (`crackbox/`, future `gateway/`,
  `mcpfw/`) are designed to be usable outside arizuko but share
  arizuko's single `go.mod`. We don't split them into separate
  modules; orthogonality is enforced by the import-graph rule
  (no arizuko-internal subpackage imports), not by module separation.

## Response Style

Be terse. Lead with the answer, skip preamble, skip trailing summaries
of what you just did. One-sentence replies are fine. Exceptions only
when explicitly asked or the task requires it: generating content
(specs, docs, prose), multi-step plans, root-cause walkthroughs.

## Minimality and orthogonality (non-negotiable)

Every edit, fix, skill, spec must uphold these. Don't make me restate
them on each request.

- **Minimality**: smallest change that solves the root cause. Cut prose
  that doesn't change behavior. Examples earn lines only when they
  document a real past failure (see `~/.claude/CLAUDE.md` Boring Code
  Philosophy). Hypothetical examples don't earn lines.
- **Orthogonality**: each fix touches exactly one concern. Persona
  resolution is not migrate enumeration is not dispatch lifecycle is
  not tool-use discipline. If a "fix" spans concerns, it's two fixes
  pretending to be one — split them.
- **One renderer, many sinks**: when N paths feed one consumer, exactly
  one renderer produces its input. Two paths drift silently. Same for
  skill schemas, prompt-build sites, output formatters.
- **Strict, not magical**: no silent fallbacks for missing data
  (PERSONA.md without frontmatter returns empty, not "guess from body").
  No parent-folder inheritance for group-scoped files. Operator data
  fixes belong to the operator; platform stays mechanical.

## Essence

arizuko is a multitenant Claude agent router built on plain primitives:
Go daemons, SQLite WAL, HTTP between adapters and `gated`, MCP over a
unix socket, Docker per-group containers. Every primitive scales —
`solo/inbox` and `corp/eng/sre/oncall` run the same code. Schema and
migrations live in `gated`; everything else is a thin daemon talking to
it. Read `README.md` for the daemon map, `ARCHITECTURE.md` for message
flow, the per-package `README.md` for details, this file for the
operator runbook + the philosophy.

## Build & Test

```bash
make build             # go build → ./arizuko + all daemon binaries
make lint              # go vet ./...
make test              # go test ./... -count=1 -short (fast, skips long tests)
make test-e2e          # end-to-end tests via webd slink surface (≤5 min); run before tagging
make smoke             # post-deploy health check on krons (default SMOKE_INSTANCE=krons)
make smoke SMOKE_INSTANCE=foo  # target a different instance
make images            # all docker images (router + adapters + agent)
make agent             # agent docker image (make -C ant image)

# Run a single test package
go test ./gateway/... -count=1 -run TestName
```

Tests use `modernc.org/sqlite` (pure Go, no CGO).
Exception: `gated` requires `CGO_ENABLED=1` (see Makefile).
Pre-commit hooks configured via `.pre-commit-config.yaml`.

## Architecture

See ARCHITECTURE.md for package graph, message flow, container model.

### Core vs integrations

Two flavors of feature, kept distinct in the docs:

- **System core** — always-present primitives that define the system
  shape: `gateway`, `store`, `ipc`, `auth`, `grants`, `proxyd`, `webd`,
  `dashd`, `timed`, `onbod`, `vited`, `davd`, the container runner,
  `chanlib`/`chanreg`, plus the `gated` daemon that wires them.
- **Integrations** — pluggable, deployments mix and match: per-platform
  channel adapters (`teled`, `whapd`, `mastd`, `discd`, `bskyd`,
  `reditd`, `emaid`, `twitd`, `linkd`); optional capability hooks
  (Whisper transcription via `WHISPER_BASE_URL`, TTS via `ttsd` +
  `TTS_BASE_URL`, oracle skill via `OPENAI_API_KEY` / `CODEX_API_KEY`
  in folder secrets, crackbox egress isolation, sandbox backend
  choice).

A minimal deployment runs only core + one channel adapter; a maxed-out
deployment runs all of them. Add new integrations via the extension
points in `EXTENDING.md`; the core evolves as a unit.

## Docs layout

Root UPPERCASE files: `README.md`, `ARCHITECTURE.md`, `SECURITY.md`,
`ROUTING.md`, `EXTENDING.md`, `GRANTS.md`, `CHANGELOG.md`, `CLAUDE.md`.
Per-daemon detail lives next to the source (e.g. `ipc/SECURITY.md`).
No `docs/` directory — add a per-daemon `SECURITY.md` when its threat
model outgrows a row in the root table.

### When to read what

- **README.md** — daemon map, public pitch, build/test entry.
- **ARCHITECTURE.md** — package graph, message flow, SQLite schema.
- **SECURITY.md** — threat model + egress + secrets boundaries.
- **ROUTING.md** — route table, topic/sticky/reply rules.
- **EXTENDING.md** — channels, actions, routing, mounts, skills,
  tasks, diary, autocall extension points.
- **GRANTS.md** — tier model + tool authorization scope syntax.
- **CHANGELOG.md** — what shipped, dated.

Keep `EXTENDING.md` current as extension points evolve (channels,
actions, routing, mounts, skills, tasks, diary; skill scopes;
permission tiers).

## Layout

See `ARCHITECTURE.md` for the package graph and `README.md` for the
daemon + library tables. Schema and migrations live in `store/` (gated
owns them). Per-package details co-located in each `<pkg>/README.md`.

## Conventions

- JSONL files use `.jl` extension (not `.jsonl`)
- XML tags for prompt structure, JSON for IPC/MCP/structured output
- Per-turn agent output delivered via the `submit_turn` JSON-RPC
  method on the same MCP unix socket; hidden from `tools/list`
- IPC: MCP over unix socket, socat bridge into container
- Business features (gates, grants, onboarding) are DB-backed with CLI +
  chat command for management. Infra (ports, timeouts, images, paths) stays
  as env vars in `.env`.

### Trust boundaries

`proxyd` signs identity headers; every backend verifies via
`auth/middleware.go` (`RequireSigned` strict / `StripUnsigned` lenient).
Never trust `X-User-Sub` without a sig check. Full trust model in
`SECURITY.md`.

### Subagent worktrees

For non-trivial agent work (>5 files, migrations, new specs,
cross-package refactors), pass `isolation: "worktree"` to avoid
conflicts with parallel subs or main-tree edits. Trivial edits
run on the shared tree. The Agent tool cleans up empty worktrees
automatically; otherwise it returns the worktree path + branch.

## Design principles

### Simple stays simple, complex goes deeper

arizuko's primitives scale with need. `solo/inbox` and
`corp/eng/sre/oncall/launch-q3` run the same code. Every primitive
has a one-line setup AND a deep-config path: group hierarchy
(arbitrary depth), topic kinds (default thread or `task`/`meeting`),
grants (tier defaults or per-folder rules), channels (env-var
trivial, dashd UI managed), secrets (folder-scoped by default,
user-scoped when needed). Don't force structure where it isn't
needed; don't fight it where it is.

## Data Dir

`/srv/data/arizuko_<name>/` per instance:

- `.env` — config (gateway reads from cwd)
- `store/` — SQLite DB (`messages.db`)
- `groups/<folder>/` — group files, logs, diary
- `groups/<folder>/media/<YYYYMMDD>/` — downloaded inbound attachments
- `ipc/<folder>/` — MCP unix sockets
- `groups/<folder>/.claude/` — agent session state

## Config

`.env` in data dir or env vars (`core.LoadConfig`). Anchor vars:
`CHANNEL_SECRET`, `AUTH_SECRET`, `HOST_DATA_DIR`, `CONTAINER_IMAGE`,
`WEB_HOST`, `ASSISTANT_NAME`. Per-daemon vars documented in each
`<daemon>/README.md`. Business state (gates, grants, onboarding) lives
in the DB; infra toggles live in env.

## Entrypoint

```
arizuko create <name>          seed data dir, .env, default group
arizuko run <instance>         generate compose + docker compose up
arizuko chat <instance>        interactive Claude Code on root MCP socket
arizuko invite <instance> ...  issue/list/revoke onboarding invites
```

Full command list in `cmd/arizuko/README.md`. Daemons are standalone
binaries (`gated`, `timed`, ...); see README for the full table.

## Service Architecture

Daemons end in `d`. Libraries don't. Shared SQLite (WAL). The full
daemon + library table lives in `README.md` — don't duplicate it here.
`gated` owns the schema; everything else connects read/write but never
migrates.

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

1. Add entry to `CHANGELOG.md` (release block + `>` blockquote — see "## Announcing")
2. Add migration file `ant/skills/self/migrations/NNN-vX.Y.Z-summary.md` — **every release**, including docs-only (stub body is fine; the file existing is what fires the auto-migrate broadcast)
3. Update `ant/skills/self/MIGRATION_VERSION`
4. Update "Latest migration version" in `ant/skills/self/SKILL.md`
5. Rebuild agent image

Spec: `specs/4/P-personas.md ## Versioning`. The auto-migrate hook
in `gateway.checkMigrationVersion` is the single trigger for both
skill updates AND chat broadcasts; bumping `MIGRATION_VERSION` is
what fires it. Tag and broadcast travel together.

## Tagging a new version

1. Move CHANGELOG.md [Unreleased] to `[vX.Y.Z] — YYYY-MM-DD`
2. `git tag vX.Y.Z`, tag docker images (`arizuko:vX.Y.Z`, `arizuko-ant:vX.Y.Z`)
3. Add `.diary/YYYYMMDD.md` entry

## Announcing

Each release entry opens with a `>` blockquote — that's the chat
broadcast (Telegram/Discord/WhatsApp), extracted verbatim by the
`migrate` skill. Keep it ≤ 9 lines:

```markdown
> arizuko vX.Y.Z — DD Mon YYYY
>
> • <feature> (`<api>`) — <one-line user benefit>
> • ...
>
> Full notes: github.com/kronael/arizuko/blob/main/CHANGELOG.md
```

Rules: 3–6 bullets; lead with the biggest user-facing change; active
voice; one line each; user benefit before internal detail. NO migration
numbers, file paths, commit SHAs in the blockquote — those stay in the
maintainer-facing `### Added/Changed/Fixed`. Group when more than 6
land at once: `• Voice & media — send_voice + per-platform dispatch`.

## Deploy policy

- **krons** is the test/deploy target. Always deploy here first.
- Other instances only on explicit user request.
- Docker requires `sudo`. `make images` / `make agent` will fail without it.

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
