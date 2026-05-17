---
status: active
---

# specs/6 — platform adapters (per-channel behavior)

Per-platform adapter specs: behavior, surface, and quirks that are
specific to one channel (Slack, Telegram, Discord, WhatsApp, Mastodon,
Bluesky, Reddit, email, LinkedIn, Twitter, …). Cross-cutting
gateway/router/auth/MCP concerns live in [specs/5/](../5/).

| Spec                                           | Status  | Hook                                                                                                                                                                                                                                   |
| ---------------------------------------------- | ------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [D-slack-agent-pane.md](D-slack-agent-pane.md) | shipped | Full Slack AI sidebar support: pane_sessions table; assistant_thread_started/\_context_changed event handlers; setTitle on open; setSuggestedPrompts after every reply; pane_context surfaced to agent prompt; PERSONA.md frontmatter. |
| [00-finalise-plan.md](00-finalise-plan.md)     | meta    | Historical: bucket-6 finalisation plan from the pre-split era. Most referenced specs now live in [specs/5/](../5/).                                                                                                                    |
