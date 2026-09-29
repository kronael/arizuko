---
status: shipped
---

# Versioning & Personas

## Versioning

- `MIGRATION_VERSION` integer per group, baked into agent image
- **One migration file per release.** File name
  `NNN-vX.Y.Z-summary.md` ties the integer to the CHANGELOG version.
  Every release ships one — including docs-only — so the auto-migrate
  trigger fires on every version. Stub body for no-skill-change
  releases is fine ("release-marker for vX.Y.Z broadcast").
- Gateway compares per-root-group `MIGRATION_VERSION` against the
  source's on every gated start (`gateway.checkMigrationVersion`);
  behind → posts `/migrate` to the root agent + notifies child groups
- `/migrate` diffs canonical vs session skills, copies changed
  skill dirs, runs numbered `.md` migration scripts in order, then
  broadcasts the latest CHANGELOG blockquote to every group
- Version lives in `MIGRATION_VERSION` file, not skill frontmatter

Rationale: tying the broadcast to skill changes alone misses
docs-only releases (the v0.33.1 web-docs polish never reached chats
because nothing bumped the integer). Tying to the CHANGELOG via a
second trigger path duplicates state. The "one migration per
release" rule keeps the trigger in one place: skill update and
announce ride the same auto-migrate hook. Global integer is not
broken, just inelegant; per-skill counters would mean parsing YAML
from every skill on spawn. Instance-specific migrations: conditional
steps inside the migration `.md`.

## Image distribution

- Single `arizuko-ant:latest` built from `ant/`, exported to a root filesystem
  tree by `make rootfs`
- Per-instance selection is by TREE, not by tag: `RUNED_ROOTFS_DIR` in `.env`
  names the exported directory runed boots (spec `6/41` §"The image and the guest
  kernel"). The tree is content-addressed, so two instances on the same release
  share one copy
- Selective upgrades: export the new tree, repoint `RUNED_ROOTFS_DIR`, restart
  one instance

## Persona files

- `ant/CLAUDE.md` seeded to `~/.claude/CLAUDE.md`
- `ant/skills/` seeded to `~/.claude/skills/`
- Group folder: `PERSONA.md`, `CLAUDE.md`, `facts/`
- `share_mount(readonly=true)` grant: downgrades the shared mount to RO
  (5/33 — a grant decision, not a tier)

Products (`../5/21-products.md`) deliver curated persona + skill bundles
per group, selected at creation.

## Voice, register, and surface — three orthogonal layers

Three layers determine how an agent sounds. They compose; each owns a
different concern.

1. **Persona owns voice** (per-agent, per-group). A group's `PERSONA.md`
   is the canonical operator-edited source for character, quirks, lore,
   and register flavor. It is retained and may override freely; atlas
   keeps his register. Voice adds flavor, not a minimum baseline.
2. **`ant/CLAUDE.md` `# Response Style` owns the universal register and
   brevity.** It defines the warm caveman baseline (~90% caveman / 10%
   warm): concrete nouns and verbs, no marketing adjectives, no
   three-noun stacks. Seeded into every agent, read every session,
   independent of persona and surface.
3. **`ant/output-styles/*.md` own per-surface length and format** (spec
   5/Y) — telegram vs email vs panel. One named style is selected at
   bind, loaded once per session, not additive or per-message.

The register baseline lives in `ant/CLAUDE.md` because putting it in
personas would scatter it, and putting it in output styles would not work
cleanly: only one output style loads at session start.
