# anteval

Agent-capability eval — a black-box prober that certifies a live arizuko
agent can operate the platform itself. Spec:
[`specs/5/37-agent-capability-eval.md`](../specs/5/37-agent-capability-eval.md).

Each case injects one real task through a public surface, lets the live agent
do it with its own MCP tools, and asserts an **externally observable effect** —
an HTTP status, a callback the agent's artifact made, or a message visible via
REST/MCP — never the agent's prose, never the instance's internal state. Zero
arizuko-internal imports: a black-box client over the same surfaces a human or
external tool uses (sibling component, `specs/11/A`).

## Run

```bash
make build
./anteval run https://krons.fiu.wtf \
  --api   http://localhost:8081 \ # routd /v1 base reachable from the eval host
  --token "$AGENTEVAL_TOKEN" \    # bearer (messages:write + read) for the eval folder
  --chat  web:eval \              # chat JID tasks are injected into (folder must own it)
  --sink-addr :9099 \            # local bind for the callback sink (routable iface)
  --sink  https://eval-host:9099 \# URL the agent containers call back to
  --smoke                         # gate subset; omit for all 19
# selectors: --dimension web | --case pub-200 ; output: --md report.md --json report.json
./anteval dash report.json      # re-render a saved report
make validate                     # load+validate the case catalog (no target)
```

Exit is non-zero if any case fails. `dash` renders a saved JSON report.

## How a case proves itself

Templates expand per run: `{nonce}` (unique per run+case), `{sink}`, `{target}`,
`{chat}`, and `{cb.KEY}` — a query param the agent handed back through the
callback sink (e.g. a freshly minted chat-link token). Checkers:

- `callback` — the agent wired an artifact (skill, MCP tool, app, webhook,
  child) that fires `{sink}/cb/{nonce}`; firing is the proof.
- `http_status` — a `{cb.url}`/`{cb.token}` URL returns the expected code
  (publish → 200, gated → 401, deleted/denied → 404).
- `rest_reply` — a **bot-authored** message carrying `{nonce}` is readable in
  `{chat}` via REST (the user-injected prompt is excluded, so the marker the
  harness itself sent can't false-pass). `rest_observe` matches any author.
- `mcp_roundtrip` — same over the MCP face (`--mcp`).
- `parity_sentinel` — `{nonce}` is identical via REST and MCP (uniform surface).

The run mints a nonce per case and embeds it in every name/URL/body, so runs are
idempotent and never collide; teardown is best-effort.

## Wiring seam

`pkg/run/target.go` (`HTTPTarget`) is the only place that knows the surface
contract: it injects tasks via routd `POST /v1/messages` (ack `{ok,id}`) and
reads chats via `GET /v1/messages/inspect?jid=` (rows carry `content` +
`is_bot_message`). `--api` points at routd's reachable `/v1` base (the eval is
an operator-host tool, like the `eval` skill reaching localhost ports); the
target arg is proxyd's public base for `/pub` `/priv` `/chat` probes. The
injecting token needs `messages:write` + read scope on the eval folder, and the
`--chat` JID must be a real chat that folder owns.

Two known gaps (honest, not silent): routd exposes **no cost READ** endpoint
(cost is write-only), so `Cost()` is 0 over pure REST and `max_tokens` budgets
don't bite live until a cost source is wired; and `--mcp` expects an
inspect-compatible MCP-over-HTTP face — unset, the `mcp_roundtrip`/`parity`
cases fail loudly ("surface not configured") rather than false-pass.

The callback sink binds locally; the agent containers must be able to reach
`--sink`, so the eval host has to be on the target folder's crackbox egress
allowlist (or run on an already-allowed host). A default-deny refusal there is a
deploy gap, not a capability failure.

## v1 limits

`max_wall_ms` is the enforced per-case budget; `max_tokens`/`max_turns` are
declared and reported but not capped from outside. `dash` renders markdown; a
hosted HTML dashboard would be a later `serve` split.
