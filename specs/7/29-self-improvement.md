---
status: open-questions
---

# Self-Improvement

How arizuko agents observe their own behavior, identify failure patterns,
and feed improvement proposals back to operators.

---

## Problem

Agents run in isolated containers. They produce output and exit. There
is no mechanism for the system to:

- Notice when agents are confused, looping, or degrading over time
- Accumulate observations across sessions
- Surface improvement proposals to operators without auto-applying them

The operator currently discovers problems reactively (user complains,
agent stops responding, circuit breaker fires). We want proactive
detection and structured improvement proposals.

---

## What Exists Today

- `diary/` — agents can write diary entries via `/diary` skill
- `container logs` — each run written to `groups/<folder>/logs/`
- `task_run_logs` — scheduler records fire history
- `chats.errored` — marks chats that failed without output
- `/eval` skill — operator-run health check (produces `.diary/` + `.ship/`)

The eval skill is a manual operator tool. It runs when the operator
asks. It is not agent-driven.

---

## Proposed: Agent Self-Eval

An agent can run a periodic self-eval by scheduling a task (via `timed`)
that reads its own logs and diary, compares against expectations, and
writes findings.

### Mechanism

1. Agent registers a cron task: `sender=scheduler-isolated`, daily or weekly
2. The scheduled run reads:
   - Last N diary entries (`diary/*.md`)
   - Last N container logs (`logs/container-*.log`)
   - Any `.ship/critique-*.md` from past eval runs
3. Produces a structured report: what is working, what is degrading, gaps
4. Writes observations to `diary/YYYYMMDD.md` (under `## Self-Eval` heading)
5. Writes improvement proposals to `.ship/critique-<TOPIC>.md`
6. Sends a summary to the operator (via `send_message` to root group or operator JID)

No auto-applying of fixes. Operator reviews `.ship/` and decides what to ship.

---

## Open Questions

### Q1: What does "degradation" mean for an agent?

Possible signals:

- Response latency (container duration trending up)
- Output length (getting longer = more confused?)
- Session resets increasing (agents forgetting context more often)
- Repeated apologies / "I don't know" phrases in output
- User follow-up rate (user keeps asking same question)
- Tool call failure rate (MCP denials, path errors)

None of these are currently tracked. Where do they go? New table? Log aggregation?

### Q2: Cross-session memory vs eval

The agent already has `facts/`, `diary/`, session resumption. Self-eval
overlaps with this. The distinction should be:

- `diary/` = what happened (factual log)
- `facts/` = verified knowledge
- `critique/` = what should change (proposals for operator)

Is the eval skill just a structured way to write to `critique/`? Or is
there a separate evaluation loop that is more automated?

### Q3: Operator notification

When the agent finds a problem, how does it notify the operator?
Options:

- `send_message` to root group (if agent is in a child group)
- Write to a shared `critique/` dir (root group reads it)
- A dedicated "health" channel (webhook, email, Telegram)

Current `notify` library exists but is operator-driven, not agent-driven.
Agents sending to root group requires a grant rule (`send_message(jid=local:root)`).

### Q4: Cross-group visibility

An individual agent only sees its own logs. It cannot see other groups.
For system-wide self-improvement, only the root agent (or the operator eval skill)
has the full picture.

Should self-improvement be:
a) Per-agent (each improves itself, sends proposals up)
b) Root-only (root agent aggregates all logs, proposes system changes)
c) Operator-driven only (eval skill, human in the loop)

### Q5: Improvement scope

What can an agent actually propose changing?

- Its own SOUL.md / CLAUDE.md (self-configuration)
- Its skill set (request new skills from operator)
- Grant rules (request more permissions)
- Cron schedule (adjust own timing)
- Routing rules (redirect certain messages elsewhere)

Agents can modify their own group directory. They cannot touch other groups.
Root agent can modify system config. Boundary is well-defined.

### Q6: When does self-eval run?

If scheduled via `timed`, it competes with user messages for the agent's
attention. An isolated run (`context_mode = isolated`) avoids polluting
the conversation context. But it still consumes a container slot.

Alternative: eval as a post-run hook (after each container exit, if
container was long-running). But this adds latency and complexity.

---

## Non-Goals

- Automated self-modification without operator approval
- Cross-instance eval (each instance is its own operator domain)
- Replacing the `/eval` operator skill (complement, not replace)

---

## Relation to Other Specs

- `28-agent-services.md` — servd for persistent services; self-eval would
  be a scheduled task (timed), not a service
- `specs/4/10-ipc.md` — MCP tools available to agent for reading diary,
  writing files, sending messages upward
- `.claude/skills/eval/SKILL.md` — the operator-side eval; self-improvement
  is the agent-side complement
