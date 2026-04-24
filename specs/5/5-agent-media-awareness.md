---
status: shipped
---

# Agent media awareness

Agent sees `[media attached: /workspace/group/media/.../file.pdf (application/pdf)]`
but doesn't know it can `Read(path)` natively. Add a media-handling
section to `ant/CLAUDE.md` and howto skill level 1 explaining the
format for PDFs/images/voice/video.

Rationale: Claude reads PDFs/images natively; the agent just needs the
instruction. Fixes observed "I can't display this" responses.

Unblockers: write the CLAUDE.md section, update
`ant/skills/howto/SKILL.md`, add migration entry, bump
MIGRATION_VERSION.

> Shipped 2026-04-24: `ant/CLAUDE.md` "Inbound media attachments"
> section now tells the agent explicitly that PDFs/images/markdown/
> JSON/source attachments are on-disk files readable via `Read` — no
> "I can't display this" fallback. Migration `071-media-awareness.md`;
> MIGRATION_VERSION bumped 70 → 71.
