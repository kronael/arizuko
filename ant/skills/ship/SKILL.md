---
name: ship
description: Plan and ship a feature using the ship autonomous coding agent. Use when asked to build something substantial.
user-invocable: true
---

# Ship

1. **Explore** — use the Explore agent to understand the codebase and task
2. **Plan** — write `.ship/plan-<name>.md` with concrete tasks, file paths,
   and acceptance criteria
3. **Run** — `ship .ship/plan-<name>.md`
4. **Verify** — `make build && make test`
5. **Clean** — delete completed `.ship/` artifacts

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
