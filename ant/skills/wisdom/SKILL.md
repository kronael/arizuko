---
name: wisdom
description: >
  Write or edit SKILL.md, CLAUDE.md — skill descriptions, ALWAYS/NEVER
  rules, frontmatter. USE for creating a new skill, refining an
  existing one, debugging why a skill won't trigger, writing
  ALWAYS/NEVER statements. NOT for general code (use the matching
  language skill).
user-invocable: true
---

# Wisdom

## SKILL.md anatomy (canonical)

```yaml
---
name: short-name        # kebab-case, ≤20 chars
description: >          # one-sentence what-it-does + USE/NOT triggers
  Deploy web apps to /workspace/web/pub/. USE for "build a page",
  "deploy a site", dashboards. NOT for /howto pages (use howto).
user-invocable: true    # default true — user can /slash it
disable-model-invocation: true  # optional — user-only, Claude never auto-triggers
arg: <spec>             # optional — only if takes args
---
```

`description` is the entire matching surface. arizuko's dispatch
(`resolve/SKILL.md:42-47`) reads ONLY this field — any `when_to_use:`
field is silently ignored. Anthropic spec also lists `description` as
canonical; arizuko's reading matches.

## Description shape: USE / NOT

- One factual sentence about what it does.
- `USE for <triggers / phrases / file types>.`
- `NOT for <near-miss skill> (use <other>).` — points at the
  disambiguation neighbour, prevents wrong-skill matches.
- Keyword density matters — include 2–4 literal phrases users type
  ("verify this", "build a hub", "where is X defined") plus 1–2 file
  types or contexts (`.py files`, `Cargo.toml`, "scheduled task").
- Max 1024 chars; aim for 1–3 sentences.

## user-invocable default

`user-invocable: true` on almost every skill. Set `false` ONLY for
internal-only skills invoked by other skills (e.g. `dispatch` runs
inside `resolve`; `resolve` runs on every prompt via gateway nudge).
If a human might ever type `/<skillname>`, it's `true`.

## Body rules

- Imperative voice: ALWAYS, NEVER, MUST, SHOULD
- Concrete steps with code, not prose explaining concepts
- One skill = one capability. If it does two things, split it.
- Under 200 lines; link to supporting files if larger
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
description: >
  One sentence about what it does. USE for X, Y, Z.
  NOT for <other-skill> (use <other>).
---

# My Skill

## Steps
1. Do this
2. Then this
EOF
```

Test that `/resolve` matches it for the intended task; if not, sharpen
`description` — that's the only field resolve reads.

## Common problems

| Symptom               | Fix                                         |
| --------------------- | ------------------------------------------- |
| Never triggers        | Vague description — add keywords, USE phrases |
| Triggers wrong        | Description too broad — add a NOT clause    |
| Rules ignored         | Buried in prose — convert to ALWAYS/NEVER   |
| Agent does extra work | Add explicit NEVER rules                    |
| Too long              | Split into focused skills                   |

## Debugging

```bash
ls ~/.claude/skills/
grep -E '^description:' ~/.claude/skills/*/SKILL.md
diff ~/.claude/skills/myskill/SKILL.md /workspace/self/ant/skills/myskill/SKILL.md
```

## Historical: when_to_use

Older skills carried a separate `when_to_use:` field. arizuko's
dispatch awk loop never read it; folded the content into
`description:` in v0.33.26. Don't reintroduce the field — it's
silently ignored.

## Canonical location

- Source: `/workspace/self/ant/skills/` (read-only, baked into image)
- Agent copy: `~/.claude/skills/` (seeded on first spawn; `/migrate` syncs)
