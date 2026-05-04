---
status: planned
brand: rhias
---

# Product: reality agent (Rhias)

A persistent agent grounded in the user's actual life and context.
Not a task runner or a search tool — a presence that holds ongoing
threads about real-world situations: relationships, decisions, projects,
commitments. Template at `ant/examples/rhias/`.

## Value prop

Most agents respond to queries. Rhias tracks reality: what the user
is navigating, what's unresolved, what's changed. It holds context
across weeks and months, surfaces what was said before, and helps the
user stay coherent across the messy, non-linear shape of real life.

## What it does

- **Context threads**: maintains named threads in facts/ for ongoing
  real-world situations (a negotiation, a decision, a relationship arc,
  a project) — updated across sessions without the user having to
  re-explain
- **Recall on demand**: "what did I say about X last month?" — retrieves
  from diary + facts/, synthesises, surfaces
- **Reflection**: at the end of a session or on request, offers a
  structured summary of what was discussed and what's unresolved
- **Autonomous monthly sweep**: compacts diary and episodes; surfaces
  what changed, what was resolved, what's still open (via timed task)
- **Content subfolder**: a separate `rhias/content` group for
  longer-form content sessions — drafting, editing, publishing — that
  the agent manages with distinct memory from the main conversation

## Skills

| Skill            | Required | Notes                                             |
| ---------------- | -------- | ------------------------------------------------- |
| diary            | yes      |                                                   |
| facts            | yes      | named situation threads, long-term context        |
| recall-memories  | yes      |                                                   |
| compact-memories | yes      | monthly autonomous sweep                          |
| users            | yes      |                                                   |
| web              | optional | for grounding real-world context in external info |
| oracle           | optional | for longer synthesis across many threads          |

## Template folder

```
ant/examples/rhias/
  SOUL.md         — curious, non-judgmental; holds context without
                    being asked; asks clarifying questions before
                    filing anything; never gives advice unprompted;
                    references prior threads naturally
  CLAUDE.md       — open every session by scanning diary for recent
                    context; update facts/threads/ after each session;
                    monthly: run compact-memories, produce a "what
                    changed this month" summary
  skills/         — diary, facts, recall-memories, compact-memories, users
  facts/
    threads/      — one file per ongoing situation; operator seeds with
                    active contexts on first deploy
  tasks.toml      — monthly memory compaction + sweep cron
```

## Channels

Telegram (primary — personal, ongoing conversations).
WhatsApp (alternative for non-Telegram users).
A `content` subfolder for longer creative or editorial sessions.

## Depends on

- timed — for monthly autonomous sweep and memory compaction
- Subfolder routing: `rhias/content` and any other named subcontexts
  require group hierarchy configured in routing rules

## Branding note

Deployed as **Rhias**. Two-channel setup (Telegram + WhatsApp) requires
separate adapter tokens. The content subfolder is a distinct arizuko
group with its own skills and memory, routed from the operator's setup.

## Open

- "Reality agent" framing: the name and concept come from the operator's
  vision — the product shape (named threads, monthly sweep, no task
  execution) is the concrete expression of it
- Situation thread format: flat markdown vs structured YAML frontmatter
- Proactive surfacing: on session open vs on schedule vs on user request
- Distinction from personal assistant: Rhias is context-first (what's
  happening in your life?); personal assistant is capability-first
  (what can I do for you?)
