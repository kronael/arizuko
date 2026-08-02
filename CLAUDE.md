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
- **MCP + REST hand-rolled and uniform**: every resource is reachable
  via both MCP (for agents) and REST (for humans / external tools)
  through one hand-written handler — no auto-generated DSL, no
  catalog-driven mapper. arizuko is agent-first; MCP is the canonical
  protocol; REST is the boundary impedance match for non-MCP callers.
  Spec: `specs/5/17-openapi-mcp.md`. Cost is N+M hand-rolled
  handlers; gain is one shape across the platform — agent and human
  see the same actions and the same folder-containment discipline, each
  checked by its own injected gate (agent tier-grants vs operator
  scope/ACL — see "Auth is a uniform middleware").
- **A resource's name IS its wire identity, globally unique**: the
  resreg `Name` becomes the `/v1/<name>` REST path AND the MCP tool
  prefix. Two daemons must NEVER register the same resource name —
  routd's message-routing table is `routes`, proxyd's reverse-proxy
  table is `proxyd_routes`; never both `routes`. Owner resolution is
  compose service naming (`<daemon>:8080` via the `<DAEMON>_URL` env
  compose writes) — NOT a lookup registry; identity is configured, not
  derived. Cost: proxyd's live route resource drifted to `Name:
"routes"` while its catalog + OpenAPI already said `proxyd_routes`
  (fixed 2026-07-01).
- **Every cold-tier management entity is a resreg resource — no
  exceptions**: a new operator/agent-managed table ALWAYS registers a
  resreg `Resource` (REST handler + `x-mcp-*` annotations → MCP derived,
  per `5/17`). NEVER hand-roll a bespoke `ipc/ipc.go` management tool +
  `dashd` direct-DB CRUD for it — that drifts the agent and operator
  surfaces apart (the ~45 hand-rolled ipc.go tools are the debt `5/16`
  reverses). A management resource without a resreg registration is a
  review-blocker. Only hot-tier agent actions (`reply`/`send`/`inspect_*`,
  no REST twin) are MCP-only, hand-authored.
- **Auth is a uniform middleware, bound to handler + params**: authn
  (who) and authz (may you do THIS to THESE params) are two orthogonal
  middleware layers every daemon applies identically. The authz gate is
  INJECTED per surface — `resreg` carries no auth policy of its own — and
  binds `(action, required-scopes, target-resolver)` at route/tool
  registration. Operator REST uses the default `auth.Authorize` (scope/ACL,
  no tier); the agent MCP socket (routd) injects
  `db.Authorize(sub, folder, "mcp:"+tool, params)` — the tier-default-grants
  path. Same handler, same folder-containment discipline, two identity
  sources (socket-folder vs JWT-folder) and two injected gates — never a
  second hand-rolled check inside the handler. A handler that resolves a
  `jid`/`folder`/`run_id` param MUST bind it to the caller's folder. Drift =
  cross-folder access (cost a krons split review 2026-06-07). Spec:
  `specs/5/17-openapi-mcp.md`.

(Spec-first change discipline lives in the `specs` skill.)

## Essence

arizuko is a multitenant Claude agent router built on plain primitives:
Go daemons, SQLite WAL, HTTP between adapters and `routd`, MCP over a
unix socket, Docker per-group containers. Every primitive scales —
`solo/inbox` and `corp/eng/sre/oncall` run the same code. Schema and
migrations live in each split daemon (`routd`/`runed`/`authd` own their
own DBs); adapters are thin daemons talking to `routd`. Read `README.md` for the daemon map, `ARCHITECTURE.md` for message
flow, the per-package `README.md` for details, this file for the
operator runbook + the philosophy.

## Build & Test

Full target list (build/images/agent/smoke) in [INSTALL.md](INSTALL.md).
Dev loop:

```bash
make lint              # go vet ./...
make test              # go test ./... -count=1 -short (fast, skips long tests)
make test-e2e          # end-to-end via webd route-token surface (≤5 min); run before tagging

