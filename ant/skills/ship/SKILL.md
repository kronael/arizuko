---
name: ship
description: Plan and ship a feature using the `ship` autonomous coding agent CLI. Falls back to in-session implementation when `ship` binary is missing.
when_to_use: Use when asked to build something substantial.
user-invocable: true
---

# Ship

Same host-tool pattern as `/oracle`: the `ship` binary is the surface,
this skill is the guide. If `ship` is absent, do the plan + implement
in-session instead of crashing the turn.

1. **Detect** — `command -v ship >/dev/null` or fall back (see below)
2. **Explore** — use the Explore agent to understand the codebase and task
3. **Plan** — write `.ship/plan-<name>.md` with concrete tasks, file paths,
   and acceptance criteria
4. **Run** — `ship .ship/plan-<name>.md`
5. **Verify** — `make build && make test`
6. **Clean** — delete completed `.ship/` artifacts

## Missing-tool fallback

```bash
if ! command -v ship >/dev/null 2>&1; then
  echo "ship CLI not in image; planning + implementing in-session"
  # Carry on with the plan, do the work yourself.
fi
```

Tell the user "ship CLI not available here, doing it in-session" and
proceed. Do NOT crash the turn.

## Plan format

```markdown
# Plan: <name>

## Tasks

### 1. <task title>
<what to do, which files, exact changes>

## Acceptance
- <verifiable check>
```

Design specs (`specs/N/*.md`) can be passed directly to ship if they already
contain concrete deliverables, file paths, and acceptance criteria. ship runs
adversarial verification rounds and auto-commits.
