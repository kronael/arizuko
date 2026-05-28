---
status: draft
depends: [4/9-acl-unified, 6/A-hierarchical-skills]
relates-to: [5/5-uniform-mcp-rest, 5/N-oauth-services, 5/6-middleware-pipeline]
---

# specs/5/E — external capabilities: progressive tool disclosure

## The problem

An agent should be able to use external platforms (Slack ~200 methods,
GitHub, Linear, Notion, …) and arizuko-core tools without drowning in
tool definitions. Two hard constraints, both measured:

1. **Tool defs ride the request prefix on every turn.** The Anthropic
   Messages API is stateless: the `tools` array is sent with every
   call, positioned before the system prompt and messages. 1000 tools
   = 1000 defs sent every turn. Prompt caching makes re-sending cheap
   (~10% on cache hit) but does NOT reduce context-window usage or
   attention dilution — the model still reasons over all 1000.

2. **Mutating the tools array nukes the cache.** `tools` is first in
   the prefix; changing it per turn invalidates the cache from `tools`
   onward (system prompt + messages re-billed at full price). So
   "agent enables/disables tools per turn" is the single most
   expensive option.

## The native answer already exists

Anthropic ships the **Tool Search Tool** (`platform.claude.com/docs/
en/agents-and-tools/tool-use/tool-search-tool`): tools marked
`defer_loading: true` are NOT in the eager `tools` array. The model
sees only the Tool Search Tool + non-deferred tools. When it needs a
capability, it searches; matching tool schemas expand into context as
**tool results in the message stream** — append-only, cache-friendly,
not a mutation of the `tools` prefix. Once a schema appears in a
search result, the model calls it as a native typed tool.

Measured: 85% token reduction; Opus 4.5 tool-selection accuracy 79.5%
→ 88.1% with it enabled.

**arizuko's agent already has it.** `ant/src/index.ts:488` lists
`ToolSearch` in `allowedTools`. The mechanism is present; what's
missing is marking connector/MCP tools `defer_loading: true` so they
stop loading eagerly.

This is the same shape as `resolve` for skills — an explore tool the
model drives to find what it needs — but native, for tools, and
maintained by Anthropic.

## Why this beats the REST-catalog bridge

The original 5/E posited a `bridged` daemon: a REST-request catalog
(URL template + scope extractor + auth header per upstream), exposing
one MCP server whose tools dispatch to REST. That's framing (3) of the
old three-framings debate. The Tool Search Tool obsoletes most of it:

- **Dispatch already ships for MCP upstreams.** `ipc/connector.go`
  mounts a third-party MCP server as a stdio subprocess, proxies
  `tools/call`, injects secrets, scrubs results, gates via
  `auth.Authorize("mcp:"+name)`. Most platforms (Slack, GitHub,
  Linear, Notion) ship MCP servers. For them, no REST catalog is
  needed — mount the server, mark its tools deferred.
- **Disclosure is native.** No hand-rolled skill-catalog-per-connector,
  no generic `connector_call(opaque_json)` dispatch tool. The model
  searches, gets typed MCP tools, calls them the way it's tuned to.

The REST catalog survives only as the **fallback for upstreams with no
MCP server** — a pure REST API arizuko wants to expose. Then a thin
catalog (URL template + scope + auth) wraps it as deferred MCP tools.
Rare path, not the main one.

## REST+skills vs MCP+search — the contrast

The user's framing: "MCP and REST with progressive skills is the same
thing, just a different implementation." Correct. Both are progressive
disclosure over a hierarchy. They differ in discovery layer + dispatch:

|                  | REST + skills                                        | MCP + Tool Search                                 |
| ---------------- | ---------------------------------------------------- | ------------------------------------------------- |
| Discovery        | `resolve` (arizuko skill hierarchy + Haiku classify) | Tool Search Tool (Anthropic-native)               |
| What's disclosed | skill body (prose: how-to, args as docs)             | tool schema (typed, callable)                     |
| Dispatch         | generic `connector_call(name, tool, args)` meta-tool | native MCP `tools/call`                           |
| Eager surface    | 1 dispatch tool                                      | Tool Search Tool + core                           |
| Cache            | catalog enters via message stream (skill content)    | schemas enter via search results (message stream) |
| Model fit        | constructs args from docs                            | native typed tool-use (tuned for it)              |
| Maintained by    | arizuko                                              | Anthropic                                         |

Both keep the `tools` prefix flat and frozen; both put the variable
catalog in the message stream (cache-friendly). The decisive
difference: **the model is tuned to call MCP tools natively.** A typed
tool the model invokes directly beats a generic dispatch tool fed
opaque JSON it built from prose docs. And Anthropic maintains the
search mechanism + improves model accuracy against it.

**Verdict:** when the upstream is (or can be) an MCP server, use
MCP + Tool Search (`defer_loading`). It is the better-fitting,
lower-maintenance, cache-equivalent answer.

## Division of labor: skills vs tools

Skills (6/A) and the Tool Search Tool are complementary, not
competing — they disclose different content:

- **Tool Search Tool** discloses **tools** — discrete callable
  functions (`slack.chat_postMessage`, `github.create_issue`). Native,
  typed, one call.
- **Skills** (`resolve` + 6/A) disclose **knowledge + workflows** —
  "how to run a deploy", multi-step recipes, prose guidance, persona,
  the rules around using a set of tools. Not a single tool call.

