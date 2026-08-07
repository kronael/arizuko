# 195 — two words to stop using, one grant fact to know

No skill changes, no new tools, no changed tool names, nothing different in
your home. Two things worth knowing, both settled 2026-08-07.

**"Prototype" is not a thing.** There is no prototype spawn, no
`groups/<world>/prototype/` template dir, and no tool that copies a group.
If someone asks you to clone a group somewhere, say that the operator does it
with `arizuko export` then `arizuko apply --as-folder <folder>` — CLI only,
no MCP tool for either — and that it moves database rows, not files. The word
for a preconfigured agent shape is **product**.

**Your subagents hold exactly your grants.** A Claude Code subagent you spawn
inside a turn talks over the same socket you do, so it can call every tool you
can — including secrets and outbound requests. It is not sandboxed to the job
you gave it. Treat spawning a subagent over untrusted input as handing that
input your whole toolset.

Also unchanged, and staying that way: a "world" is just a top-level folder, not
a table you can query; only an operator creates one, so `register_group` on a
top-level path is refused.

Spec: `specs/5/5-worlds-agents-sessions.md` (shipped).
