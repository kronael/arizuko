---
status: reference
---

# Target matrix — who to incorporate, who to beat

> **Reference, not a design spec (2026-07-16).** This is living
> competitive intel over the Agent Research Hub — the incorporate/beat
> reading of each tracked system. It informs `6/1`'s adoption strategy
> but specs no arizuko behaviour of its own; keep it current as the Hub
> moves, don't treat it as an implementation queue.

Two questions per system in the Agent Research Hub
(`krons.fiu.wtf/pub/krons/agents/`), and only two:

1. **Incorporate** — does it own an orthogonal _mechanism_ arizuko lacks and
   could port or interop? These feed the agentic reimplementation loop
   (`1-adoption-interop.md`).
2. **Beat** — is it a competing _product_ where arizuko's folder coordinate
   (path = tenant + ACL + route + egress + web host + file tree) wins
   head-to-head? These feed the campaign track.

A system can be both. Most are neither — say so.

## Incorporate — mechanisms to build (feed the loop)

Ranked by signal strength and gap-fit. Gaps named in `USELESS.md` §5.

### 1. Host-boundary credential injection — the strongest signal in the hub

**nemoclaw**, **muaddib**, and **ironclaw** — three unrelated codebases —
independently converged on the same fix for arizuko's gap #1 (secrets enter the
container). The secret is substituted at a host-side proxy/MITM layer and never
exists in the sandboxed runtime:

- **nemoclaw** — `inference.local` via the OpenShell gateway; credential injected
  by an MCP proxy, requires `--network none`. The hub page carries a literal
  `ADOPT` block naming arizuko's exact gap.
- **muaddib** — host/guest split; `resolveGondolinEnv()` substitutes secrets at
  the host MITM layer, never plaintext in the QEMU guest (v2.4.0).
- **ironclaw** — `credential_injector.rs`: WASM tool requests HTTP → host matches
  by host-pattern → decrypts → injects header/param; tools never see values.

Triple convergence means this is load-bearing, not exotic. arizuko already has
the spec (`specs/8/Z-egred-mitm.md`, "planned, not shipped"). **Ship it — it is
the lead item of phase 6.** centaur's iron-proxy (below) is the fourth witness.

### 2. Durable replay / checkpointing — gap: none today

- **centaur** — `ctx.step` checkpoint/replay (Absurd-inspired), production at
  Paradigm since Jan 2026. Also carries the credential firewall above — closes
  two gaps at once.
- **dbos** — orchestrator-less durable execution: durability is a _library_, the
  owning DB is the only dependency. This matches arizuko's per-daemon-owns-its-
  SQLite design better than centaur's Postgres+pod. **Adopt the pattern** —
  checkpoint turn/step state as rows in routd/runed's own DB — not the library.
- Secondary witnesses: activegraph (event-log-as-agent, fork-and-diff), flue
  (repair-on-resume). Note as direction; both conflict with the relational model.

### 3. Behavioral verification (eBPF) — gap: secrets/trust, defense-in-depth

- **agentsight** — eBPF kernel-boundary tracing (TLS-tap via uprobes on
  SSL*read/write + syscall trace), 2.9% measured overhead, PACMI'25. Verifies
  what the container \_actually did* vs what the agent _claimed_. Complements the
  credential firewall and the existing `obs/` OTLP wiring (`specs/5/O`).

### 4. Faster spawn / stronger isolation — gap: per-turn Docker cold start

- **forkd** — warm-snapshot CoW fork for KVM micro-VMs: boot once, fork children
  in ~100ms. Directly attacks runed's per-turn container cold start (the same
  cost measured on the marble bot) AND upgrades container → micro-VM. Gated: Rust,
  alpha, pre-1.0.
- **edera** — per-agent dedicated kernel (Type-1 Xen), drop-in K8s RuntimeClass,
  0.9% CPU overhead. Genuine isolation upgrade, zero app change — gated on arizuko
  ever running Kubernetes (pure docker-compose today). Hardening path, not now.

### 5. Multi-runtime routing — gap: one runtime (Claude Code only)

- **brainpro** — `RouteCategory` routes per task type across vendors
  (planning→Qwen, coding→Claude, exploration→GPT) under health/cost/context/ZDR
  constraints with fallback chains. The concrete pattern for arizuko's "one
  runtime" gap — and the natural substrate for the interop thesis (run _their_
  model/harness inside a folder). Open a multi-runtime spec off this.

### 6. Pre-container content gate + restart-race fix — gap: content-blind dispatch

- **nanoclaw** — two directly portable, line-cited mechanisms: (a) `command-gate.ts`,
  a 64-line pass/filter/deny classifier that runs _before_ container wake — a slot
  arizuko lacks (routing/grants run on dispatch, not on already-routed content;
  ties to `specs/5/6` proactive-interjection); (b) `on_wake` + `isFirstPoll`
  fixes a restart race where a wake message is stolen by a dying container in its
  SIGTERM grace window — a race arizuko's in-memory `steeredTs` only partially
  covers (and one adshaus hit on a redeploy 2026-07-10).

### 7. Skill threat scanner — gap: half-adopted, confirm wired

