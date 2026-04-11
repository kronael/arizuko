---
status: draft
source: hermes-agent peel (2026-04-11), deep research pass 2
---

# specs/6/8 — self-eval via sub-query at container exit

After the main turn completes, run a second, restricted `query()` call
inside the same container to review the turn and decide whether any
findings are worth persisting. Fires every N container runs per group,
gated by a counter in the DB. No extra daemons, no background threads,
no new containers. Fits arizuko's ephemeral-container model.

## Problem

Arizuko has no reflection loop. The agent spawns for a message burst,
completes the turn, writes its response, the container exits, and
everything transient is gone. Nothing is ever written back to
`~/.claude/CLAUDE.md` or `~/.claude/skills/` unless the user explicitly
asks. Over time the agent never gets smarter about:

- User preferences it observed but didn't act on
- Workflow patterns it discovered the hard way
- Tool combinations that worked where direct ones failed
- Project context worth remembering across sessions

Hermes solves this with a **background daemon thread** that forks a
separate `AIAgent` after the user's response is delivered. That design
is incompatible with our ephemeral-container model — when the container
exits, there's nothing left to run the review. But we can do the
equivalent **inside the container**, as a second `query()` call
sequenced after the main one.

## What Hermes does — verbatim

(See `/home/onvos/app/refs/hermes-agent/run_agent.py:1923-2056`)

### Memory review prompt

```
Review the conversation above and consider saving to memory if appropriate.

Focus on:
1. Has the user revealed things about themselves — their persona, desires,
preferences, or personal details worth remembering?
2. Has the user expressed expectations about how you should behave, their work
style, or ways they want you to operate?

If something stands out, save it using the memory tool.
If nothing is worth saving, just say 'Nothing to save.' and stop.
```

### Skill review prompt

```
Review the conversation above and consider saving or updating a skill if appropriate.

Focus on: was a non-trivial approach used to complete a task that required trial
and error, or changing course due to experiential findings along the way, or did
the user expect or desire a different method or outcome?

If a relevant skill already exists, update it with what you learned.
Otherwise, create a new skill if the approach is reusable.
If nothing is worth saving, just say 'Nothing to save.' and stop.
```

### How Hermes wires it

- **Trigger**: turn counter for memory, tool-iter counter for skills.
  Defaults 10/10, configurable. Reset on direct invocation of the tool.
- **Agent**: fresh `AIAgent` fork, same system prompt and tools as
  parent, but with nudge intervals zeroed (**recursion block**) and
  `max_iterations=8`. `quiet_mode=True`.
- **Input**: full conversation history + one of the three prompts
  above as the next user message.
- **Timing**: background daemon thread, fires AFTER `final_response`
  is delivered to the user, only if the turn wasn't interrupted.
- **Output**: tool_call stream is scanned post-hoc; successful
  memory/skill writes are summarized as `💾 {actions}` to stderr and
  optionally via a gateway callback.
- **Budget**: 8 iterations hard max. Failures are debug-logged, never
  block the main turn. Resources cleaned up in a `finally`.

### System-prompt guidance on the MAIN agent

(`agent/prompt_builder.py:144-171`)

Hermes also injects guidance into the main agent's system prompt so
that it knows WHEN to save proactively. Abbreviated:

- **Memory**: "Save durable facts... user preferences, environment
  details, tool quirks... keep it compact... prioritize what reduces
  future user steering... do NOT save task progress or completed-work
  logs."
- **Skills**: "After completing a complex task (5+ tool calls), fixing
  a tricky error, or discovering a non-trivial workflow, save the
  approach as a skill... when using a skill and finding it outdated,
  patch it immediately — don't wait to be asked."

This matters: the review sub-query is the SAFETY NET. The main agent
is expected to curate proactively. Review only catches what the main
agent missed.

## Arizuko adaptation

### Architectural mismatch and the fix

