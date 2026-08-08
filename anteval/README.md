# anteval

Agent-capability eval — a black-box prober that certifies a live arizuko
agent can operate the platform itself. Spec:
[`specs/5/9-agent-capability-eval.md`](../specs/5/9-agent-capability-eval.md).

Each case injects one real task through a public surface, lets the live agent
do it with its own MCP tools, and asserts an **externally observable effect** —
an HTTP status, a callback the agent's artifact made, or a message visible via
REST/MCP — never the agent's prose, never the instance's internal state. Zero
arizuko-internal imports: a black-box client over the same surfaces a human or
external tool uses (sibling component, `specs/6/7`).

## Run

```bash
make build
# mint the injecting bearer (operator, on the instance host):
arizuko token krons issue bearer eval --scope messages:write,messages:read,cost:read
./anteval run https://krons.fiu.wtf \
  --api   http://localhost:8081 \ # routd /v1 base reachable from the eval host
  --token "$AGENTEVAL_TOKEN" \    # bearer minted above, for the eval folder
  --chat  web:eval \              # chat JID tasks are injected into (folder must own it)
  --sink-addr :9099 \            # local bind for the callback sink (routable iface)
  --sink  http://172.22.0.1:9099 \# URL the agent containers call back to (docker-gateway IP works host-local)
  --smoke                         # gate subset; omit for all 19
# selectors: --dimension web | --case pub-200 ; output: --md report.md --json report.json
./anteval dash report.json      # re-render a saved report
make validate                     # load+validate the case catalog (no target)
```

Live preconditions (spec § "Live-run preconditions"): the eval folder exists
(`arizuko group <inst> add web:eval eval`), its `MaxChildren` is raised for the
subagent cases, and the sink URL is reachable from agent containers.

**Grant the eval folder the tools its cases need, or half the run measures your
grant table instead of the agent.** The `role:member` floor is messaging only
(send/reply/react); `register_group`, `issue_webhook`, `issue_chat_token`,
`add_acl` and `web_route_set` are all explicit delegations. On the 2026-08-08
live run four of eight smoke cases failed purely because the eval folder held
none of them — and the agent had correctly named the missing grant in chat
while the harness reported a bare `timeout: no callback`.

Each case now declares `requires = [...]`; a timeout quotes it back so the
reader can tell an ungranted tool from a failed agent. To grant them:

```bash
for a in mcp:register_group mcp:delegate_group mcp:issue_webhook \
         mcp:issue_chat_token mcp:add_acl mcp:remove_acl mcp:web_route_set; do
  arizuko group <inst> grant folder:eval 'eval/**' "$a"
done
```

Exit is non-zero if any case fails. `dash` renders a saved JSON report.

## How a case proves itself

Templates expand per run: `{nonce}` (unique per run+case), `{sink}`, `{target}`,
`{chat}`, and `{cb.KEY}` — a query param the agent handed back through the
callback sink (e.g. a freshly minted chat-link token). Checkers:

- `callback` — the agent wired an artifact (skill, MCP tool, app, webhook,
  child) that fires `{sink}/cb/{nonce}`; firing is the proof.
- `http_status` — a `{cb.url}`/`{cb.token}` URL's FIRST response carries the
  expected code (publish → 200, gated → 303-to-login, deleted/denied → 404);
  redirects are asserted, never followed.
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

`Cost()` reads routd `GET /v1/cost?turn_id=` (token needs `cost:read`); a 404
— no cost recorded yet, or a routd predating the endpoint — reads as 0, other
failures surface. One known gap (honest, not silent): `--mcp` expects an
inspect-compatible MCP-over-HTTP face — unset, the `rest-mcp-parity` case
fails loudly ("surface not configured") rather than false-pass; the platform's
chat-token MCP face lacks an inspect-read (proposal in `BUGS.md`).

The callback sink binds locally; the agent containers must be able to reach
`--sink`, so the eval host has to be on the target folder's crackbox egress
allowlist (or run on an already-allowed host). A default-deny refusal there is a
deploy gap, not a capability failure.

## v1 limits

`max_wall_ms` is the enforced per-case budget; `max_tokens`/`max_turns` are
declared and reported but not capped from outside. `dash` renders markdown; a
hosted HTML dashboard would be a later `serve` split.
