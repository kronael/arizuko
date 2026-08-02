---
status: shipped
---

> Shipped 2026-04-22 (`90215aa`, `b3465ee`): the `<autocalls>` block is
> rendered at prompt-build time, replacing `router.ClockXml`. Registry:
> `routd/prompt.go` (`var autocalls`, `renderAutocalls`). No MCP tools
> were moved — the "candidates" the draft listed never existed as distinct
> tools; autocalls pre-empted the need.

# Autocalls — inline fact injection instead of MCP tools

**The principle:** if calling a tool and injecting the result costs fewer
tokens than serializing the tool's description, skip the tool — just
inject the fact.

MCP tool descriptions cost tokens on **every** turn, including turns where
the agent never calls them. A tool returning one line ("current time",
"instance name", "active session id") burns 100+ tokens of schema to
produce 20 tokens of output. Autocalls invert that: resolve the value at
prompt-build time and paste it into the system message. Zero schema, one
line.

An autocall has **no agent-visible schema** — the agent sees only the
resulting text. It must resolve in microseconds: no I/O, no locks, no
network (`autocallCtx` is prebuilt facts, `routd/prompt.go`).

## When to autocall vs ship an MCP tool

| Criterion                    | Autocall        | MCP tool             |
| ---------------------------- | --------------- | -------------------- |
| Result size                  | ≤ 1 line        | bounded but variable |
| Needs args                   | no              | yes                  |
| Agent decides _when_ to read | no (every turn) | yes (on demand)      |
| Side effects                 | none            | allowed              |
| Freshness                    | prompt-build    | call-time            |

Heuristic: **schema cost vs content cost.** If the tool description
(name, description, input schema, output shape) exceeds 3× the data
returned, autocall it. Most "what is X right now" queries qualify.

## Shipped set

`now`, `instance`, `folder`, `session` (truncated to 8 chars) — the four
in `routd/prompt.go`. Empty values are omitted from the block.

<!-- UNVERIFIED as of 2026-08-02: the original draft's example block also
showed `tier` and `container`. Neither is in the registry, and `tier` is
gone from the platform entirely (5/33). The 2026-05-01 planned extensions
`unread` (per-JID messages-since-cursor) and `errors` (errored-row count)
were never built. -->

## Out of scope

- Dynamic autocalls that depend on user input — that is an MCP tool with
  arguments.
- Replacing facts/diary/users injection — those are already block
  injections, not tools. This spec is about tool→autocall migration only.
