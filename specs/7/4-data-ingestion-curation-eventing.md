---
status: open-questions
depends: specs/7/2-data-model.md, specs/7/3-git-as-truth.md
---

# specs/7/4 — data ingestion, curation, eventing in the agent-is-data world

## Why this exists

Phase 7 lands the platform thesis: MCP+REST as the runtime surface
(7/1), tiered entities (7/2), git as cold-tier truth (7/3). What it
does NOT say is what happens at the edges where data enters, gets
curated, and triggers reactions across products. After 7/3 ships, an
"agent product" is largely an entry in a git repo (`agents.toml`
block + persona + skills + ACL refs + secret refs). The user's open
question (`.diary/20260525.md`):

> once we deliver the agent-is-data, the whole thing will just be an
> entry in a git repo somewhere. but i guess we will rather need
> something more. spec this as an open question in a suitable phase.

This spec poses the questions. Design happens later.

## Where we are today

**Ingestion** — 10+ channel adapters (`teled`, `slakd`, `whapd`,
`discd`, `mastd`, `bskyd`, `reditd`, `emaid`, `twitd`, `linkd`,
`webd`) `POST /v1/messages` into the shared store via the channel
protocol (`specs/4/1`); `timed` injects scheduled prompts; `davd`
mounts WebDAV for human drops; `filewd` (`specs/8/3`) proposes
agent-side file events; MCP connectors land data as tool-call results.
All paths converge on `messages`. No "ingestion source" entity, no
per-source dedup key, no per-source retention.

**Curation** — `/compact-memories` rolls day → week → month into
`episodes/` and `diary/` (v0.45.9 hardened, `ff2c0eb`); `MEMORY.md`
per folder; `facts/` as mounted KB (no embeddings); `.diary/` per
day. N skills writing to N filesystem locations. No curation-pipeline
abstraction.

**Eventing** — `routes` table dispatches inbound → folder (sticky
bindings, observe windows, engagement TTLs all ride this one
mechanism); `set_routes` MCP tool mutates it; `route_tokens` tie
external callers to folders (`specs/5/W`); `scheduled_tasks` fire as
messages; `round_done` SSE on `web:<folder>` for web subscribers. No
pub/sub primitive, no cross-product subscription.

## What changes under agent-is-data + git-as-truth

Cold tier (config) goes to git; hot tier (queue, cursors, in-flight)
stays in SQLite; secrets stay AES-256-GCM in SQLite with refs in
git. The three axes map onto that boundary differently:

- **Ingestion** is hot-path: inbound payloads are queue writes.
  Adapter config (which workspace, which secret) is cold; the payload
  is hot. The product manifest carries neither today.
- **Curation** straddles. Inputs are hot (messages, attachments);
  outputs are filesystem artifacts (`MEMORY.md`, `episodes/`,
  `.diary/`) that 7/3 commits to git on the per-turn boundary. So
  outputs land in git automatically — but cadence, scope, and
  retention are not declared anywhere a product can carry.
- **Eventing** is the messiest. Routes are cold; dispatch is hot.
  Cross-product reactions ("when A sees X, notify B") have no
  primitive — agents synthesize them by calling MCP at turn end.

## Open questions

Numbered. Each has a one-line frame and the axes; none has a designed
answer.

### Q1. Inbound storage shape — git, SQLite, or both?

Already flagged in `7/3 ## Hot path`. Restated because every other
ingestion question depends on it. Options: hot-only in SQLite |
per-day digest in git (`turns/<YYYY-MM-DD>.jl` per chat per folder) |
per-source split (Slack digest, webhook full, voice media-only).
Affects audit, replay, fork cost, repo size.

### Q2. Product manifest — does it declare ingestion?

`PRODUCT.md` today (`specs/8/P-product-templates.md`) declares
`name`, `tagline`, `skills`, `[[env]]` hints. Phase 7 introduces
`agents.toml` as the platform-level composer. Does it extend to
ingestion?

```toml
[[ingestion]]
source = "slack:team-eng"
dedup_key = "ts"
retention = "90d"
digest = "per-day"
```

Options: fully declarative in manifest | connectivity stays in
`services/*.toml`, manifest is silent | hybrid (connectivity in
services TOML, semantics in `agents.toml`).

### Q3. Curation — declared cadence or agent-driven?

Today an agent decides when to compact (every ~100 turns,
PreCompact). Operators wire `scheduled_tasks` rows by hand. Options:
`[[curation]]` blocks declaring cadence + scope | stay agent-driven
| hybrid (manifest guarantees a floor, agent extends).