# Run a single test package
go test ./routd/... -count=1 -run TestName
```

Tests use `modernc.org/sqlite` (pure Go, no CGO).
Exception: the `arizuko` binary builds with `CGO_ENABLED=1` (see Makefile).
Pre-commit hooks configured via `.pre-commit-config.yaml`.

## Architecture

See ARCHITECTURE.md for package graph, message flow, container model.

### Core vs integrations

Two flavors of feature: always-present **core** primitives that define
the system shape vs pluggable **integrations** (channel adapters +
optional capability hooks) that deployments mix and match. A minimal
deployment runs core + one channel adapter; a maxed-out one runs all.
Canonical list: ARCHITECTURE.md "Core vs integrations". Add new
integrations via `EXTENDING.md`; the core evolves as a unit.

### Discoverability

Every HTTP-serving daemon mounts `GET /openapi.json` returning an
OpenAPI 3.1 doc for the resources it owns. The doc is engine-generated
from `resreg.Resource.RowType` reflection — no `huma`, no `swag`, no
hand-rolled JSON, no codegen step. Endpoint is public; mount before
auth middleware. Drift between handler and doc is structurally
impossible because both read the same struct.

Aggregator landing: `/pub/arizuko/reference/openapi.html` lists every
daemon's `/openapi.json` URL with a one-line description. Spec:
[`specs/5/8-yaml-manifests.md`](specs/5/8-yaml-manifests.md)
§"OpenAPI emission" (subsumes the former openapi-discoverable spec).

### Observability

Three substrates (audit_log / journald / OTLP), all opt-in: see
[ARCHITECTURE.md](ARCHITECTURE.md) `## Observability`. Wiring rules for a
daemon or when adding instrumentation:

- Every daemon calls `defer obs.Setup("name", instance)()` at top of `main()`.
- Export env (unset → zero overhead): `OTEL_EXPORTER_OTLP_ENDPOINT` (logs),
  `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` (spans), `METRICS_ENABLED`
  (Prometheus `/metrics`).
- Adding metrics: keep label cardinality bounded (folder, model, status — not
  unbounded IDs). Adding spans: `obs.StartSpan`/`EndSpan` at the listed sites
  (turn lifecycle, model calls, container spawns, cross-daemon HTTP).
- `audit_log` stays SQLite-canonical; OTLP is observability only.

Spec [`specs/5/O-observability.md`](specs/5/O-observability.md), library
[`obs/`](obs/).

## Docs layout

Root UPPERCASE files: `README.md`, `INSTALL.md`, `ARCHITECTURE.md`, `SECURITY.md`,
`ROUTING.md`, `EXTENDING.md`, `GRANTS.md`, `CHANGELOG.md`, `CLAUDE.md`.
Per-daemon detail lives next to the source (e.g. `ipc/SECURITY.md`).
No `docs/` directory — add a per-daemon `SECURITY.md` when its threat
model outgrows a row in the root table.

### When to read what

- **README.md** — daemon map, public pitch, build/test entry.
- **INSTALL.md** — prerequisites, blocker fixes, step-by-step setup. Start here
  when helping a user install arizuko on a fresh machine.
- **ARCHITECTURE.md** — package graph, message flow, SQLite schema.
- **SECURITY.md** — threat model + egress + secrets boundaries.
- **ROUTING.md** — route table, topic/sticky/reply rules. `/chat/<token>/`
  and `/hook/<token>` surfaces; spec `specs/5/W-webhook-routes.md`.
- **EXTENDING.md** — channels, actions, routing, mounts, skills,
  tasks, diary, autocall extension points.
- **GRANTS.md** — pointer to `specs/5/32-acl-unified.md` (canonical) + the operator concepts page.
- **CHANGELOG.md** — what shipped, dated.

Keep `EXTENDING.md` current as extension points evolve (channels,
actions, routing, mounts, skills, tasks, diary; skill scopes;
permission tiers).

### Updating the web docs

Operator-facing web docs (the `/pub/...` site) live in
`template/web/pub/` — that's source-of-truth. Voice and style guide
is in `template/web/CLAUDE.md`.

**Arizuko visuals are load-bearing — never adopt another site's look.**
We borrow IA + content patterns from external references (Divio
four-category split, Stripe three-column layout, dbt's reference-page
rhythm — see `~/facts/technical-guide-structure-patterns.md` on krons),
but the visual identity stays ours: hub.css palette, its corner radii, dense
typography, arizuko color twists (hub.css is the source of truth, not any
px figure). External references inform structure
and tone; the look is the arizuko brand and does not move.

Workflow:

1. Edit pages under `template/web/pub/`.
2. Verify locally: open the HTML directly or via any static file
   server. No build step.
3. Sync to running instances (krons hosts the canonical site at
   `https://krons.fiu.wtf/pub/arizuko/`):

   ```bash
   sudo rsync -a --delete template/web/pub/ /srv/data/arizuko_krons/web/pub/arizuko/
   ```

4. Verify live: `curl -s https://krons.fiu.wtf/pub/arizuko/concepts/routing.html | head`.

The arizuko docs live under `/pub/arizuko/` on the krons host (one
of several sites that vited serves from
`/srv/data/arizuko_krons/web/pub/`). Don't sync to other instances'
web roots unless they explicitly serve the docs site too.

