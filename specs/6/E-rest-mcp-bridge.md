---
status: brainstorm
depends: [9-acl-unified]
relates-to: [6-middleware-pipeline, D-slack-agent-pane]
---

# specs/6/E — REST ↔ MCP bridge (brainstorm)

## Why this is a brainstorm

Three competing framings sit on the table; none are crisp yet:

1. **MCP everywhere** — make MCP the single intra-cluster protocol;
   every adapter is an MCP server; gated multiplexes.
2. **MCP router + N platform daemons** — gated routes MCP calls,
   each platform gets a Go daemon that wraps its API and enforces
   auth.
3. **REST ↔ MCP mapping layer** — keep REST as the wire format for
   anything REST-shaped (which is most platform APIs); generate
   MCP tool catalogs from REST descriptors; auth gates at the
   mapping layer.

Framing (3) is the simplest if it works, because REST is the
existing reality and MCP is a wrapper. Writing this down before
committing.

## The problem we're trying to solve

Slack's API has ~200 methods (channels, threads, files, reactions,
search, canvas, huddles, AI search, ...). Today `slakd` exposes a
narrow `Send / SendFile / Like / Edit / Delete / Quote / Repost`
interface — replicates ~10 of Slack's methods. For an agent to use
the rest, we either:

- Reimplement Slack methods in slakd one by one (today's path; doesn't
  scale; reinvents the platform SDK)
- Give the agent direct API access (no authorization; security hole)
- **Wrap the platform API as authorized tools the agent can call
  via MCP** (the design question this spec opens)

Same pattern wanted for X, GitHub, Linear, Notion, etc.

## Why MCP at all when REST exists

The agent (Claude Code in container) consumes MCP tools — that's
the protocol the model uses to discover and invoke tools. So the
agent's edge MUST be MCP. The question is what's on the OTHER side
of that edge:

| Inside the cluster         | Pros                                   | Cons                              |
| -------------------------- | -------------------------------------- | --------------------------------- |
| MCP (option 1)             | Uniform; tool catalogs auto-aggregate  | New servers to write per platform |
| REST + mapping (option 3)  | Reuses platform SDKs and existing REST | Mapping layer is the new code     |
| MCP servers + REST (mixed) | Each daemon picks its style            | Two ways to do everything         |

So "MCP everywhere internally" is justified only if writing MCP
servers is cheaper than writing REST-to-MCP mappers. For most
platforms with mature REST APIs, the mapper is cheaper — declarative.

## The REST ↔ MCP bridge sketch

One daemon — call it `bridged` (or fold into gated) — that:

1. **Loads a catalog** of platform tool definitions per upstream.
   Each definition declares:
   - The MCP tool name (`slack:chat.postMessage`)
   - The REST request shape (URL template, method, body schema)
   - The auth header (token from folder secrets)
   - The scope extractor: a JSON-path or expression that pulls the
     authorization scope from the arguments (e.g. `args.channel`
     → `slack:T<workspace>/channel/<id>`)
   - The action string for `auth.Authorize` (e.g. `slack:chat.write`)

2. **Exposes one MCP server** to the agent. Tool catalog is the
   union of all loaded upstreams plus arizuko-core tools.

3. **On every `tools/call`**:
   - Extract scope from args via the declared expression
   - Call `auth.Authorize(caller_folder, action, scope, args)`
   - If allowed: render the REST request from the template, send it,
     return the response payload as the MCP tool result
   - If denied: return MCP error without touching the upstream

4. **Catalog source** — open question. Three candidates:
   - Hand-written TOML/YAML per platform (operator authors it)
   - OpenAPI spec import (Slack and GitHub publish OpenAPI;
     auto-generate tool definitions)
   - Off-the-shelf MCP servers used as catalog source (parse their
     tool list, hijack the dispatch). Less work but less control.

### Example catalog entry

```toml
[[tool]]
name        = "slack:chat.postMessage"
action      = "slack:chat.write"
scope       = "slack:{{ .workspace }}/channel/{{ .channel }}"
method      = "POST"
url         = "https://slack.com/api/chat.postMessage"
auth_header = "Bearer ${SLACK_BOT_TOKEN}"
content_type = "application/json"
body_schema = "schemas/slack/chat.postMessage.json"
```

The schema file defines the MCP tool's input shape; the bridge
validates incoming args against it, renders the URL/body, calls.

## What survives across framings (1), (2), (3)

These are stable regardless of which framing we pick:

