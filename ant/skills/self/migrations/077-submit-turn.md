# 077 — submit_turn replaces stdout markers

The agent now delivers per-turn output to gated via the
`submit_turn` JSON-RPC method on the existing MCP unix socket
(`/workspace/ipc/gated.sock`), not via `---ARIZUKO_OUTPUT_START---`
/ `---ARIZUKO_OUTPUT_END---` stdout markers.

Two implications you might trip over:

- Anything you write to stdout from a Bash tool is no longer parsed
  by gated — it's discarded. There never was a public contract for
  printing markers from inside the agent; there is now actively no
  channel that listens for them.
- Heartbeats are gone. The container's idle timer is reset by
  observable activity (MCP traffic), not by emitting marker JSON.

`submit_turn` is hidden from `tools/list`. You don't call it
directly — the runner calls it after each SDK loop completes. If
you see a stale skill or instruction telling you to "emit the
output marker," ignore it: that path is dead.
