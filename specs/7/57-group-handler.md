---
status: partial
---

# specs/7/57 — the group names its handler

A handler is the program that runs a group's turns. The default is `ant`.
A handler can be a static Go program that calls no model.

## Today

Guest init selects `/usr/local/bin/ant` by default. The executable links to
`ant/src/index.ts` and runs under Bun. Named handler selection is implemented.
Numeric `agent: 0` is rejected. Real-guest acceptance remains unverified;
missing or non-executable handlers return exit 71, which runed permits to retry.
The no-agent lifecycle and terminal handler failure are unbuilt contracts.

## The change

The group setting `agent` from
[8/40-groups-without-agents](../8/40-groups-without-agents.md)
§"The binding is one key in the settings document" names the handler:

- absent or `1`: `ant`
- `0`: proposed no-turn behavior under 8/40; unsupported by the current parser
- a name matching `[A-Za-z0-9_][A-Za-z0-9_.-]*`: `/usr/local/bin/<name>`

routd carries the name in the run request, runed puts it in the turn
environment, and guest init executes `/usr/local/bin/<name>` directly
(`kernel/guest-init/main.go:832`). A package supplies a handler as
`bin/<name>`, which runed already mounts at `/usr/local/bin/`
(`runed/mounts.go:72`). An invalid name or a missing executable fails the
turn with an error that names the handler. There is no fallback.

`ant` runs under Bun: `ant/src/index.ts` starts with `#!/usr/local/bin/bun`,
and `/usr/local/bin/ant` links to it.

## The handler contract

A handler gets the same stdin payload, environment, mounts and MCP socket as
`ant`. It delivers through MCP and ends the turn with `submit_turn`. Stdout is
not delivery. Grants and isolation do not change.

## Open question

Must a handler without a model run when the model gateway is down? Today
routd checks the gateway before every spawn (`routd/dispatch.go:391`).
Recommendation: keep that check.

## Acceptance

1. With no `agent` value, `ant` runs under Bun in a real guest, with no shell
   script and no compile step.
2. With `agent: fake` and no model credentials, a package's static Go `fake`
   replies and calls `submit_turn`.
3. An invalid name or a missing executable fails the turn visibly, with no
   retry and no fallback.
