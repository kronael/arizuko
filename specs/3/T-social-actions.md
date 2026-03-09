# Social Actions — Outbound

**Status**: open

Outbound actions for social platforms. Not a new interface —
entries in the existing action registry, exposed as MCP tools.
Gateway resolves platform from JID prefix.

## Actions

| Action        | Platforms                                       |
| ------------- | ----------------------------------------------- |
| `post`        | reddit, twitter, mastodon, bluesky, fb, threads |
| `reply`       | all                                             |
| `react`       | all                                             |
| `repost`      | twitter, mastodon, bluesky                      |
| `follow`      | reddit, twitter, mastodon, bluesky              |
| `unfollow`    | reddit, twitter, mastodon, bluesky              |
| `set_profile` | mastodon, bluesky, reddit                       |
| `delete_post` | all                                             |
| `edit_post`   | reddit, mastodon, fb                            |
| `close`       | gateway (marks thread group closed)             |
| `delete`      | gateway (removes thread group)                  |
| `ban`         | reddit, discord, twitch, youtube, mastodon      |
| `unban`       | reddit, discord, twitch, mastodon               |
| `timeout`     | discord, twitch, youtube                        |
| `mute`        | reddit, twitter, mastodon, bluesky              |
| `block`       | twitter, mastodon, bluesky, twitch              |
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

## Open

- Media upload flow — presigned URLs or inline base64?
- Rate limit tracking per platform per JID
- Retry/backoff strategy for transient failures
- Content length validation per platform
- Thread-aware posting (tweet threads, reddit comment chains)
