---
name: soul
description: >
  DEPRECATED — superseded by `/persona` (read-only refresh of ~/PERSONA.md)
  and direct operator edit of the file. USE /persona to re-read the persona
  body. NOT for authoring — the persona file is operator-edited canonical
  truth; agents read but never write it.
user-invocable: true
disable-model-invocation: true
---

# Soul (deprecated in v0.33.25)

Renamed to **persona**. The persona file lives at `~/PERSONA.md`
(was `~/SOUL.md`). The gateway prepends a `<persona>` summary on
every turn from the frontmatter `summary:` field.

To re-read the full persona body: `/persona`.

To author or edit the persona: the operator edits `~/PERSONA.md`
directly. Agents do not write to it from any skill.

This skill stays as a soft redirect for older bookmarks. Use
`/persona` for read-only refresh.
