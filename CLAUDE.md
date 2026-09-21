# CLAUDE.md

## Identity is configured, never derived

NEVER `filepath.Base()` a runtime path to discover project name, container name, network name, or instance flavor. Compose generation writes those into env vars; daemons read them, never reverse-engineer them. Cost an outage on krons (2026-04-29): auto-deriving from container's `/srv/app/home` got `home` instead of `arizuko_krons`, every spawn failed `docker network connect`, and the queue replayed the failure forever.

## Canonical paths

- GitHub: `github.com/kronael/arizuko` — the home of this project.
- `go.mod` module: `github.com/kronael/arizuko`. All imports
  `github.com/kronael/arizuko/<pkg>`.
- Shippable sibling components (`crackbox/`, future `gateway/`,
  `mcpfw/`) are designed to be usable outside arizuko but share
  arizuko's single `go.mod`; orthogonality is enforced by the import-graph
  rule (no arizuko-internal subpackage imports), not by module separation.

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
- **One declaration, many readers** — the dual of the rule above, and the
  one that actually gets violated. When a second consumer needs the same
  facts at a different TIME (compose-generation before any DB exists) or
  in a different SHAPE (an env var, not rows), that justifies a second
  **reader**, never a second **format**. Ask "who else reads this?", not
  "what file do I need?". Cost: BUGS `L44` (a package declared grants and
  routes in two JSON sidecars beside the manifest that already carried
  both; `specs/5/28-packages.md`).
- **A format is a coupling; keep the parts orthogonal.** A package ships
  docker's compose fragment AND arizuko's manifest — two files because
  they answer to two owners, not one file that knows about both. Split by
  who defines the format, never by what is convenient to parse together.
- **Strict, not magical**: no silent fallbacks for missing data
  (PERSONA.md without frontmatter returns empty, not "guess from body").
  No parent-folder inheritance for group-scoped files. Operator data
  fixes belong to the operator; platform stays mechanical.
- **MCP + REST hand-rolled and uniform**: every resource is reachable
  via both MCP (for agents) and REST (for humans / external tools)
  through one hand-written handler — no auto-generated DSL, no
  catalog-driven mapper. MCP is the canonical protocol; REST is the
  boundary impedance match for non-MCP callers. Spec:
  `specs/5/17-openapi-mcp.md`.
- **A resource's name IS its wire identity, globally unique**: the
  resreg `Name` becomes the `/v1/<name>` REST path AND the MCP tool
  prefix. Two daemons must NEVER register the same resource name —
  routd's message-routing table is `routes`, proxyd's reverse-proxy
  table is `proxyd_routes`; never both `routes`. Owner resolution is
  compose service naming (`<daemon>:8080` via the `<DAEMON>_URL` env
  compose writes) — NOT a lookup registry. Cost: proxyd's live route
  resource drifted to `Name: "routes"` while its catalog + OpenAPI already
  said `proxyd_routes` (fixed 2026-07-01).
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
  registration. Operator REST uses the default `auth.Authorize` (scope/ACL);
  the agent MCP socket (routd) injects
  `db.AuthorizeTurn(sender, turn-folder, target, "mcp:"+tool, params)` —
  the granted-row path, where the row's scope glob IS the containment and
  the turn's folder confers in full while the attested sender adds only
  inside that folder's subtree (`specs/5/33-paths-roles.md` §"Two axes").
  Never a second hand-rolled check inside the handler. A handler that
  resolves a `jid`/`folder`/`run_id` param MUST bind it to the caller's
  folder. Drift = cross-folder access (cost a krons split review
  2026-06-07). Spec: `specs/5/17-openapi-mcp.md`.

(Spec-first change discipline lives in the `specs` skill.)

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

Every test that touches a store runs against a DynamoDB endpoint, so the suite
needs docker: `dyntest` starts one container per test binary and drops it at the
end. `ARIZUKO_TEST_DYNAMO_ENDPOINT` points the same suites at an endpoint that
is already running — the PostgreSQL deployment, or Amazon's own service.
Pre-commit hooks configured via `.pre-commit-config.yaml`.

## When to read what