May 2026 audit (`.diary/20260525.md ## 11:30`) found 578 silent
"success" cron runs with 35+ rollups missing. Either direction must
make the failure mode loud.

### Q4. Curation output location — git repo, instance tree, SQLite?

Three plausible homes: per-product git repo (forking carries
curated state) | per-instance "curated" tree (for outputs that span
products — instance-wide rollups, cross-folder briefings) | SQLite
warm tier (for high-volume curated streams: embeddings, FTS indexes).
Likely a mix. Which kind goes where, and is that declared or
conventional?

### Q5. Cross-product eventing — routes, subscriptions, or new?

Today the only cross-folder primitive is the routes table. Options:
routes scale (N rows per relationship; boring, traceable) |
`[[subscription]]` in `agents.toml` (product A subscribes to
"inbound with label=urgent in corp/eng/sre"; platform delivers
without route-row pollution) | real pub/sub layer (NATS-shaped).
Routes are the boring-tech answer; subscriptions are
declarative-additive; an event bus spends innovation tokens.

### Q6. "Something more" — minimum platform under the git repo

`git clone arizuko-agent-foo` does NOT give a running agent. What
must exist outside the repo: secrets resolver (refs in git, blobs in
operator SQLite), container runner, channel adapter processes,
ingestion daemons, curation primitives (`timed`, compact-memories,
diary writer), reconcile loop (`arizuko apply`).

The question is which of these are platform-fixed (operator runs one
per instance) vs product-pluggable (manifest declares "needs slakd +
mastd + davd"; platform installs on apply). The current daemon set
is fixed; under agent-is-data, is it still?

### Q7. Retention and deletion — git history, SQLite GC, per-source?

Operator says "drop ingested data older than 90 days." Three
mechanisms collide: SQLite GC (straightforward, nightly vacuum
extends) | git history rewrite (destroys the audit guarantee 7/3
sells; not viable) | per-source policy (Slack ≠ webhook ≠ voice).

GDPR / right-to-be-forgotten makes this load-bearing for EU-data
operators. Does `arizuko retention apply` walk SQLite + scrub media
files + add a tombstone commit? Per source? Per folder? Per user
sub? Unanswered.

### Q8. Eventing scope — per-folder, per-product, per-instance, federated?

Today eventing is per-folder (routes, sticky, engagement) or
per-instance (operator notifications via `role:operator`). No
per-product (a product can span folders), no cross-instance
(sloth → krons), no federated (third-party subscribers).

Options: stay per-folder only | add per-product (manifest carries
subscription set) | add per-instance pub/sub (operator-level
reactions: any product hitting circuit-breaker → dashd notification)
| add federated (git remotes as the federation primitive;
cross-instance events through `git push` of a sidecar branch + pull
on the subscriber — slow, durable, fits the thesis).

Each scope is a different cost. The platform may want one, two, or
all four.

## Decision deferred

This spec poses questions. Design happens later, after 7/1-7/3 ship
and the cold/hot boundary is real code instead of intent. Each
question may resolve independently; some compose (Q1 ↔ Q2 ↔ Q4;
Q5 ↔ Q8). Resolutions land as separate specs under `specs/14/` or
later, not as amendments here.

## What this spec does NOT do

- Does NOT propose a mechanism. No `[[ingestion]]` syntax is being
  ratified; the examples above are illustrations, not decisions.
- Does NOT extend `specs/8/3-file-event-stream.md`. That spec is
  narrowly scoped to in-container `filewd`; this is the platform-level
  surface around it.
- Does NOT touch the messages-table schema. Hot tier shape is
  `specs/7/2-data-model.md`.
- Does NOT replace `specs/8/8-company-brain.md`. That spec frames
  the retrieval-layer gap; this spec is the platform-side surface
  independent of company-brain positioning.
- Does NOT presume `agents.toml` exists yet. The composer is drafted
  separately (`specs/7/index.md ## What this phase is NOT`); this
  spec asks how ingestion/curation/eventing would compose into it.

## Pointers

- Framing journey: `.diary/20260523.md` (14:00 onward; three-lens
  synthesis at 15:00).
- Original prompt: `.diary/20260525.md` (evening).
- Adjacent platform specs: `specs/7/1-mcp-rest-unification.md`,
  `specs/7/2-data-model.md`, `specs/7/3-git-as-truth.md`.
- File-event-stream (in-container, narrow): `specs/8/3-file-event-stream.md`.
- Retrieval-layer gap framing: `specs/8/8-company-brain.md`.
- Ingestion failure mode behind Q3: `.diary/20260525.md ## 11:30`
  (578 silent cron successes, 35+ missing rollups).
