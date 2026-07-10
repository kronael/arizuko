---
status: draft
---

# Adoption — interop first, campaign second, agentic reimplementation as the engine

The end goal is blunt: people stop running their own agent harness and run
arizuko instead. This spec is the path to that, and the honest reason it might
not be persuasion.

## Problem

arizuko competes in a crowded field (see the Agent Research Hub + `USELESS.md`):
Claude Code, OpenClaw, NanoClaw, ElizaOS, Hermes, and a new one every week. Each
already runs someone's agents today. Asking them to rip that out and adopt
arizuko is a high switching cost against a working incumbent. A better mousetrap
loses to the mousetrap already screwed to the wall.

The orthogonal component is real but narrow (`specs/5/A`, `USELESS.md` §4): the
folder coordinate — one path is tenant, ACL, route, egress, web host, and file
tree — and the web-native building blocks (publish = a file write). That is the
layer _above_ the harness. It does not require replacing the harness to be
useful. That is the wedge.

## Two tracks, ranked

**1. Interop first (primary).** Do not ask anyone to switch. Let arizuko wrap
what they already run:

- **Runtime pluralism.** runed spawns "just another binary." Today that's Claude
  Code. Make the runtime swappable so an OpenClaw / a raw model / another harness
  runs inside an arizuko folder-agent. arizuko becomes the multi-tenant + routing
  - web layer over _their_ agent, not a replacement for it. (`USELESS.md` §5.3
    names this gap: one runtime today.)
- **MCP as the seam.** arizuko is already MCP-native (agent side) + REST (human
  side). Every capability another harness exposes over MCP is reachable; every
  arizuko resource is reachable by their agent. Interop is protocol work, not a
  rewrite.
- **Import, don't convert.** Read their config: a Claude Code `settings.json` +
  skills, an OpenClaw channel map, a character.json. Mount it into a folder
  as-is. Adoption cost → near zero: keep your harness, gain folders + tenancy +
  egress + web.

**2. Campaign second (supporting).** Persuasion rides on proof, not slogans:

- The Agent Research Hub (comparative, source-cited) is the credibility asset —
  it already reads other systems honestly.
- `USELESS.md` is the counter-marketing move: the self-critique that makes the
  honest claims believable.
- The `web-native Linux` framing (`specs/5/A`) is the one-line pitch.
- Campaigning without the interop is asking people to take a leap. Interop makes
  the pitch "add arizuko," not "replace your stack."

## The engine — an agentic reimplementation loop

The interop surface is large (N harnesses × their features) and moving (each
ships weekly). Hand-building it does not scale. Dogfood instead: arizuko's own
agents build arizuko's interop.

A scheduled loop (`timed` → a folder-agent), one system at a time:

1. **Read** the target system's source (the Hub already clones repos into
   `refs/`) and its released changes since last run.
2. **Extract** the orthogonal component + the config/protocol surface an arizuko
   folder would need to host or import it.
3. **Reimplement / shim** the smallest interop that lets an arizuko folder run or
   import that system, in an isolated worktree.
4. **Verify** end-to-end (spawn a folder on that runtime, drive a turn), gate on
   an adversarial check, open the change for operator review.
5. **Record** in the Hub (that system's page) + a compatibility matrix.

This reuses machinery that already exists: the Hub's monthly code-analysis
protocol, worktree-isolated subagents, the migrate/broadcast delivery path. The
loop is the same shape arizuko already runs on itself (`specs/11/7` self-learning);
here it points outward.

## Non-goals / honest risks

- **Not a shim graveyard.** Interop that dilutes the folder coordinate is a
  liability. Every adapter must keep tenancy + egress + ACL intact, or it is not
  worth carrying.
- **The moat must stay the reason.** If arizuko is only "compatible with X," X
  wins on maturity. Interop is the on-ramp; the folder coordinate is why they
  stay. Lead with the wedge, not the compatibility list.
- **Validation is still autobiographical** (`USELESS.md` §3). One external
  operator running one imported harness inside arizuko is the first real proof —
  optimize the loop for that, not for feature count.
- **The reimplementation loop is unproven and expensive.** Scope it to one
  target, measure whether the interop it produces is actually adopted, before
  scaling the fleet.

## Ties

`specs/5/A` (positioning) · `USELESS.md` (honest gaps this closes) · the Agent
Research Hub (source of truth on targets) · `specs/11/7` self-learning (the loop,
turned outward) · runtime pluralism in runed.
