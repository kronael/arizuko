---
status: shipped
---

# Turn retry on failed completion

A turn whose execution fails without an answer — a crash, a launch fault,
a provider refusal — is retried from its saved input on a fixed schedule,
and one status message in the chat says what happened. A turn that ran out
of time is not retried. Motivating incidents: BUGS 2026-06-14 "deep-dive
replies cut off"; BUGS `L794` (a full host disk met four launches 11–15 s
apart); BUGS `L771` (a one-hour timeout bought another full attempt).

`routd/turn_control.go` owns the decision (`classifyTurn`, `complete`), the
schedule (`retryAt`) and the instance breaker. `store/turn_control.go` owns
the durable rows.

## Decisions

**"Did a bot row land?" is the success test, not "did `submit_turn`
fire?"** A partial reply that reached the channel beats a retry that re-does
the work, so an agent that posted a `reply` and then died counts as success.
A status row never counts as that reply.

**Three retries on a schedule, four launches in all.** An interactive turn
waits 30 s, 2 min and 5 min after successive failures; a scheduled task's
turn waits 15 min, 45 min and 180 min, so a host fault such as a full disk
has about four hours to clear. `MAX_TURN_RETRY` lowers the budget (`-1`
disables retry); a value above 3 is capped at 3 with a startup warning.
Global config only — a retry budget is a property of the runtime, not of a
tenant's content.

**A waiting retry is durable and releases the cursor.** One guarded batch
saves the turn's input and ambient message keys, its due time
(`next_attempt_at`) and its failure cause on `turn_context`, sets
`state='pending_retry'` and advances the chat cursor past exactly that batch
(`store/turn_control.go` `DeferTurn`). The poll selects due turns and replays
the saved keys with the saved folder, topic, sender and return address
(`routd/turn_control.go` `replay`); it does not resolve current routes. New
input keeps flowing while a retry waits. Fresh input never resets a due
time. A retry does not re-elevate a `/root` turn. Deleting the group retires
its waiting turns.

**What is not retried:** turns that replied, turns the agent explicitly
errored via `submit_turn`, user-cancelled or stopped turns, a turn that ran
out of time, and failures runed marked terminal. Busy consumes no retry.

**runed decides terminal-vs-retryable; the decision travels, not the exit
code.** Container exit 125/126/127, a failed start of a misconfigured
substrate and a run timeout are terminal: the site that produces the exit
classifies it, the decision rides `RunOutcome.Terminal`
(`runed/api/v1/types.go`), and routd obeys. The zero value means retryable.
routd never classifies runed's error prose. (BUGS F73, `L771`)

**One status message per turn and chat.** The first failure posts one line
with a short cause and the retry time ("Server storage is full. Retry 1/3 at
03:15:00 UTC."). The retry edits it ("Retry 1/3 is running."), a later failure
edits the cause and time, success deletes it, and exhaustion edits it to the
final cause ("… Stopped after 3 retries."). The cause comes from a short
allowlist (`turnFailureCause`): storage full, model unavailable, time limit
reached, agent could not start, agent stopped unexpectedly, setup needs
attention, or "Agent could not complete the request". No error prose, path or
stderr reaches the chat. The message lifecycle is
[`6/46-turn-progress.md`](../6/46-turn-progress.md); a channel that cannot
edit and delete (email) gets the first and the final post only.

**The retry turn is told it is a retry.** A system note ("This is retry
attempt N of M. The previous attempt did not complete.") opens the prompt.
It does not guess the cause.

**One breaker per instance.** A failed execution — crash, launch fault,
timeout, transport outage or provider refusal — opens the `turn_control`
singleton (`phase` closed/open/probe, `episode`, `next_probe_at`,
`probe_turn_id`, `probe_attempt_id`). While it is open no new agent launches;
runs already admitted may finish, and their success cannot close it. The
failed turn's next retry is the one probe. A probe that runs without a
failure closes the breaker; another failure applies the next delay. When the
probing turn exhausts its budget, the breaker stays open for one hour; then
the oldest due retry probes, or with none the first turn admitted. A chat the
breaker refuses is woken by the 30 s recovery tick once the breaker admits
again. Startup reads the breaker before admitting work. A provider refusal also sets the turn's due time no earlier
than the provider's reset time and records the reporting folder's
`model_pauses` row ([`6/60-model-credential-pause.md`](../6/60-model-credential-pause.md)).
An intentional stop never opens the breaker. The breaker is automatic runtime
state with no operator write API.

**Agent limits.** `MAX_CONCURRENT_AGENTS` (default 5) bounds the agents that
run at once on the instance. Scheduled tasks hold at most one of those slots,
so a free slot still serves another folder's chat. One folder runs one turn
at a time.

**A folder takes its input oldest first, across its chats.** A turn waits
while the folder holds older input: a pending retry, or a user row of another
chat that resolves to the folder and is past that chat's cursor. The folder
is resolved at dispatch, so the check runs the chat's own batch resolution;
it runs on the chats whose waiting rows may sort before the turn's first row.
Each `chats` item carries that span: `input_first`, the user row appended
while none waited past the cursor, and `input_last`, the newest user row.
Every user-row append writes both, so a dispatch reads the `chats` partition
once and no other chat that holds nothing older (BUGS `L827`).

**Retry does not extend engagement.** The user is still waiting, and the
saved return address delivers the eventual answer where they asked.

## Related

- [`E-routd.md`](E-routd.md) § Turn lifecycle — the terminal-signal
  reconciliation this hooks into, and why stale `running` rows sweep to
  `'expired'` rather than `'done'`.
- [`P-runed.md`](P-runed.md) — container lifecycle, run TTL.
- [`G-engagement.md`](G-engagement.md) — `ENGAGEMENT_TTL`.
