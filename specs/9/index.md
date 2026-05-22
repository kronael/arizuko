---
status: active
---

# specs/9 — operator tools

Operator-facing controls: usage visibility, spend limits, and
per-instance branding. No user-visible UX changes; all
operator-configured via env vars or dashd.

| Spec                                         | Status | Hook                                                             |
| -------------------------------------------- | ------ | ---------------------------------------------------------------- |
| [13-onbod-branding.md](13-onbod-branding.md) | draft  | Per-instance brand surface for onbod (env vars + assets).        |
| [4-rate-limits.md](4-rate-limits.md)         | draft  | Usage tracking + per-group rate limits + dashd /usage page.      |
| [14-plugins.md](14-plugins.md)               | draft  | MCP-tool plugin layer: manifest, CLI install, dashd catalog.     |
| [19-cost-caps.md](19-cost-caps.md)           | draft  | Per-folder cost ceilings via `cost_log` + Anthropic billing API. |
