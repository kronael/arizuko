---
name: ship
description: Plan and ship a feature using the ship autonomous coding agent (uvx ship). Use when asked to build something substantial.
---

# Ship

`uvx ship` is an autonomous coding agent. Give it a spec or plan file and it
executes the tasks, runs adversarial verification, and auto-commits.

## Workflow

1. **Explore** — understand the codebase and task
2. **Write plan** — write `.ship/plan-<name>.md` with concrete tasks, file paths, and acceptance criteria
3. **Run** — `uvx ship .ship/plan-<name>.md`
4. **Verify** — `make build && make test`
5. **Clean** — delete completed `.ship/` artifacts after shipping

## Plan format

```markdown
# Plan: <name>

## Tasks

### 1. <task title>

<what to do, which files, exact changes>

## Acceptance

- <verifiable check>
```

## Usage

```bash
uvx ship .ship/plan-<name>.md   # run a plan
uvx ship specs/N/some-spec.md   # ship a spec directly if it has concrete deliverables
uvx ship --check <file>         # validate spec only, don't execute
```

Ship reads `.ship/` for plans, state, and critiques.
Clean completed artifacts from `.ship/` after shipping.
