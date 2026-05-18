---
status: shipped
depends: []
relates-to: [5/G-engagement]
---

# Per-surface output styles

## Problem

`ant/CLAUDE.md` (lines 478-511) carries a "Response size + medium"
table — sweet spot and hard cap per surface (DM, channel, thread,
Slack pane, web chat, email). Today this table is prompt-rule only:
the agent reads it, but nothing in the runtime tells the agent
_which_ row applies to the current turn. The agent guesses from the
`outputStyle` value (`slack`, `telegram`, `discord`, `email`), which
only encodes platform, not surface within the platform.

Spec `5/G-engagement.md` (lines 151-174) proposed fixing this by
emitting a `<surface>...</surface>` tag in `buildAgentPrompt`,
computed from JID shape + thread context. That introduces a _second_
mechanism for the same concern: output styles already shape output
per platform, and we'd be adding a parallel prompt tag to shape
output per surface.

We already have one renderer for "how the agent shapes output for
this turn": the output style file written into the container's
`~/.claude/settings.json`. Extend it to per-surface; drop the
`<surface>` tag.

## Mechanism

### File naming

Split each platform's output-style file by surface. Convention:
`<platform>-<surface>.md`. Concretely:

```
ant/output-styles/
  slack.md              # fallback (already shipped, v0.40.7)
  slack-dm.md           # 1:1 DM
  slack-channel.md      # top-level channel reply (hard 200ch cap)
  slack-thread.md       # threaded reply (full ceiling)
  slack-pane.md         # assistant.threads pane (spec 6/D)

  telegram.md           # fallback
  telegram-dm.md
  telegram-group.md

  discord.md            # fallback
  discord-dm.md
  discord-channel.md    # guild text channel (incl. thread)

  email.md              # one shape, no further split

  web.md                # slink chat / web widget (new — full markdown ceiling)
```

