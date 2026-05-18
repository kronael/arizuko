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
| [F-audit-stream.md](F-audit-stream.md)                   | draft   | Append-only SIEM-export stream (Splunk/Datadog) from `audit_log` table.                                                                                                                                                                |
| [G-slack-multi-workspace.md](G-slack-multi-workspace.md) | draft   | Slack OAuth install flow + multi-workspace support in slakd.                                                                                                                                                                           |
| [H-per-daemon-secrets.md](H-per-daemon-secrets.md)       | shipped | Per-daemon channel secrets: each adapter gets its own `CHANNEL_SECRET_<LABEL>`.                                                                                                                                                        |
| [00-finalise-plan.md](00-finalise-plan.md)               | draft   | Historical: bucket-6 finalisation plan from the pre-split era. Most referenced specs now live in [specs/5/](../5/).                                                                                                                    |