`template/web/pub/` is checked into git. Edits to `/srv/data/.../web/pub/`
are NOT — they're a deployment artifact. If you find improvements on
the live krons that aren't in template, copy them back before
overwriting.

## Layout

See `ARCHITECTURE.md` for the package graph and `README.md` for the
daemon + library tables. Each split daemon owns + migrates its own DB
(`routd.db`/`runed.db`/`auth.db`/`onbod.db`); `store/` is the shared
schema library. Per-package details co-located in each `<pkg>/README.md`.

## Refine scope

`/refine` (or any user request like "clean up", "polish", "finalize")
covers the full surface in one pass:

- **Code** — `improve` + `simplify` agents: minimize, orthogonalize,
  delete dead paths, kill duplication
- **Repo docs** — root UPPERCASE files (`README.md`, `ARCHITECTURE.md`,
  `SECURITY.md`, `GRANTS.md`, `EXTENDING.md`, `CHANGELOG.md`),
  per-package `<pkg>/README.md`, `specs/index.md` + spec frontmatter
- **Web docs** — `template/web/pub/` operator-facing pages, including
  `concepts/`, `reference/`, `howto/`. Drift sweep + link check + match
  against latest `CHANGELOG.md` blockquote
- **Verify** — `make build && make lint && go test ./... -short` green
- **Commit** — single `[refined] <summary>` commit per pass

If a refine round finds nothing to change, it commits nothing and
reports a clean state. Multiple rounds are valid — each pass surfaces
issues the prior one couldn't see.

## Conventions

- JSONL files use `.jl` extension (not `.jsonl`)
- XML tags for prompt structure, JSON for IPC/MCP/structured output
- Per-turn agent output delivered via the `submit_turn` JSON-RPC
  method on the same MCP unix socket; hidden from `tools/list`
- IPC: MCP over unix socket, socat bridge into container
- Business features (gates, grants, onboarding) are DB-backed with CLI +
  chat command for management. Infra (ports, timeouts, images, paths) stays
  as env vars in `.env`.
- **Adding a channel adapter**: ship a `template/services/<daemon>.yml`
  (a partial compose file — docker `include`s it verbatim) + a
  `<daemon>-routes.json` if it needs inbound web paths. No edit to
  `proxyd/main.go` or `compose/compose.go`. Specs:
  `specs/5/7-proxyd-standalone.md`, `specs/5/27-compose-native-packaging.md`.
- **Daemon HTTP port: `:8080` inside the container, always.** Every
  daemon's `LISTEN_ADDR` code-default is `:8080`; every package fragment
  in `template/services/` declares `LISTEN_ADDR=:8080` explicitly
  (set in both places so neither drifts). Docker network namespacing
  makes per-container `:8080` collision-free. Multi-daemon local-dev
  sets `LISTEN_ADDR=:90XX` explicitly; this is the exception. Backend
  URLs (proxyd routes, compose generation, intra-container
  `ROUTER_URL`) all hardcode `:8080`. Don't invent per-adapter port
  numbers in code defaults — keep them `:8080` so code-and-template
  agree.

### Trust boundaries

`proxyd` proves identity with a `service:proxyd` ES256 bearer verified
by `auth.ProxydTransit`, then trust-stamps `X-User-*` headers. Backends
verify the bearer (`auth/middleware.go`), never a per-request signature;
`X-User-*` is trusted only when that transit proof holds. Full trust
model in `SECURITY.md` §"Identity header trust".

### Split write-discipline

FS-mounted daemons (dashd, onbod, CLI) write owned tables directly to the
owner's DB; non-mounted daemons (slakd, timed) write via the owner's HTTP API +
service token.

### Subagent worktrees

For non-trivial agent work (>5 files, migrations, new specs,
cross-package refactors), pass `isolation: "worktree"` to avoid
conflicts with parallel subs or main-tree edits. Trivial edits
run on the shared tree. The Agent tool cleans up empty worktrees
automatically; otherwise it returns the worktree path + branch.

NEVER run multiple code-editing subagents in parallel on the shared
main tree — they interleave (one reverts another's edits, mid-flight
commits, reviewers read half-edited files). Run code edits ONE AT A
TIME unless the user explicitly authorizes parallel overlapping
changes; for genuine parallel code work, give each sub its own
`isolation: "worktree"`. Parallel on the shared tree is fine only for
READ-ONLY subs (verify / review / research).

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

- `.env` — config (daemons read from cwd)
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
binaries (`authd`, `routd`, `runed`, `timed`, ...); see README for the full table.

