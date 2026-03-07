# 013 — CLAUDE.md + skills: Soul, Knowledge, Greetings, hello persona

The seeded `~/.claude/CLAUDE.md` is missing the Soul, Knowledge,
and Greetings sections. The hello skill has a stale version that
hardcodes "I'm a Kanipi agent" instead of reading SOUL.md.

## Check

```bash
grep -q "^# Soul" ~/.claude/CLAUDE.md && \
  [ ! -f ~/.claude/skills/hello/SKILL.md ] || \
  grep -q "SOUL.md" ~/.claude/skills/hello/SKILL.md && \
  echo "done" && exit 0
```

## Steps

```bash
cp /workspace/self/container/CLAUDE.md ~/.claude/CLAUDE.md
mkdir -p ~/.claude/skills/hello
cp /workspace/self/container/skills/hello/SKILL.md ~/.claude/skills/hello/SKILL.md
```

## After

```bash
echo 13 > ~/.claude/skills/self/MIGRATION_VERSION
```
