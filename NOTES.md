# Notes

Research notes, shipping kudos, and decisions that don't fit in CHANGELOG,
specs, or diary. Append-only; never delete, only annotate with outcomes.

---

## 2026-04-10 — MCP CLI for in-container scripts

**Context.** Scripts running inside the agent container sometimes need
to call MCP tools (`send_message`, `send_file`, etc.) without being the
agent itself. Historically a dead bash script (`send-to-group`, pre-mig-015)
wrote to the file-based IPC queue that no longer exists. We needed a
replacement.

**False start.** First pass (commit `fa35874`): shipped `ant/bin/arizuko-mcp`,
a 220-LOC stdlib Python MCP client with hardcoded `message <jid>` / `file
<jid>` subcommands. User pointed out this was anti-orthogonal — like
shipping `http post-user` instead of `http POST /users`. The tool-name
shortcuts coupled a general MCP client to arizuko-specific vocabulary.
**Lesson:** general protocol tools should be general. HTTPie doesn't know
about "users" and neither should an MCP CLI.

**Second pass.** Shipped commit `3d2e15d` using `@apify/mcpc`, which has
HTTPie-style `key:=value` / `key=value` param grammar — exactly the
orthogonal pattern we wanted. User then asked: "apify? is it the correct
one?" — catching the supply-chain question we'd skipped past.

**The landscape, as of 2026-04-10:**

| Tool                 | Stars | Maintainer                                     | Grammar             | Install               |
| -------------------- | ----- | ---------------------------------------------- | ------------------- | --------------------- |
| `f/mcptools`         | ~1.5k | `f` (community, neutral)                       | JSON via `--params` | `go install` / brew   |
| `@apify/mcpc`        | lower | **Apify Inc** (commercial web-scraping vendor) | HTTPie `key:=val`   | `npm install -g`      |
| `steipete/mcporter`  | niche | Peter Steinberger (individual)                 | colon `key:val`     | `npm`                 |
| `jlowin/fastmcp` CLI | high  | FastMCP author                                 | HTTPie              | `pip install fastmcp` |

**No Anthropic-official CLI exists.** `claude mcp add` is a config
helper inside Claude Code, not a tool-calling CLI. modelcontextprotocol.io
ships SDKs and a web inspector, not a shell client.

**Decision.** `@apify/mcpc` (as shipped in `3d2e15d`). Rationale:

1. **Best tool wins.** On ergonomics, mcpc's HTTPie-style `key:=val` /
   `key=val` grammar is objectively more usable in shell scripts than
   mcptools' JSON-only `--params '{"jid":"123","text":"hi"}'`. For a
   CLI meant to be called from arbitrary scripts inside the container,
   ergonomics compound — a verbose pattern repeated in 20 places is
   20 places of friction.
2. **Open source neutralises vendor lock-in.** The supply-chain framing
   I briefly argued for was overcalibrated. OSS means forkable, auditable,
   replaceable — Apify's commercial incentives don't leak into the
   published code. The npm package is `@apify/mcpc` but the binary runs
   the same whether Apify Inc exists tomorrow or not; if they abandon
   it, we fork.
3. **Full MCP surface.** `tools-list`, `tools-call`, `resources-*`,
   `prompts-*`, `tasks-*`, `logging-set-level`, `ping`, `grep` — mcpc
   covers more of the spec than mcptools, and adds useful things like
   a session bridge (even if we don't need the persistence; it's a
   free optimisation when we do).
4. **Considered alternatives.** `f/mcptools` (~1.5k stars, community)
   has more stars but worse ergonomics for our call pattern.
   `steipete/mcporter` is an individual side project. `jlowin/fastmcp`
   CLI pulls the whole FastMCP Python runtime as a dep.

**Shape of the chosen call pattern:**

    mcpc connect "socat UNIX-CONNECT:$ARIZUKO_MCP_SOCKET -" @s
    trap 'mcpc @s close' EXIT
    mcpc @s tools-call send_message jid:="$JID" text:="hi"

**Non-choice: don't ship a wrapper.** The first-pass mistake was exactly
a wrapper. Resist rebuilding it with better intentions. If scripts hate
the session bookkeeping, they can define a local `mcp-send() { ... }`
function at the top of the script — scoped to that script's concerns,
not baked into the image.

**Kudos.** To the user for catching the anti-orthogonal shortcuts in
the first pass AND for correctly reframing the vendor concern on the
second pass ("it's open source, so I'm not afraid of using it"). Both
corrections tightened the decision: (a) the tool should be general,
not arizuko-specific; (b) the tool should be chosen on merit, not on
defensive neutrality bias.
