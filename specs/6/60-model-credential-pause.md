---
status: draft
---

# A provider refusal pauses the model credential

A model pause holds queued messages until their model credential can work again.

## A failed credential must not start another guest

Owner measurements, 2026-11-08: rhias/trip on krons exits 78 within about eight
seconds per message after a monthly spend limit. A disposable eval receives
107 HTTP 429 responses in ten minutes while live turns share its sloth token.

Code references below use `f14b3a7a3f646332a578fe1e5901a0242b8c96dc`.
`ant/src/index.ts:494` maps terminal failure to exit 78;
`runed/runtime.go:21` and `runed/qemu.go:654` recognize that exit.
Exit 78 also covers HTTP 400 and other request errors (`ant/src/claude.ts:367`).
`routd/dispatch.go:556` retries nonterminal failures without replies, then
marks messages errored and consumes them. New messages start new attempts.

`routd/loop.go:360` backs off outbound delivery, not model turns.
The queue counts errors by chat (`queue/queue.go:257`); runed counts guest
failures by folder (`runed/manager.go:395`). Both reset on another request.
`routd/loop.go:908` deletes errored messages and the root session when runed
reports an open breaker.
Phase 7's durable suppression uses `chats.errored`
(`store/chats.go:171`, `routd/dispatch.go:593`); it suppresses notices, not guests.

## The existing turn claim owns admission

```text
messages ──► routd turn claim (changed) ──admitted──► runed ──► guest
                    │ guard (new)                           │
                    ▼                                       │
             model_pauses (new) ◄──provider failure (new)─────┘
                    ▲
       Loop maintenance (changed) ──probe (new)──► provider
                    ▲
       dashboard / CLI ──clear (new)

chat/folder breaker counters + prune/reset (removed)
```

Production boundaries:

```yaml
- path: ant/src/model-access.ts (new)
  job: resolve the actual credential and perform a bounded provider probe
  names: [ModelAccess, ProviderFailure]
  never: hold pause state or run tools
- path: ant/src/backend.ts, ant/src/claude.ts, ant/src/codex.ts (changed)
  job: share selection and normalize provider-origin failures
  names: [parseHarness, Frame]
  never: classify assistant or tool text as a provider refusal
- path: ant/src/index.ts, ant/src/mcp.ts, routd/api/v1/types.go (changed)
  job: carry ProviderFailure through submit_turn
  names: [TurnResult]
  never: infer scope from exit 78
- path: runed/model_access.go, runed/api/v1/model_access.go (new)
  job: expose service-authenticated resolve and probe operations without a guest
  names: [ModelAccess]
  never: interpret harness JSON or persist pauses
- path: runed/secrets.go, runed/render.go, runed/mounts.go, Dockerfile (changed)
  job: package the ant helper and bind resolution to the credential delivered
  names: [Turn.Env, Turn.Stdin]
  never: add a Runtime method or widen Turn or RunResult
- path: >-
    store/model_pauses.go (new),
    store/turn_context.go, store/turn_results.go (changed)
  job: persist pauses and guard the existing turn claim and result writes
  names: [ModelPause, OpenTurnContext, RecordTurnResult]
  never: store plaintext model credentials
- path: routd/dispatch.go, routd/turns.go, routd/loop.go (changed)
  job: hold batches, publish notices and drive recovery
  names: [Loop, TurnResult]
  never: parse provider error strings
- path: >-
    routd/model_pauses.go, resreg/resources/model_pauses.go,
    dashd/model_pauses.go (new)
  job: share the pause list and clear handler across REST, MCP, CLI and dashboard
  names: [model_pauses]
  never: bypass the resource authorization gate
- path: queue/queue.go, runed/manager.go, runed/api/v1/types.go (changed)
  job: remove breaker accounting while retaining execution admission
  names: [GroupQueue, Manager, RunOutcome]
  never: own a second pause or retry policy
```

### Resolve the credential before admission

[The harness document](53-per-group-harness.md) remains opaque to Go.
Ant's `parseHarness` owns whole-document replacement (`ant/src/backend.ts:124`);
`runed/render.go:100` carries the group value.

Resolution uses the same input snapshot as the spawn: instance model keys
overlaid by the triggering user's own keys (`routd/dispatch.go:863`,
`runed/secrets.go:35`), plus direct Codex login files (`runed/mounts.go:175`).
System triggers use `service:routd` (`routd/dispatch.go:785`). The selected
adapter resolves credential precedence once; the spawn and probe consume
that selection. Unused Claude keys cannot pause a Codex turn.