## Quick Setup

Full install walkthrough — prerequisites, build, `.env` vars, adapter tokens,
register group, run, verify — lives in [`INSTALL.md`](INSTALL.md). Read that
first when helping a user install arizuko.

Operator-specific here:

- **Deploy = `sudo systemctl restart arizuko_<instance>`** — `arizuko run`
  is initial setup only.
- Docker requires `sudo` (`make images` / `make agent` fail without it).
- If healthchecks are green but the agent doesn't reply, see the "Nothing
  works" checklist below.

## Service Architecture

Daemons end in `d`. Libraries don't. Shared SQLite (WAL). The full
daemon + library table lives in `README.md` — don't duplicate it here.
Each split daemon owns + migrates its own schema; no daemon migrates
another's.

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

## OTLP export (operator observability)

To push events to an OTel collector (Grafana / Tempo / Datadog / Honeycomb),
set `OTEL_EXPORTER_OTLP_ENDPOINT` in the instance `.env`; unset → zero
overhead, stderr JSON only. Set → every slog event fans to stderr AND OTLP,
and records carrying `turn_id` get a deterministic TraceID
(`sha256(instance + "/" + turn_id)[:16]`) so the collector groups one turn.
Model + wiring in the `### Observability` runbook above and
[ARCHITECTURE.md](ARCHITECTURE.md) `## Observability`.

## Shipping changes

1. Add entry to `CHANGELOG.md` (release block + `>` blockquote — see "## Announcing")
2. Add migration file `ant/skills/self/migrations/NNN-vX.Y.Z-summary.md` — **every release**, including docs-only (stub body is fine; the file existing is what fires the auto-migrate broadcast)
3. Update `ant/skills/self/MIGRATION_VERSION`
4. Update "Latest migration version" in `ant/skills/self/migration.md`
   (SKILL.md is the index; the version line lives in migration.md)
5. Rebuild agent image

Spec: `specs/4/P-personas.md ## Versioning`. The auto-migrate hook
in `routd.checkMigrationVersion` is the single trigger for both
skill updates AND chat broadcasts; bumping `MIGRATION_VERSION` is
what fires it. Tag and broadcast travel together.

## Tagging a new version

1. Move CHANGELOG.md [Unreleased] to `[vX.Y.Z] — YYYY-MM-DD`
2. `git tag vX.Y.Z`, tag docker images (`arizuko:vX.Y.Z`, `arizuko-ant:vX.Y.Z`)
3. **Release the docs too** — a release ALWAYS ships the web docs: bump the
   `ARIZUKO_VERSION` stamp in BOTH `template/web/pub/arizuko/assets/hub.js`
   (the one the doc pages load) AND `template/web/pub/assets/hub.js`
   (twin — collapse tracked in BUGS 2026-07-14), update any
   page whose surface changed (per `template/web/CLAUDE.md` maintenance map),
   then deploy via the `cp` workflow and verify `/pub/*` returns 200. Docs are
   part of the release, not an afterthought.
4. Add `.diary/YYYYMMDD.md` entry

## Announcing

Each release entry in `CHANGELOG.md` opens with a `>` blockquote — keep it
for the record. Shape:

```markdown
> arizuko vX.Y.Z — <tagline>
>
> <one sentence: what changed, why a user cares>
>
> • <change> — <what now works better>
> ...
>
> Full notes: github.com/kronael/arizuko/blob/main/CHANGELOG.md
```

The `migrate` skill broadcasts only the version header line + the first `>`
sentence + changelog URL — not the full blockquote. The bullets and voice
guide are for the CHANGELOG, not the broadcast.

## Deploy policy

- **krons** is the test/deploy target. Always deploy here first.
- Other instances only on explicit user request.
- Docker requires `sudo`. `make images` / `make agent` will fail without it.

## "Nothing works" checklist

Healthchecks green but the agent doesn't reply — usually one of:

1. **`arizuko-ant` image missing**. Look for `pull access denied for arizuko-ant` in journalctl. Fix: `sudo make -C ant image`.
2. **Adapter disconnected**. `docker ps` shows `(unhealthy)` or `/health`
   returns 503 — platform link is down. whapd waits for QR scan, mastd
   stream dropped, etc. Check adapter logs, not routd's.
3. **Adapter silent**. `sudo journalctl -u arizuko_<inst> --since "10 min ago" | grep -viE health`.
4. **Container exit 125** in runed logs = image/compose mismatch, not a code bug.

Docker log driver is `none` — use `journalctl -u arizuko_<inst>`, not `docker logs`.

## Migrating from kanipi

See `MIGRATION.md`.
