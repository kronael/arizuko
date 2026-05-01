---
status: shipped
---

> Shipped 2026-04-22 (commits `90215aa`, `b3465ee`): `<autocalls>` block
> rendered at prompt build time with `now`, `instance`, `folder`, `tier`,
> `session`. Replaces `router.ClockXml`. Registry in `gateway/autocalls.go`.
> No MCP tools were moved — the "candidates" listed below didn't exist as
> distinct tools; autocalls pre-empts the need.
>
> Planned extension (2026-05-01): add `unread` (per-JID
> messages-since-cursor) and `errors` (errored-row count for the
> folder). Lets the agent see accumulated traffic and failures every
> round without an MCP call. Subsumes the deleted N-listener spec
> — agent-controlled digesting beats a system-level mode.

# Autocalls — inline fact injection instead of MCP tools

General tooling principle: **if calling the tool and injecting the
result takes fewer tokens than serializing the tool's description,
skip the tool — just inject the fact.**

MCP tool descriptions cost tokens every turn. A tool that returns a
single line ("current time", "instance name", "today's UTC date",
"Claude model", "active session ID") burns 100+ tokens of schema on
every prompt to produce 20 tokens of output. Autocalls invert the
ratio: resolve the value at prompt-build time and paste it into the
system message. Zero schema, one line.

## Shape

An autocall is a named function the gateway evaluates as part of
system-message assembly. It has no agent-visible schema — the agent
sees only the resulting text.

```
<autocalls>
now: 2026-04-19T14:46:12Z
instance: krons
folder: mayai
session: 87072486-…
tier: 2
container: arizuko-krons-mayai-1745077571
</autocalls>
```

## When to autocall vs MCP tool

| Criterion                    | Autocall        | MCP tool             |
| ---------------------------- | --------------- | -------------------- |
| Result size                  | ≤ 1 line        | bounded but variable |
| Needs args                   | no              | yes                  |
| Agent decides _when_ to read | no (every turn) | yes (on demand)      |
| Side effects                 | none            | allowed              |
| Freshness                    | prompt-build    | call-time            |

Heuristic: **schema cost vs content cost**. If tool description
(name + desc + input schema + output shape) > 3× the data returned,
autocall. Most "what is X right now" queries fall into this bucket.

## Candidates already paying the schema tax

- `get_current_time` — autocall `now`
- `get_instance_info` — autocall `instance`, `folder`, `tier`
- `get_session` — autocall `session_id`, `message_count`
- parts of `get_identity` — static parts autocall, variable parts tool

## Implementation sketch

Autocall registry lives in `ipc/autocalls.go` (or similar) alongside
tool registry. Registered autocalls are resolved by gateway during
system-message assembly, output into a `<autocalls>` XML block right
before the prompt's message list.

```go
type Autocall struct {
    Name string
    Eval func(ctx AutocallCtx) (string, error)
}
```

`AutocallCtx` carries folder, session, tier, timestamp, runtime info
— same context the tool handlers get. No async, no network — must
resolve in microseconds.

## Phase

Phase 8 — depends on having a stable MCP surface to subtract from.
Ship by:

1. Audit current tools. Flag any that qualify (result ≤ 1 line, no
   args, pure read).
2. Build autocall registry + XML block injection.
3. Move each flagged tool to autocall, delete MCP registration.
4. Measure prompt-token delta before/after.

## Why this matters

Token budget is finite. Every tool ever built accretes schema. Without
an autocall path, small facts permanently rent space in every turn's
tool list — including turns where the agent never calls them.
Autocalls give us a place to put knowledge that is _always relevant
and small_, rather than forcing everything through the tool protocol.

## Out of scope

- Dynamic autocalls that depend on user input — that's just an MCP
  tool with arguments.
- Replacing injection of facts/diary/users — those are already block
  injections, not tools; this spec is about tool→autocall migration.
