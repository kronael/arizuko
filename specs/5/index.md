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

| Spec | Status | Hook |
| ---- | ------ | ---- |

## Runtime — the turn and the container

| Spec | Status | Hook |
| -------------------------------------------------------- | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------- |
| [9-agent-capability-eval.md](9-agent-capability-eval.md) | shipped | `anteval` — a black-box capability prober driving real tasks through the public surfaces of an instance it creates and removes; facts in `anteval/README.md`, `howto/build-a-product.html` |

## Tenancy, onboarding, credentials

| Spec | Status | Hook |
| ---------------------------------------------------------- | --------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
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
