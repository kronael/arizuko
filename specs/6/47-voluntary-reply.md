---
status: shipped
---

# Keep one delivery decision and no prose fallback

A voluntary reply reaches the user through a delivery tool; printed prose stays
in the harness transcript.

**Shipped decision:** keep the implemented addressed-delivery check, one nudge
and explicit delivery tools. Do not add a prose parser, another silence tool or
a success-rate guarantee. Local tests prove the mechanism. They do not prove
fleet acceptance or a week-long outcome rate.

## The code already owns the delivery decision

`routd/loop.go` declares whether the trigger addressed the agent.
`routd/turns.go` reads bot messages and successful social-action audit rows for
the exact turn. A progress row is not a delivered answer.
`routd/mcp.go` shares one persisted nudge count between `check_turn` and
`submit_turn`. A successful addressed turn with no delivery gets one
continuation. A second missing delivery ends with a notice. An unaddressed
successful turn can end silently.

`ant/src/index.ts` resumes a nudge with the same turn ID and continuity token.
Claude and Codex Stop hooks ask the same check. Pi uses the submission check.
Fresh follow-ups wait for their own run and bound MCP socket.
These hot-tier lifecycle methods add no management resource or dashboard row.
The lasting contract belongs to [ipc](../../ipc/README.md) and
[ant](../../ant/README.md).

## Remove the tag convention at its source

The packaged solve, oracle and recall-memories skills do not need a `<think>`
wrapper: all printed prose already stays private. Remove the wrapper
instruction from those three skills. The ant README states the same tool-only
delivery contract. No renderer, wire field, grant, cache or expiry changes.

The deleted-code claims in the filing need no code change.
`ipc/README.md` already describes explicit delivery.
`ant/README.md` already points to the shared grammar test:
`tests/contract_ant_test.go`, `TestStatusBlockGrammarMatchesAcrossLanguages`.

## More enforcement would duplicate existing state

A second silence detector would replace a decision based on recorded turn
facts with another interpretation of model prose. A delivery counter inside
a Stop hook would duplicate the store's delivery evidence.
Neither changes the contract's remaining uncertainty: whether a model uses
the tool after receiving a nudge.

Estimated cost of another parser or hook counter: **1–2 engineering days**,
plus continuing review for every delivery verb and harness. Keep the existing
mechanism. The instruction correction takes less than one engineering day.
A weekly rate measurement costs **seven elapsed days**, plus operator review;
it does not need another daemon or table.

## Local verification is separate from owner acceptance

`routd/voluntary_reply_test.go` covers addressed and ambient decisions, one
shared concurrent nudge, successful social delivery, exact socket binding,
holding before recording, and completion outcomes.
`routd/voluntary_main_test.go` runs the real ant entrypoint and MCP client
with a controlled harness. It proves continuation and follow-up identity.
`routd/loop_test.go`, `TestResolve_Addressed`, covers trigger classification.
`ant/src/index.test.ts` covers private prose and same-turn continuation.
`ant/src/reply-owed.test.ts` covers the Stop check and both hook renderers.
The Go/TypeScript payload and status-grammar tests compare both sources.

These checks do not call a live model. A Codex MCP configuration assertion is
not proof that a live Codex turn calls `reply`.

The following owner acceptance remains unmeasured by this closeout:

- On sloth, an unaddressed GitHub comment stays silent, an addressed comment
  receives a reply, and an instruction to print an answer causes one correction.
- A disposable Codex smoke turn reaches `reply` through the MCP configuration.
- One week of `arizuko_nudged_turns_total` measures delivered and silent outcomes.
  No claim of fewer than five percent silent outcomes follows from local tests.

The packaged instruction correction requires a future agent-image rebuild to
reach installations. Image creation, deployment, paid model runs and live data
changes are outside this task. This decision closes additional mechanism work;
it does not mark those rollout checks complete or remove their cost.
