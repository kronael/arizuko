# 064 — per-migration announcement dispatch

New skill: `/announce-migrations`. Root-only. Paired `.md` alongside
each `store/migrations/NNNN-*.sql` is inserted into `announcements`
at migration time; gated drops a `system_message` into the root
group on startup when any pending rows exist.

Root fan-out sends each `.md` to every `chats.jid` exactly once,
keyed by `announcement_sent(service, version, user_jid)`. Inner
groups get a one-line `system_message` (origin=`migration`) on
completion — no broadcast there.

Replaces release-level CHANGELOG broadcasts from migration 060 with
granular per-migration notes.
