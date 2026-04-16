# 012 — soul skill replaces character.json

`character.json` is removed. Agent personality is now defined by the
`soul` skill (`~/.claude/skills/soul/SKILL.md`). Group-level `SOUL.md`
in `/workspace/group/` overrides it.

If you had a custom `character.json`, convert it to a `SOUL.md` in your
group directory with equivalent voice/style instructions.
