---
status: blocked
depends: [6/A-hierarchical-skills, 5/N-oauth-services]
relates-to: [5/5-uniform-mcp-rest]
---

# specs/5/E — tool-deferral enablement + REST-only bridge fallback

Thin spec. The progressive-tool-disclosure _principle_ (eager
messaging, deferred platform tools, Tool Search Tool, cache rationale)
and the skills-vs-tools division now live in
[`../6/A-hierarchical-skills.md`](../6/A-hierarchical-skills.md)
§"Tools side: deferred disclosure". This spec owns only the two things
6/A doesn't: the **SDK enablement blocker** and the **no-MCP-server
REST fallback**.

## Enablement blocker (the real gate)

Marking MCP connector tools `defer_loading: true` requires SDK support
arizuko didn't have at `@anthropic-ai/claude-agent-sdk@0.2.34`:

- SDK internally maps `tool.deferLoading → defer_loading: true`
  (`cli.js`) and Claude Code uses it for its own built-ins (why
  `ToolSearch` works for the agent — `ant/src/index.ts:488`).
- But 0.2.34's typed config exposed **no knob** to defer MCP _server_
  tools: `McpServerConfig` has no defer field; `allowedTools` is an
  allowlist only.

**Two paths:**

- **(a) SDK upgrade** to 0.3.x (latest 0.3.153) — if it exposes a
  per-server / per-tool defer config, the split is mostly config.
  **In progress** (SDK upgrade + defer wiring). Update this section
  with the actual 0.3.x mechanism once confirmed.
- **(b) Self-implement** `search_tools` inside arizuko's own MCP
  server (gated socket): expose a search tool + an internally-managed
  deferred catalog. Doesn't depend on SDK defer support.

Path (a) is preferred if the upgrade is clean. Resolve before any
non-SDK implementation.

## No-MCP-server REST fallback

Most platforms ship MCP servers — mount them via `ipc/connector.go`
(built; proxies `tools/call`, injects secrets, gates via
`auth.Authorize`). For an upstream with **no** MCP server, wrap its
REST API as deferred MCP tools via a thin catalog. Catalog entry shape:

```toml
[[tool]]
name        = "slack:chat.postMessage"     # deferred MCP tool name
action      = "slack:chat.write"           # auth.Authorize action
scope       = "slack:{{ .workspace }}/channel/{{ .channel }}"  # ACL scope
method      = "POST"
url         = "https://slack.com/api/chat.postMessage"
auth_header = "Bearer ${SLACK_BOT_TOKEN}"  # from folder secrets
body_schema = "schemas/slack/chat.postMessage.json"
```

The bridge: validate args against `body_schema`, extract `scope`, call
`auth.Authorize(folder, action, scope, args)`, render the request,
dispatch, return the payload as the MCP tool result. Lives in gated or
a small `bridged` daemon. Per-folder visibility gates which tools the
search can surface; ACL gates which scopes a visible tool may hit.

**Build only when a wanted upstream genuinely lacks an MCP server.**
5/N's "catalog vs off-the-shelf" decision says: default to mounting an
off-the-shelf server; this catalog is the fallback. Until a real
no-server upstream appears, this section is design-on-the-shelf.

## Out of scope

- The disclosure principle + skills/tools split → 6/A.
- Connector dispatch → `ipc/connector.go` (built).
- OAuth token storage for upstreams → 5/N + 11/14.
- Inbound push events → edge adapters (slakd/discd/…), unchanged.

## Pointers

- [`../6/A-hierarchical-skills.md`](../6/A-hierarchical-skills.md) §"Tools side" — the principle.
- `ant/src/index.ts:483-497` — SDK wiring; where defer config lands.
- `ipc/connector.go` — MCP-server dispatch (built).
- [`N-oauth-services.md`](N-oauth-services.md) — external services that use this.
- Anthropic Tool Search Tool:
  <https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool>
