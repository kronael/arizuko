---
status: active
---

# specs/11 — operator tools

Operator-facing controls: spend limits, adapter recovery, inbound-mail
trust, per-instance branding. No user-visible UX changes; everything here is
configured by env or `dashd`.

| Spec                                               | Status     | Hook                                                                                                                                                                                                     |
| -------------------------------------------------- | ---------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [13-onbod-branding.md](13-onbod-branding.md)       | draft      | per-instance brand surface for onbod (env vars + assets)                                                                                                                                                 |
| [15-whapd-self-rebind.md](15-whapd-self-rebind.md) | shipped    | operator self-service WhatsApp re-pair, no shell dance — `/v1/pair/start` + `/v1/pair/status` on whapd, driven from dashd                                                                                |
| [16-whapd-auth-rotate.md](16-whapd-auth-rotate.md) | draft      | whapd auto-rotates its auth dir on a 401 storm instead of looping forever                                                                                                                                |
| [17-emaid-auth.md](17-emaid-auth.md)               | partial    | emaid sender auth + quarantine routing. Tiers 1–2 (A-R pinning, allowlist) shipped; tier 3 DKIM is pre-wired and warns off                                                                               |
| [19-cost-caps.md](19-cost-caps.md)                 | superseded | per-folder cost ceilings. The caps and the pre-spawn gate shipped and stand; the ledger under them is the model gateway's, not `cost_log` (6/39). The mid-turn `<budget_notice>` nudges were never built |

## Deleted 2026-08-02

| was                       | why                                                                                                                                                                                                       |
| ------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `4-rate-limits.md`        | proposed `usage_log` + `usage_limits`, neither of which exists. The spend gate shipped instead as `routd/budget.go` over the model gateway's meter — see `19` and `6/39`. It also cited the dead gateway. |
| `14-plugins.md`           | an MCP-tool plugin layer with its own manifest, CLI and catalog. The package manager shipped that surface — `5/28`, `arizuko packages`, `cmd/arizuko/packages.go`.                                        |
| `18-daemon-dashboards.md` | superseded by [`../6/28-cockpit-index.md`](../6/28-cockpit-index.md), which carries its routing/auth/theme/hub model over almost verbatim and fixes the read path.                                        |

## Numbering

These specs were renumbered from an `8/xx` and `10/xx` scheme. Every code
comment carrying an old number has been repointed: `emaid/main.go` and
`emaid/auth.go` cite `11/17`, and `dashd/channels.go` cites `11/15` — the
self-service re-pair surface, which is what that file serves.

A bare `<phase>/<id>` in prose is the form no check validates. `tests/specrefs`
reads links and `depends:`; `tests/webdocs` reads the published pages and, as
of 2026-09-20, accepts a colon after "spec" so `Spec:&nbsp;7/F` no longer slips
past it. Source comments have no guard at all, which is why they are the thing
a renumber has to sweep by hand (BUGS `L43`, `L237`).

**Do not rewrite `5/34` on sight.** An older note here warned that `5/34` in
code meant cost caps. RE-MEASURED 2026-09-20: all eleven `5/34` sites are about
dedup, outbound resolution and route targets, which is exactly what
`specs/5/34 — Channel adapter protocol` holds. The citations are correct.
