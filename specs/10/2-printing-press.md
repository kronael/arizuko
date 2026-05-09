---
status: spec
---

# Printing Press integration

Bring the [Printing Press](https://printingpress.dev/) generator into
the ant agent: a CLI that turns APIs / OpenAPI specs / HAR captures /
URLs into agent-native Go CLIs with local SQLite mirrors and bundled
MCP servers. Pairs naturally with arizuko ant — the agent already
prefers CLIs over raw HTTP, and SQLite is the platform's storage
default.

## What Printing Press is

A Go CLI generator that emits, from any of `--spec` (OpenAPI),
`--har` (DevTools capture), or a URL (browser-sniffs the API):

- a Cobra-based CLI binary `<api>-pp-cli`
- a matching MCP server `<api>-pp-mcp`
- domain-specific SQLite tables with FTS5 indexes
- a `sync` command (incremental, cursor-tracked) and a `search`
  command (offline, fast)
- a Claude Code skill directory the agent can install

Three principles, paraphrased from the project: (1) a local SQLite
mirror beats a remote API call, (2) compound commands beat ten round
trips, (3) an agent-native CLI beats raw HTTP. The published library
ships ~53 pre-built CLIs across travel, media, food, dev, commerce,
PM. Sources: <https://printingpress.dev/>,
<https://github.com/mvanhorn/cli-printing-press>.

## Why integrate

- **Agent ergonomics.** ant's CLAUDE.md already steers agents toward
  CLIs over inline scripts. printing-press CLIs are tuned for that
  shape — agents don't have to learn a new HTTP shape per API.
- **Offline / low-cost.** Cached SQLite means the agent answers
  cheaply on cold restarts and after rate-limit hits. Cuts external
  API spend for repeated lookups.
- **MCP for free.** Every generated CLI ships with an MCP server.
  That maps directly onto gated's per-group MCP socket model — register
  the generated MCP server in `~/.claude/settings.json`, agents pick
  it up next session.
- **Compound queries.** "Show flights with Rotten Tomatoes scores
  for the in-flight movies" only works when both data sources are
  local. arizuko's tasks (research, briefings, content pipelines)
  often want cross-source joins.
- **Self-extension primitive.** ant's `/self` skill already documents
  how to add MCP servers via `~/.claude/settings.json`. printing-press
  generated MCP servers slot in cleanly — agents can install new APIs
  on demand, persisting across sessions.

## Integration shape

Five layers, simplest first.

### Layer 1 — install printing-press in the ant image

`ant/Dockerfile` already has Go 1.26+ and the toolchain. Add:

```dockerfile
RUN go install github.com/mvanhorn/cli-printing-press/v4/cmd/printing-press@latest
```

Now every agent container has the `printing-press` binary on PATH.
A new ant skill `~/.claude/skills/printing-press/SKILL.md` documents:

- Generating a CLI from an OpenAPI spec / HAR / URL
- Where the output lands (`/home/node/printing-press/library/<api>/`)
- How to register the generated MCP server in `settings.json` for
  next-session pickup

The skill teaches the agent _when to reach for printing-press_:
"You're about to write a wrapper script for an HTTP API; instead,
generate a CLI once, cache its data."

**Cost**: ~5 LOC in the Dockerfile + ~1 skill file. No runtime
overhead — the binary sits there until invoked.

### Layer 2 — pre-baked CLIs from the library

Bake a curated subset of [printing-press-library](https://github.com/mvanhorn/printing-press-library)'s
~53 CLIs into the ant image at `/usr/local/share/printing-press/`.
Examples by category:

| Category | CLI candidates                       | Why bundle                                   |
| -------- | ------------------------------------ | -------------------------------------------- |
| Dev      | `pp-pypi`, `pp-docker-hub`, `pp-nvd` | Frequent dev-skill use, low storage          |
| Media    | `pp-movie-goat`, `pp-recipe-goat`    | Match `creator`, `personal`, `trip` products |
| News     | `pp-news`, weather/sports CLIs       | Match `strategy` product                     |
| PM       | `pp-linear`, `pp-github`             | Match `pm` and `developer-style` workloads   |
| Travel   | `pp-flightgoat`, `pp-hotel`          | Match `trip` product                         |

A skill discovery hook (or `/printing-press list`) shows what's
available; agents `cp` the chosen one to `~/.claude/skills/<name>/`
or just call the binary inline.

Pre-bake decisions are reversible — start with a small "starter
pack" (~10 CLIs) and expand based on actual usage.

### Layer 3 — per-product CLI manifests

Each `ant/examples/<product>/PRODUCT.md` gains an optional section:

```toml
[[printing_press]]
cli = "pp-flightgoat"
[[printing_press]]
cli = "pp-recipe-goat"
servings_default = 4
```

`SetupGroup` reads these and copies the named CLIs (from the bundled
set or `go install`s from upstream) into the new group's
`~/.claude/skills/`. Agents in that group find them out of the box
without separate setup.

**Hook:** the existing skill-seeding code in `container/runner.go`
(`seedSkills`) extends to read `PRODUCT.md` `[[printing_press]]`
blocks and seed the matching directories.

### Layer 4 — generate `pp-arizuko` from `/v1/*`

Once [R-platform-api.md](../6/R-platform-api.md) lands and each
daemon serves OpenAPI at `/v1/openapi.json`, run:

```bash
printing-press generate \
    --spec http://gated:8080/v1/openapi.json \
    --spec http://timed:8080/v1/openapi.json \
    --spec http://onbod:8080/v1/openapi.json \
    --spec http://webd:8080/v1/openapi.json \
    --spec http://proxyd:8080/v1/openapi.json \
    --name arizuko
```

The result: `pp-arizuko` CLI + `pp-arizuko-mcp` server with:

- Local SQLite mirror of groups, tasks, routes, messages
- `pp-arizuko sync groups` / `pp-arizuko sync tasks` etc.
- Agent-native subcommands like
  `pp-arizuko tasks list --tenant atlas/main --status active`
- Compound query: `pp-arizuko query "active tasks blocked > 7d"`

Bake into the agent image as the canonical platform interface.
Replaces a lot of the inline `sqlite3` / `curl` patterns currently
documented in skills.

### Layer 5 — agent-driven on-demand generation

Agents that hit a new API generate a CLI for it on the fly:

```bash
printing-press generate --url https://api.example.com --name example
# 5-30 min later (per Phase 1.5/3 timing on printingpress.dev):
~/printing-press/library/example/example-pp-cli sync
~/printing-press/library/example/example-pp-cli search ...
```

Persist the result in `/home/node/printing-press/library/<api>/`
(group-private, survives container restart). For shared/global CLIs,
agents promote into `/workspace/share/printing-press/` (root-only).

The "gen pause" — printing-press's pipeline takes 5-30 min for new
APIs (per the README's phase timings) — fits arizuko's existing
container model: long-running generation runs in a container that
stays up beyond the typical turn, writes results to the mounted
home, and exits. The generated artifact persists.

## Concerns and answers

- **Build time inside the agent container.** Generating a fresh CLI
  invokes `go build` for the new binary. Container-side Go is
  already present; ~30s build per CLI is acceptable for an
  on-demand generation. Pre-baked CLIs don't need this.

- **Disk growth.** Each generated CLI is ~10-30 MB binary + a few MB
  SQLite cache. Per-group home directories already mount writable;
  cap with a periodic janitor or per-group quota.

- **License + provenance.** Both repositories appear to be open
  source (MIT or similar — verify before bundling). Pre-baked CLIs
  carry the upstream's license and provenance manifest
  (`.printing-press.json`). Document in `ant/Dockerfile` LICENSE
  comments.

- **Upstream churn.** Pin the printing-press version in the
  Dockerfile (`@v...`) — not `@latest` — and bump on a known
  cadence. Generated CLIs from a pinned version are reproducible.

- **MCP server registration.** Each generated `*-pp-mcp` is a
  stdio MCP server. Agents register it in
  `~/.claude/settings.json` `mcpServers` (per ant `/self` skill);
  next session it's live alongside the built-in `arizuko` server.

- **Conflict with the gated MCP socket model.** None — gated's
  socket carries platform capabilities (`send`, `schedule_task`,
  etc.); printing-press MCPs carry per-API capabilities. Both
  active simultaneously, different tool namespaces.

## What this spec is not

- Not a hard dependency. arizuko works without printing-press; this
  is an enrichment.
- Not a replacement for the platform API. `pp-arizuko` is a generated
  client over `/v1/*`; the canonical contract stays the OpenAPI spec.
- Not a sandbox or trust mechanism. printing-press-generated CLIs
  run with whatever grants the agent has. Apply normal grant scopes.

## Implementation phases

Each independently shippable.

1. **Layer 1** — printing-press binary in ant image, skill file,
   docs. Smallest demo: agent generates a CLI for a new API on
   demand. **(½ day)**
2. **Layer 2** — starter pack of ~10 pre-baked CLIs. Validate per-
   product matchings. **(1 day)**
3. **Layer 3** — `PRODUCT.md` `[[printing_press]]` block + seeding
   in `SetupGroup`. **(½ day)**
4. **Layer 4** — `pp-arizuko` once OpenAPI specs land per
   [R-platform-api.md](../6/R-platform-api.md). Phase-coupled to
   phase 6 progress. **(1 day after API ships)**
5. **Layer 5** — on-demand generation as a documented pattern;
   janitor for disk; promotion to `/workspace/share/`. **(1 day)**

## Open

- **Pinned version.** Pick a version after testing v4 in the ant
  image; freeze.
- **Pre-bake selection criteria.** Start small, expand by usage —
  but who decides? Lean: track agent invocations of `printing-press
generate` over a month, pre-bake the top hits.
- **Promotion to global.** Agents may want to share a generated CLI
  across groups (`/workspace/share/`). Permission model: root-only
  write, all-groups read. Matches existing share-mount behavior.
- **Update cadence for pre-baked CLIs.** Upstream library bumps
  often. Cadence: rebuild ant image weekly, pin upstream commit at
  build time.
- **Compound query examples.** Build a "starter compound" doc for
  agents — e.g. "compose `pp-flightgoat` + `pp-news` for
  weather-aware flight rec". Aligns with arizuko's product
  catalog.

## Code pointers

- `ant/Dockerfile` — install printing-press binary; bundle starter
  pack.
- `ant/skills/printing-press/SKILL.md` (new) — agent-facing usage
  docs.
- `container/runner.go` — `seedSkills` extension to read
  `PRODUCT.md` `[[printing_press]]` blocks.
- `ant/examples/<product>/PRODUCT.md` — per-product CLI manifests.
- `specs/6/R-platform-api.md` — OpenAPI exposure prerequisite for
  Layer 4.