- **`auth.Authorize` is the single gate.** No new auth machinery.
- **Action namespace**: `<upstream>:<verb>` (`slack:chat.write`,
  `x:tweet`, `github:issues.create`). Free string today; just a
  vocabulary convention.
- **Scope strings extend the existing JID-shaped scopes.** Same
  glob matching the ACL already does.
- **slakd's outbound code becomes optional** once any of the three
  framings ship. Adapter outbound is duplicative with platform
  bridges/MCP servers.
- **Inbound stays HTTP webhook + signature verification** in the
  edge adapters (slakd, discd, etc.). MCP isn't a good fit for
  push-event ingestion regardless of framing.

## What's still unclear

- **Does the bridge live in gated or as a separate daemon?**
  Gated already does MCP server + auth gating; adding REST dispatch
  is a small extension. But "gated does everything" is the failure
  mode the platform/genericization specs (6/R) explicitly want to
  break out of. Probably new daemon `bridged` with gated as one of
  its consumers.
- **Streaming responses.** Some REST APIs stream (Slack's
  `chat.startStream`); MCP's response model is request/response.
  Either skip streaming endpoints in v1, or invent an MCP
  notification convention for partial results.
- **Cost / rate limits.** Each upstream has its own rate limit
  budget. Bridge owns the budget — or upstream errors flow through
  unchanged and the agent retries? Probably the former; bridge is
  the single throughput chokepoint per upstream.
- **OpenAPI auto-import quality.** Slack's OpenAPI is incomplete;
  some endpoints aren't described. Hand-written catalog entries are
  the floor; OpenAPI is a nice-to-have generator.
- **Tool surface curation.** Slack has ~200 methods. Exposing all
  of them to every agent is wrong. Per-folder enable list lets the
  operator pick (e.g. `atlas` folder gets `chat.write +
search.messages + files.upload`; nothing else). Same shape as
  per-folder ACL but inverted — declares what tools are even
  _visible_ before ACL gates which scopes they can hit.
- **Secret injection.** Bridge needs the bot token per workspace.
  Probably reads from folder secrets (the existing folder-scoped
  secrets table); operator binds `slack_bot_token` to a folder
  subtree, bridge picks it up at call time.
- **Outbound only?** Bridge handles agent→platform calls. Edge
  adapters handle platform→arizuko. Does anything need bidirectional?
  Possibly events the agent subscribes to actively (e.g. "notify me
  if a new message lands in #incidents"). MCP has notifications;
  bridge could pass them through. Open.

## Migration path (rough)

1. Build the bridge as a new daemon with one upstream catalog: arizuko-core
   (existing tools, declared as bridge entries). Compare against today's
   gated MCP behavior; should be identical. Proves the model.
2. Add `slack` upstream with a hand-written catalog of ~10 tools matching
   slakd's current outbound surface (postMessage, files.upload, reactions.add,
   search.messages, etc.). Bridge alongside slakd. Folder operators choose
   per-route which to use.
3. Add `x` and `github` upstreams. By this point hand-writing catalogs is
   either bearable or auto-generation is needed.
4. Slakd outbound deprecated. Eventually deleted.
5. Edge adapters (slakd, discd, ...) stay as inbound-only daemons.

## What this is NOT

- NOT a rewrite of the agent's MCP socket. Agent sees the same
  tool-catalog API.
- NOT an HTTP→MCP shim for arbitrary external APIs. The bridge
  is scoped to platforms arizuko explicitly supports, with operator-
  authored catalogs.
- NOT a replacement for slakd/discd/etc. Edge adapters keep their
  inbound role. Only outbound is touched.
- NOT a generic MCP gateway (mcp-proxy, etc.). We need auth and
  per-folder scoping that generic proxies don't enforce.

## Open: framings (1) vs (2) vs (3)

The MCP-everywhere framing (1) is intellectually clean but
expensive (write MCP servers per platform). The N-daemons
framing (2) duplicates auth logic per daemon. Framing (3) — the
REST/MCP bridge — keeps the platform SDKs out of arizuko's code
entirely. Probably the right answer, but the catalog-authoring
cost is real and not yet measured.

Decision criterion: how many tool definitions would slakd's
existing outbound surface need? Count is ~12 (Send, SendFile,
SendVoice, Like, Dislike, Edit, Delete, Quote, Repost, Forward,
Post, Typing → setStatus). If those 12 fit in <300 LOC of catalog
TOML + ~200 LOC bridge runtime, framing (3) wins on size. If the
catalog grows unwieldy, fall back to framing (1).

Concrete next step: prototype the bridge with 3 tools, measure.
Don't draft the full spec until that's done.
