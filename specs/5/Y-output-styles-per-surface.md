---
status: shipped
depends: []
relates-to: [5/G-engagement]
---

# Per-surface output styles

## Problem

`ant/CLAUDE.md` carried a "Response size + medium" table — sweet spot and
hard cap per surface (DM, channel, thread, Slack pane, web chat, email).
It was prompt-rule only: the agent read the table but nothing told it
which row applied to the current turn. It guessed from the `outputStyle`
value (`slack`, `telegram`, …), which encodes platform, not surface
within the platform.

## Decision: extend the existing renderer, don't add a second one

We already have exactly one renderer for "how the agent shapes output
this turn" — the output-style file named in the container's
`~/.claude/settings.json`. Split that per surface. The rejected
alternative was a `<surface>…</surface>` prompt tag computed in
`buildAgentPrompt` (proposed by an earlier revision of
[`G-engagement.md`](G-engagement.md)): it would shape output per surface
in parallel with output styles already shaping it per platform — two
mechanisms, one concern, guaranteed drift.

**Naming: `<platform>-<surface>.md`, two segments, no deeper.** No
platform-less surface keys. `ant/output-styles/` ships
`slack-{dm,channel,thread,pane}`, `telegram-{dm,group}`,
`discord-{dm,channel}`, `email.md`, `web.md`, plus the bare
`<platform>.md` fallbacks. Platforms whose length envelope isn't
interesting (WhatsApp, Reddit, Mastodon, Bluesky, Twitter, LinkedIn) keep
their single platform file; a channel with no matching file gets no
`outputStyle` override and Claude Code falls back to its default.
Operators override per folder via `~/.claude/output-styles/` — Claude
Code's own settings precedence, no arizuko mechanism.

**Name selection and file loading are separate concerns.** The picker
does no host-side existence check: it derives the name, and Claude Code's
`readOutputStyle` resolves `<home>/.claude/output-styles/<name>.md` at
agent startup. That is what makes per-group operator overrides work for
free. (An earlier revision specced a host-side `stat` fallback chain
against `hostStylesDir`; it shipped without it — the image contract, "the
agent image ships every per-surface file in the table", replaces it.)

**The length policy lives in the style file, not in the platform.** Each
file opens with its surface's sweet spot, hard cap, and when to invoke
the long-answer pattern (write to `~/reports/`, post summary + link).
`ant/CLAUDE.md` reduces to a pointer at whatever file `outputStyle`
selected; the long-answer pattern itself stays there because it is
surface-agnostic.

## Picker

`container/runner.go:893` `pickOutputStyle(channel, chatJID, topic,
paneLookup)` → `deriveSurface` (`:909`), called from the settings seed at
`:830`. Returns `<channel>-<surface>` when the surface table matches,
else bare `<channel>`, else `""` when channel is empty.

Surface derivation is the table in `deriveSurface` — read it there. The
two non-obvious inputs:

- **Slack `Topic` is the thread signal.** slakd sets it to `thread_ts`
  for in-thread inbounds and leaves it empty otherwise, so `Topic != ""`
  means "thread" _for Slack_. Other platforms' `Topic` is the
  arizuko-internal topic name, a different thing — none of them has a
  `-thread` style file, so the difference never surfaces in picker
  output.
- **Pane detection is runner-side**, `paneLookup(channelID)` against
  `pane_sessions`, only for Slack `channel`-kind JIDs. No new state, no
  slakd change.

## What this is NOT

- **NOT runtime length enforcement.** No truncation, no auto-splitting,
  no post-hoc trim. The agent self-caps from the style file's text — same
  trust model as every other prompt rule.
- **NOT a personality override.** Personality is `PERSONA.md`
  (`4/P-personas.md`). Output style is formatting dialect + length
  envelope.
- **NOT route configuration.** Surface is derived from the turn's
  destination, never picked per route.
- **NOT engagement-coupled.** [`G-engagement.md`](G-engagement.md) covers
  _when_ the agent fires; this covers _how_ it shapes output once it has.

Note: `buildAgentPrompt` **does** emit one `<surface>` tag —
`<surface>slack-pane</surface>` from `routd/prompt.go:58` `paneHints`,
alongside `<pane-context>`. That is the pane-context feature, not this
spec's mechanism, and it is the only `<surface>` value ever emitted.

## Closed questions

- **Web iframe vs chat widget**: no native iframe surface — `web:` JIDs
  arrive via GET+SSE however the host page embeds the widget, and there
  is no routing-layer split to drive a style split. `web.md` covers all.
- **Email direction split**: none. One `email.md`; length and tone
  judgment is the model's.

## Open: extending the per-turn envelope

The shipped substrate is the output-style file plus `<topic>`,
`<surface>`/`<pane-context>` and `<link-context>` sibling tags. The shape
of any FUTURE per-turn hint (engagement TTL, reply-mode, recent activity)
is parked. Three framings were on the table: **A** one attribute-bearing
`<modality/>` block (compact, single parse — but the agent learns a new
attribute per feature); **B** one sibling tag per concern (each spec owns
its tag — but prompt sprawl); **C** a JSON `<context>` blob
(programmatically clean — but mixes JSON into an XML prompt).

**B is the default direction** — it is what shipped — unless the hint
count explodes, at which point A or C earn their cost. A new hint earns
inclusion when it lands in ONE place in `buildAgentPrompt` plus one rule
line in `ant/CLAUDE.md`, survives doubling the hint count without
becoming unreadable, forces no new parsing on the agent, and is greppable
from the agent side.
