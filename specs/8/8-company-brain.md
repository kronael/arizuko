# Company Brain — arizuko positioning and gaps

## Status: not planned

## The use case

"Company brain" tools give teams a persistent AI that knows their docs,
decisions, people, and projects. The agent answers questions across
Confluence, Notion, Slack history, Drive, Jira, etc.

## arizuko's angle

arizuko is the **action layer**, not the retrieval layer.
Competitors answer questions about company knowledge.
arizuko agents act on it — read Slack intake, write daily briefs,
escalate issues, coordinate across teams, run on schedule.

The framing: pair arizuko with a vector store skill (or Onyx as a
retrieval backend) and you get both: semantic search AND agents that act.

## Key competitors

- **Glean** — enterprise semantic search, 100+ SaaS connectors, RBAC-mirrored permissions
- **Dust.tt** — agent-first, 50+ connectors, per-user agent memory, cloud SaaS
- **Onyx** (fka Danswer) — open-source, self-hosted, hybrid BM25+dense search, 50+ connectors
- **Guru** — curated verified-card model, Slack-first, SME review workflows
- **Notion AI / Confluence AI** — in-workspace AI, permission-aware, no self-hosting

Reference: `refs/onyx/` (cloned), `refs/onyx.md` (analysis).

## Genuine gaps today

1. **No connector ingestion pipeline.** No OAuth crawlers, no delta sync
   from Confluence/Notion/Drive. `facts/` and WebDAV mounts are manual.
2. **No semantic search / vector store.** No embedding pipeline. Agents
   grep mounted files; breaks at enterprise corpus scale.
3. **No permission inheritance from source systems.** ACL is folder-level,
   operator-managed. Can't replicate Jira/Salesforce permission graphs.

## Data ingestion (primary gap)

The biggest blocker for company-brain positioning is ingestion. Without
automated connectors, the operator populates `facts/` manually or via WebDAV
mounts. That's fine for small knowledge bases; it breaks for anything
Confluence/Notion/Drive-scale.

### Possible directions

- **Connector skills**: lightweight per-source skills (Notion read,
  Confluence search) that write to `facts/` on a schedule via `timed`.
  Each skill = one OAuth token + one pull loop. No build-in embedding.
- **Onyx as retrieval backend skill**: agent calls Onyx search API via
  an MCP skill, gets ranked results, acts on them. Delegates the entire
  ingestion + embedding problem to Onyx. No need to build RAG into arizuko.
- **products/company-brain/**: product page framing arizuko as the
  action+coordination layer, Onyx/Glean as the retrieval layer.

### Action items (not yet planned)

- [ ] Connector skill: Notion read → facts/
- [ ] Connector skill: Confluence search → facts/
- [ ] Onyx integration skill (search MCP tool calling Onyx API)
- [ ] products/company-brain/ page (intro + setup)
- [x] Landing page "acts on knowledge" bullet (done in v0.43.0)
