---
status: discussion
relates-to: [F-topic-lineage, D-slack-agent-pane, G-engagement]
---

# specs/6/I — per-turn modality envelope (discussion)

## What this is

A discussion-status spec to nail down ONE thing: how the agent
learns, on every turn, what channel/modality/meta context it's
operating in.

Today this is scattered:

- `<topic name="X" />` — emitted always (spec 6/F). Shipped.
- `<pane-context jid="…" />` — emitted when Slack pane is active
  (spec 6/D). Shipped.
- `<surface>` hint — referenced by 6/D + 5/G, never actually
  emitted in the prompt. Not shipped.
- `<rule>` lines — emitted around observed messages. Shipped.
- `<inherited>` — was emitted; replaced by plain-cp fork in 6/F
  rev 6. Removed.

The operator's intuition: "let the bot know which channel modality
he's in and give him some meta setup with every message." That's a
single primitive (per-turn envelope) that gathers all of the above
into one canonical block instead of letting each spec invent its
own piece.

## Why this is `discussion`, not `spec`

The shape isn't obvious yet. Several open framings:

### Framing A — one block, all hints

```xml
<modality
  surface="slack-channel-thread"
  topic="#deploy"
  parent-topic=""
  engagement-ttl-left="540"
  pane-context="slack:T4/channel/C123"
  reply-mode="thread"
/>
```

Single tag the agent parses once per turn. Maximally compact.
Risk: agent has to learn one new attribute every time a feature
adds to the envelope; no progressive disclosure.

### Framing B — composable children (current direction)

```xml
<topic name="#deploy" />
<surface>slack-channel-thread</surface>
<pane-context jid="…" />
<engagement until="2026-05-16T20:30:00Z" />
```

One sibling per concern. Easier to add/remove fields; each spec
owns its own tag. Risk: prompt sprawl as more features land.

### Framing C — JSON `<context>` blob

```xml
<context>{"topic":"#deploy","surface":"slack-channel-thread",…}</context>
```

Programmatically clean. Risk: agent has to JSON-parse; XML in the
rest of the prompt mixes with JSON awkwardly.

## What scope/modality info actually needs to flow

Catalog from existing specs + plausible future needs:

| Hint                                                             | Source spec | Shipped?      | Why agent needs it                    |
| ---------------------------------------------------------------- | ----------- | ------------- | ------------------------------------- |
| `topic`                                                          | 6/F         | ✓             | scope replies; don't conflate threads |
| `parent_topic` / lineage                                         | 6/F         | metadata only | mostly invisible to agent             |
| `surface` (slack-pane / slack-channel-thread / discord-dm / etc) | 5/G + 6/D   | no            | length cap, tone, available actions   |
| `pane-context` (workspace channel user is viewing)               | 6/D         | ✓             | fetch related history before replying |
| `engagement` (ttl / state)                                       | 5/G         | no            | knows when re-mention is needed       |
| `reply-mode` (thread / top-level / new-thread)                   | 5/G         | no            | guide outbound `thread_ts` decision   |
| `surface-cap` (chars + lines)                                    | 5/Y         | no            | per-platform length budget            |
| `recent-activity` (last_inbound_at, conversation-rate)           | future      | no            | pacing decisions                      |

Open question: is this catalog exhaustive? What other meta will
the agent reasonably want over the next year?

## Why "remove for now"

Spec 5/G defined `ENGAGEMENT_TTL` before this design was firm. The
engagement-state primitive shipped (columns on `chat_reply_state`,
`BumpEngagement`, `resolveOrEngaged`). The per-turn `<engagement>`
envelope hint and the per-platform length/surface piece move here
for redesign.

## Decision criteria

Pick the framing that:

1. Lets a NEW hint land in ONE place (single touch-point in
   `buildAgentPrompt` + one rule line in ant CLAUDE.md), not
   five.
2. Survives doubling the hint count without becoming unreadable.
3. Doesn't force the agent to learn new parsing (XML it already
   gets in `<message>`, `<reply-to>`, `<observed>`).
4. Is greppable from the agent side: "show me my modality" should
   be one regex.

Framing B (composable children) probably wins by #1 + #3 unless
the hint count explodes; then A or C earn the cost.

## What this is NOT

- NOT a re-design of `<topic>` — that ships as-is per 6/F.
- NOT a re-design of `<pane-context>` — same.
- NOT a commitment to ship `<surface>` or `<engagement>` or
  per-platform length caps. Those wait for this discussion to
  settle.
- NOT a replacement for spec 5/G — engagement-state is shipped.
  This spec owns the envelope question (how the agent learns it
  per-turn); 5/G owns the engagement-state question.

## Open questions

1. Does the envelope need a version field? When we add a new hint,
   how does the agent know it's a new thing vs the agent missing
   the rule for it?
2. Should the envelope be SAME for every adapter (slack/discord/
   telegram/web) or shaped per surface? Probably same with optional
   attrs that are empty for adapters that don't support that hint.
3. Where does the envelope render in the prompt? Before sysMsgs?
   After persona? Today `<topic>` lands in `rules` section of
   `buildAgentPrompt` after `personaBlock`. Should stay there.
4. Do we want operator override per folder? E.g. "don't emit
   `<engagement>` for atlas". Probably yes via a settings file.
   But that's a v2 concern.

## Migration if we ship framing B

- New helper `gateway.buildModalityEnvelope(folder, jid, topic)`
  returns the XML block. Called once in `buildAgentPrompt`.
- Each new hint is one method on the helper.
- ant CLAUDE.md gets one paragraph documenting the envelope shape
  - a rule per hint about how to use it.