Runed invokes the packaged ant helper locally, without a guest or provider
request. It returns credential ID, revision and probe descriptor, never secrets.
The ID fingerprints the provider endpoint, authentication kind and selected
credential material. Identical credentials share an ID across users and
folders within this routd instance. Rotation changes the revision/ID.
Runed rejects a changed snapshot before spawning; routd resolves it again.
Pauses cover this instance's turns, not other instances sharing the token.

LiteLLM virtual keys change per attempt (`routd/dispatch.go:485`,
`routd/gateway.go:108`), so they cannot key a pause. The shipped gateway has
one wildcard upstream credential (`template/services/litellm/litellm.yml:82`).
The gateway URL and its single configured upstream slot key this pause.
Probing uses a short-lived virtual key against that upstream. Credential
rotation inside the gateway takes effect on that probe.
Multiple upstreams require an explicit gateway-reported identity shared by all
aliases using that credential. Unknown upstream identity stays per-turn.
Virtual-key expiry, folder/team budget and end-user budget refusals are not
upstream credential failures. Existing budget ownership stays in LiteLLM.

### Only a provider-origin refusal opens a pause

The adapter emits `ProviderFailure{reason, reset_at?}` with reasons
`spend_limit`, `usage_limit`, `rate_limit`, `auth_refused`, or `unavailable`.
Routd binds it to the resolved credential in the turn context; the guest
cannot choose another credential ID.

Accepted sources are a Claude SDK `rate_limit_event` rejection that ends the
attempt (`ant/src/claude.ts:422`), or the provider's structured error response.
For CLI-only errors, the adapter accepts anchored provider error envelopes,
including `API Error: 429` with a known credential-scoped rate-limit code,
and fixture-backed spend-limit/authentication codes or exact messages.
`routd/failure_notice_test.go:47` supplies `Monthly spend limit reached`;
the classifier test also requires its provider envelope. Reset times come from its
rate-limit headers; `Retry-After` is a minimum delay, not proof of recovery.
An informational usage event does not open a pause.

An explicit invalid/expired model credential qualifies; a generic 403 does
not. A known provider-wide unavailable response qualifies; a model-specific
overload, tool's
HTTP 503, local timeout, missing session, invalid model, content rejection,
context overflow, unknown error or exit 78 alone stays per-turn.
Codex normalizes its own error events and CLI envelope
(`ant/src/codex.ts:173`); it does not reuse Claude's text matcher.
Provider failures stop adapter retries as soon as exposed.

### A pause holds messages and recovers without a turn

Current admission, `store/turn_context.go:149-152`:

```go
opened, err := s.TryWriteEntities(ctx, nil, objstore.Write{
    Op: objstore.Put, Entity: TurnContexts, Key: key, Attrs: fresh,
    Cond: objstore.Cond{Item: objstore.ItemAbsent},
})
```

Proposed admission, schematic:

```text
GuardedBatch(pause absent or cleared at observed revision,
             existing conditional turn claim with credential ID/revision)
```

The store's existing conditional batch is the one serialization point
for admission, pause creation and clearing. The rebind at
`store/turn_context.go:164` takes the same guard. A dispatch admitted before
a pause may finish; no later admission succeeds for that credential.
No pause mutex or second scheduler exists.
Unrelated credentials retain the existing folder/concurrency limits.

A pause row holds credential ID/revision, episode ID, reason, opening time,
next probe time, attempt count and sanitized evidence. A pause report records
the failed attempt and opens the row atomically. It does not finalize the
message turn through `routd/turns.go:235` or occupy its final `turn_results` key.
Run return closes the old callback surface; recovery uses the existing rebind.
Paused input and its replay state are exempt from retention and stale-turn GC.

Paused batches keep their cursor, content, session and retry count. They do
not enter `MarkMessagesErrored`, breaker pruning, `finishTurn(endFailed)` or
the busy backoff. New inbound, schedules and restart recovery use the same
guard. A later command cannot advance the cursor across held rows
(`routd/dispatch.go:79`); command completion leaves that prefix queued.
Within one chat, an earlier paused batch holds later batches in order.
Partial replies do not consume unanswered input; resumption keeps session and
delivery IDs.

