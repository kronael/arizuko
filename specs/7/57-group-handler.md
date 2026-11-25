---
status: partial
---

# specs/7/57 — the group names its handler

A handler is the program that runs a group's turns. The default is `ant`.
A handler can be a static Go program that calls no model.

## Today

Guest init selects `/usr/local/bin/ant` by default. The executable links to
`ant/src/index.ts` and runs under Bun. Named handler selection is implemented.

A handler that cannot start ends the turn terminally. Guest init reports exit
78 for an invalid name, a missing executable or a file it cannot execute
(`startExit`, `kernel/guest-init/main.go:931`), and its log frame names the
handler. runed reads 78 as terminal (`guestTerminalExit`, `runed/qemu.go:655`), so routd does not
retry and sends one failure notice.

`agent: 0` is refused. routd ends the turn once with a failure notice and
calls no runner (`TestInvalidGroupAgentFailsOnce`). A group without an agent
is the 8/40 draft, which waits for the owner's answers; this spec builds no
part of it.

Acceptance 2 and 3 have tests: `TestFakeHandlerRepliesAndSubmitsTheTurn`
(`routd/handler_fake_test.go`) and `TestMissingHandlerFailsTheTurnOnce`
(`runed/handler_start_test.go`). Acceptance 1 is not proven. The real-guest turn
needs KVM, a 2 GiB guest and the release rootfs. On 2026-11-12 the build host had
178 MB free disk and less than 2.5 GiB available memory, so no guest ran.

## The change

The group setting `agent` from
[8/40-groups-without-agents](../8/40-groups-without-agents.md)
§"The binding is one key in the settings document" names the handler:

- absent or `1`: `ant`
- `0`: proposed no-turn behavior under 8/40; unsupported by the current parser
- a name matching `[A-Za-z0-9_][A-Za-z0-9_.-]*`: `/usr/local/bin/<name>`

routd carries the name in the run request, runed puts it in the turn
environment, and guest init executes `/usr/local/bin/<name>` directly
(`agentCmd`, `kernel/guest-init/main.go:849`). A package supplies a handler as
`bin/<name>`, which runed already mounts at `/usr/local/bin/`
(`runed/mounts.go:74`). An invalid name or a missing executable fails the
turn with an error that names the handler. There is no fallback.

`ant` runs under Bun: `ant/src/index.ts` starts with `#!/usr/local/bin/bun`,
and `/usr/local/bin/ant` links to it.

## The handler contract

A handler gets the same stdin payload, environment, mounts and MCP socket as
`ant`. It delivers through MCP and ends the turn with `submit_turn`. Stdout is
not delivery. Grants and isolation do not change.

## The gateway refuses the model call, not the spawn

routd does not ask the gateway about spend before a spawn. Before the turn's
key is minted, `reconcileCaps` (`routd/budget.go`, called at
`routd/dispatch.go:598`) creates the folder's team when the gateway has none and
moves the team and user caps onto the operator's; it compares no spend and
refuses nothing. A gateway that does not answer the reconcile or the mint fails
the turn as a dependency outage (`ErrDependencyDown`), so a handler on an
instance with a gateway still waits for it to answer.

The gateway refuses the model call itself: LiteLLM answers `budget_exceeded`
with HTTP 422 by default and 429 when configured
(`routd/README.md` § "The model gateway"). A static
handler makes no model call, so no cap can stop it. When `ant` meets the
refusal, every backend ends the turn as a result refused as `budget`
(`isBudgetRefusal`, `ant/src/backend.ts:65`; never a provider failure), and the
chassis submits `submit_turn` status `budget`. routd ends the turn once with the
notice "Budget reached for today. Automatic retries stopped."
(`routd/turns.go:155`): no replay, no instance breaker, no model pause
(`routd/turn_budget_test.go`). Dispatch stays blind to what a handler does.

## Acceptance

1. With no `agent` value, `ant` runs under Bun in a real guest, with no shell
   script and no compile step.
2. With `agent: fake` and no model credentials, a package's static Go `fake`
   replies and calls `submit_turn` (`TestFakeHandlerRepliesAndSubmitsTheTurn`).
3. An invalid name or a missing executable fails the turn visibly, with no
   retry and no fallback (`TestMissingHandlerFailsTheTurnOnce`: guest init's
   exit 78, runed's terminal mapping, routd's one notice).

## Remaining

- Acceptance 1 in a real guest: `ant` under Bun with no shell script and no
  compile step; needs KVM, a 2 GiB guest and the release rootfs (LIVE).
