---
status: active
---

# specs/8 — enterprise hardening: trust primitives on top of phase 5

The trust layer — the hardening that makes arizuko survive a regulated
buyer's security review. Phase 5 builds the surfaces; this phase makes them
defensible.

| Spec                                                 | Status  | Hook                                                                                                                                             |
| ---------------------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| [A-hierarchical-skills.md](A-hierarchical-skills.md) | draft   | Nested `ant/skills/` layout + self-skill root; `resolve` descends a tree instead of enumerating all 93 SKILL.md frontmatters. Per-turn O(depth). |
| [D-slack-agent-pane.md](D-slack-agent-pane.md)       | shipped | Slack AI sidebar: persisted `pane_sessions`, thread-started/context-changed handlers, staged title + suggested prompts, pane context in prompt.  |
| [E-encryption-at-rest.md](E-encryption-at-rest.md)   | partial | AES-256-GCM on `secrets.value` under a `SECRETS_KEY` keyring (shipped). `messages.db` content columns deferred — it trades away SQL search.      |
| [X-sso-saml.md](X-sso-saml.md)                       | draft   | Enterprise SSO: SAML 2.0 SP-initiated + OIDC Authorization Code on top of existing OAuth. JIT provisioning + optional SCIM deprovisioning.       |

## Scope note

`D` is a channel-flavoured historical exception that bled in before the
phase 5/8 split was clean. Per-platform adapter behaviour belongs next to
the daemon (`slakd/README.md`), not in a phase-8 spec; future
channel-specific items go there.

## Deleted 2026-08-02

| was                          | why                                                                                                                                                                                                     |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `00-finalise-plan.md`        | a pre-split meta-plan to carve `gated` into `routerd`/`agent-runnerd`/`mcp-hostd`. `gated` was deleted outright at v0.50.0; the specs it sequenced now live in `5/`.                                    |
| `F-audit-stream.md`          | proposed `ipc_audit`. Migration `0066` consolidated `ipc_audit` + `cli_audit` into the per-daemon **`audit_log`**, specced in [`../5/I-tool-call-logging.md`](../5/I-tool-call-logging.md). Superseded. |
| `G-slack-multi-workspace.md` | unbuilt Slack OAuth multi-workspace, and channel-specific by the scope note above — belongs in `slakd/README.md` if it revives.                                                                         |
| `H-per-daemon-secrets.md`    | claimed `<DAEMON>_CHANNEL_SECRET` had shipped. It never existed (zero code hits), and `CHANNEL_SECRET` itself is retired — adapters now exchange an ES256 service token via `AUTHD_SERVICE_KEY`.        |
| `Z-egred-mitm.md`            | 593 lines proposing HTTPS-MITM on an `egred` daemon that was never built, premised on `gated`. Its own opening quoted `5/13` _rejecting_ MITM, then reintroduced it. `crackbox` (`6/8`) is the answer.  |

There is no secret-broker spec: `specs/7/Y-secret-broker.md` was folded into
[`../5/13-ext-mcp.md`](../5/13-ext-mcp.md) (commit `c6a878e4`). Code comments
in `store/secrets.go` still cite the dead `6/Y`/`8/Y` paths.
