---
name: wisdom
description: >
  Writing and improving SKILL.md and CLAUDE.md files. Use when creating
  new skills, refining existing ones, or editing agent instructions.
---

# Wisdom

## SKILL.md anatomy

```yaml
---
name: short-name        # kebab-case, ≤20 chars
description: >          # 1-2 sentences: WHEN to activate, WHAT it does
  Deploy web apps by writing files to pub/. Use when asked to
  build, create, or deploy a web app or page.
---
```

`description` is the trigger — semantic matching activates skills. Good
descriptions say "Use when asked to X" or "Use for Y errors". Avoid vague
phrases like "general utilities".

## Body rules

- Imperative statements: ALWAYS, NEVER, MUST, SHOULD
- Concrete steps with code blocks, not prose explaining concepts
- Under 200 lines; link to supporting files if larger
- One skill = one capability. If it does two things, split it.

## Creating a skill

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

Test that `/resolve` matches it for the intended task; if not, improve the
`description` — that's what resolve reads.

## Common problems

| Symptom               | Fix                                         |
| --------------------- | ------------------------------------------- |
| Never triggers        | Vague description — add keywords, use cases |
| Triggers wrong        | Description too broad — narrow scenarios    |
| Rules ignored         | Buried in prose — convert to ALWAYS/NEVER   |
| Agent does extra work | Add explicit NEVER rules                    |
| Too long              | Split into focused skills                   |

## Debugging

```bash
ls ~/.claude/skills/
cat ~/.claude/skills/*/SKILL.md | grep -A1 'description:'
diff ~/.claude/skills/myskill/SKILL.md /workspace/self/ant/skills/myskill/SKILL.md
```

## Anti-patterns

- NEVER "This skill helps you..." marketing prose
- NEVER duplicate between skills — one source of truth
- NEVER put transient info in skills (use diary/memory)
- NEVER exceed 200 lines without splitting into supporting files
- NEVER put ops procedures in CLAUDE.md — use a skill

## Canonical location

- Source: `/workspace/self/ant/skills/` (read-only)
- Agent copy: `~/.claude/skills/` (seeded once on first spawn)
- `/migrate` syncs canonical → agent copies
