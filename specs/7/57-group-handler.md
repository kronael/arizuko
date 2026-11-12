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
(`startExit`, `kernel/guest-init/main.go:924`), and its log frame names the
handler. runed reads 78 as terminal (`runed/qemu.go:652`), so routd does not
retry and sends one failure notice.

`agent: 0` is refused. routd ends the turn once with a failure notice and
calls no runner (`TestInvalidGroupAgentFailsOnce`). A group without an agent
is the 8/40 draft, which waits for the owner's answers; this spec builds no
part of it.

Acceptance 1 is not proven. The real-guest turn needs KVM, a 2 GiB guest and
the release rootfs. On 2026-11-12 the build host had 178 MB free disk and less
than 2.5 GiB available memory, so no guest ran.

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

## The gateway check stays before every spawn

routd asks the model gateway before every spawn, and a gateway that does not
answer fails the turn as a dependency outage (`routd/dispatch.go:510`). A
handler that calls no model gets the same check. One gate for every turn keeps
dispatch blind to what a handler does. The caveat: a static handler waits for
the gateway like `ant` does. This follows the recommendation and waits for the
owner's review.

## Acceptance

1. With no `agent` value, `ant` runs under Bun in a real guest, with no shell
   script and no compile step.
2. With `agent: fake` and no model credentials, a package's static Go `fake`
   replies and calls `submit_turn`.
3. An invalid name or a missing executable fails the turn visibly, with no
   retry and no fallback.
