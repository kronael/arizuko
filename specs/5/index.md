---
status: active
---

# specs/5 — platform core: surfaces, identity, routing, tenancy, runtime

The capabilities layer: cross-cutting platform specs that aren't tied to a
single channel adapter.

## Scope boundary

Per-channel adapter behaviour (Slack/Telegram/Discord quirks) lives next to the
daemon code in `teled/`, `slakd/`, etc., not here.

## Where this leads

Phase 5 is the foundation for the two phases above it. The directory `../8/`
layers enterprise hardening — encryption, audit, SSO, egress — on phase 5's
surfaces, identity, and tenancy. The directory `../9/` closes and minimizes:
`5/16` finishes the unification whose mechanism is `5/17`, `9/2` sharpens the
data entities defined here, and `9/3` moves the cold tier into git.

> Those directories are numbered one ahead of the titles inside them (`../7/`
> calls itself phase 6, `../8/` phase 7, `../9/` phase 8). The mismatch spans
> directories this phase does not own; links here address the directory, which
> is the real location.

## Identity and authorization

| Spec                                             | Status  | Hook                                                                                                                                                                                                                                                  |
| ------------------------------------------------ | ------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [32-acl-unified.md](32-acl-unified.md)           | shipped | **Canonical ACL.** One row primitive, three principals, one `auth.Authorize`, deny-wins. `EffectiveActions`/`UserScopes` are projections of the same rows, never verdicts.                                                                            |
| [33-paths-roles.md](33-paths-roles.md)           | shipped | **The authorization model.** Tiers removed: a path carries no authority, magnitude is role membership, delegation is subset-of-held via `grant_option`. Only the `group`→`path` rename is outstanding.                                                |
| [1-auth-standalone.md](1-auth-standalone.md)     | partial | authd is the sole ES256 minter; every other daemon offline-verifies through `auth/`. Absorbs the former `11-auth-api` wire surface.                                                                                                                   |
| [31-identity-pairing.md](31-identity-pairing.md) | partial | **Channel identity → verified account.** A channel user is anonymous and holds no grants; the `acl_membership` edge is the only bridge, and only the account owner's consent at `/pair/<token>` writes one. A pairing is a `kind='pair'` route token. |
| [10-web-access.md](10-web-access.md)             | shipped | `/priv` is a grant decision, not a second ACL — `auth.MatchSlot` in proxyd after auth. No grant inheritance; the slot tree nests because the mounts do.                                                                                               |
| [19-hitl-firewall.md](19-hitl-firewall.md)       | draft   | A `hold:mcp:<tool>` rule suspends a call for human approval. Hazard: `CheckHold` must read `hold:` rows directly — routing it through `auth.Authorize` would hold every tool for the operator.                                                        |

## Surfaces — MCP, REST, web, OpenAPI

| Spec                                                     | Status  | Hook                                                                                                                                                                                                                                                      |
| -------------------------------------------------------- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [17-openapi-mcp.md](17-openapi-mcp.md)                   | partial | **Canonical mechanism.** One `resreg.Resource` wears two faces: MCP derived for agents, REST authored for humans, OpenAPI emitted from `RowType` reflection.                                                                                              |
| [16-mcp-rest-unification.md](16-mcp-rest-unification.md) | partial | The adoption program for `5/17`. All seven agent-facing cold-tier resources ride it; one owner + federation remains.                                                                                                                                      |
| [8-yaml-manifests.md](8-yaml-manifests.md)               | partial | YAML carrier for cold-tier config, plus the (unbuilt) full-instance archive superset: config + secrets + message history + group filesystem trees. Engine and format shipped; the CLI still opens the frozen `messages.db`, so it is inert in production. |
| [7-proxyd-standalone.md](7-proxyd-standalone.md)         | shipped | Config-driven authenticating reverse proxy. Enforcement point, not authority — authd signs.                                                                                                                                                               |
| [V-web-vhosts.md](V-web-vhosts.md)                       | shipped | Per-world hostname derived as `W.<HOSTING_DOMAIN>` → `/pub/W/`. Web slots gate on the `web:publish` grant, not on rank.                                                                                                                                   |
| [J-sse.md](J-sse.md)                                     | shipped | The streaming half of the chat surface: hub, `folder/topic` keying, slow-client drop. Tokens and URLs belong to `5/W`.                                                                                                                                    |
| [M-webdav.md](M-webdav.md)                               | shipped | dufs behind proxyd auth, with a write-block guard.                                                                                                                                                                                                        |
| [3-inspect-tools.md](3-inspect-tools.md)                 | shipped | The `inspect_*` MCP family — messages, routing, tasks, session.                                                                                                                                                                                           |
| [C-message-mcp.md](C-message-mcp.md)                     | shipped | Agent-side history: `get_thread`, `fetch_history`, `find_messages`.                                                                                                                                                                                       |
| [T-voice-synthesis.md](T-voice-synthesis.md)             | shipped | `ttsd` plus the in-routd `send_voice` path.                                                                                                                                                                                                               |

