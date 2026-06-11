---
status: draft
references: specs/16/9-positioning.md
source: aeon-vs-arizuko comparison (2026-05-22)
---

# 6/4 — Positioning: componentized vs monolithic-in-GHA

Captures the architectural differentiator that surfaced studying Aeon.
Aeon is a monolith inside a GitHub Actions runtime. arizuko is a
componentized platform of small daemons sharing a SQLite schema and a
message bus. The componentized shape — not the realtime-vs-batch
execution model — is the real differentiator.

This spec updates the positioning story; no code lands here. The
implementation it enables (each daemon foldable into an agent) is
flagged for future phases.

## What Aeon got right

One repo. Skills are MD files. Triggers in YAML. Outputs commit
themselves. Easy to fork, easy to read, easy to copy. The whole
thing is `git clone aeon && add .github/workflows/me.yml`.

The cost: GHA constrains everything. No long-running state, no
realtime channel adapters, no per-tenant isolation. Aeon's
"self-healing" reduces to a clever cron loop.

## What arizuko has

The daemon layer is the third of four in the canonical stack —
primitives → components → daemons → products
([specs/5/A](../5/A-primitives-framing.md) § "The four layers").
"Components" there are the Go packages each primitive lives in; the
daemons below bundle those packages into deployable processes.

A graph of one-thing-each daemons sharing the schema in
`store/messages.db`:

| Daemon        | One thing                                             |
| ------------- | ----------------------------------------------------- |
| `gated`       | message bus + dispatch + schema owner                 |
| `timed`       | scheduled message producer                            |
| `webd`        | reverse proxy + slink chat widget                     |
| `proxyd`      | auth-gated proxy in front of every adapter            |
| `onbod`       | user onboarding + admission queue                     |
| `vited`       | static web docs server                                |
| `davd`        | per-folder workspace + filesystem-as-context          |
| `dashd`       | operator dashboard                                    |
| `crackbox`    | egress proxy with per-source allowlists               |
| `ttsd`        | TTS / voice synthesis                                 |
| `teled` / ... | one channel adapter per platform (Telegram, Slack, …) |

Plus integrations (Whisper STT, the oracle hook, the secret broker).

Replaceability is the design: any daemon could be reimplemented in
any language, in a different VM, deployed separately, as long as it
reads/writes the schema correctly. The seam is **SQLite schema +
message verb protocol** — not a Python API, not a config DSL.

## The folding pattern: every daemon is a candidate agent

Yesterday's [web-native-agents](../../template/web/pub/concepts/web-native-agents.html)
framing said it: every automation in arizuko is _also_ a folder. Once
a daemon is replaceable, it's a candidate for being replaced by an
agent-as-daemon — a folder whose persona is "be the scheduler" or
"be the auth gate" or "be the proxy".

Some daemons fold cleanly:

- `timed` could be an agent on a 1s cron reading `scheduled_messages`
  and emitting messages. The agent could _reason_ about cadence
  (back off when downstream is rate-limited) instead of being
  hardcoded.
- `onbod` could be an agent that handles the admission conversation
  directly. Today the conversational parts are already agent-driven
  via the admission queue; the daemon just gates.

Some won't fold cleanly:

- `crackbox` has to be in the network path. Can't be replaced by an
  LLM that thinks about packets.
- `gated` is the schema owner + dispatch hot path. Latency-sensitive,
  no LLM ever.

But every daemon should be _evaluated_ against the folding question.
This goes hand-in-hand with `12/f-replaceability-research.md` (which
asks "could we drop this for an off-the-shelf alternative?") — the
folding pattern asks "could we replace this with an agent that does
the same job?".

## Updates to `specs/16/9-positioning.md`

Add a new section to that spec:

```markdown
## Why componentized matters

Other code-first platforms (LangGraph, AutoGen) are monolithic
runtimes: agents are objects in one process, coordinated by code in
the same process. arizuko is _componentized_: agents (folders) and
infra (daemons) are separate processes sharing a schema. The seam is
data, not function calls.

This means:

1. Daemons can be reimplemented in any language without touching the
   agent.
2. Any daemon can be replaced by an agent doing the same job, as
   long as it speaks the schema (web-native agents framing).
3. The replaceability story (specs/13/f) and the
   agent-managing-agents story (specs/16/9) compose.
```

And update the "What arizuko Is" framing line:

> Self-hosted agent organization through code. A folder is an agent.
> Daemons are replaceable processes. The seam is data, not function
> calls. Composable, foldable, version-controlled.

## Drop `specs/12/6-workflows.md` (workflowd)

The original case for `workflowd` was a TOML flow engine — declarative
multi-step automations over the shared SQLite. The folding pattern
obviates it: every automation that wants persistence + branching is a
folder with a persona. Workflow steps become messages between folders;
the agent reasons about them.

This is the "automations are folders" reframe. Recommend:

1. Mark `specs/12/6-workflows.md` as superseded.
2. Cross-link this spec as the supersede reason.
3. The use cases workflowd was meant to cover (TOML flow for
   non-conversational batch work) are covered by folder agents +
   `timed` + state-events.

## Pitch-level wording

Three messaging anchors `specs/16/9` should add:

- **"Componentized, not monolithic."** Every daemon does one thing.
  Replace any of them without touching the rest.
- **"Every automation is a folder."** Need a scheduler? Make a
  folder. Need a moderator? Make a folder. The folding pattern
  scales because the seam is the schema.
- **"Realtime by composition, not by execution model."** Aeon does
  one batch every N minutes. arizuko handles realtime because the
  channel adapters are separate daemons; the agents themselves never
  poll.

## Honest gaps

- **Componentization has an ops cost.** N daemons = N systemd
  services, N upgrade paths, N log streams. Aeon's single-repo is
  easier to operate per-deployment. We mitigate via systemd unit
  template + `docker compose`; we don't eliminate the cost.
- **Schema migrations are a chokepoint.** All daemons must agree on
  the schema. `gated` owns migrations; everything else connects
  read/write. Adding a column = coordinated update or backward-compat
  reads.
- **The folding pattern isn't proven.** No daemon has been folded
  into an agent yet. The shape is consistent; the practice is
  forward-looking.

## Out of scope

- Implementation of the folding pattern. Each daemon-as-agent is its
  own spec.
- Marketing copy beyond the three anchor lines.
- A migration guide for users moving from Aeon to arizuko (different
  audience, different shape).

## Decisions

- The differentiator is **componentized + foldable**, not
  realtime-vs-batch. Realtime falls out of componentized; folding
  falls out of "every automation is a folder".
- `specs/16/9-positioning.md` gets a "Why componentized matters"
  section + an updated "What arizuko Is" line. This spec is the
  source.
- `specs/12/6-workflows.md` (workflowd) is recommended for supersede.
  The use cases collapse into "make a folder".
- Daemons are evaluated against two questions: (1) could we drop
  this for an off-the-shelf alternative? (`12/f`), and (2) could
  we replace this with an agent doing the same job? (this spec).

## References

- `specs/16/9-positioning.md` — the doc this updates.
- `specs/14/f-replaceability-research.md` — sibling axis.
- `template/web/pub/concepts/web-native-agents.html` — the prior
  framing of "automations are folders".
- `specs/12/6-workflows.md` — the spec this recommends superseding.
