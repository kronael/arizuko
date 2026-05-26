# Output styles per surface

Per-channel + per-surface tone, length, and formatting hints loaded into
the agent's `outputStyle` at session bind. Spec:
[`specs/5/Y-output-styles-per-surface.md`](../../specs/5/Y-output-styles-per-surface.md).

## Naming convention

`<channel>-<surface>.md` — e.g. `slack-channel.md`, `discord-dm.md`,
`telegram-group.md`. Channel-only fallback `<channel>.md` (e.g.
`web.md`) when the channel has no per-surface split.

| Channel  | Surfaces                                  |
| -------- | ----------------------------------------- |
| slack    | dm, channel, thread, pane                 |
| discord  | dm, channel                               |
| telegram | dm, group                                 |
| web      | (no split — single `web.md`)              |
| email    | (no split — single `email.md`)            |

## Selection algorithm

`container/runner.go::pickOutputStyle` resolves at every session bind:

1. `channel` = inbound platform (`slack`, `discord`, `telegram`, …)
2. `surface` = `deriveSurface(channel, chat_jid, topic, paneLookup)`
   - slack DM → `dm`; group → `channel` (or `thread` if topic set; or
     `pane` if `paneLookup(channelID)` returns true)
   - discord DM → `dm`; otherwise → `channel`
   - telegram DM → `dm`; otherwise → `group`
3. File picked = `<channel>-<surface>.md` if both stat, else
   `<channel>.md` fallback. Empty channel → no `outputStyle` set.

Files are seeded into the agent container's `~/.claude/output-styles/`
at spawn (`seedOutputStyles` in `container/runner.go:669`). Claude
Code's SDK reads the file referenced by `settings.json::outputStyle`.

## Voice + length conventions (carried by each file)

Each file is a short markdown doc the agent receives once per session.
Should cover:

- **Length budget** — characters/lines/blocks the surface accepts
  cleanly (Telegram DM long, Slack channel short, etc.)
- **Tone** — formality the surface invites
- **Markdown support** — what the platform renders (Slack `mrkdwn`,
  Discord markdown, Telegram MarkdownV2, plain for email/web/RSS)
- **Special escape hatches** — when to send a file (`send_file`),
  when to spawn a thread, when to use a status reaction

## Adding a new output-style file

1. Decide if the channel needs surface splits — if yes, name files
   `<channel>-<surface>.md` per the table above; if no, single
   `<channel>.md`.
2. Update `deriveSurface` in `container/runner.go` if you introduced
   a new surface dimension (most additions don't).
3. Write the file: ≤ 30 lines, plain prose conventions, no preamble.
   Match the voice of an existing nearby file as your template.
4. No registration step — `seedOutputStyles` discovers files by
   directory listing at spawn.

## Anti-patterns

- **Don't restate the agent's persona.** PERSONA.md handles identity;
  output-style handles surface format.
- **Don't include skills or capabilities.** That's a different layer
  (per-agent skills + the inline prompt).
- **Don't bloat.** Each file is a hint, not a manifesto. Five sentences
  per topic is the ceiling.

## Files

`discord-channel.md`, `discord-dm.md`, `discord.md`, `email.md`,
`slack-channel.md`, `slack-dm.md`, `slack-pane.md`, `slack-thread.md`,
`slack.md`, `telegram-dm.md`, `telegram-group.md`, `telegram.md`,
`web.md`.
