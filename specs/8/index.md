---
status: active
---

# specs/8 — operator tools

Operator-facing controls: usage visibility, spend limits, and
per-instance branding. No user-visible UX changes; all
operator-configured via env vars or dashd.

| Spec                                         | Status    | Hook                                                        |
| -------------------------------------------- | --------- | ----------------------------------------------------------- |
| [13-onbod-branding.md](13-onbod-branding.md) | draft     | Per-instance brand surface for onbod (env vars + assets).   |
| [4-rate-limits.md](4-rate-limits.md)         | unshipped | Usage tracking + per-group rate limits + dashd /usage page. |