## Routing and messaging

| Spec                                                             | Status  | Hook                                                                                                                                             |
| ---------------------------------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| [E-routd.md](E-routd.md)                                         | shipped | **The conversation state machine.** Owns routing rules, the message store, the orchestration loop, and channel ingress/egress.                   |
| [Q-unified-routing.md](Q-unified-routing.md)                     | shipped | One message table, one decision point, uniform `prefix:identifier` addressing.                                                                   |
| [S-jid-format.md](S-jid-format.md)                               | shipped | `<platform>:<rest>` wire form with `path.Match` globs. The proposed typed Go structs were descoped.                                              |
| [B-route-mode-ingestion.md](B-route-mode-ingestion.md)           | shipped | Route mode as a URI fragment on `target` (`folder#observe`, `#announce`) instead of a weights table.                                             |
| [G-engagement.md](G-engagement.md)                               | partial | A mention or bot reply engages `(jid, topic)` until TTL. Engagement commits after routing, never at ingest — the owning folder isn't known yet.  |
| [L-mention-promotion.md](L-mention-promotion.md)                 | shipped | Reply or reaction to the bot promotes to `verb=mention` at routd ingest.                                                                         |
| [F-topic-lineage.md](F-topic-lineage.md)                         | shipped | Topics carry `parent_topic`, `forked_at`, and a per-topic observed cursor; `fork_topic` copies the parent session.                               |
| [W-webhook-routes.md](W-webhook-routes.md)                       | partial | One `route_tokens` table, two prefixes: `/chat/<token>/` and `/hook/<token>`. Owns token auth and URL shape.                                     |
| [34-channel-protocol.md](34-channel-protocol.md)                 | shipped | The adapter contract: self-registration, send, health. Adapters authenticate with a `service:<daemon>` ES256 JWT.                                |
| [R-multi-account.md](R-multi-account.md)                         | shipped | Several accounts per adapter = several compose fragments, distinguished by `CHANNEL_NAME`.                                                       |
| [Z-message-actions.md](Z-message-actions.md)                     | shipped | Agent-initiated edit, delete, pin, unpin on messages it sent.                                                                                    |
| [Y-output-styles-per-surface.md](Y-output-styles-per-surface.md) | shipped | Output style selected per surface from the JID, falling back to the bare channel name.                                                           |
| [6-proactive-interjection.md](6-proactive-interjection.md)       | partial | Lurk mode: silence-driven turns behind a cooldown and a quiet-turn veto; the agent may emit nothing. Code + docs done; awaiting a dashd surface. |
| [12-turn-retry.md](12-turn-retry.md)                             | shipped | Reschedule a turn that died mid-execution without delivering a reply.                                                                            |

## Runtime — the turn and the container

| Spec                                                     | Status  | Hook                                                                                                                                |
| -------------------------------------------------------- | ------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| [P-runed.md](P-runed.md)                                 | shipped | **The execution plane.** Container lifecycle and run federation. The turn is credentialed by the SO_PEERCRED socket, never a token. |
| [A-primitives-framing.md](A-primitives-framing.md)       | shipped | Framing only, no behaviour: the pipeline primitives in route-first order, identity as coordinate system.                            |
| [2-agent-pipeline.md](2-agent-pipeline.md)               | shipped | Orchestration (route tokens) versus workflows (the in-container Agent tool).                                                        |
| [4-autocalls.md](4-autocalls.md)                         | shipped | Inject facts inline when schema cost exceeds content cost. Four autocalls, no tools.                                                |
| [24-live-tasklist-status.md](24-live-tasklist-status.md) | shipped | A `TodoWrite` hook renders the agent's task list into one message edited in place.                                                  |
| [I-tool-call-logging.md](I-tool-call-logging.md)         | partial | Per-tool-call logging on both surfaces; the `audit_log` table is the source of truth.                                               |
| [O-observability.md](O-observability.md)                 | shipped | Three opt-in substrates: slog+OTLP logs, spans, and 15 Prometheus metric families.                                                  |
| [9-agent-capability-eval.md](9-agent-capability-eval.md) | shipped | `anteval` — a black-box capability gate driving real tasks through the public surfaces.                                             |
| [23-skill-guard.md](23-skill-guard.md)                   | draft   | A PreToolUse threat-pattern scanner over skill writes.                                                                              |
| [22-self-learning.md](22-self-learning.md)               | draft   | Pattern recognition over a group's history producing operator-reviewed proposals, never silent rewrites.                            |

