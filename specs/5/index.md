---
status: partial
---

# specs/5 — platform core: surfaces, identity, routing, tenancy, runtime

The capabilities layer: cross-cutting platform specs that aren't tied to a
single channel adapter.

## Phase status

**Phase 5 is partial.** The shipped specs that the docs now hold are
retired; four shipped specs stay while the QEMU substrate spec depends on
them. Three files carry `reference` — analysis with no lifecycle to finish
(`25`, `26`, `35`).

## Scope boundary

Per-channel adapter behaviour (Slack/Telegram/Discord quirks) lives next to the
daemon code in `teled/`, `slakd/`, etc., not here.

## Where this leads

Phase 5 is the foundation for the two phases above it. The directory `../8/`
layers enterprise hardening — encryption, audit, SSO, egress — on phase 5's
surfaces, identity, and tenancy. The directory `../9/` closes and minimizes:
`9/2` sharpens the data entities defined here.

## Identity and authorization

| Spec | Status | Hook |
| ------------------------------------------------ | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [33-paths-roles.md](33-paths-roles.md) | defected | **The authorization model.** A path grants no authority. Role membership selects defaults. `auth.CheckGrant` checks rule mutations against the selected policy layer. **DEFECTED `L538`**: guest capabilities use the action lattice. | ; facts in `auth/README.md` § Authorization is one row and one question, `SECURITY.md` § The sender cannot lend the agent authority, `ARCHITECTURE.md` § Decided, and why |

## Routing and messaging

| Spec                                 | Status  | Hook                                                                                                                                                                                                                                                                         |
| ------------------------------------ | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [12-turn-retry.md](12-turn-retry.md) | shipped | Retry a failed turn from its saved input three times on a schedule (chat 30 s/2 min/5 min, cron 15/45/180 min) behind one instance breaker; timeouts and terminal failures never retry.; facts in `routd/README.md` § The turn in routd, `runed/README.md` § How a turn ends |

## Runtime — the turn and the container

| Spec | Status | Hook |
| -------------------------------------------------------- | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------- |
| [P-runed.md](P-runed.md) | shipped | **The execution plane.** Container lifecycle and run federation. The turn is credentialed by the SO_PEERCRED socket, never a token.; facts in `runed/README.md` § Admission is claim or reject |
| [A-primitives-framing.md](A-primitives-framing.md) | shipped | Framing only, no behaviour: the pipeline primitives in route-first order, identity as coordinate system. | ; facts in `concepts/routing.html`, `template/web/CLAUDE.md` § Voice |
| [9-agent-capability-eval.md](9-agent-capability-eval.md) | defected | `anteval` — a black-box capability gate driving real tasks through the public surfaces. **DEFECTED `L72`**: § "Approach" says teardown is best-effort, and the driver has no teardown; facts in `anteval/README.md`, `howto/build-a-product.html` |

## Tenancy, onboarding, credentials

| Spec | Status | Hook |
| ---------------------------------------------------------- | --------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| [14-credentials.md](14-credentials.md) | shipped | The credential model: env-profile versus capability versus infra. Only env-profile keys reach container env; capability credentials are broker-only (`X1`, 2026-08-09). | ; facts in `SECURITY.md` § Secret injection, `concepts/secrets.html`, `reference/env.html` § SECRETS_KEY |
| [25-integration-catalog.md](25-integration-catalog.md) | reference | The integration surface: arizuko as the gated, audited fabric an agent operates. |
| [26-integration-usecases.md](26-integration-usecases.md) | reference | The use-case corpus grounding `5/25`. |

## Docs, tooling, interop

| Spec                                                 | Status    | Hook                                                                                            |
| ---------------------------------------------------- | --------- | ----------------------------------------------------------------------------------------------- |
| [35-hermes-integration.md](35-hermes-integration.md) | reference | Connecting the Hermes harness to arizuko's messaging plane so it drops its own connector layer. |

## Retired

Deleted specs, folded elsewhere. Recorded here so the decision survives the
file.

| Spec                    | Fate                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| ----------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `11-auth-api.md`        | 2026-08-02. Merged into `authd/README.md` — it described authd's `/v1/*` wire surface, now a section there. Nothing outside this index referenced it.                                                                                                                                                                                                                                                                                                                  |
| `30-oci-packages.md`    | 2026-08-02. Superseded by [EXTENDING.md](../../EXTENDING.md#installing-packages). OCI artifacts as the distribution envelope lost to source-first, distributor-managed packages; source installation lives in `EXTENDING.md`.                                                                                                                                                                                                                                          |
| `20-ant-portability.md` | 2026-08-04. Dissolved — its one still-owned decision (a group blends an ordered LIST of products; the per-payload-kind collision rule) moved into [EXTENDING.md](../../EXTENDING.md#installing-packages), which owns package installation. State transport (`export`/`apply`) was already a one-line pointer to `resreg/README.md`, unchanged; nothing of `resreg/README.md`'s content moved. Pre-dissolve draft: `git log --follow -- specs/5/20-ant-portability.md`. |
