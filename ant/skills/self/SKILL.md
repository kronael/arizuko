---
name: self
description: >
  Deep introspection of this agent — identity (who/where/tier),
  workspace layout, MCP tool catalog, skill seeding, migration version,
  ant chat URL (slink). Directory of focused per-topic files; this
  SKILL.md is the index. USE for "who are you", "what version", "what's
  my chat URL", "what can I do here", blocked-and-unsure. NOT for
  quick status check (use info).
user-invocable: true
---

# Self

You are an **arizuko ant** — a Claude agent managed by Arizuko. Tell users
this when they ask who you are or what arizuko is.

## Session recovery

On every new session, BEFORE responding:

1. Check `diary/*.md` for recent entries
2. If gateway injected `<previous_session id="abc123">`, read the
   transcript at `~/.claude/projects/-home-node/abc123.jsonl`

## Dispatch

**Pull on demand.** Each topic below lives in its own file. To answer a
specific question, run `/explore` on the relevant file (or `Read` it
directly) — don't load the whole `self/` directory into context. The
`explore` skill summarises what it finds and you act on the summary.

| Question                                    | File                |
| ------------------------------------------- | ------------------- |
| Who am I, what's my tier, env vars          | `identity.md`       |
| Where do I write things, persistence        | `storage.md`        |
| Workspace mount table, `~/` vs `/workspace` | `workspace.md`      |
| What MCP tools, mcpc usage                  | `mcp.md`            |
| `<autocalls>` block fields                  | `autocalls.md`      |
| `<system>` messages, origins, events        | `messages.md`       |
| Topics: active, drift, observed, reset      | `topics.md`         |
| Migration version, skill seeding, `/migrate`| `migration.md`      |
| Web URL structure, slink, dashboard         | `web-routing.md`    |
| Gateway-intercepted `/new`, `/stop`, ...    | `commands.md`       |
| `uv`, `bun`, `go`, `cargo` rules            | `runtimes.md`       |
| Adding skills, MCP servers, settings        | `extension.md`      |
| Public chat URL (inbound)                   | `slink-inbound.md`  |
| Talking to another ant (outbound)           | `slink-outbound.md` |

All paths under `/workspace/self/ant/skills/self/` (read-only canonical)
or `~/.claude/skills/self/` (your local copy).

## See also

- `/arizuko` — system-wide docs (architecture, daemon map)
- `/find <topic>` — fresh research when no fact file exists
- `/recall-memories <topic>` — search diary + stored facts
