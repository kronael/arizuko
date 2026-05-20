# Company Brain — arizuko positioning and gaps

## Status: research

## The use case

"Company brain" tools give teams a persistent AI that knows their docs,
decisions, people, and projects. The agent answers questions across
Confluence, Notion, Slack history, Drive, Jira, etc.

## Key competitors

- **Glean** — enterprise semantic search, 100+ SaaS connectors, RBAC-mirrored permissions
- **Dust.tt** — agent-first, 50+ connectors, per-user agent memory, cloud SaaS
- **Onyx** (fka Danswer) — open-source, self-hosted, hybrid BM25+dense search, 50+ connectors
- **Guru** — curated verified-card model, Slack-first, SME review workflows
- **Notion AI / Confluence AI** — in-workspace AI, permission-aware, no self-hosting

Reference: `refs/onyx/` (cloned), `refs/onyx.md` (analysis).

## arizuko's angle

arizuko is the **action layer**, not the retrieval layer.
Competitors answer questions about company knowledge.
arizuko agents act on it — read Slack intake, write daily briefs,
escalate issues, coordinate across teams, run on schedule.

The framing: pair arizuko with a vector store skill (or Onyx as a
retrieval backend) and you get both: semantic search AND agents that act.

## Genuine gaps today

1. **No connector ingestion pipeline.** No OAuth crawlers, no delta sync
   from Confluence/Notion/Drive. `facts/` and WebDAV mounts are manual.
2. **No semantic search / vector store.** No embedding pipeline. Agents
   grep mounted files; breaks at enterprise corpus scale.
3. **No permission inheritance from source systems.** ACL is folder-level,
   operator-managed. Can't replicate Jira/Salesforce permission graphs.

## Possible directions

- **Onyx as retrieval backend skill**: agent calls Onyx search API via
  an MCP skill, gets ranked results, acts on them. No need to build RAG
  into arizuko itself.
- **products/company-brain/**: product page framing arizuko as the
  action+coordination layer, Onyx/Glean as the retrieval layer.
- **Connector skills**: lightweight per-source skills (Notion read,
  Confluence search) that write to `facts/` on a schedule via `timed`.

## Action items (not yet planned)

- [ ] products/company-brain/ page (intro + setup)
- [ ] Onyx integration skill (search MCP tool calling Onyx API)
- [ ] Landing page "acts on knowledge" bullet (done in v0.43.0)
