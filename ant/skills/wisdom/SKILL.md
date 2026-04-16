---
name: wisdom
description: >
  Writing and improving SKILL.md and CLAUDE.md files. Use when creating
  new skills, refining existing ones, or editing agent instructions.
---

# Wisdom

How to write, improve, and debug skills and CLAUDE.md.

## SKILL.md anatomy

```yaml
---
name: short-name        # kebab-case, ≤20 chars
description: >          # 1-2 sentences: WHEN to activate, WHAT it does
  Deploy web apps by writing files to pub/. Use when asked to
  build, create, or deploy a web app or page.
---
```

- `description` is the trigger — semantic matching activates skills
- Bad: "general utilities" / "helps with stuff"
- Good: "Use when asked to build X" / "Use for Y errors"

## Skill body rules

- Imperative statements: ALWAYS, NEVER, MUST, SHOULD
- Concrete steps with code blocks — not prose explaining concepts
- Under 200 lines; link to supporting files if larger
- One skill = one capability. If it does two things, split it.

## CLAUDE.md rules

- Project-specific patterns only (skills handle reusable knowledge)
- Architecture, build commands, state machines, external systems
- Under 200 lines — overflow to `.claude/*.md` files
- Same format: statements + examples, not paragraphs

## Creating a new skill

1. Check existing skills for overlap: `ls ~/.claude/skills/`
2. Write SKILL.md with frontmatter + body
3. Test: does `/resolve` match it for the intended task?
4. If not, improve the `description` — that's what resolve reads

```bash
mkdir -p ~/.claude/skills/myskill
cat > ~/.claude/skills/myskill/SKILL.md << 'EOF'
---
name: myskill
description: >
  Specific trigger description. Use when X happens or user asks for Y.
---

# My Skill

## Steps
1. Do this
2. Then this

## Rules
- ALWAYS do X
- NEVER do Y
EOF
```

## Improving a skill

Common problems and fixes:

| Symptom | Cause | Fix |
|---------|-------|-----|
| Skill never triggers | Vague description | Add specific keywords and use-cases |
| Skill triggers wrong | Description too broad | Narrow to exact scenarios |
| Agent ignores rules | Buried in prose | Convert to ALWAYS/NEVER bullets |
| Agent does extra work | Missing NEVER rules | Add explicit "don't do X" |
| Skill too long | Scope creep | Split into focused skills |

## Debugging skills

```bash
# What skills exist?
ls ~/.claude/skills/

# What does resolve see?
cat ~/.claude/skills/*/SKILL.md | grep -A1 'description:'

# Is the skill seeded from canonical?
diff ~/.claude/skills/myskill/SKILL.md /workspace/self/ant/skills/myskill/SKILL.md
```

## Anti-patterns

- NEVER write "This skill helps you..." marketing prose
- NEVER duplicate between skills — one source of truth per topic
- NEVER put transient info in skills (use diary/memory instead)
- NEVER use vague descriptions ("general utilities", "misc helpers")
- NEVER exceed 200 lines without splitting into supporting files
- NEVER put ops procedures in CLAUDE.md — use a skill

## Canonical skill location

- Source: `/workspace/self/ant/skills/` (read-only in container)
- Agent copy: `~/.claude/skills/` (read-write, seeded once on first spawn)
- Use `/migrate` to sync canonical → agent copies across groups
