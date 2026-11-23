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

`routd/turn_control.go` owns replay safety (`classifyTurn`, `complete`) and the
instance breaker. `retry` owns the schedule and failure budget.
`store/turn_control.go` owns the durable rows.

## Decisions

**"Did a bot row land?" is the success test, not "did `submit_turn`
fire?"** A partial reply that reached the channel beats a retry that re-does
the work, so an agent that posted a `reply` and then died counts as success.
A status row never counts as that reply.

**Nine retries, ten launches in all.** The delay starts at 30 seconds, doubles, and caps at one hour. The tenth failure stops. Chat and scheduled turns use the same policy.

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

**A finished turn moves to the archive.** `turn_context` holds the live
turns: running, `pending_retry`, finished ones whose status is still owed, and
done ones whose run is still out, which a stop and the run's return still
write. The write that leaves a turn `expired`, or `done` with its run returned,
with nothing owed, deletes it there and puts it, whole, into `turn_archive` in
one batch
(`store/turn_archive.go` `writeTurn`), so crash recovery, the deferred and
owed-status passes and the expiry sweep read only live turns. Both entities are
keyed `(routd, turn id)`: a late frame, a delivery retry and the cost endpoint
find a finished turn by its id. A done turn in the archive is never bound
again; an expired one is bound afresh. A run's chats are keyed by the run in
`run_turns`, so the run-end typing clear reaches a steered turn that already
finished. A write to an archived turn lands where the turn is, and one that
leaves a status owed again moves it back live in the same batch: a failed
turn's final status is acknowledged before its run epilogue clears progress.
`Open` moves the `done` and `expired` rows owing nothing of an older
store once (`MigrateTurnArchive`, behind a `counters` marker): after a restart
no run of the old process is out.

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
with a short cause and the retry time ("Server storage is full. Retry scheduled. Retry 1 at
03:15:00 UTC."). The retry edits it ("Retry 1 is running."), a later failure
edits the cause and time, success deletes it, and exhaustion edits it to the
final cause ("Server storage is full. Automatic retries stopped."). The cause comes from a short
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
singleton (`phase` closed/open/probe, `episode`, `folder`, `next_probe_at`,
`probe_turn_id`, `probe_attempt_id`). `folder` is its scope. A failed run holds
the folder it ran in: that folder launches no new agent and every other folder
runs. A second folder that fails meanwhile waits on its own turn's retry, and
its older input keeps its newer turns back. A provider refusal holds every
folder (`folder` empty): the folders share the credential.
Runs already admitted may finish, and their success cannot close it. The
failed turn's next retry is the one probe. A failure that is never retried — the
agent replied and then exited, a time limit, a terminal fault — has no retry:
the first one holds for the first retry delay, and the folder's next turn
probes. A probe that runs without a failure closes the breaker; another failure
applies the next delay. When the probing turn exhausts its budget, the breaker
stays open for one hour. No admission waits for one named turn: once the breaker
is due, the next turn its folder admits is the probe, and the folder's input
order picks that turn. Under a breaker that holds every folder, the folder of
the probe owner goes first while that turn is due. A probe owner that is not
due, or that waits behind older input of its folder, therefore holds nobody
(BUGS `L826`). A chat the
breaker refuses is woken by the 30 s recovery tick once the breaker admits
again. Startup reads the breaker before admitting work. A provider refusal also sets the turn's due time no earlier
than the provider's reset time and records the reporting folder's
`model_pauses` row ([`6/60-model-credential-pause.md`](../6/60-model-credential-pause.md)).
An intentional stop never opens the breaker, and neither does a turn that ends with
status `budget` (the gateway refused a model call over a spent cap: one notice, no
replay). The breaker is automatic runtime
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
