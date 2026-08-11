---
status: shipped
---

# Turn retry on failed completion

A turn that dies mid-execution (SIGKILL/OOM, `RUNED_RUN_TIMEOUT`) never
reaches `submit_turn` or `reply`. The user sees silence, the guard
against re-feeding a terminal turn blocks re-dispatch, and the only
recovery is a fresh @mention. Motivating incident: BUGS 2026-06-14
"deep-dive replies cut off".

Retry fires on a clean `200 {outcome:"error"}` from runed
(`routd/dispatch.go:307`). A _transport_ failure is a different case
already handled by the loop — routd doesn't know whether the run
happened, so it leaves the cursor un-advanced and the poll re-feeds
([`E-routd.md`](E-routd.md) § routd↔runed).

## Decisions

**"Did a bot row land?" is the success test, not "did `submit_turn`
fire?"** routd already tracks `TurnHasBotReply`. A partial reply that
reached the channel beats a retry that re-does the work, so an agent that
posted a `reply` and then died counts as success. No bot row at all is
the failure.

**Automatic and silent, bounded at `MAX_TURN_RETRY` (default 3)**
(`core/config.go:250`, `routd/cmd/routd/main.go:188`). From the user's
side this is one slightly slower reply; only the final failure surfaces,
as an error notice. Retry state is `turn_context.retry_count` +
`state='pending_retry'` (`routd/migrations/0013-retry-count.sql`,
`routd/db.go:911` `IncrementRetryCount`), so it survives a routd restart
and `RunningTurns` re-feeds `running` and `pending_retry` alike
(`routd/db.go:929`).

**Short fixed backoff (10s, `routd/dispatch.go:467`), no exponential
ramp.** The old container is
already dying and the new one is a fresh spawn — there is nothing to wait
for except the reaper. A long ramp would just extend the user's silence.

**Global config only** — no per-folder or per-skill override. A retry
budget is a property of the runtime, not of a tenant's content.

**The retry turn is told it is a retry.** A system note ("attempt N of M,
the previous attempt was killed before completing, be conservative with
resource usage") is injected into the prompt, because the most common
cause is OOM and the most useful response is for the agent to do less.

**What is retried**: the same input message and attachments, a fresh
container, the same conversation context. **What is not**: turns that
replied, turns the agent explicitly errored via `submit_turn`,
user-cancelled turns, and failures runed marked terminal.

**runed decides terminal-vs-retryable; the decision travels, not the
exit code.** Not every `outcome:error` is transient: container exit
125/126/127 and a failed `docker` start are configuration faults, and
a retry only spawns `MAX_TURN_RETRY` more doomed containers. The site
that produces the exit code classifies it (`container/runner.go`
`terminalExit`), the decision rides the pinned response as
`RunOutcome.Terminal` (`runed/api/v1/types.go`), and routd obeys
(`routd/dispatch.go`): no retry, and the failure notice carries runed's
error text so the user sees the real cause. The raw exit code stays
inside the daemon that owns the container — if routd classified runed's
error prose instead, that would be a second classification path built
on a format string. The zero value means retryable, so an older runed
keeps the old retry behavior. (BUGS F73)

**Retry does not extend engagement.** From the user's side the turn is
still in progress — they are waiting — so the window is naturally alive.
Three 10s delays are negligible against `ENGAGEMENT_TTL` either way.

## Related

- [`E-routd.md`](E-routd.md) § Turn lifecycle — the terminal-signal
  reconciliation this hooks into, and why stale `running` rows sweep to
  `'expired'` rather than `'done'`.
- [`P-runed.md`](P-runed.md) — container lifecycle, run TTL.
- [`G-engagement.md`](G-engagement.md) — `ENGAGEMENT_TTL`.
