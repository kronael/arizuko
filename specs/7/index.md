---
status: active
---

# specs/7 — operator cockpit

Every daemon serves its own dashboard from its own `/dash/<daemon>/` HTMX
namespace, rendering its own source and reading/writing only through its own
`/v1` surface — it observes AND controls every aspect of itself. A lean
`dashd` hub probes and links them, AWS-Console style. One renderer per
daemon, one hub.

[`1-cockpit-index.md`](1-cockpit-index.md) is the anchor: architecture, the
`/v1`-only read path, routing, auth, theme, non-goals. Every other spec
points back at it and adds only what does not generalise.

## Specs

| Spec                                                     | Status  | Covers                                                                                                                        |
| -------------------------------------------------------- | ------- | ----------------------------------------------------------------------------------------------------------------------------- |
| [1-cockpit-index.md](1-cockpit-index.md)                 | draft   | architecture, `/v1` read path, routing, auth, theme, non-goals                                                                |
| [2-dashd-hub.md](2-dashd-hub.md)                         | draft   | the `dashd` hub + the cross-cutting operator pages that stay central                                                          |
| [3-per-daemon-dashboards.md](3-per-daemon-dashboards.md) | partial | routd, runed, authd, proxyd, onbod, timed, crackbox, webd, davd, ttsd — one table plus the per-daemon reasoning that survives |
| [11-adapter-contract.md](11-adapter-contract.md)         | draft   | the shared adapter dashboard grammar + health model, defined once                                                             |
| [12-adapter-dashboards.md](12-adapter-dashboards.md)     | draft   | the ten channel adapters instantiating that contract                                                                          |
| [41-social-adapter-model.md](41-social-adapter-model.md) | draft   | the boundary between social adapters (asymmetric visibility, selective engagement) and chat adapters (bidirectional, push)    |

## What is actually built

Almost none of it. `onbod` serves `/dash/onbod/` with working approve /
deny / reprompt controls, and its four `/v1/onboarding` endpoints all ship.
`timed` serves `/dash/timed/` — the overview page only. Every other daemon's
dashboard is unwritten, and `dashd` still reads several DBs directly, which
is the read path this phase exists to retire.

## Merged and deleted 2026-08-02

Twelve near-identical instantiations of `1`'s nine-section template collapsed
into two files. What was cut is the repeated architecture, auth, theme and
HTMX prose already stated in `1`; what was kept is each daemon's page list,
control matrix, required `/v1` work, and any reasoning that does not
generalise.

| was                                        | now                                                                                                                               |
| ------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------- |
| `3-routd-dashboard.md`                     | [`3`](3-per-daemon-dashboards.md) — kept the two-breaker distinction: routd's chat-queue breaker is not runed's spawn breaker     |
| `4-runed-dashboard.md`                     | `3` — kept kill-run-is-the-revoke; **the tokens page was deleted, not merged** (below)                                            |
| `5-authd-dashboard.md`                     | `3` — kept the metadata-only rule and the two distinct identity-table families                                                    |
| `6-proxyd-dashboard.md`                    | `3` — kept "no route cache, no reload verb"                                                                                       |
| `7-onbod-dashboard.md`                     | `3` — its "required `/v1` work" was stale; those endpoints already ship                                                           |
| `8-timed-dashboard.md`                     | `3` — kept the ownership split: routd owns the task rows, timed owns the fire-loop runtime                                        |
| `9-crackbox-dashboard.md`                  | `3` — kept the orthogonality constraint (cannot import `arizuko/auth`, hence a static bearer) and its port deviation from `:8080` |
| `10-webd-davd-ttsd-dashboard.md`           | `3` — kept why `davd` gets no `/dash/` at all                                                                                     |
| `12-whapd-teled-slakd-dashboard.md`        | [`12`](12-adapter-dashboards.md) — kept the slakd stale-503 incident (2026-06-19)                                                 |
| `13-mastd-bskyd-reditd-linkd-dashboard.md` | `12`                                                                                                                              |
| `14-discd-emaid-twitd-dashboard.md`        | `12`                                                                                                                              |

**The runed "broker tokens" page was rot, not content.** `mcp_tokens` and
the per-spawn brokered token were deleted
(`runed/migrations/0003-drop-mcp-tokens.sql`), and `runed/broker.go` — which
`4` linked as a code pointer — does not exist. A turn is credentialed by the
SO_PEERCRED unix socket, not a token, so the page and its proposed
`GET /v1/tokens` were dropped rather than carried forward.

Three more claims were false against the code and were corrected, not
carried: `7` listed onbod's dashboard and all four `/v1/onboarding`
endpoints as "to add" when they ship; `8` listed routd's `/v1/tasks`
CRUD as "to add" when `mountTasks` serves it; and `1`, `5`, `9`, `10`
and `11` registered dashboards via `[[proxyd_route]]` in a
`template/services/<daemon>.toml`, a packaging format that no longer
exists — registration is a `services/<name>-routes.json` array or
`coreProxydRoutes`.

This phase superseded the former `11/18-daemon-dashboards.md`, deleted
2026-08-02; it reconciles the shipped `3/d`, `4/Q` and `4/V` — see
[`1-cockpit-index.md`](1-cockpit-index.md) "Reconciliation of prior specs".
