---
status: shipped
---

# Social Actions — Outbound

Outbound actions for social platforms. Not a new interface —
entries in the existing action registry, exposed as MCP tools.
Gateway resolves platform from JID prefix.

## Actions

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

## Architecture

See `U-channel-actions.md` for registration, filtering, and
the generic agent-runner proxy. Each channel exports actions
from `src/channels/{platform}/actions.ts`, registered on
connect, filtered per group in the manifest.

## Action schemas

```typescript
// post — create new content
{ jid: string, content: string, media?: string[] }

// reply — respond to existing content
{ jid: string, target: string, content: string }

// react — like/upvote/favourite
{ jid: string, target: string, reaction?: string }

// repost — share/boost/retweet
{ jid: string, target: string }

// follow / unfollow
{ jid: string, target: string }

// set_profile — update display name, bio, avatar
{ jid: string, name?: string, bio?: string, avatar?: string }

// delete_post / edit_post
{ jid: string, target: string, content?: string }

// close — mark thread group closed (no new messages)
{ group: string }

// delete — remove thread group entirely
{ group: string }

// ban / unban — remove user from community
{ jid: string, target: string, duration?: number, reason?: string }

// timeout — temporary mute (seconds)
{ jid: string, target: string, duration: number }

// mute / block — account-level silencing
{ jid: string, target: string }

// pin / unpin — sticky content
{ jid: string, target: string }

// lock / unlock — prevent new replies
{ jid: string, target: string }

// hide — suppress content without deleting
{ jid: string, target: string }

// approve — release from moderation queue
{ jid: string, target: string }

// set_flair — tag content or user
{ jid: string, target: string, flair: string }
```

All schemas use Zod. `jid` determines platform via prefix.
`target` is platform-native ID (post ID, user ID, etc.).

These are the MCP tool names — generic verbs. The handler
switches on platform internally. See `U-channel-actions.md`.

## Resolved decisions

- **Media upload**: file path on disk. Agent writes media
  to group folder, passes path. Gateway uploads via platform
  client. No presigned URLs, no base64.
- **Rate limits**: exponential backoff (1s, 2s, 4s, max 60s).
  Return structured error `{ error: 'rate_limited', retry_after_ms }`.
  Agent decides whether to retry.
- **Content length**: gateway validates per platform before
  sending. On exceed: return error with max length, don't
  truncate or split. Agent rewrites.
- **Thread-aware posting**: not in this milestone. `post`
  creates standalone content. Thread continuation is future
  work (add `thread` field to post schema later).

## Scope

This milestone: register the 27 action handlers in
`src/actions/social.ts` with generic verb names. Handlers
switch on `platformFromJid()`. Only mastodon and bluesky
channels implemented (build-first tier). Other platforms
are stubs that return "not implemented" errors.

## Acceptance criteria

1. `src/actions/social.ts` exports all 27 actions
2. Actions registered in `src/ipc.ts` alongside existing actions
3. `platformFromJid()` utility in `src/router.ts`
4. Client registry (`registerClient`/`unregisterClient`) works
5. Mastodon: post, reply, react, repost, follow work end-to-end
6. Bluesky: post, reply, react, repost, follow work end-to-end
7. Other platforms return structured "not implemented" error
8. All existing tests pass
