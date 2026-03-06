# Work — current task state

Skill-managed working state file. Inspired by brainpro's
WORKING.md but implemented as an agent skill, not gateway.

## Path

```
/workspace/group/work.md
```

Single file per group. Agent-written, agent-read.

## Purpose

Captures what the agent is currently doing. Unlike diary
(historical record) or MEMORY.md (tacit knowledge), work.md
is ephemeral — it describes the active task, blockers, next
steps. Overwritten each time, not appended.

## Skill: `/work`

Agent runs `/work` to update current task state. The skill
instructs the agent to overwrite `/workspace/group/work.md`
with a short summary:

```markdown
## Current task

Implementing IPC file sending — path translation bug.

## Blockers

- hostPath() uses APP_DIR, should use GATEWAY_ROOT

## Next

- Fix hostPath, rebuild, deploy to REDACTED
- Test with manual IPC message
```

No YAML frontmatter — plain markdown. Max ~20 lines.

## Gateway injection

On session start, if `groups/<folder>/work.md` exists,
inject as system message:

```
[work] <contents>
```

Loaded in full (no truncation — file is short by design).
Injected after diary, before conversation history.

## Triggers

1. **`/work` skill** — agent-initiated, anytime
2. **Pre-session nudge** — if work.md exists and is >24h old,
   gateway adds annotation: "work.md is stale — update or
   clear with /work"
3. **Session end** — no automatic write (agent decides)

## Relationship to other layers

| Layer     | Timeframe  | Content           |
| --------- | ---------- | ----------------- |
| work.md   | Right now  | Active task       |
| diary     | Today      | What happened     |
| episodes  | Week/month | Aggregated        |
| facts     | Permanent  | Concepts/entities |
| MEMORY.md | Persistent | Tacit knowledge   |

work.md is the most volatile — updated frequently,
often fully rewritten. Diary captures the history
that work.md cycles through.
