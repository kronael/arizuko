# agenteval

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
./agenteval run https://krons.fiu.wtf \
  --token "$AGENTEVAL_TOKEN" \    # bearer for an eval root folder
  --chat  web:eval \              # chat JID tasks are injected into
  --sink  https://eval-host:9099 \# sink URL the agent containers can reach
  --smoke                         # gate subset; omit for all 19
# selectors: --dimension web | --case pub-200 ; output: --md report.md --json report.json
./agenteval dash report.json      # re-render a saved report
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
- `rest_reply` / `rest_observe` — a message carrying `{nonce}` is readable in
  `{chat}` via REST.
- `mcp_roundtrip` — same over the MCP face (`--mcp`).
- `parity_sentinel` — `{nonce}` is identical via REST and MCP (uniform surface).

The run mints a nonce per case and embeds it in every name/URL/body, so runs are
idempotent and never collide; teardown is best-effort.

## Wiring seam

`pkg/run/target.go` (`HTTPTarget`) is the only place that knows the surface
paths (routd REST `/chats/{jid}/messages`, `/turns/{id}/cost`; proxyd `/pub`
`/priv` `/chat` `/hook`; MCP via `--mcp`). Adjust there if the surface moves.

The callback sink binds locally; the agent containers must be able to reach
`--sink`, so the eval host has to be on the target folder's crackbox egress
allowlist (or run on an already-allowed host). A default-deny refusal there is a
deploy gap, not a capability failure.

## v1 limits

`max_wall_ms` is the enforced per-case budget; `max_tokens`/`max_turns` are
declared and reported but not capped from outside. `dash` renders markdown; a
hosted HTML dashboard would be a later `serve` split.
