---
status: partial
---

# Social Actions — Outbound

Generic verb MCP tools; gateway resolves platform from JID prefix.

## Scope

Chat primitives only. Moderation (ban, pin, lock, kick), social-graph
(follow, repost, set_profile), and edit_post are **out of scope** —
not chat primitives, handled out-of-band by operators.

## Actions

| Action        | Platforms                           | Status  |
| ------------- | ----------------------------------- | ------- |
| `reply`       | all adapters                        | shipped |
| `post`        | reditd, bskyd, mastd, discd         | partial |
| `react`       | discord, mastodon, bluesky          | planned |
| `delete_post` | discord, mastodon, bluesky, reddit  | planned |
| `close`       | gateway (marks thread group closed) | planned |
| `delete`      | gateway (removes thread group)      | planned |

## Tool shapes

- `post`: `{ jid, content, media? }`
- `reply`: `{ jid, target, content }` (shipped as `send_message`/`send_reply`)
- `react`: `{ jid, target, reaction? }`
- `delete_post`: `{ jid, target }`
- `close` / `delete`: `{ group }`

## Decisions

- Media upload: file path on disk. Agent writes to group folder;
  gateway uploads via platform client.
- Rate limits: exponential backoff (1s, 2s, 4s, max 60s). Return
  `{ error: 'rate_limited', retry_after_ms }`.
- Content length: gateway validates per platform; on exceed return
  error with max length. Agent rewrites — never truncate or split.
