---
name: wisdom
description: Patterns for writing, structuring, and improving SKILL.md and CLAUDE.md files.
when_to_use: Use when creating a new skill, refining an existing one, or editing agent instructions.
---

# Wisdom

## SKILL.md anatomy

```yaml
---
name: short-name        # kebab-case, ≤20 chars
description: >          # one factual sentence — what this skill does
  Deploy web apps by writing files to pub/.
when_to_use: >          # trigger phrases — "Use when...", example requests
  Use when asked to build, create, or deploy a web app or page.
user-invocable: true    # true = user can /slash it; false = Claude-only
disable-model-invocation: true  # user-only; Claude never auto-triggers
---
```

`when_to_use` is the trigger — semantic matching activates skills on this field.
`description` is a factual one-liner (what the skill does). Avoid vague phrases
like "general utilities". No duplication between the two fields.

## Body rules

- Imperative statements: ALWAYS, NEVER, MUST, SHOULD
- Concrete steps with code blocks, not prose explaining concepts
- Under 200 lines; link to supporting files if larger
- One skill = one capability. If it does two things, split it.
- NEVER put a "When to use" section in the body — use when_to_use frontmatter instead.

## Creating a skill

```bash
mkdir -p ~/.claude/skills/myskill
cat > ~/.claude/skills/myskill/SKILL.md << 'EOF'
---
name: myskill
description: One factual sentence about what this skill does.
when_to_use: Use when X happens or user asks for Y.
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
cat ~/.claude/skills/*/SKILL.md | grep -E '(description|when_to_use):'
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
