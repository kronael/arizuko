---
status: draft
---

# specs/7/57 — the group names its handler

A handler is the program that runs a group's turns. The default is `ant`.
A handler can be a static Go program that calls no model.

## Today

Go guest init always starts `/app/entrypoint.sh`
(`kernel/guest-init/main.go:122`). That script compiles TypeScript on each
spawn and runs Node (`ant/entrypoint.sh:7`). A group cannot select another
program.

## The change

The group setting `agent` from
[8/40-groups-without-agents](../8/40-groups-without-agents.md)
§"The binding is one key in the settings document" names the handler:

- absent or `1`: `ant`
- `0`: no turn, as 8/40 says
- a name matching `[A-Za-z0-9_][A-Za-z0-9_.-]*`: `/usr/local/bin/<name>`

routd carries the name in the run request, runed puts it in the turn
environment, and guest init executes `/usr/local/bin/<name>` directly
(`kernel/guest-init/main.go:832`). A package supplies a handler as
`bin/<name>`, which runed already mounts at `/usr/local/bin/`
(`runed/mounts.go:72`). An invalid name or a missing executable fails the
turn with an error that names the handler. There is no fallback.

`ant` runs under Bun: `ant/src/index.ts` gets a `#!/usr/local/bin/bun` line,
and `/usr/local/bin/ant` links to it. `ant/entrypoint.sh` is deleted.

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
