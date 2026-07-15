---
status: draft
---

# arizuko as a toolbox — the components, usable separately

Two framings of one artifact, both true:

1. **A way to run your agents** (integrated) — the platform: the folder
   coordinate + daemons that run any harness _correctly, at scale, in your
   infra_ (tenancy, keys, egress, auth, cost, audit, web). This is the bet
   (`6/4`).
2. **An agglomeration of tools** (decomposed) — independently-useful
   components. Use the whole platform, or **raid it for the one part you need.**

Not a pivot. What an orthogonal shippable component _is_ — a sibling dir
that builds/tests/ships/runs without arizuko, zero arizuko-internal imports,
consumed only through a published surface — is defined in `6/7`; the
four-layer stack (primitives → **components** → daemons → products, `5/A`)
already names Components as a layer. This spec makes the toolbox real:
_identify which pieces stand alone, and how to present them._

**Why both.** The platform is a bet (multi-tenant control plane + adoption). The
tools are useful **whether or not the bet lands.** Shipping tools de-risks the
platform — the same artifacts serve both futures, and we lose nothing by not
betting the whole thing on adoption.

## The pieces — measured, not asserted

A component is standalone-ready only if it does not import arizuko-internal
packages (the import-graph rule, `CLAUDE.md`). Internal-dep counts measured
2026-07-14 (`grep` over each package's imports):

| Component                                   | internal deps                               | readiness                             | what it is / who wants it alone                                                                                                                                                                                                                                                                                                                                                                 |
| ------------------------------------------- | ------------------------------------------- | ------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **crackbox**                                | 0                                           | ⚠ egress **fails open** (BUGS, HIGH)  | egress proxy + KVM sandbox — but per-folder default-deny is **not enforced today** (routd swallows the allowlist-resolve error → nil list → fail-open). Fixing that is the real "ship it" gate, not a README.                                                                                                                                                                                   |
| **dockbox** _(concept)_                     | in `container/` (11)                        | candidate — heavy decouple            | "how you run a container with an agent": `docker run` + mounts + skill-seed + MCP socket. The **Docker sibling to crackbox's KVM**. Today coupled in `container/`; a clean `dockbox` is a decouple task.                                                                                                                                                                                        |
| **servekit** _(concept)_                    | proxyd 8 · webd 8 · dashd 13 · vited/davd 0 | candidate — heavy decouple            | "connect an agent to the web + run/observe/control it as a service": auth-gated reverse proxy (`proxyd`) + token chat/SSE/webhooks/`/me` portal (`webd`) + static `/pub` `/priv` (`vited`) + operator console (`dashd`) + WebDAV workspace (`davd`). Deeply arizuko-domain today (folder identity, `routd.db` reads, `groups/` FS); a decouple/naming candidate like dockbox, not a standalone. |
| **obs**                                     | 0                                           | **ships now**                         | opt-in tri-substrate observability — `audit_log` + journald + OTLP with turn-scoped TraceIDs — for any Go daemon. `defer obs.Setup(name, instance)()`.                                                                                                                                                                                                                                          |
| **router**                                  | 1 (`core`)                                  | near — lift `core` types              | routing-table DSL (match→target, `#observe`/`#announce`/topics, shadow detection). Anyone routing events to handlers.                                                                                                                                                                                                                                                                           |
| **grants**                                  | 2 (`core`,`store`)                          | near — needs a store seam             | capability-auth DSL `[!]action(param=glob)` + folder-containment. Anyone needing per-actor scoped grants.                                                                                                                                                                                                                                                                                       |
| **resreg**                                  | 4 (`audit`,`auth`,`core`,`store`)           | **roadmap = `mcpfw`**                 | one handler → REST + MCP + OpenAPI + YAML + injected Gate. The two-face API engine (`5/17`). Needs a substrate interface to decouple.                                                                                                                                                                                                                                                           |
| store, auth, chanlib, chanreg, ipc, compose | 3–11                                        | platform-internal — **don't extract** | these _are_ the platform; lifting them out is a category error.                                                                                                                                                                                                                                                                                                                                 |

Honest read: **`obs` is the only piece that plausibly ships as-is.** `crackbox`
looked like the sure thing (0 deps, production proxy) — but its per-folder egress
is currently **fail-open**, an OPEN HIGH bug (`BUGS.md`, 2026-07-14). router/grants
are a small decouple; resreg is the `mcpfw` roadmap; dockbox names a concern worth
a clean extraction later. `core`/`theme` are 0-dep but low-value alone.

**Caveat — readiness here is a hypothesis, not a verdict.** Import-purity (0
internal deps) means a package _compiles_ alone; it does NOT mean it _works_
alone, has a clean documented contract, or that its guarantee holds. crackbox is
the counterexample: 0 deps **and** broken. Deciding _what_ each component
actually is, and _how_ to extract it cleanly, is real per-component design +
hardening work — this table is where that starts, not its result.

**ant is not in this table — on purpose.** ant is not a shippable component; it
is (a) the **interface** into arizuko (`ant <folder> [prompt]`, the `ant/ant`
launcher over the `arizuko-ant` image) and (b) a **composition template** — the
worked example of how the pieces fit for someone building their own. What's
extractable _under_ ant is the runner (**dockbox**/crackbox), not ant itself.
The standalone ant already exists (in TS); there is nothing to "extract" — there
is a thing to _point people at as the reference_. (This is the salvaged kernel
of the dropped `13/b`; the Go rewrite is gone, the "agent-as-a-folder + how it's
run" idea lives here.)

## The web-and-service stack — `servekit` (working name, tentative)

The operator's fifth candidate: "connect your agent to the web and give a good,
organized way to run and manage it as a service." arizuko already ships this —
as a _stack of daemons_, not one package: `proxyd` (auth-gated reverse proxy,
per-world vhosts, runtime route table), `webd` (the `web` channel — SSE hub,
token-scoped `/chat/<token>/` + `/hook/<token>`, the authed `/me` portal and
`/mcp` bridge), `vited` (`/pub` `/priv` static origin), `dashd` (the operator
HTMX console — routes, secrets, grants, tasks, audit), and `davd` (WebDAV
workspace over upstream `dufs`). Together: give an agent a web presence, then
run/observe/control it as a service.

Run the three questions (`6/6`) honestly, and the readiness is the same shape as
dockbox, not obs:

- **Contract** — "put an agent on the web and manage it as a running service:
  public site + token chat + webhooks + file access, plus an operator console
  for routes/secrets/grants/tasks/audit."
- **Does it hold standalone?** No. `proxyd` stamps folder/JID identity, `webd`
  registers as the arizuko `web` channel and reads `routd.db`, `dashd` is ~39
  arizuko-specific routes over `routd.db`/`messages.db` + the `groups/` FS. Only
  `vited` (generic Vite static) and `davd` (upstream `dufs` + a healthcheck
  wrapper) are near-generic. Internal deps: proxyd 8, webd 8, dashd 13.
- **Stranger vs cruft** — a stranger gets the _shape_ (auth-gated reverse proxy
  with per-path tiers, a URL-token web chat + SSE, webhook ingest, a WebDAV
  browser, an operator console). The arizuko-shaped cruft is the bulk:
  folder-as-identity, JID routing, the `routd.db`/`groups/` layout, and the
  resreg two-face + grant/tier gate.

So `servekit` is **platform-internal / coupled today** — a decouple-and-name
candidate like `dockbox`, **not** a shipped standalone. The name is tentative
(alternatives floated: `agentweb`, `webbox`); pick one only if the decouple work
is actually scheduled.

**Rebrand angle (proposal — recorded, not adopted).** The operator floated this
component as a possible _headline identity for arizuko itself_: "connect your
agent to the web and run it as a managed service." Captured here as a candidate
positioning line for the operator to decide — **not** a change to the `5/A`
grand-message framing (primitives → components → daemons → products, ownership +
language-shaping as the two halves). Non-committal by design; a rebrand needs
sign-off, and this component is a decouple candidate, not shipped.

## How to present them

- **Packaging.** One `go.mod` today (`CLAUDE.md`); orthogonality is enforced by
  the **import-graph rule, not a module split.** Each standalone piece keeps its
  top-level dir + a README with a standalone pitch + a `cmd/` when it's a binary
  (crackbox has this). **No premature module split** — the import rule is the
  guarantee; split only if an external consumer actually needs a separate module.
- **The toolbox surface.** A "Components" page (web docs `reference/components/`
  - a README section): each piece with one-line what-it-does, standalone status
    (ships / roadmap), an install-or-import snippet, and a link to its spec. Lead
    with the two that ship — crackbox, obs.
- **The dual pitch.** Landing line: **"Run your agents in your infra — or take
  the parts. arizuko is a platform _and_ a toolbox."** Platform pitch for
  adopters; per-tool pitch for the dev who just wants egress control or a
  two-face API and will never run the whole thing.
- **Per-tool front door.** crackbox already has one (`crackbox proxy serve`,
  specs `6/8`·`6/10`). obs needs a 2-paragraph standalone README + a drop-in
  example. resreg→`mcpfw` needs the decouple _before_ it can be presented as
  standalone.

## What has to happen (minimal, ordered by ROI)

1. **crackbox** — **fix the fail-open first** (`BUGS.md`, 2026-07-14):
   fail-CLOSED on an empty per-id policy, surface (don't swallow) routd's
   allowlist-resolve error, add a containment test (tight allowlist → 403 on a
   non-listed host). A security proxy that fails open is not shippable. THEN
   sharpen the README + demo. (Needs a focused crackbox audit; not a one-liner.)
2. **obs** — write the standalone README + drop-in example (import,
   `defer obs.Setup(...)()`, the three env vars). **Zero code.**
3. **router / grants** — introduce a thin seam so they stop pulling
   store/heavy deps (small decouple), or document them "liftable on request."
4. **resreg → `mcpfw`** — the substrate interface (store/auth/audit behind an
   interface). This is the _one real engineering task_ here, and it's the
   `mcpfw` aspiration already named in `CLAUDE.md`.

## What we do NOT do

- No module split (the import rule suffices).
- No standalone extraction of platform-internal packages (store/container/ipc/…)
  — category error.
- No marketplace, no model-agnosticism in core (the harness's job) — carried
  from `6/4`.

## Ties

`6/7` (what a component is — the pattern this catalogue applies) · `6/6`
(the extraction method) · `6/1` (adoption strategy) · `6/4` (fleet services) ·
`5/A` (four-layer stack — Components is a layer) · `6/8` (crackbox standalone) ·
`5/17` (resreg two-face → `mcpfw`) · `obs/` · `proxyd`/`webd`/`vited`/`dashd`/`davd`
READMEs (the `servekit` stack) · `CLAUDE.md` (shippable siblings + import-graph
rule).