- **README.md** — daemon + library tables, public pitch, the shape
  (`solo/inbox` and `corp/eng/sre/oncall` run the same code), direction.
- **INSTALL.md** — prerequisites, blocker fixes, `.env`, adapter tokens,
  troubleshooting (the "healthchecks green but no reply" checklist). Start
  here when helping a user install arizuko.
- **ARCHITECTURE.md** — package graph, message flow, SQLite schema, core vs
  integrations, data dir + container mount layout, observability, design
  principles.
- **SECURITY.md** — threat model, egress, secrets, identity header trust
  (`proxyd` is the sole `X-User-*` stamper; backends trust the headers only
  behind its `service:proxyd` bearer).
- **ROUTING.md** — route table, topic/sticky/reply rules. `/chat/<token>/`
  and `/hook/<token>` surfaces; spec `specs/5/W-webhook-routes.md`.
- **EXTENDING.md** — channels, actions, routing, mounts, skills, tasks,
  diary, autocalls, output styles, host mounts, swapping the harness. Keep
  it current as extension points evolve.
- **GRANTS.md** — pointer to `specs/5/33-paths-roles.md` (the model) +
  `specs/5/32-acl-unified.md` (the row + evaluator) + the operator concepts page.
- **CHANGELOG.md** — what shipped, dated.
- **MIGRATION.md** — migrating from kanipi.
- **`cmd/arizuko/README.md`** — every `arizuko` subcommand (`create`, `run`,
  `chat`, `invite`, `send` vs `chat` vs `talk`, …).
- **`<pkg>/README.md`** — per-daemon detail next to the source (env vars,
  coverage gaps, e.g. `ipc/SECURITY.md`). No `docs/` directory.
- **`resreg/README.md`** — the OpenAPI engine and why a daemon cannot
  advertise a resource it does not mount.
- **`template/web/CLAUDE.md`** — the operator web docs (`/pub/arizuko/`,
  source `template/web/pub/`): voice, the visual identity, the maintenance
  map, and the ONLY deploy-to-krons procedure. Loads when you touch that
  tree.
- **`specs/CLAUDE.md`** — definition of done for a spec; loads under `specs/`.
- **`ant/skills/self/migrations/CLAUDE.md`** — the agent migration system.
- **`.claude/skills/eval/checks.md`** — `/eval <instance>`: the 23 health
  checks and the error census. `.claude/skills/eval/gates.md` holds the five
  post-deploy gates and the `anteval` setup.

## Discoverability

Every HTTP-serving daemon mounts `GET /openapi.json`, engine-emitted from
`resreg.Resource.RowType` reflection and the daemon's own mux — no
`huma`, no `swag`, no hand JSON, no codegen; public, mounted before auth.
Never derive the advertised set by probing paths (`resreg/README.md`).

## Observability

Every daemon calls `defer obs.Setup("name", instance)()` at the top of
`main()`; with `OTEL_EXPORTER_OTLP_ENDPOINT` unset that is zero overhead.
`audit_log` stays SQLite-canonical; OTLP is observability only. Wiring
rules: `obs/README.md`; model: `ARCHITECTURE.md` §Observability.

## Refine scope

`/refine` (or "clean up", "polish", "finalize") covers the full surface in
one pass: **code** (minimize, orthogonalize, delete dead paths, kill
duplication); **repo docs** (root UPPERCASE files, per-package
`<pkg>/README.md`, `specs/index.md` + spec frontmatter); **web docs**
(`template/web/pub/` drift sweep + link check + match against the latest
`CHANGELOG.md` blockquote); **verify** (`make build && make lint && go test
./... -short` green); **commit** (single `[refined] <summary>` per pass). A
round that finds nothing commits nothing.

## Conventions

- JSONL files use `.jl` extension (not `.jsonl`)
- XML tags for prompt structure, JSON for IPC/MCP/structured output
- Per-turn agent output delivered via the `submit_turn` JSON-RPC
  method on the same MCP unix socket; hidden from `tools/list`
