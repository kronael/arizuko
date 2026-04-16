# 016 — user context, status blocks, think blocks

Gateway now:

- Injects `<user id="tg-123" name="Alice" memory="~/users/tg-123.md" />`
  before `<messages>` when a sender is identified. Profiles in
  `~/users/<platform>-<id>.md` with YAML frontmatter — edit to remember
  preferences.
- Strips `<think>` blocks — use for private reasoning.
- Sends `<status>` blocks immediately as interim messages.
- Adds `reply_to` attribute when the user replied to a specific message.
