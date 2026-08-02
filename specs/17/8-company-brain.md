---
status: not planned
---

# Company brain — positioning, and the one real gap

A flagship use case in the grand message
([5/A-primitives-framing](../5/A-primitives-framing.md)). Unlike the shipped
products it is **not** an `ant/examples/` template and should not become
one yet: its single genuine gap is connector ingestion + retrieval, which is
an integration, not a primitive arizuko is missing.

## The angle

"Company brain" tools give teams an AI that knows their docs, decisions,
people, and projects. arizuko is the **action layer**, not the retrieval
layer. Competitors answer questions about company knowledge; arizuko agents
act on it — read intake, write daily briefs, escalate, coordinate, run on
schedule.

That is a recomposition of existing primitives, not new machinery: intake
arrives as Events (Slack, email, webhook, WebDAV), Routing lands it in the
right folder, the folder's `facts/` + memory is the State, a scheduled Turn
writes the brief, and Authorization scopes what each team's brain may read.
Retrieval is the one piece arizuko does not own — it delegates to an
external backend via a skill.

Pair arizuko with a vector-store skill (or Onyx as the retrieval backend)
and you get both: semantic search **and** agents that act.

## Competitors

- **Glean** — enterprise semantic search, 100+ connectors, RBAC-mirrored.
- **Dust.tt** — agent-first, 50+ connectors, per-user memory, cloud SaaS.
- **Onyx** (fka Danswer) — open-source, self-hosted, hybrid BM25+dense.
- **Guru** — curated verified-card model, Slack-first, SME review.
- **Notion AI / Confluence AI** — in-workspace, permission-aware, no
  self-hosting.

## Genuine gaps

1. **No connector ingestion.** No OAuth crawlers, no delta sync from
   Confluence/Notion/Drive. `facts/` and WebDAV mounts are manual — fine for
   a small knowledge base, broken at Confluence scale. This is the blocker.
2. **No semantic search.** No embedding pipeline; agents grep mounted files.
   Breaks at enterprise corpus size.
3. **No permission inheritance from source systems.** ACL is folder-level
   and operator-managed; it cannot replicate a Jira or Salesforce permission
   graph.

## Directions (unscheduled)

- **Connector skills** — one lightweight skill per source (Notion read,
  Confluence search) writing into `facts/` on a `timed` schedule. One OAuth
  token and one pull loop each; no embedding built in.
- **Onyx as a retrieval backend** — the agent calls Onyx's search API
  through a skill and acts on ranked results, delegating ingestion and
  embedding entirely. Cheapest path to a credible answer; build nothing.
