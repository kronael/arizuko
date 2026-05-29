---
status: draft
---

# specs/15 — multiplayer: shared sessions, durable streams, presence

What we steal from **Centaur** (paradigmxyz/centaur — "multiplayer,
self-hosted, secure agents", open-sourced 2026-05-21; Slack-native,
FastAPI control plane, Postgres state, per-thread sandbox) and from the
**krons agents hub** (`https://krons.fiu.wtf/pub/krons/agents/` — a
published static catalog comparing eleven agent frameworks, arizuko
among them).

Centaur and arizuko are near-twins in shape: both are self-hosted
multi-tenant agent routers with channel adapters, per-conversation
container isolation, and network-level credential injection. arizuko
already matches or beats Centaur on isolation (crackbox vs iron-proxy),
secret broker, per-folder identity, and surface uniformity (MCP+REST).
The honest gaps are all on the **multiplayer** axis — Centaur's word
for _multiple humans collaborating in one shared, observable,
crash-durable agent session_:

- arizuko's live stream (`5/J` SSE) is **best-effort**: no event-id
  cursor, no replay, drops slow clients silently. Centaur's
  `GET /agent/threads/{key}/events?after_event_id=` replays from a
  durable event log and emits a terminal snapshot on reconnect.
- arizuko has **no final-delivery guarantee**: poll-based outbound, but
  no retry-until-acked outbox for the terminal reply. Centaur has
  `agent_final_delivery_outbox`.
- arizuko has **no presence**: a shared web chat can't show who else is
  watching or that the agent is mid-turn.
- arizuko has **no published agent catalog**: the operator sees groups
  behind dashd admin auth, but there's no multiplayer "what agents live
  here" surface — which is exactly what the krons hub demonstrates as a
  publishable artifact.

Each row below is a draft spec for one such gap, fit to arizuko's
existing daemons (routd owns the event store + loop; webd is the SSE
sink; vited publishes static `/pub`). Nothing here adds a foreign
runtime, a workflow engine (that overlaps `12/6`), or Kubernetes.

| Spec                                                         | Status | Hook                                                                                                                                 |
| ------------------------------------------------------------ | ------ | ------------------------------------------------------------------------------------------------------------------------------------ |
| [1-durable-turn-stream.md](1-durable-turn-stream.md)         | draft  | Event-id'd turn event log in routd; `5/J` SSE + `get_round` MCP replay from `after_event_id`, terminal snapshot on reconnect.        |
| [2-final-delivery-outbox.md](2-final-delivery-outbox.md)     | draft  | Retry-until-acked outbox in routd for the terminal channel reply; survives adapter/process restart.                                  |
| [3-shared-session-presence.md](3-shared-session-presence.md) | draft  | Presence frames on the webd hub: who is subscribed to a (folder, topic), and an agent-working indicator.                             |
| [4-agents-catalog.md](4-agents-catalog.md)                   | draft  | Published `/pub/<group>/agents/` catalog of an instance's folders + one-line capability — multiplayer discoverability, vited-served. |
