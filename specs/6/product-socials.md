---
status: planned
---

# Product: socials manager

Manage a multi-platform social presence: schedule, post, cross-post,
monitor, and surface what's gaining traction.
Template at `ant/examples/socials/`.

## Value prop

The agent runs the distribution layer: takes content (from the operator
or from the creator product), chooses timing and platform mix, posts
and cross-posts, watches for engagement signals, and tells the operator
what's working. The operator approves before anything goes out.

## What it does

- Accepts content briefs or drafts from the operator (or from the
  creator pipeline via forwarded message)
- Suggests platform mix and timing ("post to bsky now, schedule mastodon
  for 09:00, repost to discord at peak")
- Posts and cross-posts via MCP: post, repost, quote, like
- Monitors reply threads and engagement via inspect_messages / fetch_history
- Runs a weekly digest: what posted, what got traction, what flopped
- Surfaces viral angles: "this reply blew up — want to expand it into a thread?"

## Skills

| Skill           | Required | Notes                                    |
| --------------- | -------- | ---------------------------------------- |
| diary           | yes      |                                          |
| facts           | yes      | brand voice, platform preferences, goals |
| recall-memories | yes      |                                          |
| web             | yes      | trend research, hashtag research         |
| oracle          | no       | optional for deeper trend analysis       |

## MCP tools used

`post`, `repost`, `quote`, `like`, `delete`, `edit`,
`schedule_task`, `fetch_history`, `inspect_messages`

## Template folder

```
ant/examples/socials/
  SOUL.md           — distribution strategist; understands platform tone
                      differences; never posts without approval signal
  CLAUDE.md         — approval gate: ALWAYS require operator confirmation
                      before calling post/repost; log every post to diary;
                      weekly digest on Mondays via scheduled task
  skills/           — diary, facts, recall-memories, web
  facts/
    brand.md        — seed with brand voice, tone, target audience
    platforms.md    — which platforms, account handles, audience notes
  tasks.toml        — weekly digest cron
```

## Channels

Operator-facing: Telegram or Discord (where operator approves posts).
Outbound: bsky, mastodon, discord (announcement channels), reddit,
twitter/X, linkedin.

## Depends on

- At least one social adapter (bskyd, mastd, discd, reditd, twitd,
  linkd) — mix depends on operator
- timed — for scheduled posts and weekly digest
- HITL firewall _(deferred)_ — when shipped, replace manual approval with
  `hold_if` on post/repost; for now approval is a `/go` chat command
- Rate limits _(unshipped)_ — needed for production to prevent runaway
  posting on API rate-limited platforms

## Developer capabilities embedded

Generates HTML threads, formats markdown for platform-specific rendering,
scripts batch posting — all via standard skills, no separate product.

## Web page pitch

One agent, all your platforms. The socials manager drafts, schedules,
and cross-posts content — and surfaces what's actually gaining traction
so you can double down on what works.
