---
name: oracle
description: Ask a second model (codex CLI) for a sanity check or second opinion. Auth via `~/.codex` mount or `OPENAI_API_KEY`/`CODEX_API_KEY` in folder secrets.
when_to_use: >
  Use when uncertain about a tricky algorithm, a library Claude doesn't know
  well, or before committing to a non-obvious implementation.
---

# Oracle

Drives the `codex` CLI (`@openai/codex`) as a subprocess for a
one-shot second opinion. No new MCP tool, no new daemon — the binary
is on `PATH`, the auth state is either mounted from the host or
injected as folder env, the skill is the surface.

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

## Auth — two equivalent paths

**Path A — host-side `~/.codex` mount (preferred for ChatGPT login).**
When the operator's gated process has `HOST_CODEX_DIR=/path/to/.codex`
in its env, every spawned agent gets that dir bind-mounted at
`/home/node/.codex` (rw). codex CLI reads `auth.json` from there
just like on the host — including ChatGPT-OAuth refresh-token
rotation, session/history persistence, and config. No env vars
needed; auth flows from a single host login. Probe with:

```bash
codex login status   # "Logged in using ChatGPT" / "Logged in using API key"
```

**Path B — `CODEX_API_KEY` / `OPENAI_API_KEY` env (folder secrets).**
The CLI also accepts an API key from the container env — same
folder-secret path that injects channel tokens, `WHISPER_BASE_URL`,
etc. Resolution walks folder ancestors root→F deepest-wins; deeper
folders override shallower. Spec:
`specs/5/32-tenant-self-service.md` §Secrets. AES-GCM at rest
(`store/migrations/0034-secrets.sql`); requires `AUTH_SECRET` set on
the gated process. Insert via the standard secrets path; restart the
group's container so `container.resolveSpawnEnv` picks it up.

## Missing-auth fallback

If neither path is configured, `codex exec` fails fast with an auth
error. Detect first, then explain or skip:

```bash
if ! codex login status >/dev/null 2>&1 \
   && [ -z "${CODEX_API_KEY:-}${OPENAI_API_KEY:-}" ]; then
  echo "oracle unavailable — no codex login on host mount and no folder secret"
  exit 0
fi
codex exec "$prompt"
```

Tell the user "oracle isn't configured" and continue with whatever
you can do without it. Do NOT crash the turn.

## Output shape

`codex exec "<prompt>"` writes the final message to stdout (one
block, may be multi-paragraph). With `--json`, it emits JSON Lines —
one event per line, terminal event has the full final message.
Treat the answer as advisory; it's a second opinion, not a verdict.
Cite when you act on it ("codex flagged that this loop allocates per
iteration; adjusted to reuse the buffer").

Spec: `specs/5/H-call-llm-mcp.md`.
