---
status: active
---

# specs/10 — operator tools

Operator-facing controls: usage visibility, spend limits, and
per-instance branding. No user-visible UX changes; all
operator-configured via env vars or dashd.

| Spec                                               | Status | Hook                                                                                        |
| -------------------------------------------------- | ------ | ------------------------------------------------------------------------------------------- |
| [13-onbod-branding.md](13-onbod-branding.md)       | draft  | Per-instance brand surface for onbod (env vars + assets).                                   |
| [4-rate-limits.md](4-rate-limits.md)               | draft  | Usage tracking + per-group rate limits + dashd /usage page.                                 |
| [14-plugins.md](14-plugins.md)                     | draft  | MCP-tool plugin layer: manifest, CLI install, dashd catalog.                                |
| [15-whapd-self-rebind.md](15-whapd-self-rebind.md) | draft  | Operator-only self-service WhatsApp re-pair (no shell dance) when a session is invalidated. |
| [16-whapd-auth-rotate.md](16-whapd-auth-rotate.md) | draft  | whapd auto-rotates the auth dir on 401 storms instead of looping forever.                   |
| [17-emaid-auth.md](17-emaid-auth.md)               | draft  | emaid sender auth (DMARC/DKIM/SPF) + allowlist + quarantine routing for unverified mail.    |
| [18-daemon-dashboards.md](18-daemon-dashboards.md) | draft  | Each daemon owns its own `/dash/`; dashd becomes the central index/hub.                     |
| [19-cost-caps.md](19-cost-caps.md)                 | draft  | Per-folder cost ceilings via `cost_log` + Anthropic billing API.                            |
