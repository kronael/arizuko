---
status: shipped
depends: [../5/G-engagement]
relates-to: [../5/F-topic-lineage]
---

# specs/8/D — Slack agent pane (assistant.threads.\*)

## Why

Slack's "Agents & AI Apps" sidebar is a distinct surface from DMs and
channels: it carries a thread title, suggested-prompt buttons, a thinking
indicator, and awareness of which workspace channel the user is currently
looking at. slakd originally treated pane messages as plain DMs, so every one
of those affordances went unused — the pane rendered empty and generic
between turns.

The design decision worth keeping: a **pane session** is the triple
`(team_id, user_id, thread_ts)`, and it is **persisted**, not held in
memory. Persistence is what lets the pane survive a slakd restart and what
lets routd and the agent see pane state without interrogating slakd. The
in-memory map is a read-through cache, not the truth.

The second decision: the pane's `context.channel_id` — the channel the user
is _viewing_, not the one they are typing in — is surfaced to the agent as
prompt context. Without it the agent answers "what's going on here?" about
the wrong channel.

## Where it lives

- [`routd/migrations/0010-pane-sessions.sql`](../../routd/migrations/0010-pane-sessions.sql)
  — the `pane_sessions` table in the split (legacy twin:
  `store/migrations/0056-pane-sessions.sql`).
- [`store/pane_sessions.go`](../../store/pane_sessions.go) — row accessors.
- [`slakd/bot.go`](../../slakd/bot.go) — `assistant_thread_started` and
  `assistant_thread_context_changed` handlers; `setTitle` /
  `setSuggestedPrompts` calls, both fired async so a slow Slack API never
  stalls the turn.
- [`routd/prompt.go`](../../routd/prompt.go) — surfaces pane context into the
  agent prompt.
- [`ipc/ipc.go`](../../ipc/ipc.go) — the `pane_set_prompts` and
  `pane_set_title` MCP tools. Both **stage** a value on the owning adapter
  that fires after the next outbound into the pane; adapters with no pane
  semantics return `chanlib.ErrUnsupported`. That staging shape is why these
  are fire-and-forget tools rather than direct API calls.

## Not this

Pane support does not change routing or engagement rules — a pane thread is
still a JID, resolved by the same route table as any other. It adds a surface
affordance, not a new tenancy or delivery path.
