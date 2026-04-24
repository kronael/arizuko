---
status: partial
---

> Gaps: `local:` prefix still referenced in router; outbound still
> callback-driven (no `status` column on messages); `send_file` is a
> distinct MCP tool (see `ipc/ipc.go:368`), not folded into
> `send_message(..., files=[])`.

# Unified message routing

Single message table, single router decision point, uniform
`prefix:identifier` addressing.

Principles:

- All messages (user input + agent output) flow through DB.
- Router is sole decision point; no direct enqueue/callback shortcuts.
- Uniform addressing: channels = `platform:account/id`, groups =
  folder path (no prefix). `:` in target distinguishes typed destinations
  (`daemon:`, `builtin:`, `folder:`) from bare folder paths.
- Remove `local:` prefix.

Router priority on resolution: @mention (explicit) > reply-chain
(implicit continuation) > sticky (session) > default.
Agent messages (sender without `:`) never get content-based routing.

Shipped so far (v0.25): agent outputs written via `PutMessage`,
delegation/escalation as messages with `forwarded_from`, removed
`EnqueueTask`/`OutboundEntry`. MCP tools write messages directly.

Unblockers: drop `local:` prefix, poll-based outbound delivery (kill
callback), `status` column for processing state, unify `send_file` into
`send_message(..., files=[])`.
