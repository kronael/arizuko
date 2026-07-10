---
status: draft
---

# Riding a demand class — how the loop devises a non-intrusive answer

`1-adoption-interop.md` reads a _system_ and builds interop for it. A hype —
knowledge graphs, taxonomies, RAG, "codebase maps" — is not a system. It is a
_demand class_: a category of user want, restated by every vendor as a reason to
buy heavy machinery (a graph DB, a vector store, an always-on index).

This spec is not the answer to graphs. It is the **method the loop runs to devise
the answer automatically**, for any such demand class. Writing one answer by hand
is the thing phase 6 exists to stop doing.

## The method (the loop runs this, not a human)

Given a demand class (from user requests, hub-tracked competitor features, or a
trend), the loop:

1. **Strip to the primitive need.** "Talk to my knowledge graph" → "let the agent
   traverse this data." Discard the machinery the pitch bundles with the need.
2. **Try the primitives first.** Can the folder tree (hierarchy), bundled files
   (nodes/edges), and the tools the container already has (`jq`, `grep`, FTS5)
   serve the stripped need with **no new always-on subsystem**? Default yes.
3. **Generate the product.** If yes, emit a folder + the dataset + a
   one-paragraph brain + a route/chat link — a working bot, in an isolated
   worktree, no core change.
4. **Escalate only on proof.** If the primitives genuinely can't (millions of
   edges, sub-second joins), mount an external engine as a **folder capability**
   (`/mnt` read-only, or an MCP tool the agent calls) — never a core subsystem.
   The engine is a tool the agent uses, not the substrate arizuko runs on.
5. **Verify + record.** Drive the generated bot end-to-end, gate on an
   adversarial check, record the demand-class → answer mapping, open for review.

The output of steps 1–3 is almost always the same shape — a folder, files, an
agent that traverses them — which is exactly why it can be automatic. The loop is
not inventing per-demand; it is applying one discipline (primitives over
subsystems) to each new want.

## Reference output — marble

marble (`products/marble/`) is what step 3 produces, verified live: a 1,590-topic
/ 3,221-edge prerequisite graph answered by `jq` over three bundled JSON files —
no graph DB, no index, no sync. A folder, three files, a Haiku agent, one
`/chat/` link. A graph-RAG vendor would have sold a database and an ingestion
pipeline for this; the loop should reach the folder answer on its own, for any
dataset, without a human writing the spec each time. marble is the proof the
generated shape works, not a hand-authored answer to keep.

## Why automatic matters here

Hand-authoring an answer per demand class is O(hypes) human work that goes stale
as the hypes rotate. The loop makes it O(1): one method, applied to whatever the
market is selling this quarter. The discipline it encodes — strip to the need,
try primitives, escalate to a mounted capability only on proof — is the same
interop-at-the-boundary rule as `1-adoption-interop.md`, pointed at demands
instead of systems.

## Non-goals

- **No hand-written per-demand answers.** If a human is speccing the concrete
  answer, the loop failed — fix the method, not the instance.
- **No graph DB or vector store in core.** An always-on second datastore _is_ the
  intrusive machinery the method exists to avoid.
- **The folder coordinate stays the substrate.** Every generated answer rides on
  it (`specs/5/A`); none replaces it.

## Ties

`1-adoption-interop.md` (the loop; this is its demand-class mode) ·
`2-target-matrix.md` (graphify/activegraph are inputs, not adoptions) ·
`specs/5/A` (the folder coordinate) · the marble product (`products/marble/`, the
reference output).
