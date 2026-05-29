---
status: superseded
superseded_by: V-web-vhosts
---

# One URL, one backing store: web publish single-source

> **Superseded by [`V-web-vhosts.md`](V-web-vhosts.md) § "One URL, one
> backing store"** (2026-05-28). This file is kept so inbound links resolve.

The rule — each `/pub/<seg>/` URL is backed by exactly one store, no
ownerless static trees under `<data>/web/pub/`, cross-group aliases are
`web_routes` redirects (never file copies), and `set_web_route`'s top-level
path-claim is first-claim/unclaimed-only — now lives in 5/V alongside the
FHS slot model it constrains. The marinade `/pub/guides` drift incident
(2026-05-28) that motivated it is recorded there.

Operational cleanup shipped on all instances; the `set_web_route`
path-claim code enforcement is tracked separately.
