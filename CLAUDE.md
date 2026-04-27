# CLAUDE.md

## Response Style

Be terse. Lead with the answer, skip preamble, skip trailing summaries
of what you just did. One-sentence replies are fine. Exceptions only
when explicitly asked or the task requires it: generating content
(specs, docs, prose), multi-step plans, root-cause walkthroughs.

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

See `ARCHITECTURE.md` for the package graph and `README.md` for the
daemon + library tables. Schema and migrations live in `store/` (gated
owns them). Per-package details co-located in each `<pkg>/README.md`.

## Conventions

- JSONL files use `.jl` extension (not `.jsonl`)
- XML tags for prompt structure, JSON for IPC/MCP/structured output
- Container output delimited by `---ARIZUKO_OUTPUT_START---` / `---ARIZUKO_OUTPUT_END---`
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

`.env` in data dir or env vars (`core.LoadConfig`). Anchor vars:
`CHANNEL_SECRET`, `AUTH_SECRET`, `HOST_DATA_DIR`, `CONTAINER_IMAGE`,
`WEB_HOST`, `ASSISTANT_NAME`. Per-daemon vars documented in each
`<daemon>/README.md`. Business state (gates, grants, onboarding) lives
in the DB; infra toggles live in env.

## Entrypoint

```
arizuko create <name>          seed data dir, .env, default group
arizuko run <instance>         generate compose + docker compose up
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
