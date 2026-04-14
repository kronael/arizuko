# 056 — add web chat sections to howto + web skills

## Goal

Howto content and web skill now cover slink (web chat) and
multi-platform. Existing groups need the updated skill files.

## Check

```bash
grep -q "slink" ~/.claude/skills/web/SKILL.md 2>/dev/null && echo "done" || echo "needed"
```

If "done", skip.

## Steps

```bash
cp /workspace/self/ant/skills/web/SKILL.md ~/.claude/skills/web/SKILL.md
cp /workspace/self/ant/skills/web/template/pub/howto/CONTENT.md ~/.claude/skills/web/template/pub/howto/CONTENT.md
cp /workspace/self/ant/skills/howto/SKILL.md ~/.claude/skills/howto/SKILL.md
```

## After

```bash
echo 56 > ~/.claude/skills/self/MIGRATION_VERSION
```
