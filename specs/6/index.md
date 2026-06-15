---
status: active
---

# specs/6 — operator cockpit

Every daemon serves its own dashboard from its own `/dash/<daemon>/`
HTMX namespace, rendering its own source and reading/writing only
through its own `/v1` surface — it observes AND controls every aspect
of itself. A lean `dashd` hub probes and links them, AWS-Console style.
One renderer per daemon, one hub.

[`1-cockpit-index.md`](1-cockpit-index.md) is the anchor: architecture,
the `/v1`-only read-path, routing, auth, theme, non-goals, and the
per-daemon spec template. Every other spec points back to it and adds
only its own page list + show/control matrix.

## Specs

| Spec                                                                                 | Status | Covers                                                                                                                                                                                                                  |
| ------------------------------------------------------------------------------------ | ------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [1-cockpit-index.md](1-cockpit-index.md)                                             | draft  | architecture, `/v1` read-path, routing, auth, theme, non-goals                                                                                                                                                          |
| [2-dashd-hub.md](2-dashd-hub.md)                                                     | draft  | dashd hub + retained cross-cutting operator pages                                                                                                                                                                       |
| [3-routd-dashboard.md](3-routd-dashboard.md)                                         | draft  | queue, circuit breaker, channel-registry health, errored chats                                                                                                                                                          |
| [4-runed-dashboard.md](4-runed-dashboard.md)                                         | draft  | active runs, history, capacity, broker tokens, kill run                                                                                                                                                                 |
| [5-authd-dashboard.md](5-authd-dashboard.md)                                         | draft  | keys, tokens, OAuth providers, identity links                                                                                                                                                                           |
| [6-proxyd-dashboard.md](6-proxyd-dashboard.md)                                       | draft  | live route table, denials, auth transit                                                                                                                                                                                 |
| [7-onbod-dashboard.md](7-onbod-dashboard.md)                                         | draft  | admission queue, gates, invites                                                                                                                                                                                         |
| [8-timed-dashboard.md](8-timed-dashboard.md)                                         | draft  | scheduled tasks, next ticks, recent runs                                                                                                                                                                                |
| [9-crackbox-dashboard.md](9-crackbox-dashboard.md)                                   | draft  | egress policy, blocked attempts, registrations                                                                                                                                                                          |
| [10-webd-davd-ttsd-dashboard.md](10-webd-davd-ttsd-dashboard.md)                     | draft  | thin web/file/voice surfaces, combined                                                                                                                                                                                  |
| [11-adapter-contract.md](11-adapter-contract.md)                                     | draft  | shared adapter dashboard grammar + health model                                                                                                                                                                         |
| [12-whapd-teled-slakd-dashboard.md](12-whapd-teled-slakd-dashboard.md)               | draft  | session chat adapters                                                                                                                                                                                                   |
| [13-mastd-bskyd-reditd-linkd-dashboard.md](13-mastd-bskyd-reditd-linkd-dashboard.md) | draft  | stream/poll social adapters                                                                                                                                                                                             |
| [14-discd-emaid-twitd-dashboard.md](14-discd-emaid-twitd-dashboard.md)               | draft  | mixed gateway adapters                                                                                                                                                                                                  |
| [41-social-adapter-model.md](41-social-adapter-model.md)                             | draft  | Boundary: social adapters (bskyd/mastd/reditd/twitd/linkd — asymmetric visibility, selective engagement) vs chat adapters (bidirectional, push-events). Open: observer delivery, triage skill, rate-limit coordination. |

Supersedes `specs/10/18-daemon-dashboards.md`; reconciles the shipped
`3/d`, `4/Q`, `4/V` (see `1-cockpit-index.md` "Reconciliation").
