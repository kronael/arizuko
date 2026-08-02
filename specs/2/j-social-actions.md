---
status: shipped
---

> Renamed 2026-04-24: `react` → `like` for semantic alignment with platform
> UI (favourite/like/heart). Downvote counterpart (reddit, future) will be
> `dislike`, not `hate`.

# Social Actions — Outbound

Generic verb MCP tools; gateway resolves platform from JID prefix.

## Scope

Chat primitives only. Moderation (ban, pin, lock, kick), social-graph
(follow, repost, set_profile), and edit_post are **out of scope** —
not chat primitives, handled out-of-band by operators.

## Actions

Agent-facing verbs are `send`, `reply`, `post`, `like`, `dislike`,
`delete`, `edit`, `forward`, `quote`, `repost`, `send_file` — registered
in `ipc/ipc.go`, with per-adapter support declared through
`chanlib.BotHandler` and the `NoSocial` / `NoFileSender` embeds. The
live support matrix lives in each daemon's `README.md`, not here: a
table in a spec drifts the moment an adapter gains a verb.

An adapter that cannot do a verb returns a structured
`*chanlib.UnsupportedError` carrying `{Tool, Platform, Hint}` rather
than a bare failure, so the agent learns the fallback from the error
([`../4/19-action-grants.md`](../4/19-action-grants.md) §structured
unsupported errors).

The planned gateway-level group verbs (`close`, `drop_group`) never
shipped — neither name exists in the codebase. `drop_group` was the
rename of a proposed `delete` to avoid colliding with the platform
`delete` tool; the naming rule survives (different intents get different
tool names, never one tool with a `kind` switch), the verbs do not.

## Decisions

- Media upload: file path on disk. Agent writes to group folder;
  gateway uploads via platform client.
- Rate limits: exponential backoff (1s, 2s, 4s, max 60s). Return
  `{ error: 'rate_limited', retry_after_ms }`.
- Content length: gateway validates per platform; on exceed return
  error with max length. Agent rewrites — never truncate or split.
