---
status: active
---

# specs/11 — operator tools

Operator-facing controls: spend limits, adapter recovery, inbound-mail
trust, per-instance branding. No user-visible UX changes; everything here is
configured by env or `dashd`.

| Spec                                               | Status  | Hook                                                                                                                       |
| -------------------------------------------------- | ------- | -------------------------------------------------------------------------------------------------------------------------- |
| [13-onbod-branding.md](13-onbod-branding.md)       | draft   | per-instance brand surface for onbod (env vars + assets)                                                                   |
| [16-whapd-auth-rotate.md](16-whapd-auth-rotate.md) | draft   | whapd auto-rotates its auth dir on a 401 storm instead of looping forever                                                  |
| [17-emaid-auth.md](17-emaid-auth.md)               | partial | emaid sender auth + quarantine routing. Tiers 1–2 (A-R pinning, allowlist) shipped; tier 3 DKIM is pre-wired and warns off |

## Deleted

| was                       | when       | why                                                                                                                                                                                                         |
| ------------------------- | ---------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `4-rate-limits.md`        | 2026-08-02 | proposed `usage_log` + `usage_limits`, neither of which exists. The spend gate shipped instead as `routd/budget.go` over the model gateway's meter — see `routd/README.md`. It also cited the dead gateway. |
| `14-plugins.md`           | 2026-08-02 | an MCP-tool plugin layer with its own manifest, CLI and catalog. The package manager shipped that surface — `EXTENDING.md`, `arizuko packages`, `cmd/arizuko/packages.go`.                                  |
| `18-daemon-dashboards.md` | 2026-08-02 | superseded by the cockpit, whose routing, auth, theme and hub model `dashd/README.md` states.                                                                                                               |
| `19-cost-caps.md`         | 2026-09-23 | per-folder cost ceilings. The caps and the pre-spawn gate ship and stand; `routd/README.md` § The model gateway holds the enforcement.                                                                      |

## Numbering

These specs were renumbered from an `8/xx` and `10/xx` scheme. Every code
comment carrying an old number has been repointed: `emaid/main.go` and
`emaid/auth.go` cite `11/17`. The self-service re-pair surface in
`dashd/channels.go` points at `whapd/README.md`.

A bare `<phase>/<id>` in prose is the form no check validates. `tests/specrefs`
reads links and `depends:`; `tests/webdocs` reads the published pages and, as
of 2026-09-20, accepts a colon after "spec" so `Spec:&nbsp;7/F` no longer slips
past it. Source comments have no guard at all, which is why they are the thing
a renumber has to sweep by hand (BUGS `L43`, `L237`).
