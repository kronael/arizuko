---
status: unshipped
---

# Oracle — ask another model

Experimental. One narrow question: how does Claude consult a second
model when uncertain? Today's answer: a **codex skill** in
`ant/skills/oracle-codex/`. The skill drives the `codex` CLI as a
subprocess, returns its answer as text. No new daemon, no new MCP
endpoint.

## Scope (this pass)

- `ant/skills/oracle-codex/SKILL.md` — when to invoke (disagreement
  with self, second opinion on tricky algorithm, library Claude
  doesn't know well)
- A `codex` binary on the agent container's PATH (add to `ant/Dockerfile`)
- Secret: `OPENAI_API_KEY` (or `CODEX_API_KEY`) in folder secrets,
  exported into the container env via existing secrets-injection path

That's it. No `call_llm` MCP tool. No OpenRouter. No cost tracking.
No model allowlist config. The skill is the surface; the CLI is the
backend; the secret is the auth.

## Future (not this pass)

If multiple oracles emerge (Codex, Gemini CLI, local llama, …) and
Claude needs to route between them, an `oracle` MCP tool that picks
the right backend per question becomes the natural shape — a Q&A
routing layer. Don't build that until the second oracle exists.

## Acceptance

- Folder with `OPENAI_API_KEY` in its secrets and the `oracle-codex`
  skill enabled can ask Codex a question and get an answer
- Folder without the secret falls back gracefully (skill reports the
  missing key, doesn't crash)
- No new IPC / MCP / daemon
