---
name: oracle
description: >
  Run `codex` CLI as subprocess for a one-shot second opinion from a
  different model. USE for "ask the oracle", "second opinion", "/oracle",
  tricky algorithms, unfamiliar libraries, self-disagreement after a
  `<think>` round, before non-obvious implementations. NOT for routine
  uncertainty (use recall-memories + find first — faster, no external
  call).
user-invocable: true
---

# Oracle

## Call it

```bash
# Argv form — short prompts
codex exec "is there a stdlib equivalent of Python's bisect in Go?"

# Stdin form — multi-line context
cat <<'EOF' | codex exec -
Review this CRDT merge function for ordering bugs:
<paste code>
EOF

# Pipe form — feed another command's output as context
go test ./... 2>&1 | codex exec "summarize the failure and propose the smallest fix"
```

Useful flags: `--json` for machine-readable output, `-o <path>` to
write the final message to a file, `--ephemeral` to skip session
persistence (default is fine for one-shots).

## Auth

Two paths, codex picks up whichever is present:

- **Path A — host mount**: operator sets `HOST_CODEX_DIR=/path/to/.codex`; gated bind-mounts it at `/home/node/.codex`. ChatGPT-OAuth, no env vars needed. Probe: `codex login status`
- **Path B — folder secret**: `CODEX_API_KEY` or `OPENAI_API_KEY` in folder secrets (`specs/5/32-tenant-self-service.md §Secrets`). Restart the group container after inserting.

## Missing-auth fallback

Detect before calling; do NOT crash the turn:

```bash
if ! codex login status >/dev/null 2>&1 \
   && [ -z "${CODEX_API_KEY:-}${OPENAI_API_KEY:-}" ]; then
  echo "oracle unavailable — no codex login on host mount and no folder secret"
  exit 0
fi
codex exec "$prompt"
```

## Output

Stdout = final message. `--json` emits JSONL; terminal event has the full message.
Treat as advisory. Cite when you act on it.

## NEVER leak cost to the user

codex writes a cost/token summary line to its own output (something
like `tokens used: 4321 ($0.11)` or a `total_cost_usd` field in
`--json` mode). That is **internal accounting**, not chat content.

- NEVER quote, paraphrase, or forward codex's cost/token line into a
  reply, status, file caption, or summary.
- NEVER mention dollar amounts, token counts, or "cost" sourced from
  codex output.
- Extract the answer (the model's actual message text) before you
  show anything to the user. Drop the trailing cost block.
- If you must reason about the cost, do so inside `<think>` only —
  never in visible output.

Use the `log_external_cost` MCP tool (see below) to record the
spend internally. That is the **only** sanctioned destination for
the cost number.

## Cost reporting (spec 5/34)

Anthropic spend is tracked automatically (gateway captures usage from
every Claude Code turn). **codex/openai spend isn't**, so report it
explicitly after each call so the budget gate covers it.

Use `--json` so the per-call usage is machine-readable. After the
codex run, call the `log_external_cost` MCP tool with what you
captured:

```bash
codex --json exec "$prompt" > /tmp/oracle.jsonl
# parse the terminal `task_complete` event for token_usage + total_cost_usd
```

Then in your turn:

```
log_external_cost(
  provider="codex",
  model="<the model codex used, e.g. gpt-5>",
  input_tokens=<token_usage.input>,
  output_tokens=<token_usage.output>,
  cost_usd=<total_cost_usd>,
)
```

Skipping this hides the call from cost-caps. The operator still sees
it on OpenAI's invoice but the per-folder cap doesn't include it.

Spec: `specs/5/H-call-llm-mcp.md`, `specs/10/19-cost-caps.md`.
