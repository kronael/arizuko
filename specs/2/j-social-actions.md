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

| Action       | Platforms                           | Status  |
| ------------ | ----------------------------------- | ------- |
| `reply`      | all adapters                        | shipped |
| `post`       | reditd, bskyd, mastd, discd         | partial |
| `like`       | discord, mastodon, bluesky          | partial |
| `delete`     | discord, mastodon, bluesky, reddit  | shipped |
| `close`      | gateway (marks thread group closed) | planned |
| `drop_group` | gateway (removes thread group)      | planned |

> Updated 2026-04-24: `like` and `delete` are implemented at the
> adapter + gateway layer on the listed platforms and registered as
> MCP tools in `ipc/ipc.go`. Planned gateway-level group verb renamed
> `delete` → `drop_group` to avoid collision with the platform tool.

## Tool shapes

- `post`: `{ jid, content, media? }`
- `reply`: `{ jid, target, content }` (shipped as `send_message`/`send_reply`)
- `like`: `{ jid, target, reaction? }`
- `delete`: `{ jid, target }`
- `close` / `drop_group`: `{ group }`

## Decisions

- Media upload: file path on disk. Agent writes to group folder;
  gateway uploads via platform client.
- Rate limits: exponential backoff (1s, 2s, 4s, max 60s). Return
  `{ error: 'rate_limited', retry_after_ms }`.
- Content length: gateway validates per platform; on exceed return
  error with max length. Agent rewrites — never truncate or split.
