---
status: draft
---

# Wrapping the harness fleet — arizuko as the service layer above N Hermeses

Phase 6 says "wrap what you run." This is the worked example. The harness is
**Hermes** (NousResearch); the wrap is **many Hermes deployments running inside
arizuko folders** while arizuko moves one level up and supplies the shared
services the harness lacks. arizuko is not a Hermes competitor — it is the
control plane above a _fleet_ of harnesses (Hermes, Claude Code, OpenClaw, …).
"A product you own, not a chatbot you rent" (`5/A`), one rung up: the platform
that hosts the rentable agents.

Reference corpus for this spec (raw, gitignored): `.refs/` —
`hermes-docs-distillation.md`, `hermes-comparative-analysis.md`,
`hermes-layers-service-recording-cost.md`. Clone at `refs/hermes-agent` @ v0.8.0.

## The layering

```
   folder = tenant + ACL + route + egress + web host + file tree   ← arizuko (services)
   ───────────────────────────────────────────────────────────
   Hermes(solo/inbox)   Hermes(corp/eng)   ClaudeCode(atlas)   …   ← harness fleet
```

One box, many folders, a harness per folder. The harness does the agent work;
arizuko owns _how it runs and for whom_. Same code from `solo/inbox` to
`corp/eng/sre/oncall` (`5/A`, `6/1`).

## Services arizuko provides to the fleet (the "one level up")

Lead: **keys + security** — the two the operator named. Each row is a Hermes gap
(confirmed in `.refs/hermes-docs-distillation.md`) that the folder coordinate
closes for every harness in the fleet.

| Service                   | arizuko component                                                                     | Hermes gap it closes                                                                     | wrap mechanism                                                                                                                                   |
| ------------------------- | ------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| **Keys / secrets**        | SECRETS_KEY folder-scoped encryption + **host-boundary credential injection** (`8/Z`) | keys stored plaintext in `~/.hermes/.env`, `auth.json`, `mcp-tokens/*` (chmod 0600 only) | secret substituted at the host proxy; the harness runtime never sees the value. **This is the phase-6 lead item** (triple convergence, `6/2` §1) |
| **Egress / network**      | **crackbox** per-folder default-deny allowlist + DNS filter (`12/9`)                  | one global SSRF blocklist; no per-tenant/per-skill egress                                | the harness's container dials crackbox; egress inherits down the folder tree                                                                     |
| **Identity / authz**      | folder-containment + grant DSL `[!]action(param=glob)`                                | binary admin-vs-user over slash commands; single shared API bearer                       | injected authz Gate binds every action to the caller's folder (`5/17`)                                                                           |
| **Tenant isolation**      | ephemeral per-group container / KVM (`crackbox pkg/host`, `12/9`)                     | one long-lived shared container per profile; users share its filesystem                  | a fresh container per turn per folder; nothing leaks between tenants                                                                             |
| **Cost / budget**         | per-tenant spend gate — `store/cost_log.go` (`SpendTodayFolder/User`)                 | per-session `/usage` only; no caps, no cross-tenant aggregation                          | every harness call logs (folder, user, cents); the gate caps the tenant                                                                          |
| **Audit / observability** | `audit_log` + `obs/` (journald + OTLP, turn-scoped TraceIDs)                          | plaintext `gateway.log`; no metrics/tracing/structured audit                             | every turn lands on the tenant's audit trail                                                                                                     |
| **Routing / web**         | routing DSL (`#observe`/`#announce`/topics) + `/pub` `/priv` + WebDAV                 | home-channel delivery resolver; no daemon-served web                                     | routes replace home-channel logic; the tenant gets a web presence                                                                                |

## Two ways to wrap (the phase-6 engine)

Not a fork of Hermes — a per-capability choice.

1. **Run-as-is.** Hermes-Python runs inside the folder via runed's swappable
   runtime (the adapter contract, `7/11`). arizuko wraps services around it.
   Fastest; Hermes stays upstream and keeps its model breadth + skill ecosystem.
