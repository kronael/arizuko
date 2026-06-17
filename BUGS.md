# BUGS.md — open issues queue

## dashd reads SQLite directly — violates spec 6/1 read-path contract (2026-06-16, open)

`dashd/main.go:106, 170-177, 182-191` opens `routd.db`, `onbod.db`, and `messages.db` directly.
Spec 6/1 §"Read-path: /v1 only, no direct DB" forbids this. Direct DB reads miss all live process
state (runed active runs, routd breaker/queue depth, adapter session health, timed next-tick) —
state that only exists in daemon memory. The cockpit spec was written to force /v1 reads precisely
because snapshots can't see that state.

- **Severity:** high
- **Scope:** dashd, cockpit spec 6/1
- **Affected:** all dashd pages (groups, status, invites, memory, tokens, activity)
- **Source:** dashd/main.go:106,170-191; specs/6/1-cockpit-hub.md
- **Status:** open
- **Fix:**

## No operator audit-log page (2026-06-16, open)

`audit.Emit` writes events (dashd/main.go:114-124) but no `/dash` handler reads them. Spec 6/5
covers authd identity events only; there is no operator-facing "who did what" view for dashd
actions (approve/deny onboarding, revoke token, etc.). Table stakes for multitenant control planes.

- **Severity:** medium
- **Scope:** dashd, authd
- **Affected:** operators — no audit trail in UI
- **Source:** dashd/main.go:114-124; grep: no audit handler in dashd
- **Status:** resolved — /dash/audit/ shipped
- **Fix:**

## No usage/analytics page (2026-06-16, open)

`GroupUsageBulk` (dashd/main.go:789-822) surfaces msgs, tokens/7d, $/7d, last-active per folder —
but only on the groups detail page, three clicks deep. There is no instance-wide usage summary:
no aggregate message volume, no agent response-rate trend, no total spend. Specs 6/1 and 6/2 have
zero occurrences of "usage", "throughput", or "metric". Not specced, not built.

- **Severity:** medium
- **Scope:** dashd specs 6/1, 6/2
- **Affected:** operators, business visibility
- **Source:** dashd/main.go:789-822; specs/6/2-dashd-hub.md
- **Status:** resolved — /dash/usage/ shipped
- **Fix:**

## dashd: routes + task-detail visible to any auth'd user (2026-06-17, open)

`handleRoutes` GET (`routes_admin.go:28`) gates on `requireUser` only — any auth'd caller can
enumerate the full route table including routes targeting other tenants' folders. `handleTaskDetail`
GET (`tasks_admin.go:21`) also gates on `requireUser` with no folder-scope check — any auth'd
caller who knows a task ID can read its full prompt and owner. Both list pages filter correctly
(`handleGroups`, `handleTasks`), but their detail/read views don't re-check scope.

- **Severity:** medium
- **Scope:** dashd/routes_admin.go:28, dashd/tasks_admin.go:21
- **Affected:** multitenant: non-operator users can see other tenants' routes/task details
- **Source:** bucket A refine review 2026-06-17
- **Status:** open
- **Fix:** add `requireOperator` or `requireVisible(folder)` gate to those GET handlers

## dashd: adminDB() panics if routd.db open fails (2026-06-17, open)

`main.go:226-231` warns and continues if `routd.db` fails to open, leaving `d.dbRoutd = nil`.
`adminDB()` (`main.go:307`) returns `d.dbRoutd` unconditionally. Any request to `handleStatus`,
`handleGroups`, `handleTasks`, `writeActivityRows`, etc. then panics at the `.Query` call.

- **Severity:** medium
- **Scope:** dashd/main.go:226-231, main.go:307
- **Affected:** startup — routd.db missing/corrupt causes panic on first request
- **Source:** bucket A refine review 2026-06-17
- **Status:** open
- **Fix:** make routd.db open failure fatal, or nil-check in adminDB() and return 503

## Activity page: no relative timestamps and no pagination (2026-06-16, open)

`writeActivityRows` emits raw ISO8601 nanosecond timestamps (dashd/main.go:688). No "N min ago"
rendering anywhere in dashd. Activity hard-caps at 50 rows with no "older" affordance. During an
incident you cannot scroll back past 50 events and must parse UTC strings by eye.

- **Severity:** low
- **Scope:** dashd/main.go activity handler
- **Affected:** operators during incident response
- **Source:** dashd/main.go:688, 628-639
- **Status:** partial — relative timestamps added (`relativeTS`, abbr tooltip still shows ISO full);
  pagination not yet implemented
- **Fix:** `relativeTS` function + writeActivityRows updated (2026-06-16)

## Nav chrome: no identity/scope signal (2026-06-16, open)

The persistent nav (`dashNavFor`) shows no identity, no folder scope, no operator badge. The only
tell for operator status is whether `services`/`invites` links appear. For a console that gates
destructive mutations, the current identity and scope must be visible at all times, not one click
away (profile page). CTO audit criterion: auth trust signal always on screen.

- **Severity:** low
- **Scope:** dashd/main.go dashNavFor
- **Affected:** all dashd pages
- **Source:** dashd/main.go:362-376
- **Status:** resolved — name + ◆ operator badge added to nav as profile link (2026-06-16)
- **Fix:** `dashNavFor` identity badge using X-User-Name/X-User-Sub + operator flag
