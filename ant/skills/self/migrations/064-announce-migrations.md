# 064 — migration announcements are LLM-managed

Earlier drafts tried to drive migration announcements from gated
(startup sysmsg + `announcements`/`announcement_sent` tables). That
path was ripped out.

Announcements are now entirely LLM-driven via `/migrate`:

- Root runs `/migrate`, reads the latest `CHANGELOG.md` entry, and
  fans it out to every registered group using `~/.announced-version`
  as the idempotency claim.
- Per-migration granularity is not worth the infra complexity — one
  short message per release is enough.

Nothing runs on gated startup. Nothing writes to a DB ledger. Best
effort from the root agent.
