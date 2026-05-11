---
name: release
description: >
  Prepare a release. Version bump, changelog, docs alignment, git tag.
  USE for "/release", "tag a release", "bump version", "cut vX.Y.Z",
  CHANGELOG.md updates, release plumbing. NOT for a single commit ship
  (use commit + ship).
user-invocable: true
---

# Release

## Process

1. **Detect scope** — `git tag --list` + `git log` since last tag.
   - No prior tags → first release. Default `v0.1.0` (matches pyproject's
     usual default); skip the bump step if pyproject already says it.
2. **Version bump** — patch default. Discover the version file:
   - Python: `pyproject.toml` `version = "..."` (each subdir pyproject
     in a monorepo gets bumped independently)
   - Rust: `Cargo.toml` `version = "..."`
   - JS/TS: `package.json` `"version": "..."`
   - CLAUDE.md may pin which file is canonical when multiple exist;
     otherwise discover the deepest one and bump there.
3. **Changelog** — `CHANGELOG.md` at repo root.
   - File exists with `[Unreleased]` → move to `[vX.Y.Z] — YYYYMMDD`.
   - File missing → create with one section `[vX.Y.Z] — YYYYMMDD`.
     First release needs no `[Unreleased]` placeholder.
   - Multi-deployable repos (sibling subdirs with own pyproject) —
     each subdir gets its OWN `CHANGELOG.md` for that deployable. Root
     changelog summarises across them.
   - Empty since-last-tag → generate entries from `git log <last>..HEAD`.
4. **Docs alignment** — only spawn refine if README/CLAUDE.md carry
   version-dependent stats (test counts, line counts, version strings).
   Skip when nothing version-shaped is documented.
5. **Verify** — `make test`, `make smoke` if defined. For monorepos
   with sibling deployables, run each subdir's `make test` too.
6. **Commit** — version files + CHANGELOG(s) in one `[release]` commit.
7. **Tag** — `git tag vX.Y.Z`. ONE tag per repo even when there are
   multiple deployables; subdir versions track in their own pyprojects.

## Rules

- ALWAYS `git tag vX.Y.Z` on the release commit
- NEVER push (`git push`)
- Default to patch bump unless user says otherwise
- No changes since last tag → "nothing to release", stop
- First release with no prior tag → tag whatever pyproject already
  says (typically 0.1.0). Don't fabricate a 0.0.0 → 0.1.0 bump just
  to have a delta.
- Monorepo sibling deployables → ONE `git tag` at repo root; each
  deployable's pyproject version is independent of the tag
