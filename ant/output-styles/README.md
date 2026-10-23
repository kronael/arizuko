# Output styles per surface

Per-channel + per-surface tone, length, and formatting hints loaded into
the agent's `outputStyle` on every spawn. Length and format per surface live
here, in one file per surface, and nowhere else: a second mechanism for the
same concern, such as a `<surface>` tag in the prompt, would drift from this
one. The only `<surface>` tag routd emits is `<surface>slack-pane</surface>`,
and it belongs to the Slack assistant pane, not to formatting.

## Naming convention

`<channel>-<surface>.md` — e.g. `slack-channel.md`, `discord-dm.md`,
`telegram-group.md`. Channel-only fallback `<channel>.md` (e.g.
`web.md`) when the channel has no per-surface split.

The channel is the JID scheme (`whatsapp`, `discord`), never the daemon
name (`whapd`, `discd`) — a file named after the daemon ships and is
never read.

| Channel  | Surfaces                                  |
| -------- | ----------------------------------------- |
| slack    | dm, channel, thread, pane                 |
| discord  | dm, channel                               |
| telegram | dm, group                                 |
| web      | (no split — single `web.md`)              |
| email    | (no split — single `email.md`)            |
| whatsapp | (no split — single `whatsapp.md`)         |
| github   | (no split — single `github.md`)           |

GitHub renders the agent's markdown as is, but the stock size and shape of a
reply are wrong there. Every comment stays on the pull request and notifies everyone who
watches it, and a review finding belongs on its code line. `github.md` sets
the TL;DR lead, a cap for each kind of text, and the rule to do the work
before the reply. `runed/output_style_test.go` checks that the style name of
each channel has a shipped file.

## Selection algorithm

`runed/settings.go::pickOutputStyle` resolves the name on every spawn:

1. `channel` = inbound platform (`slack`, `discord`, `telegram`, …)
2. `surface` = `deriveSurface(channel, chat_jid, topic, paneLookup)`
   - slack `dm` → `dm`; `group` → `channel`; `channel` → `pane` when
     `paneLookup(channelID)` is true, else `thread` when a topic is set,
     else `channel`
   - discord DM → `dm`; otherwise → `channel`
   - telegram `user` → `dm`; `group` → `group`
3. Name = `<channel>-<surface>` when `deriveSurface` matched, else the
   bare `<channel>`. Empty channel → no `outputStyle` set.

runed calls `pickOutputStyle` with no pane lookup (`runed/settings.go:80`),
so the `pane` branch never fires and `slack-pane.md` is never picked.

The picker does no existence check — it names a style, it does not open
one. That is what makes a per-group operator override at the same path
work for free, and it is why a name with no file is silent: the reader
(`ant/src/index.ts::readOutputStyle`) swallows the ENOENT and the agent
drops to stock formatting with nothing logged.

Files are copied into the group's `~/.claude/output-styles/` on every
spawn (`container.SeedOutputStyles`), overwriting the shipped names and
leaving a group's own custom file alone. Claude Code's SDK
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
  and converts nothing else; plain for email/web/RSS)

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
2. Update `deriveSurface` in `runed/settings.go` if you introduced
   a new surface dimension (most additions don't).
3. Write the file: ≤ 30 lines, plain prose conventions, no preamble.
   Match the voice of an existing nearby file as your template.
4. No registration step — `SeedOutputStyles` discovers files by
   directory listing at spawn.

## Anti-patterns

- **Don't restate the agent's persona.** PERSONA.md handles identity;
  output-style handles surface format.
- **Don't include skills or capabilities.** That's a different layer
  (per-agent skills + the inline prompt).
- **Don't bloat.** Each file is a hint, not a manifesto. Five sentences
  per topic is the ceiling.

## Files

`discord-channel.md`, `discord-dm.md`, `discord.md`,
`email.md`, `github.md`, `slack-channel.md`, `slack-dm.md`,
`slack-pane.md`, `slack-thread.md`, `slack.md`, `telegram-dm.md`,
`telegram-group.md`, `telegram.md`, `web.md`,
`whatsapp.md`.
