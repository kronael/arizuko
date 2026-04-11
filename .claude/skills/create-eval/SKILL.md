---
name: create-eval
description: Generate project-specific eval skill. Use when asked to create eval criteria, set up evaluation, or "create eval" for a project.
user-invocable: true
---

# Create Eval

Generate `.claude/skills/eval/SKILL.md` for the current project.

The eval skill runs periodically to read logs, verify correctness,
and accumulate observations and improvement specs over time.

## Process

1. Read CLAUDE.md, README, specs/, ARCHITECTURE.md, docs/
2. Identify what this project does and what "correct" means
3. Ask user: what does "good" look like? What does "bad" look like?
4. Ask user about known failure modes
   - Logs: check ops skill, /srv/log, /var/log, or ask
5. Write `.claude/skills/eval/SKILL.md` with:
   - Log locations and what to grep for
   - Health checks (pass/fail criteria from logs)
   - Observation and bug output pattern (see below)

## Output pattern (bake into every generated eval skill)

**Observations and bugs** → `.diary/YYYYMMDD.md`
- Append under a `## Eval` heading with timestamp
- Include: what was checked, what was found, severity (info/warn/bug)
- Never overwrite — always append

**Improvement specs** → `.ship/critique-TOPIC.md`
- Write when a pattern of bugs or degradation is found
- Refine the same file over multiple eval runs — never create duplicates
- These are inputs for the user to decide what to ship — NEVER auto-ship
- User triggers shipping explicitly (e.g. `/ship`)

**Never:**
- Auto-apply fixes
- Auto-commit or push
- Create new critique files redundantly — update existing if topic matches

## Rules

- ALWAYS read project docs before generating
- NEVER include generic criteria — derive everything from the project
- NEVER generate without asking the user first