2. **Reimplement in Go** — the agentic reimplementation loop (`6/1`). Port
   Hermes's _valuable orthogonal mechanisms_ into arizuko's native Go runed
   harness rather than the whole process: the self-learning loop (`12/7`),
   skill authoring-from-experience, cost-source provenance (from Hermes's
   `usage_pricing.CostStatus`/`CostSource`). Deeper, single-stack, no Python
   process. **"Rewrite it in Go" = this path, scoped to mechanisms.** Port the
   mechanism, not the harness.

Decision per capability: **wrap-as-is | reimplement-in-Go | hold**. The
wrap/enhance/hold pass over each primitive feeds `6/2` (incorporate vs beat).

## What we deliberately do NOT rebuild

- **Skills hub / marketplace.** Not our product. Two shapes: (a) **interop** —
  arizuko skills speak the `agentskills.io` standard Hermes already consumes;
  (b) **`kronael/tools` as a tap** — arizuko already vendors its skills from the
  `kronael/tools` upstream repo (the `sync-tools-skills` workflow); expose that
  repo as a tap any harness pulls. Either way the hub merges into the harness
  layer; arizuko curates a repo, it does not run a market.
- **Model-agnosticism.** Hermes owns provider breadth (200+ models, live
  `/model` switch); arizuko is Claude-native by choice. Hold.
- **The self-learning background thread.** Graft the mechanism (`12/7`) as a
  post-turn hook writing to the group's persistent `~/.claude`; do not import
  Hermes's long-running review daemon (incompatible with ephemeral-per-turn).

## The moat (do not forgo)

Six traits Hermes's long-running, prompt-cache-sensitive, single-operator
architecture cannot cheaply follow: folder/tier app-level tenancy · ephemeral
per-turn container · per-folder crackbox egress · uniform MCP+REST+OpenAPI
(`5/17` resreg) · folder-containment + grant DSL · per-tenant web + OTLP audit.
Their own security page lists "multi-user access control within one instance" as
out of scope — that gap is our layer. The trap is chasing their breadth (models,
20+ platforms, 88k-skill market, RL) at the cost of these six.

## The three shared layers (cost / recording / service)

Detail in `.refs/hermes-layers-service-recording-cost.md`. Summary:

- **Cost** — arizuko supplies per-tenant budget-gating (`cost_log`); Hermes
  supplies per-call price provenance (`CostStatus`/`CostSource`, models.dev).
  Graft the provenance; keep the gate. (No written cost spec yet — `cost_log.go`
  cites `5/34` but the file is absent; **write it** when the fleet-billing story
  firms up.)
- **Recording** — arizuko records for audit/OTLP; Hermes records ShareGPT
  trajectories for RL. arizuko can offer trajectory export as an opt-in,
  grant-gated per-folder tap (consented training data).
- **Service-channel** — arizuko's routing DSL subsumes Hermes's home-channel
  resolver; steal Hermes's explicit `origin|home|local|target` delivery-mode
  enum for timed/proactive output.

## Open questions / next

- Firm up `7/11` adapter-contract so runed can host a Hermes-Python runtime
  unmodified (run-as-is path).
- Ship the secrets broker / host-boundary injection (`8/Z`) — the lead item.
- Package **crackbox as the standalone egress+sandbox service** the fleet dials
  (`12/9`) — the single highest-leverage move.
- Scope the Go reimplementation: which mechanism first — self-learning (`12/7`)
  or cost provenance?

## Ties

`6/1` (interop strategy) · `6/2` (target matrix — secrets is §1) · `6/3` ·
`5/A` (positioning) · `5/17` (resreg two-face) · `8/Z` (host-boundary secrets) ·
`12/9` (crackbox) · `12/7` (self-learning) · `7/11` (adapter contract) ·
`store/cost_log.go` · `.refs/` (raw corpus).