| Hermes assumption                          | Arizuko reality                             | Fix                              |
| ------------------------------------------ | ------------------------------------------- | -------------------------------- |
| Long-running `AIAgent` process per session | Ephemeral container per message burst       | Run review inside same container |
| Per-session turn counter in memory         | Container doesn't persist counters          | Store counter in `groups` table  |
| Background daemon thread                   | Container would exit before thread done     | Sync sub-query after main query  |
| Fresh `AIAgent(...)` fork                  | TS Agent SDK, not a class we control        | Second `query()` call            |
| `nudge_interval=0` on review agent         | N/A                                         | No recursion possible by design  |
| `💾 {actions}` to stderr                   | Channel adapters already delivered response | Log to journal, not to user      |

### Design

**Trigger: per-group counter in the DB**

- New column `groups.eval_counter INT DEFAULT 0`
- `gateway.processGroupMessages()` increments on successful run ONLY
  (not on failure — failed runs don't count toward review pressure)
- When counter reaches `cfg.EvalInterval` (default 10), gateway sets
  a `reviewDue: true` flag on the container input JSON and resets the
  counter to 0
- New `core.Config.EvalInterval int` field, env var
  `ARIZUKO_EVAL_INTERVAL`. Default 10. Set to 0 to disable.

**Runner: second `query()` in ant/src/index.ts**

- After `MessageStream` for the main query ends and before `process.exit`:
  1. Check `input.reviewDue`. If false, exit normally.
  2. Call `runReview(conversationHistory)` from new `ant/src/review.ts`.
  3. `runReview` invokes the Agent SDK's `query()` with a restricted
     option set (see below).
  4. Wait for review to complete. Log summary via `log()`.
  5. Exit.

**Restricted review options**

```typescript
query({
  prompt: buildReviewPrompt(conversationHistory),
  options: {
    systemPrompt: SELF_EVAL_SYSTEM_PROMPT, // shorter than main's
    allowedTools: ['Read', 'Write', 'Edit', 'Glob'],
    disallowedTools: ['Bash', 'Task', ...everythingElse],
    maxTurns: 8,
    // No MCP tools — no send_message, no channel side-effects
    mcpServers: {},
  },
});
```

`SELF_EVAL_SYSTEM_PROMPT` is a ~30-line block:

- Identity: "You are running in self-eval mode for arizuko ant
  `${ASSISTANT_NAME}`. Your job is to review a conversation that just
  finished and decide if anything is worth persisting."
- Workspace layout (abbreviated — just the CLAUDE.md + skills paths)
- The review criteria (see skill content below)
- Conservative framing: "Most turns yield nothing. If nothing stands
  out, write nothing and stop."

**What the review can write**

1. `~/.claude/CLAUDE.md` — append or edit sections (user facts, env
   facts, project context). Bounded: the agent should keep CLAUDE.md
   under ~8KB and consolidate old entries when adding new ones.
2. `~/.claude/skills/<name>/SKILL.md` — new skill dirs for procedural
   patterns. Requires valid frontmatter (`name:` + `description:`).

That's it. No `send_message`, no IPC tools, no network.

**Output visibility**

- Review runs silent to the user (they already got their response).
- Log summary at INFO to gated's journal:
  `"self-eval wrote changes" jid=<...> files=["CLAUDE.md","skills/foo/SKILL.md"]`
- Optionally append a dated marker to the group's diary via `~/.diary/YYYYMMDD.md`:
  `## HH:MM self-eval: <N> updates` so operators can trace.
- If `runReview()` writes nothing, no log output.

### Files to add / touch

**New**:

- `ant/skills/self-eval/SKILL.md` (~60 LOC) — the evaluation criteria.
  NOT user-invocable (frontmatter: `user-invocable: false`). Contents
  are the adapted Hermes prompts + arizuko-specific categories.
- `ant/src/review.ts` (~120 LOC) — `runReview()` wrapper that builds
  the prompt from conversation history and calls `query()` with
  restricted options. Returns a summary struct.
- `ant/src/review.test.ts` (~80 LOC) — unit tests with a mocked
  `query()`.
- `store/migrations/NN-eval-counter.sql` — add `eval_counter` to groups.
- `specs/6/8-self-eval-skill.md` — this file.

**Touch**:

- `store/groups.go` — `GetEvalCounter(jid)`, `IncrementEvalCounter(jid)`,
  `ResetEvalCounter(jid)` (~30 LOC, mirrors existing counter patterns)
- `core/types.go` — add `ReviewDue bool` to container input struct
- `core/config.go` — add `EvalInterval int` (default 10, env
  `ARIZUKO_EVAL_INTERVAL`)
- `gateway/gateway.go` — in `processGroupMessages` after a successful
  run, bump counter; when threshold hit, set `reviewDue` on next
  container input, reset counter. ~25 LOC.
- `container/runner.go` — pass `ReviewDue` through to container input
  JSON. ~3 LOC.
- `ant/src/index.ts` — after main query's `MessageStream` end, call
  `runReview()` if `input.reviewDue`. ~15 LOC.
- `ant/skills/self/SKILL.md` — add a "Self-eval hygiene" section
  paralleling Hermes's MEMORY_GUIDANCE + SKILLS_GUIDANCE: tell the
  main agent to save proactively, treat review as the safety net.
  ~25 LOC.
- `ant/skills/self/migrations/055-self-eval.md` — migration note
- `ant/skills/self/MIGRATION_VERSION` → 55
- `CHANGELOG.md` — entry

**Estimated LOC**: ~400, split across Go (counter + config) and TS
(runner + skill). Medium complexity because it touches 4 packages, but
no new packages and no new processes.

## The self-eval skill content

`ant/skills/self-eval/SKILL.md` — adapted from Hermes's two prompts
plus arizuko extensions. Draft outline:

```markdown
---
name: self-eval
description: Review criteria for self-reflection runs. Fires automatically
  every N container runs. Not user-invocable.
user-invocable: false
---

# Self-Eval

You are reviewing a conversation that just finished. Decide whether
anything is worth persisting.

## Memory criteria (write to CLAUDE.md)

1. Did the user reveal things about themselves — persona, desires,
   preferences, personal details worth remembering next session?
2. Did the user express expectations about how you should behave —
   work style, communication preferences, tools they prefer?
3. Did you discover stable environment facts — tool quirks, project
   conventions, directory structures that will still matter later?

DO NOT save:

- Task progress or completed-work logs
- Temporary TODO state
- Details specific to this turn's conversation
- Things already in CLAUDE.md

Keep CLAUDE.md compact. Consolidate old entries when adding new ones.
Under ~8KB total.

## Skill criteria (write to ~/.claude/skills/<name>/)

1. Did you complete a non-trivial task that required trial and error,
   course correction, or chaining tools in a non-obvious way?
2. Did you fix a tricky error whose solution you'll want to remember?
3. Did you discover a workflow that would have been faster if a skill
   existed?
4. When using an existing skill, did you find it outdated, incomplete,
   or wrong? → patch it instead of creating a new one.

Every new skill needs a valid SKILL.md with `name:` and `description:`
fields, plus a body explaining when to use it and what it does.

## Conservative framing

Most turns yield nothing. If nothing stands out, stop without writing.

Say "Nothing to save" and stop when:

- The turn was routine question-answering
- You already have skills/memory covering what happened
- The insights are too specific to this moment to generalize

You are not asked to summarize the conversation. You are asked to
extract durable knowledge. If none, do nothing.

## Output

Write findings by calling Write/Edit directly. Do NOT produce
narrative output — just do the work, or say "Nothing to save" and stop.
```

## What we keep from Hermes

1. **Verbatim prompt framing**: "Review the conversation above and
   consider...", "Nothing to save", the two-category split, the
   conservative tone.
2. **Separate criteria for memory vs skills**: different questions,
   but merged into one review call (not two — we only have one counter).
3. **Hard max iteration cap**: 8.
4. **Restricted toolset**: read + write-to-specific-paths only. No
   channel sends, no bash, no MCP.
5. **System-prompt guidance on the MAIN agent**: teach it to save
   proactively. Review is the safety net, not the primary mechanism.
6. **Best-effort philosophy**: review failures are logged, never block
   the main turn.

## What we change

1. **Async background thread → sync sub-query** at container exit.
   Unavoidable given our ephemeral model.
2. **Per-session in-memory counter → per-group DB counter**. Survives
   container restarts.
3. **Separate memory/skill counters → single counter**. Simpler. If
   signal quality is low after phase 1, split later.
4. **Fresh AIAgent fork → second `query()` call**. The TS Agent SDK
   doesn't expose AIAgent internals, but `query()` is re-callable with
   different options. Same effect.
5. **`💾 actions` to user → log to journal**. Don't interrupt the
   user's conversation on a platform adapter.
6. **Config-configurable interval → env var only for v1**. Can promote
   to per-group later if needed.

## What we discard

1. **Background daemon thread infra** — we don't need it, the container
   is already long-lived enough.
2. **`quiet_mode=True` flag** — the Agent SDK doesn't have it; we just
   don't emit the review's streamed output to the channel adapter.
3. **Separate MEMORY.md + USER.md** — we only have CLAUDE.md. One file.
4. **Recursion prevention via nudge zeroing** — the gateway's counter
   is the only trigger, and `runReview()` doesn't increment it.
   Architecturally impossible.
5. **Memory/skill tool audit-log scanning** — we trust the file system
   - git history for audit.

## Open questions

1. **Should the review see only the current turn, or the last N turns?**
   Hermes passes the full session history. For arizuko, "session" is
   one container run. The review sub-query inside the same container
   already has full in-memory access to the MessageStream's history,
   so we get this for free. No extra work.

2. **Should failed turns count toward the counter?** Proposal: no.
   Only successful container runs increment the counter. Reasoning:
   failed turns produced nothing worth reviewing. This prevents
   spiraling review triggers when an agent is stuck in a loop.

3. **Should the reviewer be able to patch existing skills (via Edit)
   or only create new ones (via Write)?** Proposal: both. Hermes
   explicitly encourages patching (`don't wait to be asked`). Edit is
   in the allowed-tools list.

4. **Should we commit memory/skill changes to git automatically?**
   Proposal: no auto-commit. The existing per-turn git hook (if
   enabled) catches these. If not enabled, the operator-side git log
   still reflects changes via next-turn commits.

5. **Threshold tuning**. 10 is Hermes's default. Our message volume is
   lower per group (telegram DMs vs code sessions), so 10 might be too
   rare. Alternative: fire review on EVERY container run that had
   tool-calls ≥ 3. Lean toward starting with counter=10 and moving to
   signal-driven later if cost isn't an issue.

6. **Race with skill-guard hook (specs/6/7)**. If the reviewer tries
   to write a skill with a blocked pattern (because skill-guard is
   live), what happens? The Write tool call fails, reviewer retries
   or gives up, counter still advances. Fine. The interaction composes
   naturally.

## Verification

1. `make -C ant build` — clean TS compile with new review.ts
2. `cd ant && bun test src/review.test.ts` — unit tests with mocked query()
3. `make test` — Go tests for counter math, gateway trigger logic
4. Manual smoke on a dev instance:
   - Set `ARIZUKO_EVAL_INTERVAL=2` via env override
   - Send 2 messages with tool calls to a group
   - On the 2nd container run, verify gated log shows `reviewDue=true`
   - Verify ant container runs the review sub-query (new log lines)
   - Verify `~/.claude/CLAUDE.md` or `~/.claude/skills/` got updated
     IF the conversation had something worth capturing
   - Verify user does NOT see any extra message in the channel
5. Cost smoke:
   - On a group with heavy usage, observe token consumption for a day
     with review enabled vs disabled. Should be a ~10% increment at
     threshold=10. If it's more, something's wrong with the review
     prompt bounding the iteration count.

## Phase 2 (if signal is good)

- Split memory vs skill review into separate criteria, fire
  independently
- Per-group tunable interval (column in `groups` table)
- Operator-facing dashd view: "recent self-eval decisions per group"
- Revert log via git tag on self-eval commits

## References

- `/home/onvos/app/refs/hermes-agent/run_agent.py:1923-2056` — verbatim
  prompts, trigger logic, review agent setup
- `/home/onvos/app/refs/hermes-agent/agent/prompt_builder.py:144-171` —
  system-prompt guidance for main agent
- `specs/6/7-self-learning.md` — companion skill-guard hook (should
  ship before this spec so writes are safety-scanned)
- `~/.claude/projects/-home-onvos-app-arizuko/memory/reference_hermes-agent.md`
  — peel notes and reference index
- `.diary/20260411.md` — 22:55 first pushback, 23:10 deep research pass
