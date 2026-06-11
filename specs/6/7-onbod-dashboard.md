---
status: draft
depends: [1-cockpit-index]
---

# onbod dashboard — admission queue, gates, invites

Architecture, routing, auth, theme per [`6/1`](1-cockpit-index.md).
This spec adds only onbod's pages + show/control matrix.

onbod already has the richest `/v1` admin surface of the small daemons
(`onbod/main.go:164` mounts, handlers in `onbod/admin.go`): invites
list/create/revoke and gates list/upsert/delete are SHIPPED. The
dashboard's new cost is the **admission-queue read + the two queue
verbs** (approve / deny) that today exist only as the `admitFromQueue`
cron and raw SQL.

## Purpose

Watch and steer self-service onboarding: who is waiting at which gate,
what the gates allow per day, which invites are out — and unstick a
queued user without `sqlite3 onbod.db`.

## Pages

| Page            | Route                 |
| --------------- | --------------------- |
| overview        | `/dash/onbod/`        |
| admission queue | `/dash/onbod/queue`   |
| gates           | `/dash/onbod/gates`   |
| invites         | `/dash/onbod/invites` |

## Show

The state machine lives in the `onboarding` table
(`onbod/migrations/0001-onboarding.sql`): `awaiting_message` →
prompted (auth link sent, `prompted_at` set — `promptUnprompted`,
`onbod/main.go:337`) → `token_used` (OAuth bound `user_sub`) →
`queued` (gate matched — `linkJID`, `onbod/main.go:769`) → `approved`
(`admitFromQueue` within daily limit, `onbod/main.go:858`, ~60s
cadence). Stale `token_used` rows auto-reset for re-prompt after 30min
(`resetRow`, `onbod/main.go:384`).

**overview** — status counts across the state machine; today's
admissions vs the sum of gate limits; pending-prompt count (rows the
next poll tick will send links to); invite totals (live / expired /
exhausted); poll interval + greeting set/unset.

**queue** — the `onboarding` rows that need an operator's eye, grouped
by status: `queued` rows with jid, user_sub, gate, queued_at, computed
position within their gate (same ordering as the user-facing
position, `renderQueuePosition`, `onbod/main.go:828`) and today's
admitted-vs-limit for that gate; `awaiting_message` / `token_used`
rows with prompted_at + token_expires (is the link stale?). Detail
pane per row: full timeline (created, prompted_at, queued_at,
admitted_at).

**gates** — `onboarding_gates` rows: gate key (`github:org=...`,
`google:domain=...`, `email:domain=...`, `*` — `gateFromKey`,
`onbod/main.go:758`), limit_per_day, enabled, today's admitted count
(the same day-range count `admitFromQueue` uses), queue depth behind
the gate. Caption the match semantics (`matchGate`,
`onbod/main.go:286`): first match wins, `*` catches all; **zero
enabled gates = every linked JID auto-approves** (`linkJID` fallthrough,
`onbod/main.go:810`) — render that state as a visible `warn`.

**invites** — `invites` rows via the existing list shape (`inviteJSON`,
`onbod/admin.go:59`): token (rendered as the accept link), target_glob,
issued_by_sub, issued_at, expires_at, used/max uses, derived state
(live / expired / exhausted). Trailing-slash target = subgroup invite
(redeemer picks a username under it); bare target = direct admin grant
on that folder (`handleInvite`, `onbod/main.go:1000`).

## Control

| Affordance        | `/v1` verb                                                                             | Status     | Danger                                                                                                                                                                             |
| ----------------- | -------------------------------------------------------------------------------------- | ---------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| approve admission | `POST /v1/onboarding/{jid}/approve`                                                    | **to add** | **`.btn-danger`** — bypasses the gate's daily limit by design; sets `status='approved'`, `admitted_at=now` (counts against today, same as the cron's write at `onbod/main.go:903`) |
| deny admission    | `DELETE /v1/onboarding/{jid}`                                                          | **to add** | **`.btn-danger`** — drops the row; the JID restarts onboarding from scratch on its next message (routd re-POSTs `/v1/onboarding` on route miss)                                    |
| re-prompt         | `POST /v1/onboarding/{jid}/reprompt`                                                   | **to add** | no — clears `token`/`prompted_at`, status back to `awaiting_message`; next poll tick resends the auth link (manual `resetRow` for one jid)                                         |
| create gate       | `PUT /v1/gates/{gate}` (`handleGatePut`, `onbod/admin.go:223`)                         | exists     | no                                                                                                                                                                                 |
| update gate       | same verb — body `{limit_per_day}` and/or `{enabled}` (upsert + toggle in one handler) | exists     | disabling the only gate flips to auto-approve-all → confirm with the warn text                                                                                                     |
| delete gate       | `DELETE /v1/gates/{gate}` (`handleGateDelete`, `onbod/admin.go:259`)                   | exists     | **`.btn-danger`** — queued rows behind it strand (no gate to admit them)                                                                                                           |
| create invite     | `POST /v1/invites` (`handleInviteCreate`, `onbod/admin.go:90`)                         | exists     | no — form: target_glob, max_uses, expires_at; response renders the accept link once prominently                                                                                    |
| revoke invite     | `DELETE /v1/invites/{token}` (`handleInviteRevoke`, `onbod/admin.go:144`)              | exists     | **`.btn-danger`** — already-shared links die immediately                                                                                                                           |
| resend invite     | —                                                                                      | —          | **non-goal**: onbod never sends invites (they are links the operator hands out); there is no delivery to repeat — copy the link from the list instead                              |

