---
status: draft
---

# arizuko — 10-slide deck

A first cut. Each slide: the one message, the support, and a `viz:` hint for
what to show. Voice is warm-caveman — concrete, evidence where it sharpens, no
theatre. Take it from here.

---

## Slide 1 — Orchestration for agents

**The layer above the harness — where agents reshape the system and you stay in
the loop.**

- Models get better. Harnesses get better. That race isn't the one arizuko runs.
- arizuko decides _how agents run and for whom_, and keeps the human owning the
  system even as the agents rewrite it.
- A web-native Linux for agents: small primitives you compose, not a platform
  you rent.

`viz:` the wordmark; one line under it; a single folder path
`corp/eng/sre/oncall` that lights up as tenant + ACL + route + egress + host.

---

## Slide 2 — The problem

**Everyone can build _an_ agent. Nobody's building the layer that runs _many_,
for many people, safely — and keeps the owner in control when the agent changes
things.**

- One agent for one user is solved five times over (see next slide).
- The unsolved layer: multi-tenancy (whose agent, whose data, whose budget),
  and ownership (the agent edits its own persona/skills/routing — who's
  accountable?).
- Today that layer is hand-rolled per company, or skipped.

`viz:` a wall of agent logos (harnesses) → an empty box labelled "who runs
these, for whom, and who owns the changes?"

---

## Slide 3 — The space: three layers, not one

**The field looks crowded because three different layers are lumped together.**

- **Harnesses** (run one agent): Claude Code, OpenClaw, Cline, NanoClaw.
- **Platforms** (host agents): ElizaOS, Muaddib, Hermes, HOME23.
- **Infra** (make agents durable/isolated): Centaur, DBOS, forkd, Edera.
- arizuko sits at the _orchestration_ seam — above harnesses, beside platforms,
  consuming infra.

`viz:` a three-tier stack; arizuko drawn as the orchestration band that spans
harness + platform, not a fourth logo in the pile.

---

## Slide 4 — How the best of them solve it

**Each owns exactly one orthogonal thing. That's the honest strength — and the
tell.**

- OpenClaw: 24 channels from one process, no message bus.
- Muaddib: QEMU micro-VMs — API keys physically can't enter the sandbox.
- ElizaOS: the only horizontally-scalable one (Postgres + pgvector).
- NemoClaw / IronClaw / Centaur: inject secrets host-side — the agent never
  holds a key.
- Source-read, not marketing (the Agent Research Hub).

`viz:` a 2-column table: system → its one orthogonal component. Clean, cited.

---

## Slide 5 — The empty column

**Run the four lenses across the whole survey. One column is blank for everyone:
multi-tenancy with real isolation. And nobody makes the agent-owned system
auditable.**

- OpenClaw: no per-user isolation. Muaddib: one VM per _channel_, all users
  share it, zero auth. ElizaOS: scales compute, not tenant separation.
- They isolate the _process_ or the _secret_. None isolates the _tenant_ —
  container + egress + ACL bound to one identity.
- And when the agent rewrites its own config, none of them makes that a change
  you can diff and revert.

`viz:` the survey matrix; every cell filled except the "multi-tenant isolation"
and "owner-in-the-loop" columns — a stark vertical blank.

---

## Slide 6 — What arizuko is: the folder coordinate

**One path is simultaneously tenant, ACL scope, route target, egress scope, web
host, and file tree. That single coordinate is the orchestration.**

- `solo/inbox` and `corp/eng/sre/oncall` run the _same code_ — depth is the only
  difference.
- Add a tenant = add a folder + rows. The daemon graph never changes.
- Per-folder container, per-folder egress allowlist, per-folder ACL — isolation
  is the coordinate, not a bolt-on.

`viz:` one folder path exploding into its six simultaneous meanings.

---

## Slide 7 — The twist: agents reshape it, you stay in the loop

**The agent doesn't just answer — it edits the files that _are_ the system. And
every edit is yours to see, gate, and undo.**

- Persona, skills, routing, even child groups are files the LLM writes.
- Grants gate what it may change; the audit log records it; `git revert` undoes
  it. Self-modifying, human-owned.
- Nobody else in the survey makes the agent-authored system a diffable,
  revertable artifact.

`viz:` split screen — left: agent editing `PERSONA.md` / adding a route; right:
`git diff` + a grant check + a revert button.

---

## Slide 8 — How arizuko is useful: add, don't replace

**Keep your harness. Run it inside a folder. Gain tenancy, egress, web, and
ownership on top.**

- runed spawns "just another binary" — today Claude Code, tomorrow your harness,
  a raw model, or someone else's.
- Import their config as-is (a `settings.json`, a channel map). Adoption cost →
  near zero.
- Publishing is a file write: every folder gets `/pub` and `/priv`, no deploy
  step, no separate host.

`viz:` a harness logo dropping into a folder; tenancy/egress/web/ownership
rings snapping on around it.

---

## Slide 9 — Honest state (why you can trust the pitch)

**We ship a self-critique with the product. Here's what's real and what isn't.**

- Real: the folder coordinate is live; `marble` answers a 1,590-topic
  prerequisite graph over bundled JSON, no graph DB; three instances self-hosted.
- Not yet: secrets still enter the container (the host-side-injection fix is
  specced, `8/Z`, not shipped); validation is still autobiographical — no
  external installs yet.
- `USELESS.md` is in the repo. Read it before the README.

`viz:` two honest columns — "true today" / "not yet" — and a screenshot of
`USELESS.md`'s title line.

---

## Slide 10 — The ask

**Try it in one click. Self-host in one `tar`. Help us prove it past
autobiographical.**

- Live now: a public `/chat/` link — no signup, no install.
- Self-host: Docker + SQLite on one box; back up with one `tar`.
- Where it's going: host-side credential injection, multi-runtime (run any
  harness), and an agentic loop that builds the interop itself.
- The bet: own the orchestration layer as composable primitives — the web-native
  Linux for agents.

`viz:` the live chat URL as a QR + the one-line `arizuko create` command; the
roadmap as three checkboxes.

---

## Notes for the next pass

- Slides 5 (empty column) and 7 (self-modifying, human-owned) are the crux —
  everything else sets them up. If the deck gets cut to 5, keep 2, 5, 6, 7, 10.
- Every competitor claim traces to a hub page; keep the citations when this
  becomes a real deck so it survives a skeptical read.
- Decide the audience before styling: an operator/CTO deck leans on slides 6–9;
  an investor deck leans on 2, 5, 10.
