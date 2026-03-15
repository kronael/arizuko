## <!-- trimmed 2026-03-15: TS removed, rich facts only -->

## status: shipped

# Social Actions — Outbound

Outbound actions for social platforms. Entries in the existing action
registry, exposed as MCP tools. Gateway resolves platform from JID prefix.

## Action-to-Platform Matrix

| Action        | Platforms                                       |
| ------------- | ----------------------------------------------- |
| `post`        | reddit, twitter, mastodon, bluesky, fb, threads |
| `reply`       | all                                             |
| `react`       | all                                             |
| `repost`      | twitter, mastodon, bluesky, reddit              |
| `follow`      | reddit, twitter, mastodon, bluesky              |
| `unfollow`    | reddit, mastodon, bluesky                       |
| `set_profile` | mastodon, bluesky, reddit                       |
| `delete_post` | all                                             |
| `edit_post`   | reddit, mastodon, fb                            |
| `close`       | gateway (marks thread group closed)             |
| `delete`      | gateway (removes thread group)                  |
| `ban`         | reddit, discord, twitch, youtube, mastodon, fb  |
| `unban`       | reddit, discord, twitch, mastodon, fb           |
| `timeout`     | discord, twitch, youtube                        |
| `mute`        | reddit, twitter, mastodon, bluesky              |
| `block`       | twitter, mastodon, bluesky, twitch, fb          |
| `pin`         | reddit, mastodon, discord                       |
| `unpin`       | reddit, mastodon, discord                       |
| `lock`        | reddit, discord                                 |
| `unlock`      | reddit, discord                                 |
| `hide`        | youtube, facebook, instagram                    |
| `approve`     | reddit, youtube, mastodon                       |
| `set_flair`   | reddit                                          |
| `kick`        | discord                                         |

## Schema Shapes

All use generic verbs as MCP tool names. `jid` determines platform
via prefix. `target` is platform-native ID.

- **post**: jid, content, media (file paths)
- **reply**: jid, target, content
- **react**: jid, target, reaction?
- **repost/follow/unfollow**: jid, target
- **set_profile**: jid, name?, bio?, avatar?
- **delete_post/edit_post**: jid, target, content?
- **close/delete**: group
- **ban/unban**: jid, target, duration?, reason?
- **timeout**: jid, target, duration
- **mute/block/pin/unpin/lock/unlock/hide/approve**: jid, target
- **set_flair**: jid, target, flair
- **kick**: jid, target

## Decisions

- **Media upload**: file path on disk. Agent writes to group folder,
  gateway uploads via platform client. No presigned URLs, no base64.
- **Rate limits**: exponential backoff (1s, 2s, 4s, max 60s). Return
  `{ error: 'rate_limited', retry_after_ms }`. Agent decides retry.
- **Content length**: gateway validates per platform. On exceed: return
  error with max length, don't truncate or split. Agent rewrites.
- **Thread-aware posting**: future work (add `thread` field later).
