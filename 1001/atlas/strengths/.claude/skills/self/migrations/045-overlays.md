# Migration 045 — Template overlays

`/migrate` now applies template overlays in step d).

## TEMPLATES convention

To activate overlays for a group, create:

```
~/.claude/skills/self/TEMPLATES
```

One template name per line (e.g. `support`). Default base is always the
foundation; overlays stack on top in order. Operator writes this file;
`/migrate` applies it.

Available templates are in `/workspace/self/templates/<name>/`. Each overlay
carries only what it changes — anything absent inherits from default.

## Overlay layout

```
templates/<name>/
  SOUL.md                 replaces group root SOUL.md
  SYSTEM.md               replaces group root SYSTEM.md
  CLAUDE.md               merge: appends ## sections absent from current
  .claude/skills/         per-skill overrides (managed/disabled respected)
  .claude/output-styles/  replaces matching output style files
```

## After running /migrate

`~/.claude/skills/self/TEMPLATES.applied` is written with a timestamp after
each successful overlay sync. Re-running `/migrate` is idempotent.
