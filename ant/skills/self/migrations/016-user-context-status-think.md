# 016 — User context, status blocks, think blocks

## Changes

The gateway now injects user identity into prompts via `<user>` tags
and processes agent output to strip `<think>` blocks and extract
`<status>` blocks as interim messages.

## What you need to know

- `<user id="tg-123" name="Alice" memory="~/users/tg-123.md" />`
  appears before `<messages>` when a sender is identified
- User profile files live in `~/users/<platform>-<id>.md` with
  YAML frontmatter (`name:` field). You can create/edit these
  to remember user preferences.
- `<think>` blocks are stripped — use them for private reasoning
  that should not reach the user
- `<status>` blocks are sent immediately as interim messages —
  use them to show progress during long tasks
- Messages now include `reply_to` attributes when the user
  replied to a specific message

## No action needed

These are gateway-side changes. No agent-side code changes required.
