---
status: partial
depends: [6/A-hierarchical-skills, 5/N-oauth-services]
relates-to: [5/5-uniform-mcp-rest]
---

# specs/5/E — tool-deferral enablement + REST-only bridge fallback

Thin spec. The progressive-tool-disclosure _principle_ (eager
messaging, deferred platform tools, Tool Search Tool, cache rationale)
and the skills-vs-tools division live in
[`../6/A-hierarchical-skills.md`](../6/A-hierarchical-skills.md)
§"Tools side: deferred disclosure". This spec owns only the two things
6/A doesn't: the **SDK enablement** (now shipped) and the
**no-MCP-server REST fallback** (still future).

## Enablement — SHIPPED (path (a), SDK upgrade)

The blocker (0.2.34 had no knob to defer MCP-server tools) is resolved
by upgrading `@anthropic-ai/claude-agent-sdk` to **0.3.153**.

**The mechanism is per-MCP-server `alwaysLoad?: boolean`**
(`sdk.d.ts`, on `McpStdioServerConfig`/SSE/HTTP/SDK):

- `alwaysLoad: true` → all that server's tools stay eager
  (`defer_loading: false`).
- Omit it → the server's tools default to deferred behind the Tool
  Search Tool (active because `ToolSearch` is in `allowedTools`,
  `ant/src/index.ts`).
- Per-tool `alwaysLoad` exists for **SDK-defined** tools only; for
  external/stdio MCP servers the granularity is **per-server**.

**Wired** (`ant/src/mcp-servers.ts`):

- `arizuko` server (socat → gated socket) → `alwaysLoad: true`. Core
  messaging (`send`/`reply`/`inspect_*`/`send_file`) stays eager.
- Third-party connector servers (from `settings.json`) → no
  `alwaysLoad`, so they sit behind Tool Search.

**Remaining limitation (the only open item):** gated serves core +
management + `connectors.toml` tools through ONE `arizuko` server.
Because `alwaysLoad` is per-server, gated's management +
`connectors.toml` tools ride eagerly with core. Deferring _those_
(while keeping core eager) needs a **gated-side server split** — e.g.
gated exposes `arizuko-core` (alwaysLoad) and `arizuko-mgmt`
(deferred). Go-side work; do it only if the management surface grows
enough to matter. For third-party connectors (the Slack-200-tools
case), the split already works today.

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
