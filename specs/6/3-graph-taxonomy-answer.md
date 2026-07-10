---
status: draft
---

# The graph & taxonomy hype — the non-intrusive answer

There is real demand for "give my agent a knowledge graph / a taxonomy / a
codebase map." Most of the machinery sold to meet it is hype, and adopting it is
intrusive: a graph database, a vector store, a replatforming of state onto a
projection. arizuko's answer is to ride the demand without buying the machinery —
the folder is already the substrate, and the agent already traverses it.

## The hype, named

- **codebase→knowledge-graph** (graphify) — the headline "71.5x token reduction"
  is self-debunked on its own hub page (~0% real savings for typical grep-first
  use). A retrieval skill dressed as an architecture.
- **event-log/graph-as-agent** (activegraph) — elegant, but adopting it means
  replatforming arizuko's relational-per-daemon state onto a graph projection.
  Intrusive by construction.
- **vector-RAG / graph-RAG platforms** — a second datastore, a sync pipeline, an
  index to keep fresh. arizuko explicitly does _not_ ship this (README §"does not
  include") and pairs with a retrieval system instead.

The pattern across all three: a heavy, stateful, always-on subsystem sold to
solve a problem that is usually just "let the agent read the data."

## arizuko's answer — the folder is the graph

You do not adopt a graph engine. The primitives already present are enough:

- **The folder tree is the hierarchy.** `corp/eng/sre` is a graph edge. Nesting
  is the taxonomy.
- **Bundled data files are the nodes and edges.** Drop `topics.json`,
  `dependencies.json` in the folder; the agent traverses them with `jq`, `grep`,
  FTS5 — the tools it already has, in the container it already spawns.
- **No new datastore, no sync, nothing always-on.** The dataset is a read-only
  artifact in the folder. Traversal happens inside the per-turn container and
  leaves nothing behind. Non-intrusive by construction — the opposite of a graph
  platform.

The moat stays the moat: this rides on the folder coordinate
(`specs/5/A`, `USELESS.md` §4), it does not add a competing subsystem beside it.

## Proof — marble already does it

The marble taxonomy bot (`products/marble/`) is the existence proof, live on
krons:

- The Marble Skill Taxonomy: **1,590 K–5 micro-topics, a 3,221-edge prerequisite
  graph** across 8 subjects.
- Answered by **`jq` over three bundled JSON files** (`topics.json`,
  `dependencies.json`, `clusters.json`) — trace a prerequisite chain, list what a
  topic unlocks, filter by subject/age.
- **No graph DB, no vector index, no sync.** A folder, three files, a Haiku
  agent, one public `/chat/` link. Runs on the cheapest model.

A graph-RAG vendor would have sold a database and an ingestion pipeline for this.
arizuko shipped it as a folder.

## The product shape — bring-your-dataset explorer

Generalize marble into a product (it already is one, `products/marble/`): any
JSON/CSV/markdown dataset an agent can traverse becomes a chat-over-your-data bot.
Swap the files, rewrite the one-paragraph brain, keep everything else. This is the
non-intrusive way to ride the "talk to my knowledge graph" demand:

- **Non-intrusive**: the dataset is bundled and read-only; no new infra, no
  always-on process, no second source of truth to keep in sync.
- **Orthogonal**: it is the same six-primitive pipeline with different folder
  contents — no new machinery.
- **Composable**: one folder per dataset; a hierarchy of them if you have many.

## When you actually need graph queries

If a dataset genuinely needs Cypher/SPARQL-scale traversal (millions of edges,
sub-second joins), do not replatform arizuko. Mount the graph tool as a
**folder capability** — a `/mnt` read-only mount or an MCP tool the agent calls —
and keep arizuko's state relational. The graph engine is a tool the agent uses,
never the substrate arizuko runs on. Same discipline as any external system:
interop at the boundary, the folder coordinate stays intact.

## Non-goals

- **No graph DB or vector store in core.** The moment arizuko ships an always-on
  second datastore, it has become the intrusive thing it is answering.
- **Don't chase the token-reduction claim.** grep-first + FTS5 is the honest
  baseline; a projection only earns its keep on a corpus that measurably needs it.
- **The folder coordinate is still the reason.** Graph/taxonomy support is a
  product shape on top of the primitives, not a new primitive.

## Ties

`2-target-matrix.md` (graphify/activegraph land here, not adopted) ·
`1-adoption-interop.md` (interop-at-the-boundary discipline) · `specs/5/A`
(the folder coordinate) · the marble product (`products/marble/`).