- IPC: MCP over unix socket, socat bridge into container
- Daemons end in `d`; libraries don't. Each split daemon owns + migrates
  its own DB (`routd.db`/`runed.db`/`auth.db`/`onbod.db`); no daemon
  migrates another's. `store/` is the shared schema library.
- Business features (gates, grants, onboarding) are DB-backed with CLI +
  chat command for management. Infra (ports, timeouts, images, paths) stays
  as env vars in `.env`.
- **The test is dynamic vs static, and packages decide it.** Anything a
  package can install or remove at runtime is dynamic, so it belongs in the
  owning daemon's DB — never in a generated env var, which silently stops
  applying and looks healthy (BUGS `L45`: 7 of 18 shipped proxyd routes
  never reached krons). Cross-daemon reach is federation: each daemon owns
  its table and serves it on its own `/v1`; nobody reads another's env.
- **Adding a channel adapter**: a `template/services/<daemon>.yml` compose
  fragment + a `<daemon>.yaml` manifest if it needs inbound web paths; no
  edit to `proxyd/main.go` or `compose/compose.go` (`EXTENDING.md` §Adding
  a channel adapter).
- **Daemon HTTP port: `:8080` inside the container, always.** Every
  daemon's `LISTEN_ADDR` code-default is `:8080` and every fragment in
  `template/services/` declares it explicitly, so neither drifts. Backend
  URLs (proxyd routes, compose generation, intra-container `ROUTER_URL`)
  hardcode `:8080`. Multi-daemon local-dev sets `LISTEN_ADDR=:90XX`; that
  is the exception.
- **Split write-discipline**: FS-mounted daemons (dashd, onbod, CLI) write
  owned tables directly to the owner's DB; non-mounted daemons (slakd,
  timed) write via the owner's HTTP API + service token.

## Subagent worktrees

For non-trivial agent work (>5 files, migrations, new specs,
cross-package refactors), pass `isolation: "worktree"`; trivial edits run
on the shared tree. The Agent tool cleans up empty worktrees; otherwise it
returns the worktree path + branch. The parallel-edit ban is in
`~/.claude/CLAUDE.md`.

## Deploying

- **Deploy = `sudo systemctl restart arizuko_<instance>`** — `arizuko run`
  is initial setup only. Docker requires `sudo` (`make images` / `make
agent` fail without it).
- **krons** is the test/deploy target. Always deploy here first; other
  instances only on explicit user request.
- **A deploy is not done until `anteval --smoke` passes.** Gates 1-4 (build,
  ship, schema, census) pass on a fleet whose agents answer nothing — BUGS
  `L149` shipped green through all of them. Procedure:
  `.claude/skills/eval/gates.md`.
- **A release is an image.** `ant/` — skills, `CLAUDE.md`, output-styles —
  is baked to `/opt/arizuko` by the Dockerfile. `APP_SRC_DEV=1` binds
  `HOST_APP_DIR` read-only for the dev loop; NEVER set it on an instance —
  it puts an uncommitted working tree in front of every live agent, so an
  edit ships with no build, no tag and no restart (krons ran that way until
  2026-08-09).

## Shipping changes

Every release — docs-only included — adds
`ant/skills/self/migrations/<NNN>-v<X.Y.Z>-summary.md` and bumps
`MIGRATION_VERSION` (steps in `ant/skills/self/migrations/CLAUDE.md`).
That bump is the single trigger for skill sync AND the chat broadcast;
forgetting it is silent. The `CHANGELOG.md` entry follows "## Announcing".

## Tagging a new version

1. Move CHANGELOG.md [Unreleased] to `[vX.Y.Z] — YYYY-MM-DD`
2. `git tag vX.Y.Z`, tag docker images (`arizuko:vX.Y.Z`, `arizuko-ant:vX.Y.Z`)
3. Run the five post-deploy gates; `anteval --smoke` is not optional on a tag.
4. **Release the docs too**: bump `ARIZUKO_VERSION` in
   `template/web/pub/arizuko/assets/hub.js` (the only copy), update any page
   whose surface changed (`template/web/CLAUDE.md` maintenance map), deploy
   via its `cp` workflow and verify `/pub/*` returns 200.
5. Add `.diary/<YYYYMMDD>.md` entry

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
