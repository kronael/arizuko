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

- **Path A — host mount**: operator sets `HOST_CODEX_DIR=/path/to/.codex`; runed mounts it at `/home/node/.codex`. ChatGPT-OAuth, no env vars needed. Probe: `codex login status`
- **Path B — env-profile key**: `CODEX_API_KEY`/`OPENAI_API_KEY` either in the platform host `.env` (global — reaches every container via `readSecrets`) or as a user's own key via `/dash/me/env` (BYOC). Model keys scope the same way as Anthropic's: platform-or-user, NEVER folder — the store rejects folder scope (`specs/5/14-credentials.md`). Restart the group container after inserting.

## Missing-auth fallback

Detect before calling; do NOT crash the turn:

```bash
if ! codex login status >/dev/null 2>&1 \
   && [ -z "${CODEX_API_KEY:-}${OPENAI_API_KEY:-}" ]; then
  echo "oracle unavailable — no codex login on host mount and no env-profile key"
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

Keep the cost number out of visible output entirely. There is nowhere
to record it: the model gateway meters what passes through it, and a
codex call does not.

## Cost of a codex call

A codex call does not go through the model gateway, so no spend cap
covers it. The operator sees it on OpenAI's invoice and nowhere else.
Treat `/oracle` as spending real money that no limit will stop, and
use it when the answer is worth that.

Spec: `specs/6/39-gateway-owns-spend.md`.
