# 060 — migrate skill announces releases

The `/migrate` skill gained a new section **e) Announce the release**.

After applying migrations, the root agent now broadcasts the latest
`CHANGELOG.md` entry to every registered group. Until the automatic
announcement path from `specs/3/e-migration-announce.md` is implemented,
this is the manual fan-out step.

## What changed

- Root agent tracks last-announced version at `~/.announced-version`.
- Skips if already announced.
- Reads the latest `## [vX.Y.Z]` entry from `/workspace/self/CHANGELOG.md`,
  composes a short, user-facing message (no migration numbers, no
  internal jargon), and sends it to each group's primary JID via
  `send_message`.
- One message per release — not per migration.

## Why

Users on Telegram / WhatsApp / Discord never saw the v0.27 or v0.28
releases. Nothing was pushing changelog content out to them. This
closes the loop until the automatic db_utils-based broadcast ships.
