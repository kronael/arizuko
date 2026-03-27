# 005 — whisper language configuration

Added `.whisper-language` group config file support to SKILL.md.

## What changed

- `container/skills/self/SKILL.md` has a new "Group configuration files"
  section documenting `.whisper-language`: one ISO-639-1 code per line,
  gateway runs one forced transcription pass per language in addition to
  auto-detect.

## Agent action required

The updated SKILL.md will be seeded to `~/.claude/skills/self/SKILL.md`
on next `/migrate` run. No other changes needed.
