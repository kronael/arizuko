---
status: spec
depends: [G-engagement]
relates-to: [F-topic-lineage]
---

# specs/6/D — Slack agent pane (assistant.threads.\*) full support

## What this solves

Slack's "Agents & AI Apps" feature provides a dedicated sidebar UI
for installed agent apps — distinct from regular DMs and channels.
The user clicks the AI icon, opens a split pane with the bot, and
gets a richer affordance: suggested prompts, a thread title, a
"thinking…" indicator, awareness of the workspace context the user
is viewing.

slakd today handles pane events for typing (via
`assistant.threads.setStatus`, shipped earlier this session) but
otherwise treats pane messages as plain DMs. This spec finalises
full pane support: title, suggested prompts, context-channel
awareness, and pane-shaped routing.

## What we have today

- `slakd/bot.go` detects pane interactions via
  `assistant_thread.action_token` in the inbound payload.
- `recordPane(jid, threadTS)` / `lookupPane(jid)` track active panes
  in-memory with 30-min TTL.
- `Typing()` calls `assistant.threads.setStatus` when JID is in pane
  mode; no-op for regular DMs/channels.
- Outbound `chat.postMessage` works in pane (it's a DM channel),
  but `setTitle` and `setSuggestedPrompts` are never called.

What's missing:

- No handler for `assistant_thread_started` (the pane-open event).
- No handler for `assistant_thread_context_changed` (user switches
  workspace channel while pane is open).
- No `setSuggestedPrompts` after agent reply — pane feels empty
  between turns.
- No `setTitle` on first turn — pane history shows generic.
- Pane's `context.channel_id` (which channel user is _viewing_) is
  not surfaced to the agent.

## The primitive

A **pane session** is a `(workspace_team_id, user_id, thread_ts)`
triple, represented today by the in-memory `paneSession`. Persist
it to one new table:

```sql
CREATE TABLE pane_sessions (
  team_id        TEXT NOT NULL,
  user_id        TEXT NOT NULL,
  thread_ts      TEXT NOT NULL,
  channel_id     TEXT NOT NULL,        -- the DM channel where pane lives
  context_jid    TEXT,                  -- workspace channel user is viewing
  opened_at      TEXT NOT NULL,         -- RFC3339Nano UTC
  last_status_at TEXT,                  -- last setStatus call (debounce)
  PRIMARY KEY (team_id, user_id, thread_ts)
);
```

Persistence (vs the current in-memory map) lets the pane survive
slakd restarts AND lets gateway / agent know the pane state without
asking slakd. The in-memory map becomes a read-through cache.

## Events

### `assistant_thread_started`

Payload (verbatim from research):

```json
{
  "event": {
    "type": "assistant_thread_started",
    "assistant_thread": {
      "user_id": "U…",
      "channel_id": "D…", // pane DM channel
      "thread_ts": "1729999…",
      "context": {
        "channel_id": "C…", // what user was looking at
        "team_id": "T…"
      }
    }
  }
}
```

slakd handler:

1. Insert/upsert `pane_sessions` row with all fields.
2. Synthesize an inbound `<message verb="pane_open">` into the
   normal pipeline — gateway sees the pane open as a turn trigger.
3. Set `assistant.threads.setTitle` once based on the bot's
   `ASSISTANT_NAME` ("atlas — chat with the alpha"). Optionally
   per-folder override via `PERSONA.md` frontmatter `pane_title`.
4. Set `assistant.threads.setSuggestedPrompts` to a default
   starter set sourced from `PERSONA.md` frontmatter
   `pane_prompts` (array of `{title, message}`) OR a built-in
   `["help", "what's new", "summarize"]`.

Verb `pane_open` is a real verb on the inbound — gateway's existing
match-by-verb plumbing handles routing without new primitives.

### `assistant_thread_context_changed`

User switched workspace channels while pane is open. Update
`pane_sessions.context_jid`. Do NOT synthesize a turn — context
change alone isn't a user action. Agent reads context at next
turn (see below).

## Surface hint and context

The `<surface>` hint from spec 6/G gains pane-aware values:

```
<surface>slack-pane</surface>
<pane-context jid="slack:T…/channel/C…" />     <!-- when context known -->
```

`buildAgentPrompt` consults `pane_sessions` keyed by the inbound's
jid. If pane row exists with `context_jid`, emit the
`<pane-context>` hint. Agent's prompt rule: "the user is viewing
this channel; if your reply references its content, fetch via
`get_history` first."

## Outbound — pane-aware reply

When agent replies to a pane inbound:

1. Detect pane via `pane_sessions` lookup keyed by `chat_jid` (the
   DM channel D…).
2. `chat.postMessage` with `thread_ts = pane.thread_ts` — keeps
   the reply in the pane thread.
3. After successful post: call `assistant.threads.setSuggestedPrompts`
   with 3-4 contextual follow-ups, computed by:
   - First turn (`pane_open` trigger): use `PERSONA.md` defaults.
   - Subsequent turns: use `core.LastReplySuggestedPrompts` if the
     agent set them via a new MCP `set_suggested_prompts(prompts)`
     tool. Else leave empty (preserves prior prompts).

So agent controls the suggested prompts via MCP — operator gets a
hook, defaults are sane.

## MCP tools added

- **`pane_set_prompts(prompts: [{title, message}])`** — agent
  pre-stages the prompts the user will see after the next reply
  lands. Fire-and-forget; if pane isn't active, no-op.
- **`pane_set_title(title: string)`** — explicit title override
  for the current pane.

Both wrap the existing slakd outbound surface — agent calls them
mid-turn, slakd queues the call to fire after the next outbound.

## Slack app configuration

Operator-side (api.slack.com/apps):

1. **Agents & AI Apps**: toggle ON.
2. **OAuth scopes**: ensure `assistant:write` (already required for
   `setStatus`). For `chat:write` migration timeline, also keep
   `chat:write`.
3. **Event Subscriptions** → bot events: add
   `assistant_thread_started`, `assistant_thread_context_changed`.
   Keep `message.im` for inbound pane messages.

Documented in `template/web/pub/howto/slack.html` as a "Enable AI
sidebar" section.

## Migration

```sql
-- 0057-pane-sessions.sql
CREATE TABLE pane_sessions (
  team_id        TEXT NOT NULL,
  user_id        TEXT NOT NULL,
  thread_ts      TEXT NOT NULL,
  channel_id     TEXT NOT NULL,
  context_jid    TEXT,
  opened_at      TEXT NOT NULL,
  last_status_at TEXT,
  PRIMARY KEY (team_id, user_id, thread_ts)
);
CREATE INDEX idx_pane_sessions_channel ON pane_sessions(channel_id);
```

One table, no backfill.

## Code changes

| File                                              | Change                                                                                  | LOC  |
| ------------------------------------------------- | --------------------------------------------------------------------------------------- | ---- |
| `store/migrations/0057-pane-sessions.sql`         | new                                                                                     | 12   |
| `store/pane_sessions.go`                          | new — `UpsertPane`, `GetPane(jid)`, `SetPaneContext`, `SetPaneStatusAt`                 | ~70  |
| `slakd/bot.go`                                    | `assistant_thread_started` + `_context_changed` handlers; replace in-memory map with DB | ~80  |
| `slakd/bot.go` (outbound)                         | post-reply `setSuggestedPrompts` + `setTitle` calls                                     | ~50  |
| `chanlib/chanlib.go`                              | `SendRequest.SuggestedPrompts []Prompt` field; `InboundMsg.PaneContext`                 | ~10  |
| `gateway/gateway.go`                              | surface hint emission for pane; thread `pane_context_jid` into prompt                   | ~20  |
| `ipc/ipc.go`                                      | `pane_set_prompts`, `pane_set_title` MCP tools                                          | ~60  |
| `ant/CLAUDE.md` + sample `PERSONA.md` frontmatter | `pane_title`, `pane_prompts` docs                                                       | ~15  |
| `template/web/pub/howto/slack.html`               | "Enable AI sidebar" section                                                             | ~30  |
| Tests                                             | pane open/close, context change, prompts roundtrip                                      | ~120 |

**Net: ~470 LOC.**

## Migration order

1. **Schema 0057** — table only, no behavior change.
2. **slakd: persist pane sessions to DB** — replace in-memory map
   with `pane_sessions` store. Existing `Typing → setStatus` flow
   reads from DB. Verify no regression on marinade.
3. **slakd: `assistant_thread_started` handler** — emit
   `pane_open` synthetic inbound; setTitle on creation. Verify
   pane opens correctly on Slack.
4. **slakd: outbound `setSuggestedPrompts`** — initially with
   built-in defaults. Verify suggestions appear in pane.
5. **MCP tools `pane_set_prompts` / `pane_set_title`** — agent
   gains control.
6. **Surface hint + `<pane-context>` in `buildAgentPrompt`** —
   agent learns what channel user is viewing.
7. **`assistant_thread_context_changed`** — context updates on
   navigation.
8. **PERSONA.md `pane_title` / `pane_prompts` frontmatter** —
   operator overrides.

Each phase ships and is verified on atlas (marinade) before next.

## What this is NOT

- **NOT a forced pane mode** — operators can still use regular DM
  to the bot; pane is opt-in by user clicking the sidebar.
- **NOT a multi-pane manager** — one pane per (user, app, thread).
  Slack creates a new `thread_ts` when user "starts new chat" in
  the pane; old pane stays in history with its row.
- **NOT cross-platform** — Discord and others get nothing from
  this spec. Spec 6/G's `<surface>` hint already covers their
  scope.
- **NOT mandatory rich suggestions** — `setSuggestedPrompts` is
  best-effort. Agent that doesn't call `pane_set_prompts` just
  leaves the bottom of the pane bare. Cleaner than auto-generated
  filler.

## Risks

- **Rate limits**: `assistant.threads.*` are Tier 2 (20+/min, 600
  total). With per-turn `setStatus` + `setSuggestedPrompts`, busy
  panes could hit the cap. `pane_sessions.last_status_at` lets the
  adapter debounce `setStatus` to one call per ~2s.
- **Scope migration**: Slack will eventually move `assistant.threads.*`
  to require `chat:write` only (today: `assistant:write`). Keep
  both scopes in the manifest for now.
- **TTL collision with engagement (spec 6/G)**: pane sessions don't
  TTL out the way engagements do; the pane is "open until closed"
  from Slack's perspective. Keep them independent — pane lives in
  `pane_sessions`, engagement in `chat_reply_state`.

## Open questions

- **Should the `pane_open` synthetic inbound carry a content body?**
  Spec says no — pane open is "user just entered", they haven't
  spoken yet. The setTitle + setSuggestedPrompts are the actions.
  Agent doesn't run on `pane_open` itself by default; only when
  user sends a first message.
- **Should agent's reply in pane mirror to context_jid?** No.
  Pane is the agent's space; if user wants to share, they copy out.
- **Operator override for default `pane_prompts` per folder?**
  PERSONA.md frontmatter. Documented.
