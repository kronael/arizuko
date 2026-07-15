---
name: progress
description: >
  Live status checklist for multi-step work — post ONE message and `edit`
  it in place to tick tasks off, instead of a stream of ⏳ status pings.
  USE only when a turn genuinely has several steps or will take a while
  (a deploy, a multi-file change, research-then-write). NOT for simple or
  one-shot replies — a checklist there is noise.
---

# Live progress checklist

When a turn is genuinely multi-step, show the user ONE message that updates
in place, not a stream of separate status pings.

## When — and when NOT

- USE only when you are already tracking the work as steps (several of them,
  or a slow turn). **Never force a checklist onto a one-line answer** — that
  is noise. Simple reply → just reply.
- Three real milestones beat ten micro-updates. Don't announce trivial steps.

## The pattern

1. Post the checklist ONCE and **capture the returned id**:

   `send` (or `reply`) with the list —
   ```
   ☐ build
   ☐ deploy
   ☐ verify
   ```
   The tool returns `{"ok":true,"id":"<platform_id>"}`. Hold onto that `id`.

2. As each step finishes, `edit` the SAME message (`targetId` = that `id`):
   ```
   ☑ build
   ⏳ deploy
   ☐ verify
   ```
   ☑ done · ⏳ running now · ☐ pending.

3. Final edit collapses to the outcome — the checklist becomes the answer
   (or a one-line "done" if a separate reply carries the substance).

## Platform reality

`edit` works on Slack, Telegram, Discord, Mastodon, Bluesky. Where it is not
supported (email, WhatsApp, Reddit) `edit` returns unsupported — fall back to
the normal `<status>` ⏳ pings for that turn. Never retry a rejected `edit`.