A connector's _tools_ are deferred MCP tools found via search. A
connector's _usage guidance_ (when to use which tool, gotchas,
multi-step patterns) is a skill found via resolve. The skill can point
at the tools; the tools don't need the skill to be callable.

## Requirements

1. **Mark connector + non-core MCP tools `defer_loading: true`.** Core
   agent tools (`send`, `reply`, `inspect_*`, file I/O) stay eager —
   they're used every turn, small in number. Connector tools (mounted
   via `ipc/connector.go`) and any large MCP surface defer. Verify the
   Claude Agent SDK plumbs `defer_loading` per-tool or per-MCP-server;
   `ant/src/index.ts` is the wiring site (`mcpServers` +
   `allowedTools`).
2. **Eager surface stays small + static.** Tool Search Tool + core
   tools + the one-or-two always-needed connector tools. Never grows
   with connector count. Cache prefix never invalidated by tool churn.
3. **Per-folder connector visibility.** Which connectors mount for a
   folder is operator config (`connectors.toml` + per-folder enable).
   `auth.Authorize("mcp:"+name)` gates each call. Visibility (which
   tools the search can even surface) is upstream of ACL (which scopes
   a visible tool may hit). Same split the old 5/E named.
4. **REST-only fallback catalog.** For an upstream with no MCP server:
   a thin catalog (URL template, scope extractor, auth header) wraps
   it as deferred MCP tools dispatched by a small bridge. Build only
   when a wanted upstream genuinely lacks a server.
5. **Inbound stays edge adapters.** Push events (Slack events, GitHub
   webhooks) remain HTTP-webhook adapters (slakd/discd/…). This spec
   is outbound capability only.

## Open questions

1. **SDK `defer_loading` plumbing — VERIFIED, it's the blocker.**
   `ant/` pins `@anthropic-ai/claude-agent-sdk ^0.2.34`. Findings:
   - The SDK internally maps `tool.deferLoading → defer_loading: true`
     on the API tool def (`cli.js`), and Claude Code uses it for its
     own built-ins (that's why `ToolSearch` works for Claude Code's
     deferred tools).
   - BUT the typed config at 0.2.34 exposes **no knob** to mark MCP
     server tools deferred. `McpServerConfig` (stdio/SSE/HTTP) has no
     defer field; `allowedTools`/`disallowedTools` are allowlists only.
   - So at the pinned version arizuko **cannot** defer its connector
     tools through the SDK. `ToolSearch` in `allowedTools`
     (`ant/src/index.ts:488`) allowlists the tool but doesn't give
     arizuko control over which MCP tools defer.
   - Latest SDK is `0.3.153` (major bump from 0.2.x). May expose the
     knob; needs an upgrade + breaking-change audit to confirm.

   **Two paths to native tool-search:**
   (a) **Upgrade the SDK** to a version exposing per-server/per-tool
   defer config → then it's mostly config (mark platform tools
   deferred, keep messaging eager).
   (b) **Self-implement** tool-search inside arizuko's own MCP server
   (gated socket): expose a `search_tools` tool + a deferred
   catalog the server manages internally. Doesn't depend on SDK
   defer support, but is real work.
   Decide (a) vs (b) before any implementation. (a) is preferred if
   the upgrade is clean.

2. **Search quality over arizuko's own tools.** Today arizuko's ~40
   core MCP tools load eagerly. Should the rarely-used management tools
   (`set_web_route`, `set_observe_window`, …) also defer behind search,
   leaving only the per-turn essentials eager? Likely yes — same
   mechanism, applies to arizuko-core too, not just connectors.
3. **Hosted vs local MCP servers.** `connector.go` mounts local stdio
   servers. Hosted-remote MCP (Linear's `mcp.linear.app/mcp`) needs a
   proxy mode + the upstream's OAuth/DCR. Inherited from 5/N; the
   dispatch differs (HTTP proxy vs subprocess), the disclosure
   (defer_loading + search) is identical.
4. **Cold-start search cost.** First time the agent needs Slack, it
   pays one search round-trip before the tool is callable. Acceptable
   vs eager-loading 200 Slack tools every turn forever. Confirm the
   latency is a non-issue in practice.

## What this is NOT

- NOT a rewrite of the agent MCP socket. Same `tools/call` surface;
  some tools are deferred instead of eager.
- NOT a generic MCP gateway. arizuko gates every call via
  `auth.Authorize` + per-folder visibility; generic proxies don't.
- NOT a replacement for skills. Skills disclose knowledge/workflows;
  Tool Search discloses tools. Both progressive, different content.
- NOT inbound. Edge adapters keep platform→arizuko ingestion.

## Pointers

- `ant/src/index.ts:483-497` — `allowedTools` (has `ToolSearch`) +
  `mcpServers` wiring. Where `defer_loading` gets set.
- `ipc/connector.go` — third-party MCP server dispatch (built).
- [`6/A-hierarchical-skills.md`](../6/A-hierarchical-skills.md) — the
  skills-side progressive disclosure (knowledge, not tools).
- [`5/N-oauth-services.md`](N-oauth-services.md) — external services;
  uses this spec's disclosure + connector dispatch.
- Anthropic Tool Search Tool:
  <https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool>
- Anthropic advanced tool use:
  <https://www.anthropic.com/engineering/advanced-tool-use>