Gate verbs verified against source: list is `GET /v1/gates`, mutation
is `PUT /v1/gates/{gate}` + `DELETE /v1/gates/{gate}` — there is no
`POST /v1/gates`; the PUT is the upsert.

## Required `/v1` work

- `GET /v1/onboarding` — list `onboarding` rows, `?status=` filter.
  Today the resource has only the insert face
  (`POST /v1/onboarding` — `handleOnboardingInsert`,
  `onbod/admin.go:171`); this is the read the queue page stands on.
  Response: jid, status, user_sub, gate, created, prompted_at,
  queued_at, admitted_at, token_expires. **No `token` column in the
  response** — a live onboarding token is a bearer credential for
  binding a JID.
- `POST /v1/onboarding/{jid}/approve` — operator fast-path (above).
- `DELETE /v1/onboarding/{jid}` — deny/drop (above).
- `POST /v1/onboarding/{jid}/reprompt` — single-row reset (above).
- Gate/invite reads gain derived counts (today-admitted, queue depth)
  either inline on the existing list responses or computed by the dash
  handler in-process — owner's choice; no second renderer.

All four follow the existing `admin` pattern (`onbod/admin.go:21`):
bearer vs authd JWKS, scope `invites:write` for onboarding-row
mutations (the established onboarding-admission scope family,
`onbod/admin.go:172`), `gates:read|write` untouched. Mutations emit
audit rows like `linkJID`/`admitFromQueue` already do
(`onboarding.approve` / a new `onboarding.deny`).

## Auth

Per `6/1`: proxyd `auth:"user"` transit on `/dash/onbod/` +
`auth/dashauth.go` operator gate, same-origin CSRF on writes. Distinct
from BOTH of onbod's existing gates — the public-flow
`stripUnsignedGuard` (`onbod/main.go:215`) and the bearer-scoped `/v1`
admin gate (`admin.authed`, `onbod/admin.go:29`) — and from the
end-user `/onboard` dashboard (`handleDashboard`, `onbod/main.go:500`),
which stays a user-facing page, not part of this cockpit. Dash
handlers read/write `onbod.db` in-process via the same `store.New`
writers the `/v1` handlers use.

## HTMX fragments

- `GET /dash/onbod/x/queue?status=` — onboarding rows, poll-refreshed
  (the queue moves on its own via the cron).
- `GET /dash/onbod/x/gates` — gate rows + live counts.
- `GET /dash/onbod/x/invites` — invite rows.
- `POST /dash/onbod/x/approve/{jid}` / `DELETE
/dash/onbod/x/onboarding/{jid}` / `POST
/dash/onbod/x/reprompt/{jid}` — mutate + return refreshed queue rows.
- `PUT|DELETE /dash/onbod/x/gates/{gate}`, `POST
/dash/onbod/x/invites`, `DELETE /dash/onbod/x/invites/{token}` —
  mutate + return the refreshed table.

## Non-goals

Per `6/1`, plus: no cross-table user browsing (auth_users/acl/groups
are routd/authd-owned cross-reads — the user-profile view is dashd's
cross-cutting page, `6/2`); no editing of `ONBOARDING_GATES` env
seeding or greeting text (env, not data); no world
creation/deletion (worlds belong to `SetupGroup` flows, not the
admission queue); no invite delivery mechanism.

## Acceptance

- `/dash/onbod/` route registered in `template/services/onbod.toml`;
  hub tile probes `GET /health` (`onbod/main.go:144`).
- Queue page shows a freshly inserted `awaiting_message` row within
  one fragment refresh; no rendered page or `/v1` response contains a
  live onboarding `token`.
- Approve on a `queued` row flips it to `approved` immediately
  (without waiting for the cron) and the gate's today-count
  increments; the danger confirm cites the gate-limit bypass.
- Deny removes the row; the JID's next inbound message re-enters as
  `awaiting_message`.
- Re-prompt on a stale `token_used` row produces a fresh auth link on
  the next poll tick (observable: new `prompted_at`, "sent auth link"
  log line).
- Disabling the last enabled gate surfaces the auto-approve-all warn
  on the gates page and in the confirm dialog.
- Gate upsert/delete and invite create/revoke round-trip through the
  EXISTING `/v1` handlers (no parallel write path — one renderer per
  resource).
- Non-operator caller gets the theme 403 banner on every page.
