---
name: oracle
description: Ask a second model (codex CLI) for a sanity check or
  second opinion. Use when uncertain about a tricky algorithm, a
  library Claude doesn't know well, or before committing to a
  non-obvious implementation. Reads `OPENAI_API_KEY` /
  `CODEX_API_KEY` from folder secrets; reports cleanly if missing.
---

# Oracle

Drives the `codex` CLI (`@openai/codex`) as a subprocess for a
one-shot second opinion. No new MCP tool, no new daemon — the binary
is on `PATH`, the secret is in folder env, the skill is the surface.

## When to invoke

- A tricky algorithm where the trade-offs aren't obvious
- A library / API surface Claude doesn't know well
- A sanity check on a non-obvious implementation before it ships
- Disagreement with self after a `<think>` round

Don't reach for the oracle on every uncertainty — most questions
resolve via `/recall-memories` + `/find` faster and without an
external call. Use it when a second opinion is the actual bottleneck.

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

## Missing key fallback

The CLI reads `CODEX_API_KEY` (or `OPENAI_API_KEY`) from the
container env. If neither is set, `codex exec` fails fast with an
auth error. Detect first, then either explain or skip:

```bash
if [ -z "${CODEX_API_KEY:-}${OPENAI_API_KEY:-}" ]; then
  echo "oracle unavailable — no CODEX_API_KEY/OPENAI_API_KEY in folder secrets"
  exit 0
fi
codex exec "$prompt"
```

Tell the user "oracle isn't configured for this folder" and continue
with whatever you can do without it. Do NOT crash the turn.

## Adding the secret

`OPENAI_API_KEY` (or `CODEX_API_KEY`) lives in the folder secrets
table — the same path that injects channel tokens, `WHISPER_BASE_URL`,
etc. into the agent container at spawn time. Resolution walks folder
ancestors root→F deepest-wins; deeper folders override shallower.
Spec: `specs/5/32-tenant-self-service.md` §Secrets. AES-GCM at rest
in `secrets` (`store/migrations/0034-secrets.sql`); requires
`AUTH_SECRET` set on the gated process.

The operator inserts the secret via the standard secrets path
(folder scope, same as any other folder-bound API key). After insert,
restart the group's container so `container.resolveSpawnEnv` picks
the new env up.

## Output shape

`codex exec "<prompt>"` writes the final message to stdout (one
block, may be multi-paragraph). With `--json`, it emits JSON Lines —
one event per line, terminal event has the full final message.
Treat the answer as advisory; it's a second opinion, not a verdict.
Cite when you act on it ("codex flagged that this loop allocates per
iteration; adjusted to reuse the buffer").

Spec: `specs/5/H-call-llm-mcp.md`.
