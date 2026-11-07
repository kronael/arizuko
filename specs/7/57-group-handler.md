---
status: draft
relates-to:
  [
    ../8/40-groups-without-agents,
    ../6/53-per-group-harness,
    ../8/36-publish-plan,
    ../6/41-qemu-substrate,
  ]
---

# specs/7/57 — the group names its handler

A group names the program that handles its turns. The default handler is
`ant`. A handler can be a program without a model, for example a fake that
answers every message with fixed text.

## The problem

Every turn runs the same program. Guest init, in Go, starts one constant,
`agentBin = "/app/entrypoint.sh"` (`kernel/guest-init/main.go:122`). That
bash script compiles TypeScript with `bunx tsc` on each turn and then runs
`node /tmp/dist/index.js` (`ant/entrypoint.sh`). No group fact changes the
program. A test or a deterministic bot therefore needs a model credential and
a model call.

## The handler is one executable

Guest init starts the handler executable directly. There is no shell step.

- `ant` is `bun /app/src/index.ts`. Bun runs the TypeScript source, so the
  per-turn compile and `ant/entrypoint.sh` are deleted.
- A fake is a static Go binary, for example `echo`, which replies with the
  input text.

## The setting

The group setting `agent` names the handler. It is the key that
[8/40](../8/40-groups-without-agents.md) adds; this spec widens its value.

| value          | meaning                                         |
| -------------- | ----------------------------------------------- |
| absent         | `ant`                                           |
| a package name | the handler that the installed package declares |
| `false`        | no agent; 8/40 stores the rows with no turn     |

A package declares its handler as one executable path in its `arizuko.yml`:

```yaml
handler: bin/echo
```

A name that resolves to no installed package, or to a package without a
`handler`, fails the turn visibly. It never falls back to `ant`.

## The handler contract

A handler receives what `ant` receives: the stdin frame, the environment, the
mounts and the MCP socket. It answers only through the delivery tools on that
socket. Its exit code is the turn result; runed reads only the exit code
(`runed/qemu.go` `turnResult`). The handler runs in the same guest, so
isolation, egress and grants do not change.

The harness document of [6/53](../6/53-per-group-harness.md) is an `ant`
setting. Another handler ignores it.

## Module map

```yaml
- path: store/groups.go (changed)
  job: read the `agent` value as absent, false or a package name
  never: resolve the package
- path: routd/dispatch.go (changed)
  job: copy the handler name into the run request
  names: [RunRequest.Handler]
- path: runed/api/v1/types.go (changed)
  job: carry the handler name
  names: [RunRequest.Handler]
- path: runed/render.go (changed)
  job: resolve the name against the package inventory and set ARIZUKO_HANDLER
  never: start a turn for an unresolved name
- path: kernel/guest-init/main.go (changed)
  job: exec ARIZUKO_HANDLER when set, else bun /app/src/index.ts
  names: [agentCmd]
- path: ant/entrypoint.sh (removed)
- path: compose/manifest.go (changed)
  job: accept the optional `handler` path
- path: tests/fixtures/echo (new)
  job: a static Go handler that replies with the input text
```

## Acceptance

1. A group with `agent: echo` receives the echo reply through a real guest
   turn, with no model credential.
2. A group with no `agent` value runs `ant` under bun, with no compile step.
3. A group with `agent: missing` fails its turn with an error that names the
   package.
