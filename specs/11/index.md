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
| [15-whapd-self-rebind.md](15-whapd-self-rebind.md) | shipped | operator self-service WhatsApp re-pair, no shell dance — `/v1/pair/start` + `/v1/pair/status` on whapd, driven from dashd  |
| [16-whapd-auth-rotate.md](16-whapd-auth-rotate.md) | draft   | whapd auto-rotates its auth dir on a 401 storm instead of looping forever                                                  |
| [17-emaid-auth.md](17-emaid-auth.md)               | partial | emaid sender auth + quarantine routing. Tiers 1–2 (A-R pinning, allowlist) shipped; tier 3 DKIM is pre-wired and warns off |
| [19-cost-caps.md](19-cost-caps.md)                 | partial | per-folder cost ceilings. `cost_log` + the pre-spawn budget gate shipped; the mid-turn `<budget_notice>` nudges are not    |

## Deleted 2026-08-02

| was                       | why                                                                                                                                                                          |
| ------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `4-rate-limits.md`        | proposed `usage_log` + `usage_limits`, neither of which exists. The spend gate shipped instead as `cost_log` + `routd/budget.go` — see `19`. It also cited the dead gateway. |
| `14-plugins.md`           | an MCP-tool plugin layer with its own manifest, CLI and catalog. The package manager shipped that surface — `5/28`, `arizuko packages`, `cmd/arizuko/packages.go`.           |
| `18-daemon-dashboards.md` | superseded by [`../7/1-cockpit-index.md`](../7/1-cockpit-index.md), which carries its routing/auth/theme/hub model over almost verbatim and fixes the read path.             |

## Numbering warning

These specs were renumbered from an `8/xx` and `10/xx` scheme, and the old
numbers are **baked into shipped code comments** — `emaid/main.go` and
`emaid/auth.go` cite "spec 10/17" for what is now `11/17`, and
`dashd/channels.go` cites "spec 8/16" for what is now `11/16`.

Worse, ~12 code sites cite "Spec 5/34" meaning **cost caps** (`19`), from a
numbering in which `5/34` was the cost-caps spec. `specs/5/34` is now
`channel-protocol`, an unrelated shipped spec, so those comments now point a
reader at the wrong document. Fixing them is a code change, not a spec one.
