---
status: partial
---

# Social Actions — Outbound

Outbound actions for social platforms. Entries in the action registry,
exposed as MCP tools. Gateway resolves platform from JID prefix.

## Shipped actions

| Action  | How                                                    |
| ------- | ------------------------------------------------------ |
| `reply` | All adapters via `send_message` MCP tool (replyTo set) |
| `post`  | reditd, bskyd internal (submit_post / create_post)     |

## Planned actions

| Action        | Platforms (shipped adapters only)   |
| ------------- | ----------------------------------- |
| `react`       | discord, mastodon, bluesky, reddit  |
| `repost`      | mastodon, bluesky, reddit           |
| `follow`      | reddit, mastodon, bluesky           |
| `unfollow`    | reddit, mastodon, bluesky           |
| `set_profile` | mastodon, bluesky, reddit           |
| `delete_post` | all                                 |
| `edit_post`   | reddit, mastodon                    |
| `close`       | gateway (marks thread group closed) |
| `delete`      | gateway (removes thread group)      |
| `ban`         | reddit, discord, mastodon           |
| `unban`       | reddit, discord, mastodon           |
| `pin`         | reddit, mastodon, discord           |
| `unpin`       | reddit, mastodon, discord           |
| `lock`        | reddit, discord                     |
| `unlock`      | reddit, discord                     |
| `kick`        | discord                             |

## Tool shapes

Generic verbs as MCP tool names. `jid` determines platform via prefix.
`target` is platform-native ID.

- **post**: jid, content, media (file paths)
- **reply**: jid, target, content
- **react**: jid, target, reaction?
- **repost/follow/unfollow**: jid, target
- **set_profile**: jid, name?, bio?, avatar?
- **delete_post/edit_post**: jid, target, content?
- **close/delete**: group
- **ban/unban**: jid, target, duration?, reason?
- **pin/unpin/lock/unlock/kick**: jid, target

## Decisions

- Media upload: file path on disk. Agent writes to group folder;
  gateway uploads via platform client. No presigned URLs, no base64.
- Rate limits: exponential backoff (1s, 2s, 4s, max 60s). Return
  `{ error: 'rate_limited', retry_after_ms }`. Agent decides retry.
- Content length: gateway validates per platform. On exceed return
  error with max length; don't truncate or split. Agent rewrites.
