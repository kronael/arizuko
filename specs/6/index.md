---
status: active
---

# specs/6 — platform adapters (per-channel behavior)

Per-platform adapter specs: behavior, surface, and quirks that are
specific to one channel (Slack, Telegram, Discord, WhatsApp, Mastodon,
Bluesky, Reddit, email, LinkedIn, Twitter, …). Cross-cutting
gateway/router/auth/MCP concerns live in [specs/5/](../5/).

| Spec                                                     | Status  | Hook                                                                                                                                                                                                                                   |
| -------------------------------------------------------- | ------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [D-slack-agent-pane.md](D-slack-agent-pane.md)           | shipped | Full Slack AI sidebar support: pane_sessions table; assistant_thread_started/\_context_changed event handlers; setTitle on open; setSuggestedPrompts after every reply; pane_context surfaced to agent prompt; PERSONA.md frontmatter. |
| [E-encryption-at-rest.md](E-encryption-at-rest.md)       | draft   | Encrypt `secrets` table + `messages.db` at rest; filesystem-attacker threat model.                                                                                                                                                     |
| [F-audit-stream.md](F-audit-stream.md)                   | spec    | Audit log: `ipc_audit` table for MCP mutations + `cli_audit` (existing) + slog for proxyd access. No file export.                                                                                                                      |
| [G-slack-multi-workspace.md](G-slack-multi-workspace.md) | draft   | Slack OAuth install flow + multi-workspace support in slakd.                                                                                                                                                                           |
| [H-per-daemon-secrets.md](H-per-daemon-secrets.md)       | shipped | Per-daemon channel secrets: each adapter reads `<DAEMON>_CHANNEL_SECRET` with fallback to `CHANNEL_SECRET` so a leaked per-platform bearer does not compromise the others.                                                             |
| [X-sso-saml.md](X-sso-saml.md)                           | draft   | Enterprise SSO: SAML 2.0 SP-initiated + OIDC Authorization Code, on top of existing OAuth. JIT provisioning + optional SCIM deprovisioning.                                                                                            |
| [Y-secret-broker.md](Y-secret-broker.md)                 | partial | Tool-level secret broker: `injectSecretsAdapter`, `secret_use_log`, `/dash/me/secrets`, connector spawner. M0/M1 (broker middleware) not yet shipped; M2–M6 (schema, CLI, dashd, spawn-env drop) shipped.                              |
| [00-finalise-plan.md](00-finalise-plan.md)               | draft   | Historical: bucket-6 finalisation plan from the pre-split era. Most referenced specs now live in [specs/5/](../5/).                                                                                                                    |