## Tenancy, onboarding, credentials

| Spec                                                       | Status    | Hook                                                                                                                                                  |
| ---------------------------------------------------------- | --------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| [18-onboarding-model.md](18-onboarding-model.md)           | partial   | **The location axis**: an unrouted JID acquires a `routes` row. Steps 1–6 ship (picker `d9e57288`); step 7's explicit, attributed route act does not. |
| [5-worlds-agents-sessions.md](5-worlds-agents-sessions.md) | partial   | World → Agent → Session, the collapsed hierarchy. Invites (shipped) + the unbuilt tier framework, guests, and delegated OAuth.                        |
| [14-credentials.md](14-credentials.md)                     | shipped   | The credential model: env-profile versus capability versus infra.                                                                                     |
| [13-ext-mcp.md](13-ext-mcp.md)                             | shipped   | External capability injection — service descriptors map tools to REST, auth injected from folder secrets.                                             |
| [15-surrogate-oauth.md](15-surrogate-oauth.md)             | shipped   | "Connect X" fills the secrets table `5/13` reads; refresh happens at call time.                                                                       |
| [25-integration-catalog.md](25-integration-catalog.md)     | draft     | The integration surface: arizuko as the gated, audited fabric an agent operates.                                                                      |
| [26-integration-usecases.md](26-integration-usecases.md)   | reference | The use-case corpus grounding `5/25`.                                                                                                                 |

## Packaging and distribution

| Spec                                                             | Status  | Hook                                                                                                                                                                                                                                     |
| ---------------------------------------------------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [28-packages.md](28-packages.md)                                 | partial | **The package manager (canonical lifecycle)** — one package's install/upgrade/remove, built. Composition (a group blending an ordered LIST of products) is DEFERRED: its lock is instance-keyed and its subject is group-scoped (`F29`). |
| [27-compose-native-packaging.md](27-compose-native-packaging.md) | shipped | Compose `profiles:` for optional daemons and `include:` for adapter fragments — no custom DSL.                                                                                                                                           |
| [21-products.md](21-products.md)                                 | draft   | Producer side — what a product contains and how it is authored.                                                                                                                                                                          |

## Docs, tooling, interop

| Spec                                                 | Status  | Hook                                                                                            |
| ---------------------------------------------------- | ------- | ----------------------------------------------------------------------------------------------- |
| [D-docs-refs-redesign.md](D-docs-refs-redesign.md)   | shipped | The `/pub` IA: Divio categories plus a dbt-style reference rhythm. Visual identity unchanged.   |
| [36-go-1.27-adoption.md](36-go-1.27-adoption.md)     | shipped | Go 1.27 adopted ahead of GA on `go1.27rc2`, at the operator's direction.                        |
| [35-hermes-integration.md](35-hermes-integration.md) | draft   | Connecting the Hermes harness to arizuko's messaging plane so it drops its own connector layer. |

## Retired

Deleted specs, folded elsewhere. Recorded here so the decision survives the
file.

| Spec                    | Fate                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| ----------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `11-auth-api.md`        | 2026-08-02. Merged into [1-auth-standalone.md](1-auth-standalone.md) — it described authd's `/v1/*` wire surface, now a section there. Nothing outside this index referenced it.                                                                                                                                                                                                                                                                                        |
| `30-oci-packages.md`    | 2026-08-02. Superseded by [28-packages.md](28-packages.md). OCI artifacts as the distribution envelope lost to source-first, distributor-managed packages; the rejected-alternative reason lives in `5/28`.                                                                                                                                                                                                                                                             |
| `20-ant-portability.md` | 2026-08-04. Dissolved — its one still-owned decision (a group blends an ordered LIST of products; the per-payload-kind collision rule) moved into [28-packages.md](28-packages.md), which already owned one package's lifecycle and only deferred that question here. State transport (`export`/`apply`) was already a one-line pointer to `5/8`, unchanged; nothing of `5/8`'s content moved. Pre-dissolve draft: `git log --follow -- specs/5/20-ant-portability.md`. |