No platform-less surface keys. No nesting deeper than two segments.
Operators can override per-folder via `~/.claude/output-styles/`
(already supported by Claude Code's settings precedence).

WhatsApp, Reddit, Mastodon, Bluesky, Twitter, LinkedIn use their
platform-name style file unchanged; per-surface split deferred. The
agent image ships only the platform files that have corresponding
adapters with a non-trivial length envelope (slack, telegram,
discord, email, web). Channels without a matching file get no
`outputStyle` override — Claude Code falls back to its default.

### Picker

`container/runner.go` writes `settings["outputStyle"] = in.Channel`
today (one unconditional assignment guarded by `if in.Channel != ""`).
Replace the RHS with a call to a new resolver:

```go
if in.Channel != "" {
    settings["outputStyle"] = pickOutputStyle(
        in.Channel, in.ChatJID, in.Topic, hostStylesDir, paneLookup,
    )
}
```

Keep the `if in.Channel != ""` guard. When the picker returns `""`,
the assignment site still skips (or assigns empty — caller decides;
we skip).

`pickOutputStyle` signature:

```go
func pickOutputStyle(
    channel, chatJID, topic, hostStylesDir string,
    paneLookup func(channelID string) bool,
) string
```

Steps:

1. Derive `<surface>` from `(channel, chatJID, topic, pane)` per the
   table below. `paneLookup(channelID)` is the runner-side pane bit;
   for Slack it calls `store.GetPaneByChannel(channelID)` (the same
   call `gateway.paneHints` already uses). For all other channels it
   returns `false`. Lookup happens in the runner, not in the adapter.
2. Compose name `<platform>-<surface>`. Stat
   `<hostStylesDir>/<name>.md`. If present, return that name.
3. Fall back to `<platform>` and stat `<hostStylesDir>/<platform>.md`.
   If present, return `<platform>`.
4. Otherwise return `""` (no override).

One stat per spawn (two if the first miss). `hostStylesDir` is the
host-side path to `ant/output-styles/` resolved from `cfg.HostAppDir`.
This is intentionally NOT the in-container `~/.claude/output-styles/`
path — the runner is host-side; the agent image bakes the same files
from this directory at build (see `ant/Dockerfile`).

There is no separate Claude-Code-resolves-it path: the picker is the
single source of truth for which style name lands in settings.json.

### Surface detection

From `Input.ChatJID` shape (`specs/5/S-jid-format.md`) + `Input.Topic` +
runner-side pane lookup:

| Platform   | JID shape                 | Topic | Pane | Surface              |
| ---------- | ------------------------- | ----- | ---- | -------------------- |
| `slack`    | `slack:<ws>/dm/<id>`      | any   | no   | `dm`                 |
| `slack`    | `slack:<ws>/channel/<id>` | any   | yes  | `pane`               |
| `slack`    | `slack:<ws>/channel/<id>` | `""`  | no   | `channel`            |
| `slack`    | `slack:<ws>/channel/<id>` | `≠""` | no   | `thread`             |
| `slack`    | `slack:<ws>/group/<id>`   | any   | no   | `channel`            |
| `telegram` | `telegram:user/<id>`      | any   | no   | `dm`                 |
| `telegram` | `telegram:group/<id>`     | any   | no   | `group`              |
| `discord`  | `discord:dm/<id>`         | any   | no   | `dm`                 |
| `discord`  | `discord:<guild>/<chan>`  | any   | no   | `channel`            |
| `email`    | any                       | any   | no   | (none — single file) |
| `web`      | `web:<token>` / slink     | any   | no   | (none — single file) |

For Slack specifically, `Topic` is set by slakd to `thread_ts` when
the inbound is in a thread, else empty (`slakd/bot.go:255-258`). So
`Topic != ""` is a valid thread signal for Slack. Other platforms'
`Topic` semantics differ (arizuko-internal topic name vs platform
thread root) but none of them have a `-thread` style row, so the
difference doesn't surface in picker output.

Pane detection: `paneLookup(channelID)` is invoked only when
`channel == "slack"` and the parsed JID is `channel`-kind. The
runner parses `slack:<ws>/<kind>/<id>` inline (mirrors
`slakd/parseJID`); `<id>` is the channel ID passed to
`store.GetPaneByChannel`. No new state, no slakd change.

### Length policy lives in the file

Each `<platform>-<surface>.md` file opens with one paragraph stating
its surface's sweet spot, hard cap, and when to invoke the
long-answer pattern (write to `~/reports/`, post summary + link).

`ant/CLAUDE.md`'s "Response size + medium" section (lines 478-511)
collapses to one sentence:

> Your output style file (selected by `outputStyle` in settings.json)
> states the length rules for this surface. Follow them; invoke the
> long-answer pattern when your draft would exceed the sweet spot.

The "long-answer pattern" prose itself stays in `ant/CLAUDE.md` —
it's surface-agnostic and reused from every style file via reference.

## What this is NOT

- **NOT a new prompt tag.** No `<surface>` element in
  `buildAgentPrompt`. The output style file's content IS the signal.
- **NOT runtime length enforcement.** No truncation, no
  auto-splitting, no post-hoc trim. The agent self-caps from the
  style file's text. Same trust model as today.
- **NOT a personality override.** Personality stays in `PERSONA.md`
  (spec `4/P-personas.md`). Output style is "how to shape the output
  for this surface" — formatting dialect + length envelope.
- **NOT a route configuration.** Operators do not pick output style
  per route. Surface is derived from the turn's destination, not from
  routing rules.
- **NOT engagement-coupled.** Spec `5/G-engagement.md` covers when
  the agent fires. This spec covers how it shapes output once it has
  fired. The two are independent.

## Migration

- Existing groups have `outputStyle: "slack"` (or `"telegram"` etc.)
  cached in `~/.claude/settings.json`. The runner overwrites this on
  every spawn (the `settings["outputStyle"] = ...` assignment in
  `seedSettings` is unconditional within its `in.Channel != ""`
  guard), so old groups pick up the new style on next turn. No
  code-side data migration needed.
- The `migrate` skill's existing per-folder output-styles broadcast
  (`ant/skills/migrate/SKILL.md:209-212`) copies any
  `.claude/output-styles/*` files from the new image into each
  session, so the new per-surface files reach existing groups when
  they next run `/migrate`.
- Existing `slack.md`, `telegram.md`, `discord.md`, `email.md` stay
  as platform-default fallbacks. New per-surface files land
  alongside, populated by trimming the length section in each
  existing fallback file down to one surface.
- `ant/CLAUDE.md` lines 478-511 reduced to the one-sentence pointer
  above (the long-answer pattern paragraph stays).
- Spec `5/G-engagement.md` "Length policy per surface" section
  (lines 151-174) is struck and replaced with a one-line pointer
  to this spec.

No schema change. No prompt-builder change. Per-folder operator
overrides keep working unchanged.

## Tests

Unit (`container/runner_test.go`):

- `pickOutputStyle("slack", "slack:T1/dm/D1", "", dir, never)` → `slack-dm` (when `slack-dm.md` exists)
- `pickOutputStyle("slack", "slack:T1/channel/C1", "", dir, never)` → `slack-channel`
- `pickOutputStyle("slack", "slack:T1/channel/C1", "T123", dir, never)` → `slack-thread`
- `pickOutputStyle("slack", "slack:T1/channel/C1", "", dir, paneTrue)` → `slack-pane`
- `pickOutputStyle("telegram", "telegram:user/42", "", dir, never)` → `telegram-dm`
- `pickOutputStyle("telegram", "telegram:group/42", "", dir, never)` → `telegram-group`
- `pickOutputStyle("discord", "discord:dm/D1", "", dir, never)` → `discord-dm`
- `pickOutputStyle("discord", "discord:G1/C1", "", dir, never)` → `discord-channel`
- `pickOutputStyle("email", "", "", dir, never)` → `email`
- `pickOutputStyle("web", "", "", dir, never)` → `web`
- Fallback: `slack-channel.md` absent + `slack.md` present → `slack`
- Fallback: both absent → `""`
- Empty channel: `pickOutputStyle("", ...)` → `""` (assignment site skips)

## Code surface

| File                       | Change                                                                   | LOC  |
| -------------------------- | ------------------------------------------------------------------------ | ---- |
| `container/runner.go`      | new `pickOutputStyle` (table + stat-fallback); call from `seedSettings`  | ~70  |
| `container/runner_test.go` | unit cases above                                                         | ~110 |
| `gateway/gateway.go`       | pass `paneLookup` and `hostStylesDir` into Input/runner                  | ~10  |
| `ant/output-styles/*.md`   | add `slack-{dm,channel,thread,pane}`, `telegram-{dm,group}`,             | ~200 |
|                            | `discord-{dm,channel}`, `web.md`                                         |      |
| `ant/CLAUDE.md`            | collapse lines 478-511 to one-sentence pointer                           | ~−30 |
| `specs/5/G-engagement.md`  | strike "Length policy per surface" section, add pointer                  | ~−25 |
| `slakd/*`                  | **no change** — Input is gateway-constructed; pane lookup is runner-side | 0    |

Net: ~330 LOC, mostly new style-file content. Picker logic is one
table-driven function with two stat calls.

## Closed questions

- **Web surface split (iframe vs chat widget)**: no native iframe surface.
  `web:<token>` JIDs arrive via GET+SSE regardless of how the host page embeds
  the widget. The agent can't distinguish and there is no routing-layer split to
  drive an output-style split. `web.md` covers all web surfaces; no further split.
- **Email direction split**: no split. `email.md` is one file, minimal,
  same convention as every other channel. Length/tone judgment is the LLM's.
