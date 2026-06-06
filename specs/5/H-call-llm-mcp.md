---
status: shipped
---

# Oracle — ask another model

Experimental. One narrow question: how does Claude consult a second
model when uncertain? Today's answer: an **oracle skill** in
`ant/skills/oracle/` that drives the `codex` CLI as a subprocess and
returns its answer as text. No new daemon, no new MCP endpoint.

## Scope (this pass)

- `ant/skills/oracle/SKILL.md` — when to invoke (disagreement
  with self, second opinion on tricky algorithm, library Claude
  doesn't know well)
- A `codex` binary on the agent container's PATH (add to `ant/Dockerfile`)
- Secret: `OPENAI_API_KEY` (or `CODEX_API_KEY`) in folder secrets.
  Note: folder-secret env injection is deferred to spec 7/Y; for now,
  set the key directly in the instance `.env` or pass as an env var.

That's it. No `call_llm` MCP tool. No OpenRouter. No cost tracking.
No model allowlist config. The skill is the surface; the CLI is the
backend; the secret is the auth.

## Future (not this pass)

If multiple oracles emerge (Codex, Gemini CLI, local llama, …) and
Claude needs to route between them, an `oracle` MCP tool that picks
the right backend per question becomes the natural shape — a Q&A
routing layer. Don't build that until the second oracle exists.

## Acceptance

- Folder with `OPENAI_API_KEY` in its secrets and the `oracle`
  skill enabled can ask Codex a question and get an answer
- Folder without the secret falls back gracefully (skill reports the
  missing key, doesn't crash)
- No new IPC / MCP / daemon