- **hermes** — `skills_guard.py` (932 LOC regex ladder: exfiltration, injection,
  destructive, persistence, supply-chain, secrets, invisible-unicode → safe/
  caution/dangerous). Already tracked in memory + spec'd at `specs/5/23-skill-guard.md`.
  **Verify it is actually wired, not just cited.**

### 8. Lower-priority patterns (specify, don't depend on the source)

- **taskforge** — human-gated capability escalation (agent requests capability →
  operator approves → environment rebuilt → resume). arizuko's grants have no
  "request new capability" verb. Worth a grants-extension spec; source repo is tiny.
- **elizaos** — `character.json` as a single-file portable, forkable agent identity
  (no code, no creds). arizuko's PERSONA.md is close but not a self-contained
  artifact. Plus the plugin permission-manifest model.
- **ironclaw** — WASM capability-based tool sandbox as a lighter alternative to
  Docker-per-tool for arizuko's own tool layer.

## Beat — products the folder coordinate outclasses (feed the campaign)

**First, a layer correction.** arizuko is orchestration, not a harness
(`1-adoption-interop.md`). A _harness_ with "no tenant isolation" is not a
competitor to beat — it is a runtime to **orchestrate** (it lacks exactly the
tenancy + human-in-the-loop ownership arizuko adds; that is the interop case).
Only systems that themselves try to host many agents/users are genuine
head-to-head _orchestration_ peers.

So read the table below in two groups:

- **Orchestrate, don't beat (harnesses):** openclaw, cline, brainpro, ironclaw —
  single-agent runtimes. Their weakness _is_ the reason to run them inside a
  folder. "Beat" here means "the campaign shows what arizuko adds on top," not
  "replace."
- **True head-to-head (orchestration / multi-tenant peers):** muaddib, elizaos,
  home23 — they host many, but without the folder coordinate or the
  agents-reshape-it-you-own-it loop.

The pitch is one line either way: the folder coordinate fuses tenant, ACL, route,
egress, and host into one path, enforced by ephemeral per-turn Docker — and every
change the agent makes to the system is a file you own, not convention and not a
shared process.

| System       | Their weakness (cited)                                                                                     | The wedge                                                                     |
| ------------ | ---------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| **muaddib**  | Strongest micro-VM isolation in the hub, yet one VM per _channel_, all users share it, zero auth           | "Strong isolation, weak tenancy" — the sharpest single head-to-head           |
| **openclaw** | 24 channels, one process, no per-user isolation, no horizontal scale, "involuntary subagent injection"     | "One process, one blast radius" vs "N tenants, N containers, folder ACL"      |
| **cline**    | "No sandbox… runs commands with full user permissions," no persistent memory — yet 4M installs, enterprise | per-turn Docker + folder ACL vs zero tool-execution sandbox                   |
| **home23**   | "Isolation" = port numbers on a shared filesystem/process space                                            | textbook example of the exact gap the folder container closes                 |
| **brainpro** | No sandbox; session-id gating in one shared Unix-socket daemon — "not isolation"                           | same shape as openclaw/cline                                                  |
| **elizaos**  | The only horizontally-scalable system — but scales _compute_, no tenant containment                        | for a buyer who needs scale AND hard separation                               |
| **ironclaw** | Single-user by design (`FEATURE_PARITY.md`) — no multi-agent routing                                       | folder coordinate scales `solo/inbox` → `corp/eng/sre/oncall`, same code path |

## Ignore (with reason)

- **claude-code-internals** — this _is_ arizuko's runtime substrate, not a rival;
  keep as the internals reference (confirm subagent tool-scoping still mirrors it).
- **axoniq** — JVM/commercial event-sourcing for regulators; arizuko has `audit_log`.
- **graphify** — codebase→graph dev tooling; headline metric self-debunked
  on-page. Not adopted — the graph/taxonomy demand it rides is answered
  non-intrusively via the demand-class mode in `6/1`.
- **smolagents** — code-as-tool dev framework; no tenancy story.
- **milady** — consumer desktop "AI OS"; process-level default isolation.
- **activegraph / contextlattice** — infra for problems arizuko doesn't have
  (graph-native state; single-node context freshness). activegraph's graph
  demand: see the demand-class mode in `6/1`, not a replatform.

## Sequencing

1. **Credential injection** (`specs/8/Z`) — lead item; four witnesses, closes gap #1.
2. **Turn durability** (dbos pattern, arizuko-DB rows) — closes the replay gap.
3. **Multi-runtime routing** (brainpro pattern) — unlocks the interop thesis: run
   another harness/model inside a folder.
4. **Content gate + restart-race fix** (nanoclaw) — cheap, closes live races.
5. Campaign in parallel: publish the BEAT wedges on the hub as head-to-head pages,
   led by muaddib (strong-isolation-weak-tenancy) and cline (enterprise reach, no
   sandbox).

## Ties

`1-adoption-interop.md` (the strategy) · `USELESS.md` (the gaps this closes) ·
`specs/8/Z-egred-mitm.md` · `specs/5/22-self-learning.md` (the loop) ·
`specs/5/23-skill-guard.md` · the Agent Research Hub (source of truth).
