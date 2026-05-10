---
name: wisdom
description: Patterns for writing, structuring, and improving SKILL.md files.
when_to_use: Use when creating a new skill, refining an existing one, or debugging why a skill won't trigger.
user-invocable: true
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

`when_to_use` is the trigger — `/resolve` matches semantically on this field.
`description` is a factual one-liner. No duplication between the two.

## Body rules

- Imperative voice: ALWAYS, NEVER, MUST, SHOULD
- Concrete steps with code, not prose explaining concepts
- One skill = one capability. If it does two things, split it.
- Under 200 lines; link to supporting files if larger
- NEVER put a "When to use" section in the body — that's the `when_to_use` frontmatter
- NEVER "This skill helps you..." marketing prose
- NEVER duplicate logic between skills — call them, don't copy them
- NEVER store transient info in skills (releases, dates, in-flight work) — use diary/memory
- Minimality may be violated by examples that pinpoint a real past failure mode — those earn their lines because they re-anchor the rule to a concrete cost. Hypothetical examples don't earn lines.

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
EOF
```

Test that `/resolve` matches it for the intended task; if not, sharpen the
`description` and `when_to_use` — those are what resolve reads.

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
grep -E '(description|when_to_use):' ~/.claude/skills/*/SKILL.md
diff ~/.claude/skills/myskill/SKILL.md /workspace/self/ant/skills/myskill/SKILL.md
```

## Canonical location

- Source: `/workspace/self/ant/skills/` (read-only, baked into image)
- Agent copy: `~/.claude/skills/` (seeded on first spawn; `/migrate` syncs)
