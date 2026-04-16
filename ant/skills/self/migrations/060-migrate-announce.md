# 060 — /migrate announces releases

`/migrate` gained section **e) Announce the release**. After migrations
run, the root agent broadcasts the latest `CHANGELOG.md` entry to every
registered group via `send_message`.

- Last-announced version tracked at `~/.announced-version` (skip if matched)
- One message per release, not per migration
- Short user-facing message — no migration numbers, no jargon