The existing Loop cadence (`routd/loop.go:314`) probes one due row at a time.
Without a reset time, delays double from 30 seconds to one hour, with jitter.
A valid reset time defers the first probe until that time. Each probe has a
five-second deadline, no internal retries, no tools, no transcript and no VM.
It uses a provider check that proves the failed quota/auth path, or a minimal
generation with the same credential and failed model. Health and model-list
endpoints do not prove recovery. Success clears the pause; every other result
retains it and schedules the next check. A reset timestamp alone does not
clear it.

Probe results and manual clears condition on the observed episode/revision,
so a late success cannot clear a newer failure. Restart reads durable rows
before recovering turns. Store errors hold admission and report an operator error.

### One durable notice tells each affected chat

The first held batch for each chat queues: "Model access is paused: <reason>.
Your messages are saved. I will retry automatically." It includes the next
check time. Failed and later affected chats use the same path, including
forwarded reply destinations.

The outbound message ID is `(pause episode, destination chat)`. Its conditional
insert is the durable suppression record. Restart and repeated failures create
no second notice. Failed delivery uses outbound retry and dead-letter visibility,
with adapter deduplication. The pause stops typing while input remains queued.

The cold-tier `model_pauses` resreg resource supplies list/detail and clear,
with one annotated handler and injected authorization. Pause IDs have
instance-wide effects: read and clear require instance-operator scope on both
REST and MCP. A group member sees only the chat notice.
The dashboard shows reason, credential source label, affected chats, queued
count, opening time, next check and last check result, with "Clear pause".
The CLI exposes `arizuko model-pauses list` and `arizuko model-pauses clear <id>`
through that same resource. Clear records an audit event and resumes admission;
another provider refusal opens a new episode. Input and credentials remain intact.

## The pause removes overlapping failure controls

Delete the chat counter/reset at `queue/queue.go:11`, `queue/queue.go:36`,
`queue/queue.go:132-136` and the counter branches at `queue/queue.go:235-264`.
Keep folder exclusion, capacity and dependency-outage logging.
Delete runed's folder counter/reset at `runed/manager.go:139-152`,
`runed/manager.go:329-336` and `runed/manager.go:392-411`, plus their unused
failure-store helpers and `RunOutcome.BreakerOpen` (`runed/api/v1/types.go:107`).
Delete `routd/loop.go:905-916` and its call/metric at
`routd/dispatch.go:609-612` and `routd/dispatch.go:641`.

Provider failures leave the retry branch at `routd/dispatch.go:552-566`.
The bounded `MAX_TURN_RETRY` path remains for turn-local crashes without a
reply; terminal content/configuration errors still complete only that turn.
Fresh-session recovery remains adapter-owned. `backoffDelay` and delivery
retry tiers remain because they deliver messages, including pause notices.

The pause absorbs credential-failure notice suppression at
`routd/dispatch.go:592-605`. Ordinary turn failures use a per-turn outbound
notice ID; delete the `already` suppression branch at lines 598-600.
`chats.errored` remains an operator diagnostic flag, cleared on success,
not a second pause state. Remove the direct-send failure-notice path as its
callers move to the durable outbound writer.

## Contract

- A fake provider returns 429, then 200. The first failing turn opens one
  pause; new messages produce no further guests. One due probe clears it,
  and all held input completes in order. Removing the admission guard fails.
- A provider spend-limit envelope with exit 78 pauses the credential and
  preserves input. Exit 78 with HTTP 400 content failure, a quoted spend-limit
  message, and an unknown error do not pause. Removing origin checks fails.
- Two chats share credential A; a third uses B. Only A pauses. Cover caller
  overrides, system triggers, Claude/Codex selection, login files and rotation.
  Collapsing IDs to folder, backend or instance fails these cases.
- Restart during pause and failed notice delivery retain messages and one
  outbound notice per chat/episode. Race admission, repeated callbacks,
  manual clear and late probes; no newer episode clears and no final-result
  dedup key prevents recovery. Advancing a held cursor fails the test.
- A fake LiteLLM rotates virtual keys over one upstream. The same upstream
  stays paused; independent upstreams and team/user budget refusals do not
  share that pause. Probe traffic creates no guest and obeys its deadline.
- REST/MCP, dashboard and CLI tests prove list/clear parity, atomic audit,
  operator-only access and secret redaction. A Go/TS contract test reads both
  declarations of `ProviderFailure`; the runtime port stays unchanged.
