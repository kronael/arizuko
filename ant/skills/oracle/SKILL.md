---
name: oracle
description: Drives the `codex` CLI as a subprocess for a one-shot second opinion from a second model.
when_to_use: >
  Use when uncertain about a tricky algorithm, a library Claude doesn't know well, or before
  committing to a non-obvious implementation. Also on self-disagreement after a `<think>` round.
  Do not reach for oracle on routine uncertainty — most questions resolve via `/recall-memories` +
  `/find` faster without an external call.
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

Spec: `specs/5/H-call-llm-mcp.md`.
