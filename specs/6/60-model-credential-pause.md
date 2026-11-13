---
status: shipped
---

# A provider refusal pauses the instance

A provider refusal — spend limit, rate or usage limit, refused
authentication, provider outage — stops new agent launches on the whole
instance until one turn runs without a refusal. The refused turn keeps its
input and retries on the [`5/12`](../5/12-turn-retry.md) schedule; the
provider's reset time is the earliest retry. Owner decision 2026-11-09: "not
just folder, whole instance" — every folder of an instance shares the
provider credential, so a second folder's launch only meets the same refusal.

Owner measurements, 2026-11-08: rhias/trip exits 78 within eight seconds after
a spend limit; an eval receives 107 HTTP 429s in ten minutes on a shared token.

```text
ant ──ProviderFailure──► submit_turn ──► turn_control: open (instance)
                                    └──► turn_context: pending_retry, due ≥ reset
                                    └──► model_pauses row (reporting folder)
due retry = the one probe ──ok──► turn_control: closed; model_pauses cleared
                         └─refused──► next delay; exhausted → open 1 h, next probe
```

## Classification stays strict

Adapters emit `ProviderFailure{reason, reset_at?}` through `submit_turn`.
Sources are an attempt-ending SDK rejection (`ant/src/claude.ts`), structured
provider errors, or anchored CLI error envelopes with fixture-backed codes and
messages (`ant/src/codex.ts`). `Monthly spend limit reached` qualifies only
inside a provider error envelope. Informational usage events, assistant
quotations, tool errors, generic 403s and unknown errors stay per-turn. Exit 78
alone never pauses: HTTP 400 content errors also produce it. routd accepts four
reasons — `spend_limit`, `rate_limit`, `authentication`, `outage` — and
refuses any other (`routd/turn_control.go` `refuse`).

## The refused turn is the probe

`refuse` records, in one guarded batch (`store/turn_control.go`
`RecordControlledProviderFailure`): the attempt's refusal marker and cause
"Model unavailable", the turn's due time no earlier than the reset time, the
reporting folder's `model_pauses` row (reason, attempts, evidence) and the
instance breaker opened with this turn as its probe. Later callbacks from the
refused attempt are closed. The breaker, the retry schedule, the one-hour hold
after exhaustion and the status message are the shared machinery of
[`5/12`](../5/12-turn-retry.md); a provider refusal adds only the reset-time
lower bound and the folder row. A probe that completes without a refusal
closes the breaker and clears every `model_pauses` row of that episode.

The guest report is untrusted. It is bound to the reporting turn's attempt,
so it can stop launches but cannot clear another episode, and a forged report
costs the instance the retry delay, not data: input is saved, not dropped.

## One operator control

The operator-only `model_pauses` resreg resource lists each folder's row with
its queued input count and the breaker's probe and next attempt, and clears a
folder row through one annotated REST/MCP handler with injected authorization
and atomic audit. Clear removes the folder's row and preserves the breaker;
the breaker closes only through a successful probe. The dashboard
(`/dash/model-pauses/`) and `arizuko model-pauses list|clear` use the same
resource.

## Contract tests

1. Instance pause and probe recovery:
   `routd/turn_control_provider_test.go::TestTurnControlProviderInstancePause`,
   `TestTurnControlProviderSavedReplay`, `TestTurnControlProviderEpisodeFence`.
2. Reset bound, exhaustion and deadlines: `TestTurnControlProviderExhaustion`,
   `TestTurnControlProviderDeadline`, `TestTurnControlProviderEarlySubmission`.
3. Restart and transport: `TestTurnControlProviderCallbackRestart`,
   `TestTurnControlProviderTransportFailure`.
4. Operator clear: `TestTurnControlManualClear`, `routd/model_pauses_resource_test.go`,
   `dashd/model_pauses_test.go`, `cmd/arizuko/model_pauses_test.go`.
5. Provider envelopes and negative fixtures: `ant/src/provider-failure.test.ts`;
   shared declarations: `tests/contract_ant_test.go::TestProviderFailureFieldsMatchAcrossLanguages`;
   unchanged runtime port: `runed/model_pause_recovery_test.go::TestModelPauseKeepsRuntimePort`.
