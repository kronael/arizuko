---
status: active
---

# specs/6 — enterprise hardening: trust primitives on top of phase 5

The trust layer. Hardening that makes arizuko credible to regulated
buyers and enterprise security reviews:

- **Encryption at rest** — `messages.db` + `secrets` table (`E`)
- **Audit stream** — `ipc_audit` for MCP mutations + proxyd access
  log + cli_audit (`F`)
- **Per-daemon secrets** — channel-secret separation; leaking one
  adapter's bearer does not compromise others (`H`)
- **Enterprise SSO** — SAML 2.0 SP-initiated + OIDC Authorization
  Code; JIT provisioning + SCIM deprovisioning (`X`)
- **Tool-level secret broker** — `injectSecretsAdapter`,
  `secret_use_log`, `/dash/me/secrets`, connector spawner;
  per-call audit (`Y`)
- **MITM-isolated egress** — HTTPS termination on egred, `$VAR`
  placeholder swap, per-instance CA; catches opaque HTTP clients
  the broker can't (`Z`)

## Where this leads

Phase 6 hardening composes with phase 7's git-as-truth into the
platform thesis:

- **Audit stream** (`F`) provides the SQLite audit log that
  pairs with git history for warm-tier decisions; phase 7's
  per-turn decision sidecar references the same actor identities.
- **Encryption at rest** (`E`) keeps secret blobs safe in SQLite
  while phase 7 explicitly keeps secrets OUT of git (refs only).
- **Secret broker** (`Y`) + **per-daemon secrets** (`H`) lock down
  the secret access surface that phase 7 references via
  `(scope, name)` tuples in `agents.toml`.
- **SSO** (`X`) and **MITM** (`Z`) are independent enterprise
  asks; they don't depend on phase 7 but make the same buyer
  ready to adopt it.

## Scope notes

Two specs in this phase are channel-flavored historical exceptions
(D-slack-agent-pane, G-slack-multi-workspace) that bled in before
the phase 5/6 split was clean. Per-platform adapter behavior
generally lives next to daemon code (`slakd/`, `teled/`, etc.),
not as spec files. Future channel-specific items get a per-daemon
README rather than a phase-6 spec.

| Spec                                                     | Status  | Hook                                                                                                                                                                                                                                   |
| -------------------------------------------------------- | ------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [A-hierarchical-skills.md](A-hierarchical-skills.md)     | draft   | Nested `ant/skills/` layout + self-skill root; `resolve` descends a tree instead of enumerating all SKILL.md frontmatters. Per-turn cost O(depth) not O(N).                                                                            |
| [D-slack-agent-pane.md](D-slack-agent-pane.md)           | shipped | Full Slack AI sidebar support: pane_sessions table; assistant_thread_started/\_context_changed event handlers; setTitle on open; setSuggestedPrompts after every reply; pane_context surfaced to agent prompt; PERSONA.md frontmatter. |
| [E-encryption-at-rest.md](E-encryption-at-rest.md)       | partial | Encrypt `secrets` table + `messages.db` at rest; filesystem-attacker threat model. Shipped: AES-256-GCM on `secrets.value`. Deferred: `messages.db` content columns.                                                                   |
| [F-audit-stream.md](F-audit-stream.md)                   | spec    | Audit log: `ipc_audit` table for MCP mutations + `cli_audit` (existing) + slog for proxyd access. No file export.                                                                                                                      |
| [G-slack-multi-workspace.md](G-slack-multi-workspace.md) | draft   | Slack OAuth install flow + multi-workspace support in slakd.                                                                                                                                                                           |
| [H-per-daemon-secrets.md](H-per-daemon-secrets.md)       | shipped | Per-daemon channel secrets: each adapter reads `<DAEMON>_CHANNEL_SECRET` with fallback to `CHANNEL_SECRET` so a leaked per-platform bearer does not compromise the others.                                                             |
| [N-oauth-services.md](N-oauth-services.md)               | draft   | Third-party OAuth services (Gmail/Linear/GitHub/Notion/…) as agent capabilities. Index spec — mechanism ships via `6/Y` broker + `11/14` surrogate-OAuth + `ipc/connector.go`. Moved from `5/N` (depends on phase-6 broker).           |
| [X-sso-saml.md](X-sso-saml.md)                           | draft   | Enterprise SSO: SAML 2.0 SP-initiated + OIDC Authorization Code, on top of existing OAuth. JIT provisioning + optional SCIM deprovisioning.                                                                                            |
| [Y-secret-broker.md](Y-secret-broker.md)                 | partial | Tool-level secret broker: `injectSecretsAdapter`, `secret_use_log`, `/dash/me/secrets`, connector spawner. M0/M1 (broker middleware) not yet shipped; M2–M6 (schema, CLI, dashd, spawn-env drop) shipped.                              |
| [Z-egred-mitm.md](Z-egred-mitm.md)                       | draft   | HTTPS-MITM on egred: per-source TLS termination, `$VAR` placeholder swap on Authorization-class headers, CA per instance. Additive to Y — catches opaque HTTP clients (curl, requests, bash-grant scripts) the broker can't.           |
| [00-finalise-plan.md](00-finalise-plan.md)               | draft   | Historical: bucket-6 finalisation plan from the pre-split era. Most referenced specs now live in [specs/5/](../5/).                                                                                                                    |
