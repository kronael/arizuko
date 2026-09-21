# Output styles per surface

Per-channel + per-surface tone, length, and formatting hints loaded into
the agent's `outputStyle` at session bind. Spec:
[`specs/5/Y-output-styles-per-surface.md`](../../specs/5/Y-output-styles-per-surface.md).

## Naming convention

`<channel>-<surface>.md` — e.g. `slack-channel.md`, `discord-dm.md`,
`telegram-group.md`. Channel-only fallback `<channel>.md` (e.g.
`web.md`) when the channel has no per-surface split.

The channel is the JID scheme (`whatsapp`, `bluesky`), never the daemon
name (`whapd`, `bskyd`) — a file named after the daemon ships and is
never read.

| Channel  | Surfaces                                  |
| -------- | ----------------------------------------- |
| slack    | dm, channel, thread, pane                 |
| discord  | dm, channel                               |
| telegram | dm, group                                 |
| web      | (no split — single `web.md`)              |
| email    | (no split — single `email.md`)            |
| whatsapp | (no split — single `whatsapp.md`)         |
| bluesky  | (no split — single `bluesky.md`)          |
| linkedin | (no split — single `linkedin.md`)         |
| twitter  | (no split — single `twitter.md`)          |
| reddit   | none — stock markdown is already correct  |
| github   | none — stock markdown is already correct  |

`reditd` and `gitd` post the agent's text verbatim into a field their
platform renders as real markdown, so a style file would add a surface to
maintain and change nothing. `container/output_style_test.go` pins that
decision; shipping a file for either flips a bool there.

## Selection algorithm

`container/runner.go::pickOutputStyle` resolves at every session bind:

1. `channel` = inbound platform (`slack`, `discord`, `telegram`, …)
2. `surface` = `deriveSurface(channel, chat_jid, topic, paneLookup)`
   - slack DM → `dm`; group → `channel` (or `thread` if topic set; or
     `pane` if `paneLookup(channelID)` returns true)
   - discord DM → `dm`; otherwise → `channel`
   - telegram DM → `dm`; otherwise → `group`
3. Name = `<channel>-<surface>` when `deriveSurface` matched, else the
   bare `<channel>`. Empty channel → no `outputStyle` set.

The picker does no existence check — it names a style, it does not open
one. That is what makes a per-group operator override at the same path
work for free, and it is why a name with no file is silent: the reader
(`ant/src/index.ts::readOutputStyle`) swallows the ENOENT and the agent
drops to stock formatting with nothing logged.

Files are seeded into the agent container's `~/.claude/output-styles/`
at spawn (`seedOutputStyles` in `container/runner.go`). Claude Code's SDK
reads the file referenced by `settings.json::outputStyle`.

## Voice + length conventions (carried by each file)

Each file is a short markdown doc the agent receives once per session.
Should cover:

- **Length budget** — characters/lines/blocks the surface accepts
  cleanly (Telegram DM long, Slack channel short, etc.)
- **Tone** — formality the surface invites
- **Markdown support** — what the agent should emit (Slack `mrkdwn`;
  Discord CommonMark; Telegram markdown `**bold**`/`` `code` ``/`#`→bold,
  which `teled.mdToHTML` converts to Telegram HTML — never emit raw HTML;
  WhatsApp markdown `**bold**`, which `whapd.mdToWa` converts to `*bold*`
  and converts nothing else; plain text for Bluesky/LinkedIn/X, which run
  no markdown renderer at all; plain for email/web/RSS)

An adapter's send path decides what the file may ask for. Read it before
writing a style — `teled.mdToHTML` and `whapd.mdToWa` each rewrite a
SUBSET of markdown, and a rule that assumes the rest is rewritten too
teaches the agent a syntax the user sees raw.
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

`bluesky.md`, `discord-channel.md`, `discord-dm.md`, `discord.md`,
`email.md`, `linkedin.md`, `slack-channel.md`, `slack-dm.md`,
`slack-pane.md`, `slack-thread.md`, `slack.md`, `telegram-dm.md`,
`telegram-group.md`, `telegram.md`, `twitter.md`, `web.md`,
`whatsapp.md`.
