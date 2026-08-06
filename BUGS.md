# BUGS.md — open issues queue

> **Standing policy — every fix here adheres to WISDOM** (`~/.claude/CLAUDE.md`):
> minimality (smallest change at the root cause), orthogonality (one concern per
> fix, no parallel second path — amend the original), fail loud to the user on
> user-facing paths, retry only transient errors, fix causes not symptoms.
> Redesigns (new contract, changed cross-daemon control flow, auth-model or
> schema changes) stay recorded as proposals and ship only after user sign-off.

## F36 — a refresh-token family kill can miss the successor it was racing (2026-08-06, FIXED)

`Authd.Refresh` detects concurrent redeem of a one-time refresh token and
revokes the lineage, but the revoke and the successor's insert are unordered,
so the token the kill exists to stop can survive it.

The two paths, both in `authd/server.go`:

- **Loser** — `markRefreshUsed` returns `won == false` (`:299`) → `revokeFamily`
  (`:300`), which is `UPDATE refresh_tokens SET revoked_at = ? WHERE family_id
= ? AND revoked_at IS NULL` (`authd/store.go:256`). It revokes the rows that
  exist **at that moment**.
- **Winner** — wins at `:295`, then re-snapshots grants, and only at `:324`
  calls `rotateRefresh`, which INSERTS the successor.

If every loser's `revokeFamily` commits before the winner's insert, the
successor lands with `revoked_at` NULL into a family that was just killed. It
then refreshes normally for the full 30-day TTL. The reuse signal fired, the
alarm was recorded, and the credential it was about stayed live — which is the
one outcome reuse detection exists to prevent.

**Why it surfaced now, and why it is not new.** Nothing in the audit-read work
touches `Refresh`, `revokeFamily`, `rotateRefresh` or `markRefreshUsed` —
`authd/server.go` and `authd/store.go` are untouched by commits `b87bc0ff`,
`21ba9d0a`, `7a94eb61`. What changed is scheduling: `authd/audit_resource_test.go`
sorts before `bugfix_test.go`, so the package's tests now run in a different
order, and under whole-suite load the losers win the ordering.
`TestRefreshRotationRaceSingleWinner` then fails on its second assertion —
"successor must be revoked after a concurrent-reuse family kill" — which is the
race, correctly detected.

Reproduction needs BOTH the new ordering and CPU contention. It failed two of
two whole-suite runs while a second worktree was running its own suite on the
same machine, and passed three of three once that finished — so the honest
statement is "reproduces under load", not a fixed rate. It also passes when the
new test file is renamed to sort last, and at base `22456a4f` it passed both
whole-suite runs and 40 isolated ones.

Load-and-order sensitivity is the evidence, not a caveat on it: the assertion
is a pure invariant over committed rows, so the only thing scheduling can
change is whether the losers reach `revokeFamily` before the winner reaches
`rotateRefresh`. A test that fails only when the timing goes one way is
reporting a timing bug. Do not close this by re-running until it is green.

**Not fixed here, and not hidden here.** Renaming the new test file would make
the suite green and leave a live token-revocation hole, so it was left failing
and filed instead. The fix is an ordering one — revoke the family AFTER the
successor exists, or do the rotate + revoke in one transaction so a loser's
`UPDATE` cannot interleave between the winner's compare-and-set and its insert.
Either is a correctness change to the token authority's concurrency, which is
the sign-off case.

- **Severity:** high (a revoked refresh family can keep a live successor; the
  window is narrow and needs a genuine concurrent redeem, but that is exactly
  the case reuse detection is for)
- **Scope:** authd refresh rotation (spec 5/1 § refresh reuse detection)
- **Affected:** all instances
- **Source:** authd/server.go:295-302,324; authd/store.go:256-261; authd/bugfix_test.go:131-168
- **Status:** FIXED 2026-08-06
- **Fix:** one transaction, as signed off. `claimAndRotateRefresh`
  (`authd/store.go`) runs the compare-and-set and the successor's INSERT
  together, so no `revokeFamily` can commit between them: SQLite has one
  writer, and a loser learns it lost from the very UPDATE whose write lock the
  winner holds until COMMIT.
- **The remote call that looked like it forbade the transaction did not.** The
  grants re-snapshot sat between the claim and the insert, and holding a SQLite
  write lock across an HTTP round trip would stall every other write in
  `auth.db` for its timeout. It moved AHEAD of the claim instead — it reads
  `r.sub`, which `lookupRefresh` already returned, so it never depended on the
  claim's outcome. Two side effects, both improvements: a grants outage no
  longer burns the presented token, and the window in which two redeems both
  pass the `used_at` check widens by one RTT, so genuinely simultaneous
  redeems are more likely to be caught as the reuse they are.
- **Second kill source, same hole, found while fixing this one.** `logout`
  (`authd/oauth.go:413`) revokes the family too, and a revoke that commits
  BEFORE the rotation's transaction opens is not an interleave — the
  transaction cannot see it. So the claim itself now looks: the
  compare-and-set gained a `revoked_at IS NULL` conjunct alongside `used_at IS
NULL`. Without it a logout landing between a refresh's lookup and its claim
  minted a successor into the family the user had just killed.
- **Reproduced on demand before it was fixed, which the filed evidence could
  not do.** `Authd.grants` is a `GrantsFetcher` interface and `Refresh` calls
  it exactly once, so a fetcher that parks its first caller pins one redeem
  mid-rotation while a competing redeem runs start to finish inside that
  window. No sleeps and no scheduling luck: **10 of 10 failures** across five
  runs of the two new tests at base `f7f759b0`, and 10 of 10 passes after.
- **Each half is independently falsifiable, and the check found a vacuous
  test.** Strip the transaction and only
  `TestRefreshClaimRollsBackWhenSuccessorInsertFails` fails; strip the
  `revoked_at` conjunct and only `TestRefreshAfterLogoutRevokeMintsNoSuccessor`
  fails. The two barrier tests do NOT falsify the transaction on their own —
  the grants reorder alone shrinks the win→insert gap to two adjacent local
  statements, too small to steer a barrier into, and with the transaction
  removed both still passed. That is why the transaction is pinned by a
  timing-free test instead: a `BEFORE INSERT` trigger fails the successor's
  INSERT, and only a real transaction rolls the claim back with it.

## F35 — onbod's `audit_log` is the fourth owner and the only one still unreadable (2026-08-06, open)

`F29` named three daemons; there are four. `onbod` owns an `audit_log`
(`onbod/migrations/0002-audit-log.sql`) and writes real rows through five
`audit.Emit` call sites (`onbod/main.go:110,885,912,1106` plus the audited
`AddACLRow` path at `:1147`) — admission decisions, invite consumption, group
setup. `audit.Init(obdb, ...)` at `onbod/main.go:109` points them at
`onbod.db`, which no other daemon opens.

Now that routd, runed and authd each serve `GET /v1/audit`, onbod is the one
owner whose rows still need `sqlite3` on the box, and `/dash/audit/` federates
three of four sources — so the page is honest about what it shows but silently
incomplete about what exists.

The work is small and already shaped: the resource is registered once
(`resreg/resources/audit.go`), and onbod's mount would be a near-copy of
`runed/audit_resource.go` — a Store for the non-forwarder marker, an
`audit:read` gate, `audit.Query` over onbod's handle, plus `"audit"` in its
`OpenAPIResources` list and a fourth entry in `dashd.auditSources`.

It was NOT added with the other three because the sign-off named routd, runed
and authd. A new `/v1` surface on a daemon the sign-off did not mention is
exactly the change root `CLAUDE.md` says needs one, and onbod's rows carry
admission and invite data whose params column deserves the same
column-by-column audit authd's got before it is published.

- **Severity:** low (the trail exists and is correct; only the read is missing,
  and the three noisiest owners are now covered)
- **Scope:** onbod `/v1` + `dashd.auditSources` (spec 5/I)
- **Affected:** all instances
- **Source:** onbod/migrations/0002-audit-log.sql; onbod/main.go:109,110,885,912,1106; dashd/audit_page.go auditSources
- **Status:** open — needs sign-off on extending the `F29` shape to a fourth daemon
- **Fix:** mount the shared `audit` resource on onbod, audit its
  `params_summary` per call site first, then add the source to dashd.
- **Blocks:** nothing. `5/I` is `shipped` on the three signed-off daemons and
  records this as its remaining open question.

## ✅ FIXED 2026-08-06 F31 — `/dash/engagement/` cannot be built: dashd has no HTTP edge to routd, and no scope to use one (2026-08-06, FIXED)

Attempted while closing `5/G`'s definition-of-done item 6. `F12a` shipped the
read API the page needs — `GET /v1/engagement` returns `engaged_until`, and the
no-`jid` form lists live windows — so the API is no longer the blocker. The
transport is.

**Exactly one blocker, and it is a decision.**
`serviceGrants["service:dashd"]` is `{"runs:kill"}` (`authd/http.go`);
`GET /v1/engagement` requires `routes:read` or `routes:read:own_group`
(`routd/reads_http.go:22`), so routd answers 403. The entry's own comment says
"runs:kill is the whole ceiling — anything wider would let the operator UI act
beyond what it proxies", which makes widening it a sign-off, not a config edit.

Checked and DISPROVED as a second blocker: "dashd has no routd client".
It has no `ROUTER_URL` in `compose/compose.go`'s dashd env list and no routd
HTTP call today — but it needs neither, because `backendURL(envKey, service)`
(`dashd/main.go:316`) already derives a sibling's base URL from the compose
service name on the fixed `:8080`, precisely so a missing compose var cannot
break it again (`F23`). `backendURL("ROUTER_URL", "routd")` is the whole
transport, one line beside the existing `runedURL`/`proxydURL`.

Checked and DISPROVED as a third blocker: a service token carries an EMPTY
folder claim (`MintForSubject(principal, "service", nil, …, "")`,
`authd/http.go:164`), and `listEngagement` treats an empty claim as list-all
(`routd/engagement_http.go:57`), so dashd would read every tenant's windows.
That is NOT a leak for this page, because the page is operator-only the way
`/dash/proactive/` is (`requireOperator`, `dashd/authz.go:95`) and an operator
holds a `**` row already. It WOULD become one the moment the page is offered to
a folder-scoped viewer, because routd authorizes the bearer, not the
`X-User-*` headers dashd forwards — proxyd's `trustedForwarders` pattern has no
routd equivalent.

The obvious shortcut is to render the page from `d.dbRoutd`, which is what
`/dash/proactive/` does. That is the open "dashd reads SQLite directly" entry
(2026-06-16), and it does not survive the page growing a disengage button —
`POST /v1/engagement` is the only writer that keeps the audit row in routd's
own transaction.

- **Severity:** medium (blocks `5/G` item 6; `5/G` stays partial)
- **Scope:** dashd ↔ routd transport + authd serviceGrants (spec 5/G)
- **Affected:** all instances — no operator can see or end an engagement window
- **Source:** authd/http.go:26-62,164; routd/reads_http.go:22; routd/engagement_http.go:25,57; dashd/main.go:286,316; dashd/authz.go:95
- **Status:** FIXED 2026-08-06 — ceiling signed off for the READ half only.
- **Sign-off given:** `serviceGrants["service:dashd"]` is now
  `{"runs:kill", "routes:read"}`. Checked before taking it, since the entry's own
  comment called `runs:kill` the whole ceiling: `routes:read` reaches routes,
  web_routes, groups, routing-resolve, errored-chats, engagement, and route-token
  metadata. None of that is new reach — dashd is FS-mounted on routd.db
  (`dash.dbRoutd`) and `dashd/route_tokens.go` already reads that last table
  straight out of SQLite — so the scope is a strict SUBSET of what dashd holds,
  and what it buys is moving one page off the direct-DB read. It leaks no secret:
  `ListRouteTokens` selects jid/owner_folder/created_at/context and never the
  token value (`store/route_tokens.go:137`), and `/v1/route_tokens/resolve` is a
  reverse lookup needing the token already in hand.
- **Shipped:** `dashd/engagement_page.go` — `GET /dash/engagement/`, operator-only,
  listing chat / thread / group / time-left. Reads routd's list face over HTTP
  (`routdGet`, service bearer, X-User-* deliberately NOT forwarded: routd
  authorizes the bearer, unlike proxyd). `routdURL: backendURL("ROUTER_URL",
  "routd")` beside the existing two, confirming F31's own disproof that compose
  wiring was needed. `remainingTS` is new next to `relativeTS`, which measures
  `time.Since` and renders every future deadline as "now".
  `proxydUpstreamErr` was generalized to `upstreamErr(daemon, …)` rather than
  copied — one renderer, two sinks.
- **Shipped 2026-08-06 — the write half, on a second sign-off.**
  `serviceGrants["service:dashd"]` is now `{"runs:kill", "routes:read",
  "routes:write"}` and `POST /dash/engagement/disengage` ends a window now,
  behind a confirm, through routd's `POST /v1/engagement` (`ttl_seconds=0`) —
  never `dbRoutd`. The two conditions this entry named as owed were both false
  when it was written, and both are now true:
  - `POST /v1/engagement` wrote **no audit row at all** — the claim that "both
    early writers keep the audit row inside routd's transaction" held for
    neither (`disengage` writes an `ipc_audit` + `audit-system.jl` row through
    the `granted` tool wrapper, not an `audit_log` row in the write's tx; the
    REST face wrote nothing). `DB.SetEngagementAudited` now opens one tx for the
    upsert + `audit.EmitInTx` — `engagement.set` / `engagement.clear`, category
    `mutation`. The un-audited `SetEngagement` stays for the per-turn claim sites
    off one shared statement, so the two cannot drift.
  - The write's containment checked the jid's ROUTE TARGET (`ownsJID`) and the
    caller-supplied `req.Folder` (`ownsFolder`), never the folder HOLDING the
    window — so a tenant naming its own folder could clear a sibling's window
    that `GET /v1/engagement` would never have shown it. Now contained on the
    live window's claiming folder, the same predicate `ListEngaged` uses.
  `routes:write` was accepted on the same reasoning as `routes:read`: dashd is
  FS-mounted on routd.db and already WRITES routes/groups directly, so the scope
  is a subset of reach it holds and what it buys is a mutation moving off the
  direct-DB path. `authd/service_dashd_test.go` now pins the ceiling by COUNT as
  well as by name, so a fourth scope fails whether or not it was blacklisted.
- **Closed:** `specs/5/G` definition-of-done item 6 — `5/G` is `shipped`.

## ✅ FIXED 2026-08-06 F32 — timed's `/openapi.json` advertises four operations timed does not serve (2026-08-06, FIXED)

`timed/split.go:72` emits its document with
`resreg.OpenAPIHandler("timed", []string{"scheduled_tasks"})`, which renders
routd's `ScheduledTasksEndpoints` in full: `GET /v1/tasks`, and
`GET`/`PATCH`/`DELETE /v1/tasks/{taskId}`. timed's mux, three lines above,
mounts exactly `GET /health`, `GET /openapi.json` and `GET /dash/timed/`. Every
advertised path 404s.

Verified by running the emitter, not by reading the declaration:
`resreg.OpenAPI("timed", "/", []string{"scheduled_tasks"})` returns 2 paths /
4 operations.

This is the `F21`/`F27` bug class one daemon over. The routd guards that close
it — `TestRoutdMux_ServesEveryDeclaredEndpoint` and
`TestOpenAPI_EveryAdvertisedPathIsMounted` — are both in package `routd` and
probe the mux routd builds, so nothing checks any other daemon. `resreg`
cannot catch it either: `OpenAPIHandler(daemon, resources)` takes a name list
with no reference to what the caller mounts.

timed is a CLIENT of those endpoints (`timed/dash.go:82`, `timed/split.go:205,
219,230` call routd across the container boundary), which is presumably how the
name got into the list. A client is not a server.

- **Severity:** low (a generated client aimed at timed 404s; no runtime effect
  on arizuko itself)
- **Scope:** timed + the resreg OpenAPI mount contract (spec 5/17)
- **Affected:** all instances
- **Source:** timed/split.go:68-73 vs resreg/openapi.go:527; resreg/resources/scheduled_tasks.go:48-51
- **Status:** FIXED 2026-08-06 — small fix taken; the generic guard was
  measured and rejected as disproportionate.
- **Decision: the one-token fix, plus a real guard local to timed.** The
  generic option was priced before being dropped. Of the seven
  `OpenAPIHandler` callers, four (webd/authd/dashd/runed) pass `[]string{}` and
  cannot drift — an empty advertised set has nothing to mount. routd is already
  guarded in-package. That leaves onbod and proxyd, and BOTH were checked by
  running the emitter and reading their mounts: onbod advertises 8 paths and
  mounts all 8, proxyd advertises 2 and mounts both, and neither can drift,
  because each passes `resreg.RegisterREST` the SAME `resources.XEndpoints`
  slice the doc is emitted from (`onbod/onboarding_resource.go:28`,
  `proxyd/resource.go:309`). So the class had exactly one live instance, timed —
  which drifted precisely because it never calls `RegisterREST` at all.
  A daemon-generic guard would have to reach each daemon's mux; onbod's and
  timed's are built inline inside `main`/`runSplit`, so "generic" would still
  mean per-daemon refactors — two of them, to catch a class with one member and
  no way to recur in the other two. That is disproportionate, and changing
  `OpenAPIHandler` to derive the advertised set from the mux is a cross-cutting
  contract change, which `CLAUDE.md` requires be signed off rather than shipped
  inline. Filed as `F33`.
- **Fix:** `timed/split.go` — the list is now `timedOpenAPIResources`, empty,
  with the reason written where the next person will edit it. The mux moved out
  of `runSplit`'s goroutine into `newTimedMux` so a test can read the ROUTING
  TABLE timed actually builds, not a restatement of it.
- **Guard:** `timed/split_openapi_test.go`, deriving BOTH sides — advertised set
  from `resreg.OpenAPI`, served set from `newTimedMux`. Deliberately NOT the
  `daemonOwnership` copy shape, which computes expectation and actual from the
  same hand-maintained table: that test passed unchanged while this bug was live,
  proving it vacuous here. Half 1 checks every advertised path resolves (the
  class guard, and vacuous today since the list is empty); half 2 is the anchor —
  it asserts the emitter really does produce scheduled_tasks' operations, then
  that timed's mux serves none of them and advertises none of them. Proved
  falsifiable: restoring `[]string{"scheduled_tasks"}` failed this test with all
  five operations named, and no other package in the tree failed.

## F34 — "who ended this engagement" has two answers in two tables (2026-08-06, PROPOSED — needs sign-off, DISPROVED)

Found while shipping `5/G`'s disengage control, which fixed the REST half only.

A `(jid, topic)` window can be ended early by two callers, and they now record it
in two different places:

- **REST** — `POST /v1/engagement` writes an `audit_log` row inside the write's
  own transaction (`routd DB.SetEngagementAudited`, `engagement.clear`). This is
  what `/dash/engagement/`'s button uses.
- **MCP** — the agent's `disengage` tool writes an `ipc_audit` row plus an
  `audit-system.jl` line, through the generic wrapper every `granted` tool passes
  (`ipc/ipc.go:909`). No `audit_log` row, and not in the write's transaction —
  `db.SetEngagement` is a bare Exec.

So an operator asking "who stopped the bot talking in this chat" must query
`audit_log` AND `ipc_audit` and merge two schemas, and only one of the two
answers is transactional. `/dash/audit/` reads `audit_log`, so the agent's own
disengages are invisible there.

This is the same shape as root `CLAUDE.md`'s "one renderer, many sinks": two
paths into one question drift. Note the older claim that both writers were
already audited in routd's transaction — repeated in `F31`, `5/G`, the
`serviceGrants` comment and `dashd/README.md` — was true of neither before today
and is true of one now; those four sites are corrected.

The fix is not local. `ipc_audit` predates `audit_log` and ~45 hand-rolled ipc
tools feed it (`store/ipc_audit.go` already says new callers should prefer
`audit.EmitInTx`), so moving `disengage` alone would make engagement the one tool
that reports differently from its neighbours — trading a split between SURFACES
for a split between TOOLS. The real question is whether the `granted` wrapper
should emit `audit_log` for every state-changing tool, which is a cross-cutting
contract change over every ipc tool and therefore a sign-off, not an inline fix.

- **Severity:** low (both writes ARE recorded; the cost is that answering one
  question takes two queries, and `/dash/audit/` shows only half)
- **Scope:** `ipc` audit contract vs `audit_log` (specs 5/I, 5/G, 5/16)
- **Affected:** all instances
- **Source:** ipc/ipc.go:873-925,1571-1578; routd/db.go SetEngagement /
  SetEngagementAudited; store/ipc_audit.go:16; dashd audit page reads audit_log
- **Status:** PROPOSED — the narrow fix (disengage only) is worse than the gap;
  the cause fix is the `granted` wrapper, which needs sign-off
- **Blocks:** nothing. `5/G` is shipped — its DoD asks that the dashboard
  mutation be audited, and it is.

- **DISPROVED 2026-08-06.** `store.LogIPCAudit` already writes `audit_log`
  via `audit.EmitDB` (`surface=mcp`, `action=mcp.tool.invoke`); only the
  function NAME is legacy. Live krons: 50 `mcp/set_web_route` + 7
  `mcp/delegate_group` rows, and the `ipc_audit` table no longer exists there.
  The premise came from a stale field comment at `ipc/ipc.go:251` saying
  "persists one ipc_audit row" — a claim, not evidence. Comment corrected.
  No code move needed; `disengage` is audited like its ~45 neighbours.

## F33 — `OpenAPIHandler` takes an advertised set with no reference to the mux (2026-08-06, PROPOSED — needs sign-off)

The cause under `F21`/`F27`/`F32`, left standing after `F32` took the small fix.
`resreg.OpenAPIHandler(daemon, resources)` (`resreg/openapi.go:527`) accepts a
list of resource NAMES. Nothing connects that list to the routes the caller
mounts, so "advertised" and "served" are two independent declarations that agree
only by care. Three of them have disagreed so far.

The cause fix makes the disagreement impossible rather than detectable: pass the
mux — `OpenAPIHandler(daemon, mux)` — and emit only the endpoints that mux
actually resolves. There is no second list to drift. Mount ORDER is not an
obstacle: `/openapi.json` is mounted before the resources it documents, but the
handler already computes lazily and caches on first REQUEST, by which time the
mux is complete. (A mount-time check would read the slice before the routes
exist — the trap that made `daemonOwnership` vacuous.)

Not shipped inline because it changes a cross-cutting contract across seven call
sites, which `CLAUDE.md` says is a sign-off, and because it is not free: routd's
`OpenAPIResources` is a CURATED list, deliberately narrower than what is mounted
in places (`acl`'s list, `secrets`' read face, `network_rules`), and those
carve-outs are expressed today as `MCPOnly` endpoint declarations. Deriving from
the mux means proving every carve-out is expressible as "not mounted", which is
a real audit, not a signature change.

Second, smaller half: `daemonOwnership` (`resreg/resources/resources_test.go:543`)
should go. It is a hand-maintained COPY of what each daemon advertises, and
`TestOpenAPI_PerDaemonOwnership` computes both the expected and the actual path
set from that same copy — so it cannot fail for the thing it names. Demonstrated
during `F32`: the table said timed owned nothing while timed advertised
`scheduled_tasks`, and the test passed. It had already drifted three resources in
each direction (found during `F27`). Whatever replaces it must read the daemon,
not a restatement of it.

- **Severity:** low (no runtime effect; it is the recurrence risk that has now
  cost three entries)
- **Scope:** `resreg` OpenAPI mount contract (spec 5/17, 5/8) + every daemon main
- **Affected:** all instances
- **Source:** resreg/openapi.go:527; routd/server.go:256; resreg/resources/resources_test.go:529-581; timed/split.go
- **Status:** PROPOSED — needs sign-off before touching seven call sites
- **Blocks:** nothing; `F32`'s guard covers the one daemon that could drift today.

## ✅ FIXED 2026-08-06 F29 — runed's and authd's audit rows are reachable only with sqlite3 (2026-08-06, FIXED)

Spec `5/I` says each daemon owns its own `audit_log`, and two daemons now do.
Neither can be read by anything but a shell on the box.

- **runed** writes real rows: `audit.Init(db.SQL(), …)` at
  `runed/cmd/runed/main.go:51`, `audit.EmitDB` from `auditRunSlot`
  (`runed/audit.go:87`), covering `run.hold` and `run.kill`. Its whole `/v1`
  surface is `runed/server.go:37-44` — runs, holds, sessions. **No audit read
  endpoint**, and `/openapi.json` is emitted with an empty resource list
  (`runed/cmd/runed/main.go:121`), so the doc advertises zero paths.
- **authd** writes rows into `auth.db` (`audit.Init` at `authd/main.go:78`,
  `audit.Emit` at `:79` and `authd/oauth.go:356`). It mounts **no `/v1` at
  all** — only `/auth/*` OAuth routes, `/openapi.json`, `/metrics`. Same root
  cause as `F15a`.
- **`/dash/audit/`** reads `routd.db` through `d.adminDB()`
  (`dashd/audit_page.go:24,60`), so it shows routd's rows and only routd's.

So the trail exists and is correct, and an operator asking "who killed that
run" gets nothing from the dashboard. `concepts/audit.html` now says this
plainly rather than implying full coverage, but the honest doc is not the fix.

**Why this is not fixed inline.** The obvious shortcut is to open `runed.db`
from dashd the way `/dash/runed/` already opens it for `spawns`. That would
make the dashboard a second reader of a table whose owner exposes no contract,
and it does not generalise to authd, whose DB dashd does not open at all. The
fix is a read endpoint per daemon plus one federating page — new API surface on
a spec that does not describe it, which is the sign-off case (`F12a`, `F15a`,
same shape).

Two shapes, not equivalent:

- **(a) Hand-rolled `GET /v1/audit` per daemon**, matching runed's existing
  hand-rolled handlers, with dashd fanning out and merging by `created_at`.
  Smallest change. Leaves `audit_log` as a read surface with no resreg
  registration.
- **(b) Register `audit_log` as a read-only resreg resource** in each owning
  daemon, which is what root `CLAUDE.md` asks of every operator-managed entity
  and would put the rows in `openapi.json` and give MCP the same face for free
  (closing `5/I`'s open question 2, the `query_audit` self-introspection tool,
  as a side effect). More work, and it needs a read-only `Action` that does not
  itself write an audit row per call — the read/write split in `5/I` exists
  precisely to stop that.

Either way `serviceGrants["service:dashd"]` must carry a scope that admits the
new action; the entry itself now exists (`fd697e99`).

- **Severity:** medium (a shipped audit trail that no operator surface can read)
- **Scope:** runed + authd `/v1` + dashd audit page (spec 5/I)
- **Affected:** all instances
- **Status:** FIXED 2026-08-06
- **Fix:** shape **(b)** as signed off — `audit` registered ONCE as a read-only
  resreg resource (`resreg/resources/audit.go`), mounted by routd, runed and
  authd; `audit.Query` is the single reader; `/dash/audit/` federates the three
  over HTTP. routd also mounts the agent face, closing `5/I` open question 2.
  Spec `5/I` is `shipped`.
- **Note — the write-shape objection was answered, not dodged.** (b)'s stated
  cost was "a read-only `Action` that does not itself write an audit row per
  call". It needed no new machinery: `Action.Mutates()` is already false for
  `list`, so `resreg.emitAudit` returns before the insert. Denials and errors
  still land. `runed/audit_resource_test.go` pins it — three reads leave the
  table at one row.
- **Note — one resource name on three daemons is not the collision the rule
  forbids.** Root `CLAUDE.md` bars two DIFFERENT tables sharing a wire name
  (`routes` vs `proxyd_routes`). This is ONE table shape replicated per owner
  DB by `5/I`'s own per-daemon decision, so `/v1/audit` means "this daemon's
  log" and dashd federates by fanning one path out rather than writing three
  clients.
- **Note — F29's premise was wrong about authd.** "authd mounts no `/v1` at
  all" is false: `authd/http.go:124-135` mounts five, and `GET
  /v1/identities/{sub}` is already a bearer-plus-scope-gated READ of `auth.db`.
  The new endpoint reuses that gate rather than inventing a second one, so the
  change is smaller than the entry implied.
- **Note — the authd column was audited before it was published.** authd's
  `params_summary` had exactly ONE writer, `daemon.start`'s
  `{dsn, serving_keys, service_subs}`; both counts are `len()` values and the
  `login` row sets none. No signing key, refresh token or service secret is
  reachable — `audit.Query` names `audit_log` and no other table. The DSN (a
  host path, not key material, but where a credential would hide) is now
  redacted at the WRITER via `audit.redactRE` and scrubbed from history by
  authd migration `0007`. `TestAuthdParamsSummaryHasOneWriter` fails if a new
  emit site appears, so the audit cannot silently go stale.
- **Note — containment never reads the folder claim to decide authority.** An
  absent `arz/folder` is not evidence of operator status (routd stamps a folder
  only when the sub holds exactly one scope, so a two-grant tenant is equally
  claimless) — that is the recorded REST list-all leak. REST authorizes on the
  `audit:read` scope, which no human bearer can hold because a user token's
  scopes are folder globs and `auth.scopeMatches` rejects any held value
  without a colon. The claim only NARROWS an already-authorized call.
- **Also fixed here:** `5/I` open question 1 — the redaction set and the
  cap encoding are pinned (`audit/log.go`), and the spec's "1 KB" was corrected
  to the 512 bytes `audit/PLAN.md` and the code always used.
- **Not done here:** onbod's `audit_log` — see `F35`.

## ✅ FIXED 2026-08-06 F24 — proactive interjection has no dashd surface (2026-08-06, FIXED)

The last unmet definition-of-done item for spec `5/6`, and the only reason that
spec is still `partial`. An operator can turn proactive interjection on
(`PROACTIVE_ENABLED`) and set a group to `mode: lurk`, but then has no way to
see the resulting state:

- **Which groups lurk** is only visible by reading each
  `groups/<folder>/CLAUDE.md` frontmatter by hand. `routd`'s `modeCache`
  (`routd/proactive.go:200`) already holds the parsed answer per folder.
- **When a chat last fired** lives in `routd.db`'s `chat_proactive`
  (`routd/db.go:1267`) and is reachable only by SQL. The 24h cooldown means
  "why didn't it speak" is usually this row, and an operator cannot look.
- **A misconfigured block** — unknown mode, bad `quiet_hours`, bad tz — makes
  the group fire nothing and logs a config error (`proactive.go:126-155`). It is
  correctly loud in the log and completely invisible in the dashboard, which is
  where an operator would look after a group went quiet.

Not urgent while the feature ships off everywhere (nothing in `template/` sets
`PROACTIVE_ENABLED`), but it blocks calling `5/6` shipped, and the
misconfigured-group case is the one that will actually cost someone an hour.

- **Severity:** low today, medium the day an operator enables it
- **Scope:** dashd / routd proactive (spec 5/6)
- **Affected:** any operator who enables proactive interjection
- **Source:** routd/proactive.go:200,126-155; routd/db.go:1267; dashd/ (absent)
- **Status:** FIXED 2026-08-06
- **Fix:** shipped as proposed — `dashd/proactive_page.go`, `GET
/dash/proactive/`, operator-only, in the nav. Two tables: per-group mode +
  quiet hours + parse error, and per-chat last-fired from `chat_proactive`.
  Reads only, for the reason recorded above.
- **Note — the parser is now shared, not copied.** The page needs the same
  answer routd's scanner gates on, so `parseProactiveMode`/`proactiveMode`/
  `quietWindow` moved out of `routd/` into a new `proactive/` package as
  `Parse`/`Mode`/`Window` (commit `30d1bff7`). A second frontmatter reader in
  dashd would have drifted from the one that decides whether a group fires, and
  the drift would have surfaced as a dashboard claiming `lurk` about a group
  routd refuses to run. `Window` gained `Raw` so the page shows the operator's
  own quiet-hours text.
- **Note — the page states the switch, it does not read it.** dashd cannot see
  routd's environment, so a banner names `PROACTIVE_ENABLED` and says the
  feature is off unless the operator opted in. A green "enabled" dot dashd
  cannot verify would be worse than no dot.
- **Not done here:** setting `mode:` from the dashboard. Spec `5/6` makes the
  group's `CLAUDE.md` the single source, and the cooldown mandatory, so there
  is no control to offer without changing the spec — see `F29`'s sibling
  reasoning about new contracts needing sign-off.

## F25 — components/dashd.html documents the retired HMAC identity model (2026-08-06, open)

Same class as `F11`, different page, found while adding the `/dash/proxyd/` row.
`template/web/pub/arizuko/components/dashd.html` tells operators that dashd
verifies `auth.RequireSigned(PROXYD_HMAC_SECRET)` on every `/dash/*` request
(line 83), that an unsigned request carries no valid `X-User-Sig` (83), and that
they should "set `PROXYD_HMAC_SECRET` to the same value proxyd uses" (88, 98).
None of that is real: `auth/middleware.go` exports only `ProxydTransit`, no Go
file reads `PROXYD_HMAC_SECRET`, and proxyd deletes `X-User-Sig` on entry and on
forward (`proxyd/main.go:92,828`). `reference/env.html` already marks the var
retired, so the site contradicts itself.

Two smaller things on the same page:

- Line 65 links `specs/5/5-uniform-mcp-rest.md`, which does not exist under that
  name — `5/5` is worlds-agents-sessions, the mechanism spec is `5/17`. (Two
  `.go` comments cite the same dead name: `proxyd/resource.go:301` and
  `webd/routes_mcp.go:26`.)
- The page set omits five shipped control planes: `/dash/services/`,
  `/dash/audit/`, `/dash/usage/`, `/dash/routd/`, `/dash/runed/`
  (`dashd/main.go:432-439`). `/dash/proxyd/` was the sixth and is now listed.

An operator following this page sets an env var nothing reads and concludes the
auth is configured.

- **Severity:** medium (documents a security mechanism that was removed)
- **Scope:** web docs — components/dashd.html
- **Affected:** operators reading the dashd page
- **Source:** template/web/pub/arizuko/components/dashd.html:65,74,83,88,98; auth/middleware.go:22; proxyd/main.go:92,828
- **Status:** open
- **Fix:** mechanical — replace the HMAC paragraphs with the `service:proxyd`
  transit bearer (`components/proxyd.html` now carries the correct wording to
  copy), repoint the spec link at `5/17`, add the five missing pages. Worth a
  sweep for the same HMAC strings across the other component pages in the same
  pass rather than one page at a time.

## F20 — the retry-exhausted notice hardcodes "3 attempts" (2026-08-06, open)

`MAX_TURN_RETRY` is configurable (`core/config.go:250`, default 3) and
`routd/dispatch.go:313` honours it, but the message the user finally sees is a
constant: `retryExhaustedNotice` (`routd/dispatch.go:455`) reads
`"⚠️ Agent couldn't complete this request after 3 attempts."` Raise
`MAX_TURN_RETRY` to 5 and a user who waited through five spawns is told there
were three. Cosmetic, but it is the one sentence about retry any user ever
reads, and it misreports the operator's own configuration. Found while writing
`concepts/retries.html`, which had to document the mismatch rather than the
behaviour.

- **Severity:** low (wrong number in a user-facing string; no runtime effect)
- **Scope:** routd turn retry (spec 5/12)
- **Affected:** any instance with `MAX_TURN_RETRY != 3`
- **Source:** routd/dispatch.go:455 vs routd/dispatch.go:313; core/config.go:250
- **Status:** open
- **Fix:** make it a format string over `l.maxTurnRetry`, and drop the
  now-inaccurate caveat sentence from `concepts/retries.html` §"tuning it".

## F21 — `scheduled_tasks`' REST face is unadvertised and its single-source guard can't see it (2026-08-06, fixed)

`routd/tasks_http.go:26-32` builds the resource, then **replaces**
`res.Endpoints` with an inline literal mounting `/v1/tasks*` — while the
canonical `resources.ScheduledTasksEndpoints` declares `/v1/scheduled_tasks*`.
So the resource named `scheduled_tasks` serves REST at `/v1/tasks`, against
`5/17`'s "the resreg `Name` becomes the `/v1/<name>` REST path". It is also
absent from `routd.OpenAPIResources` (`routd/server.go:246`), so the endpoints
work but no `/openapi.json` reader or generated client finds them.

The guard that should have caught this doesn't. `endpoints_source_test.go:29`
asserts `srv.scheduledTasksResource(nil, false).Endpoints` equals
`resources.ScheduledTasksEndpoints` — it reads the value **before** `mountTasks`
overrides it, so the test passes while the mounted face diverges. Its own header
claims "Reverting any of these to an inline Endpoints literal breaks this test";
for this one resource that is false.

Blocks flipping `5/17` to `shipped`: its acceptance says a cold-tier resource's
MCP tool, `/v1/<res>` REST endpoint and OpenAPI entry all derive from one
handler. The spec defers the general single-sourcing work to `5/16`, but this is
a concrete instance with a test that reports it as already done.

- **Severity:** medium (undiscoverable endpoints + a green test asserting the
  opposite of what holds)
- **Scope:** resreg / routd scheduled_tasks (specs 5/17, 5/16)
- **Affected:** all instances; any OpenAPI-generated client
- **Source:** routd/tasks_http.go:26-32; resreg/resources/scheduled_tasks.go:30-31; routd/server.go:246; routd/endpoints_source_test.go:29
- **Status:** FIXED 2026-08-06 — took the truthful-doc path, NOT the rename.
  `resources.ScheduledTasksEndpoints` now declares the real mounted set
  (`GET /v1/tasks`, `GET|PATCH|DELETE /v1/tasks/{taskId}`) plus
  schedule/pause/resume as `MCPOnly` — the existing flag `RegisterREST` and
  `openapi.go` both skip and `deriveMCPTools` does not. `mountTasks` mounts that
  slice verbatim (override deleted) and `scheduled_tasks` joins
  `OpenAPIResources`. Zero wire change: the same five agent tools, the same four
  REST routes, four previously-phantom paths gone from the doc and four
  previously-hidden ones now in it.
- **Why not the rename:** `/v1/tasks` has live cross-container callers.
  `timed/dash.go:82` calls `GET /v1/tasks` for the `/dash/timed/` task table,
  and the hand-rolled fire loop shares the prefix — `GET /v1/tasks/due`,
  `POST /v1/tasks/runlog`, `POST /v1/tasks/{id}/reschedule`
  (`timed/split.go:205,219,230`), the path every scheduled task on every
  instance runs through. routd and timed restart independently, so a rename is
  a breaking change against a running fleet; worse, it would move only the CRUD
  half and split one control surface across two prefixes. The name stays the
  wire identity for MCP (tool prefix + `operationId` are still
  `scheduled_tasks.*`); only the REST path is the documented exception.
- **Guard:** `TestMountTasks_ServesCanonicalEndpoints` probes the mux
  `mountTasks` builds via `http.ServeMux.Handler`'s returned pattern, so it sees
  the mounted table rather than the constructor's return value, and refuses to
  pass vacuously when the canonical slice declares no REST endpoint. Verified
  falsifiable: restored to the pre-fix state, the old constructor assertion
  still PASSES while the new guard fails on all five endpoints and
  `TestOpenAPI_ScheduledTasksAdvertised` names each phantom path.

## F27 — `GET /v1/acl` is advertised but mounted nowhere; two more mount-time Endpoint overrides (2026-08-06, FIXED 2026-08-06)

Same class as `F21`, found while fixing it, in the two resources that were left.
`mountACL` (`routd/acl_resource.go:79`) and `mountGroups`
(`routd/groups_http.go:18`) also assign `res.Endpoints` AFTER the constructor
returns, so `TestResourceEndpoints_SingleSource` — which reads
`srv.aclResource()` / `srv.groupsResource(nil)` — cannot see what they mount.

For `acl` this is already a live doc defect, not just latent. `ACLEndpoints`
(`resreg/resources/acl.go:30-34`) declares add + remove + **list**; `mountACL`
mounts only add + remove, and no other mux registration serves `GET /v1/acl`
(grepped: zero `.go` hits). `acl` IS in `OpenAPIResources`, so routd's
`/openapi.json` advertises a `GET /v1/acl` that 404s — exactly the phantom the
`OpenAPIResources` comment says the list exists to prevent. `groups` escapes
only because it was deliberately omitted from `OpenAPIResources` for this very
reason (the comment says so), which is a workaround for the override, not a fix.

- **Severity:** medium for `acl` (an advertised endpoint that 404s), low for
  `groups` (papered over by the omission)
- **Scope:** resreg / routd acl + groups (specs 5/17, 5/16)
- **Affected:** all instances; any client generated from routd's `/openapi.json`
- **Source:** routd/acl_resource.go:79; routd/groups_http.go:18;
  resreg/resources/acl.go:30-34; resreg/resources/groups.go:35-38;
  routd/server.go:246
- **Status:** FIXED 2026-08-06
- **Fix:** the `F21` shape, applied to the whole class. `list` on `ACLEndpoints`
  and `register` on `GroupsAgentEndpoints` are now `MCPOnly`; both overrides are
  deleted so `mountACL`/`mountGroups` mount the canonical slice verbatim; and
  `groups` joined `OpenAPIResources` (its omission was the workaround, not a
  fix). Consumers checked first: `grep -rn "v1/acl"` and `"v1/groups"` across
  the tree find zero callers of either — the only hits are the declarations,
  docs describing the POST/DELETE twins, and dashd reading `GET /v1/groups`.
- **Third instance, found by the widened guard:** `network_rules` declared
  POST/DELETE/GET `/v1/network_rules` that NO daemon mounts and none
  advertises — the same class one step further along (declared, unmounted,
  unadvertised, so invisible instead of 404ing). All three actions are now
  `MCPOnly`, which is what its own comment already said in prose
  ("agent-MCP-only"). Reached only via `network_allow`/`network_deny`/
  `network_list`.
- **Also surfaced:** `daemonOwnership` in `resreg/resources/resources_test.go`
  claims to mirror the daemons' real `OpenAPIHandler` lists and had drifted
  three resources in EACH direction for routd (claimed `acl_membership` +
  `network_rules`, which routd advertises for neither; missing `route_tokens`,
  `installed_packages`, `scheduled_tasks`). It cannot import `routd` (cycle),
  so it is a copy by construction. Corrected, and its comment now says so and
  points at the routd-side check that reads the real list.
- **Guard:** the per-resource probe is now class-wide, in
  `routd/endpoints_source_test.go`:
  - `TestRoutdMux_ServesEveryDeclaredEndpoint` — for EVERY routd resource, every
    non-MCPOnly canonical endpoint must resolve on the mux `Server.Handler()`
    really builds, to a pattern byte-equal to its declaration; and an MCPOnly
    endpoint must carry no Verb/Path. Any mount function that drops, adds, or
    re-paths a face fails here, in whichever resource it happens.
  - `TestOpenAPI_EveryAdvertisedPathIsMounted` — the doc side, with NO
    hand-maintained list: every `(method, path)` in the emitted
    `/openapi.json` must resolve on the same mux.
  - `TestRoutdResources_CoverAdvertised` — keeps the constructor table honest:
    every name in `OpenAPIResources` must be probed by the first guard.
  Verified falsifiable: restoring the exact pre-fix state (list back to
  `GET /v1/acl`, the trimming literal back in `mountACL`) fails exactly those
  guards and nothing else in `routd` —
  `endpoints_source_test.go:137: acl: GET /v1/acl: routd's mux serves pattern
"", want "GET /v1/acl"` and `:181: /openapi.json advertises "GET /v1/acl" but
  routd's mux serves pattern ""`. The pre-existing constructor-equality
  assertion passes throughout, which is the blindness being closed.

## F28 — the emitted `operationId` is `<action>_<name>`; `5/17` specifies `<name>.<action>` (2026-08-06, FIXED 2026-08-06 — spec moved, emitter kept)

`specs/5/17-openapi-mcp.md:192-196` §"Resource name = wire identity" states the
`operationId` is `<name>.<action>` and calls the composed `<name>.<action>`
string "the operator-facing contract — OpenAPI `operationId`, audit `action=`
fields, metrics labels, permission-editor rows — so URL and handler-function
renames don't break it". `resreg/openapi.go:264` emits
`fmt.Sprintf("%s_%s", e.Action, r.Name)`, and the five convention-path emitters
(`:320,325,337,342,348`) match that form, so every daemon's doc ships
`list_routes`, `cancel_scheduled_tasks`, … — never a dotted id.

One of the two is wrong and neither is obviously the loser: the emitted form is
what any already-generated client holds, while the dotted form is what the spec
promises and what the MCP tool names use. Found by writing
`TestOpenAPI_ScheduledTasksAdvertised`, which now pins the EMITTED form so the
convention cannot drift again silently while the question is open.

- **Severity:** low (cosmetic in the doc; no runtime effect) but blocks `5/17`
- **Scope:** resreg OpenAPI emission (spec 5/17)
- **Affected:** every daemon's `/openapi.json`
- **Source:** resreg/openapi.go:264,320,325,337,342,348 vs specs/5/17-openapi-mcp.md:192-196
- **Status:** FIXED 2026-08-06 — the SPEC moved; the emitter is unchanged.
- **Decision:** `operationId` stays `<action>_<name>`. Three reasons, in order
  of weight:
  1. **OpenAPI's own guidance points away from a dot.** 3.1 §4.8.10.1, verbatim:
     "Tools and libraries _MAY_ use the operationId to uniquely identify an
     operation, therefore, it is _RECOMMENDED_ to follow common programming
     naming conventions." Generators turn `operationId` into a client method
     name (openapi-generator even ships `--remove-operation-id-prefix` because
     of it), and `.` is not an identifier character in Go, Java, Python, or
     TypeScript — a dotted id gets mangled by the generator, so the dot never
     reaches the caller. Verb-first also matches OpenAPI's own examples
     (`listPets`, `showPetById`).
  2. **The dotted form was never the contract it claimed to be.** The spec
     sentence said `<name>.<action>` is one string used by "OpenAPI
     `operationId`, audit `action=` fields, metrics labels, permission-editor
     rows". Checked all four: `audit_log` writes `resource=` and `action=` as
     SEPARATE fields (`resreg/resreg.go` `emitAudit`/`buildEvent`); `acl` grant
     rows carry `mcp:<flat tool name>` (`mcp:list_acl` — `routd/acl_resource.go`);
     `obs/metrics.go` has no resource/action label at all. Only
     `deriveMCPTools`' DEFAULT is dotted, and every folded resource overrides it
     via `MCPNames` to a flat name. So one edit to the spec broke nothing.
  3. **Changing the emitter is the expensive side** — a wire change for every
     generated client across eight advertised resources on eight daemons — to
     buy a form the generators would strip anyway.
- **Shipped:** `specs/5/17` §"Resource name = wire identity" rewritten: the
  identity is the `(name, action)` PAIR, with a table of the four per-surface
  spellings, so no reader can infer a single composed string again.
  `TestOpenAPI_ScheduledTasksAdvertised` keeps pinning the emitted form (its
  comment now cites the decision rather than an open bug).

## ✅ FIXED 2026-08-06 F26 — `reference/openapi.html` still documents the `scheduled_tasks` gap that `F21` closed (2026-08-06, FIXED)

`template/web/pub/arizuko/reference/openapi.html:99` says routd advertises "six"
resources and `:106` says `scheduled_tasks` "isn't in the advertised list — so
those endpoints work but no generated client will find them. A known gap, not a
missing feature." Both became false when `F21` shipped: `OpenAPIResources` now
carries seven names and `/v1/tasks*` is in the doc.

Filed rather than fixed because `template/web/pub/` was another agent's lane in
the same session. Not edited to avoid a concurrent-write collision.

- **Severity:** low (stale operator doc), but it is `5/17`'s definition-of-done
  item 5 ("Online"), so it holds the spec at `partial`
- **Scope:** web docs (spec 5/17)
- **Affected:** readers of `/pub/arizuko/reference/openapi.html`
- **Source:** template/web/pub/arizuko/reference/openapi.html:99,106 vs routd/server.go OpenAPIResources
- **Status:** FIXED 2026-08-06. The count was derived from the emitter rather
  than copied: `resreg.OpenAPI("routd", "/", routd.OpenAPIResources)` emits
  **8 resources over 15 paths** — routes, web_routes, acl, secrets,
  route_tokens, installed_packages, scheduled_tasks, groups. The page now says
  eight, lists all eight in the daemon table, describes the four that are
  narrower than CRUD (secrets write-only, installed_packages read-only, groups
  read-only, scheduled_tasks minus the three `MCPOnly` verbs), and carries the
  `/v1/tasks` carve-out as a callout so no future reader files it as drift.
  The trailing paragraph went four → three resources (`network_rules`,
  `acl_membership`, `proxyd_routes`) since `groups` and `scheduled_tasks` left
  it. Two adjacent falsehoods found while verifying and fixed in the same pass:
  the onbod row listed one of its three advertised resources, and the
  `Endpoint` example cited `POST /v1/scheduled_tasks/pause`, a path nothing
  serves — now `POST /v1/route_tokens/chat`, which is real.
  **Not** fixed: `timed`'s row, which is true as written (timed owns no
  resource) but whose document advertises four operations timed does not
  serve — filed as `F32`.
- **Was:** six → **eight** (`F27` added `groups` after `F21` added
  `scheduled_tasks` — the current list is routes, web_routes, acl, secrets,
  route_tokens, installed_packages, scheduled_tasks, groups). Replace the
  `scheduled_tasks` gap paragraph with the real shape — REST at `/v1/tasks`
  (not `/v1/<name>`) because timed calls it across the container boundary,
  schedule/pause/resume `MCPOnly` so the agent has tools the doc correctly
  omits. `groups` is the same shape mirrored: its `register` action is
  `MCPOnly`, so only `GET /v1/groups` is advertised. Then deploy and verify
  `/pub/...` 200.
- **Blocks:** `specs/5/17` — this was its ONLY unmet definition-of-done item.
  Unblocked; the spec is now `shipped`.

## ✅ FIXED 2026-08-06 F30 — `5/28` composition's lock is instance-keyed; its subject is group-scoped (2026-08-06, FIXED)

**Fixed `24e5f36d`** — the second exit was taken: `installed_packages` is
re-keyed `(folder, name)` (routd migration `0031`, store `0082`), `''` meaning
instance-wide. One lock, correctly keyed, rather than the group-scoped second
lock `5/28` forbids in its own `/migrate` paragraph. The sentinel is house
convention, not an invention — `network_rules` already ships `(folder, target)`
with `''` for a global rule.

Existing rows map to `''` and keep working untouched: every one is instance-wide
by construction, since install writes compose fragments, proxyd routes and host
files, none of which belong to a group. The CLI still writes `InstanceWide`, so
`install`/`upgrade`/`remove` behave identically; `packages list` never read the
DB at all (it lists fragments off the filesystem).

Rehearsed on `.backup` copies of all three live `routd.db` files. **All three
hold ZERO `installed_packages` rows**, so each was also run seeded with three
synthetic rows — a 0-row rehearsal proves only that the DDL parses, which is the
"matched 0 rows, looked fine" failure mode itself. Seeded result: 3 rows in, 3
out, content byte-identical, all on the sentinel, PK `(folder, name)`,
`integrity_check` ok. Pre-existing `task_run_logs` FK violations are unchanged in
count before and after (see `F37`); nothing references `installed_packages`.

Authorization deliberately did NOT move with the key — both faces still bind the
whole tree, because `list` reads across folders and the record names cross-folder
identities regardless of its own folder.

**Still open, and NOT this entry:** composition itself. Its three remaining gaps
(no `sync`/`update` verb, no `CLAUDE.md` marker convention, payload kinds that
miss the corpus) are recorded in `5/28` §"Composition's remaining gaps". `5/28`
stays `partial` for exactly those.

<details><summary>Original entry</summary>


`F3` recorded that `5/28`'s composition section is unbuilt. Trying to build it
shows it is **unimplementable as written**, which is a stronger claim and a
different fix.

The section makes `~/products.toml` a GROUP-home file — one ordered product mix
per group (`specs/5/28-packages.md`, "Composition"; the sibling `~/CLAUDE.md`
in the same paragraph is the group-dir file `5/21` seeds). But the spec also
says, of the installed-package record, "One mechanism, not two — the lock
composition (below) needs IS this per-instance **installed record**". That
record is `installed_packages(name TEXT PRIMARY KEY, source, revision,
manifest, asset_hashes, installed_at)` — no folder column, and its own
migration comment reads "one row per package installed on this **instance**"
(`routd/migrations/0020-installed-packages.sql`). A per-group mix cannot key
into an instance-keyed table, so the blend table's "On upstream update" column
(dirty-detection, clean replace) has no lock to read.

The two exits are both sign-off-shaped, which is why nothing was built:

- give composition its own group-scoped lock — a second package manager, which
  `5/28` forbids in its own `/migrate` paragraph ("the merge lib … does not
  grow into a second package manager"); or
- refold `installed_packages` to `(folder, name)` — an owned-schema change to a
  shipped routd table, changing what every existing row means.

Three further gaps block even the seed half from being *built* rather than
*invented* (all recorded in the spec's new "Composition's unresolved lock"):
no `sync`/`update` verb is specified though the table's third column requires
one; "`CLAUDE.md` appended as marked sections" names no marker convention; and
the payload kinds miss the corpus — no product in `ant/examples/` ships
`skills/`, `tasks.toml`, `settings.json`/`mcpServers`, `Dockerfile.ant` or
`migrations/`, 7 of 10 ship `SOUL.md` (auto-migrated only at READ time,
`container/runner.go:497`) which the table never names, and every product ships
`PRODUCT.md` — also unnamed, so a table-strict blend DROPS it and regresses the
verbatim `CopyDirNoSymlinks` seed (`container/runner.go:979`).

**Checked and NOT the problem:** this is *not* a duplicate of `5/21`.
`5/21:14-17` explicitly cedes composition to `5/28` ("supersedes this file's
single-template narrative: a group holds an ORDERED MIX"). The N-ary layer is a
genuine generalization of `--product <one>`; only its lock is wrong.

- **Severity:** low (no runtime effect; blocks an unbuilt section)
- **Scope:** specs/5/28 composition vs routd `installed_packages`
- **Affected:** nobody today — no reader exists
- **Source:** specs/5/28-packages.md "The installed-package record" + "Composition";
  routd/migrations/0020-installed-packages.sql; container/runner.go:497,979
- **Status:** proposed — needs a scope decision (is composition group-scoped at
  all?) before any reader is written. Do NOT implement `products.toml` first.

</details>

## F37 — `task_run_logs` holds orphan rows on all three live instances (2026-08-06, open)

Found while proving migration `0031` caused no FK damage: `PRAGMA
foreign_key_check` on a `.backup` copy of each live `routd.db` reports violations
on `task_run_logs` and only `task_run_logs` — krons 10, sloth 32, marinade 66.
Identical counts before and after `0031`, so this is pre-existing and unrelated
to the re-key; recorded here rather than fixed (record-don't-fix).

The rows point at a parent that no longer exists (the FK was added by a later
migration than the data, or a task delete did not cascade). Not currently
harmful: routd opens `routd.db` with `_pragma=foreign_keys(on)`, and SQLite does
not re-validate existing rows — only new writes are checked. It becomes harmful
the moment any migration rebuilds `task_run_logs` or its parent the way `0031`
rebuilt `installed_packages`: a rebuild under FK enforcement can fail outright or
silently drop the orphans.

The count growing with instance age (10 / 32 / 66) suggests an ongoing producer,
not a one-off backfill.

- **Severity:** low today, latent trap for the next `task_run_logs` migration
- **Scope:** routd.db `task_run_logs`
- **Affected:** krons (10), sloth (32), marinade (66)
- **Source:** `PRAGMA foreign_key_check` on read-only `.backup` copies, 2026-08-06
- **Status:** open — needs the producer identified before deciding delete-orphans
  vs relax-the-constraint. Do NOT rebuild `task_run_logs` until then.

## R3 — a killed run still launches its container, on a folder that now reads free (2026-08-05, FIXED)

`Z4`/`43cf6d7a` guarded `DB.StartSpawn` on `state='queued'` so a `DELETE`
landing between `admit`'s `CreateSpawn` and the start can no longer resurrect
a `killed` row. The row is now right — but `Manager.spawn` discarded the
result (`_ = m.db.StartSpawn(runID, sessionID)`, `runed/manager.go:312`) and
called `exec.Run` unconditionally ~20 lines later, so the LAUNCH was never
gated.

That guard made the failure worse, not better. Before it, the un-`WHERE`d
`StartSpawn` flipped `killed`→`running`: DB-wrong, but it accidentally kept
`ActiveSpawnForFolder` (which counts only `queued`/`running`) reporting the
folder busy, so nothing else could claim it. After it, the row correctly
stays `killed`, the folder reads **free**, and the just-killed run's container
still comes up — a concurrent `POST /v1/runs` for that folder is admitted and
**two containers share one folder's mount**, breaking the exclusivity
invariant `hold.go` is built on. `spawn()` also returned `Outcome: ok` for the
killed run, so routd advanced its cursor past a batch nothing had processed.

Reproduced through `Manager.spawn` before fixing: claim via `admit`,
`Kill` it, then `spawn()` — `ActiveRunID` returns `""` while the fake
executor fires and the call returns `{Outcome: ok}`. No existing test covered
it: `TestSerializationNoConcurrentDoubleSpawn` races concurrent `Run()` calls
only, and `TestKillSpawnDoesNotOverwriteTerminal` stops at the DB layer.

- **Severity:** high (folder-exclusivity, the invariant holds/restores depend on)
- **Scope:** runed run lifecycle — claim→launch gating
- **Affected:** every kind; `KindAgent` is the one that spawns a real container
- **Source:** `runed/manager.go:312`, `runed/db.go:210` (as of `ff119ed6`)
- **Status:** fixed 2026-08-05 — `f64cc46c`
- **Fix:** `StartSpawn` returns `(started bool, err error)` off `RowsAffected`;
  `spawn()` takes the claim as its FIRST act and returns
  `RunOutcome{Busy: true}` when it did not fire. `Busy` — not a new
  discriminator — because routd's existing handling is already exactly right
  for "nothing ran": cursor un-advanced, batch re-fed next poll, not charged
  to the circuit breaker (`routd/dispatch.go:278`). It terminates: the killed
  row is terminal, so the next poll's admission succeeds. No audit row —
  `runed` owns no `audit_log`, and the kill is already recorded as
  `spawns.state='killed'`; the prevented launch surfaces as `slog.Warn`. No
  `EndSpawn`/`endRun` — returning first is what keeps an aborted launch from
  opening a `session_log` row nothing closes, from clearing a real failure
  streak, and from deleting a steer callback that by then belongs to the
  folder's NEXT run.
- **Live status:** the missing gate is in the tree, but per `Z1` all three
  instances run a 2026-07-30 image, which predates `43cf6d7a` — so the
  free-folder variant is NOT live yet. It becomes live the moment `Z1`'s
  stale-image gap is closed, which makes this a redeploy blocker rather
  than an active incident. A redeploy is required for the fix to reach them.

## C1 — package names can traverse outside `services/` (2026-07-28, open)

`arizuko packages <instance> add|remove <name>` joins the unvalidated name
directly into filesystem paths. `remove ../docker-compose` resolves to the
instance's `docker-compose.yml`; deeper traversal can delete any accessible
`.yml` file. The generator validates discovered filenames, but the mutating CLI
does not apply the same boundary.

- **Severity:** high
- **Scope:** compose package CLI path containment
- **Affected:** `arizuko packages add|remove`
- **Source:** cmd/arizuko/packages.go:72-92
- **Status:** fixed 2026-07-28
- **Fix:** `mustPkgName` validates `<name>` against `pkgNameRE` (identRE shape:
  no leading `.`, no `/`) in both `add` and `remove` before any path join.

## C2 — package routes never update an existing proxyd route table (2026-07-28, proposed)

Package sidecars only change generated `PROXYD_ROUTES_JSON`, but proxyd reads
that value only when `proxyd_routes` is empty. Adding Slack to an existing
instance therefore does not install `/slack/`; removing it leaves the persisted
public route pointing at a missing service. Fixing this needs one authoritative
package-route lifecycle with provenance so removal cannot erase an
operator-edited row.

- **Severity:** high
- **Scope:** compose packages / proxyd route ownership
- **Affected:** packages with `*-routes.json`, currently slakd
- **Source:** compose/compose.go:342-367,644; proxyd/main.go:233-277
- **Status:** proposed — resolution designed in `specs/5/28-packages.md`
- **Fix:** NOT a reconciler (that model was drafted + demolished by codex
  2026-07-28, `.ship/critique-cto-20260728.md`). Resolution: **exclusive
  ownership + an install receipt.** A resource is package-owned or `local`, never
  both (no three-way merge). The receipt records the `proxyd_routes` PK a package
  owns; `packages remove` calls `DELETE /v1/routes/<pk>` for its set — no hash
  check, since an operator can't have edited a package-owned row without
  `detach`ing it (which drops it from the receipt). Needs sign-off + the receipt
  schema before implementation.

## C3 — legacy conversion overwrites native package files (2026-07-28, open)

When `<name>.toml` coexists with `<name>.yml` or
`<name>-routes.json`, conversion silently overwrites the native/operator-edited
file before deleting the TOML. This can happen after a partial/manual migration
or after `packages add`; the conversion should fail on a non-identical
destination rather than choose one source implicitly.

- **Severity:** high
- **Scope:** compose legacy migration
- **Affected:** pre-v0.62 data dirs with mixed TOML/YAML package state
- **Source:** compose/legacy.go:73-89
- **Status:** resolved 2026-07-28 — legacy converter deleted
- **Fix:** `compose/legacy.go` removed entirely once every live data dir was on
  `.yml` (krons/marinade/sloth converted, `.toml.bak` kept). `.yml` is now the
  only package format, so the overwrite hazard no longer exists.

## C4 — compose migration accepts unsupported Compose v2 versions (2026-07-28, open)

The generated file uses top-level `include`, which requires Docker Compose
2.20+, while INSTALL.md accepts any Compose v2. On an older qualifying host,
generation first converts and deletes legacy TOMLs, then Compose rejects the
new model; the old binary no longer has its adapter inputs for rollback.

- **Severity:** high
- **Scope:** compose compatibility / upgrade preflight
- **Affected:** hosts running Docker Compose v2.0-v2.19
- **Source:** compose/compose.go:601-603,656-660; INSTALL.md:20-21
- **Status:** resolved 2026-07-28 — legacy converter deleted
- **Fix:** no `generate` ever converts+deletes now (converter removed); INSTALL.md
  states the Compose 2.20+ requirement (`include:`). The destructive-migration
  race is gone with the converter.

## C5 — `packages add ttsd` installs no Kokoro dependency (2026-07-28, open)

The CLI copies only the named fragment, but `ttsd.yml` has a required
`depends_on: [kokoro]` and Kokoro is a separate package. Adding ttsd alone—or
removing Kokoro while ttsd remains—makes the whole Compose model invalid.

- **Severity:** high
- **Scope:** compose package dependencies
- **Affected:** ttsd / kokoro
- **Source:** cmd/arizuko/packages.go:70-83; template/services/ttsd.yml:11-13
- **Status:** fixed 2026-07-28
- **Fix:** `add` parses the fragment's `depends_on` and warns for any dep that is
  neither a base daemon nor an enabled package (e.g. `ttsd` → `kokoro`).

## C6 — malformed compose-managed marker can erase `.env` tail (2026-07-28, open)

`writeManagedEnv` treats any prefix match as a marker and never checks marker
balance. An unmatched begin marker causes every following operator line,
including tokens and feature flags, to be omitted from the atomic replacement.
Complete blocks, CRLF, and missing trailing newlines are otherwise idempotent.

- **Severity:** medium
- **Scope:** compose-managed `.env` rewrite
- **Affected:** data dirs with malformed or partially edited managed markers
- **Source:** compose/compose.go:443-454
- **Status:** fixed 2026-07-28
- **Fix:** `writeManagedEnv` now finds a single well-formed `[begin,end]` block and
  fails loud on any malformed marker (begin without end, duplicate, end-first)
  instead of dropping every following line — operator secrets can't be erased.

## C7 — package add can overwrite or partially install a package (2026-07-28, open)

`packages add` writes the live YAML directly before reading/writing its optional
route sidecar. Re-adding truncates an operator-customized fragment; a sidecar
read/write failure leaves the fragment enabled despite the command reporting
failure. Package input should be validated and staged before any destination is
replaced.

- **Severity:** medium
- **Scope:** compose package CLI atomicity
- **Affected:** `arizuko packages add`
- **Source:** cmd/arizuko/packages.go:70-83,100-105
- **Status:** fixed 2026-07-28
- **Fix:** `add` reads every source (fragment + optional routes) BEFORE writing any
  destination, and each write goes through `writeFileAtomic` (temp + rename) — no
  half-written fragment on a routes read/write error.

## C8 — route sidecars silently discard unknown fields (2026-07-28, open)

Package route JSON is decoded with permissive `json.Unmarshal`, so a misspelled
optional field such as `strip_prefx` is ignored and a valid-looking but
behaviorally wrong route is emitted. The compose route type also drifts from
proxyd's route shape by omitting `redirect_to`.

- **Severity:** medium
- **Scope:** compose package route schema
- **Affected:** custom package `*-routes.json` files
- **Source:** compose/compose.go:279-286,357-359
- **Status:** fixed 2026-07-28
- **Fix:** route sidecars decode with `DisallowUnknownFields` (a misspelled
  `strip_prefx` now errors), and `ProxydRoute` gained `redirect_to` to match
  proxyd's route shape.

## C9 — queue replay opens circuit breakers before runed is ready (2026-07-30, proposed)

On every full-stack restart, routd begins replaying pending group work before
runed listens on `:8080`. The three immediate `connection refused` attempts
consume each group's breaker budget, so healthy queued work remains blocked
after runed becomes ready. Live krons reproduced this fleet-wide at
07:49:05–07:49:08; restarting only routd after runed was healthy cleared it.

- **Severity:** high
- **Scope:** routd queue replay readiness / runed client retry
- **Affected:** every instance with pending work during restart
- **Source:** krons journal 2026-07-30 07:49:05–07:49:08
- **Status:** proposed (dispatch-readiness redesign, needs sign-off)
- **Fix:** gate queue replay on runed readiness, or keep transient connection
  failures outside the breaker budget until the execution plane is ready

## T1 — stalled typing indicator on timed-out/errored turns (2026-07-26, open)

Symptom (krons, telegram:user/1112184352, 2026-07-26): the agent shows "typing…"
for ~20 min then goes silent with no reply. Recurring; the user recognises it as
"the typing indication again".

Root cause (config + missed clear, one concern — the turn-end typing-clear is
unreliable and the backstop can't cover for it):
- Container RunTTL = 20 min; in-container query timeout = RunTTL−30s = 19.5 min
  (`runed/docker.go:147` `queryTimeoutMs`).
- Typing backstop `chanlib/typing.go:14` `DefaultTypingMaxTTL = 20*time.Minute`
  — **equal to RunTTL**, so it never fires before the run is already SIGKILLed.
- Event-driven clear is skipped on the failing path: `dispatch.go:270` clears
  typing only for `!out.Steered`. Follow-up messages that arrive mid-run get
  **steered** into the live container and hand typing-clear to `submit_turn` —
  which never fires when the turn hits the query-timeout wall and its
  graceful-summary fallback itself errors (observed: `Query timeout reached` →
  `Not logged in · Please run /login`, no `submit_turn`, no content). Typing
  refreshes every 5s for the full 20 min, then stops only at the backstop ==
  the container's own death.

Evidence: 4 query-timeouts + 3 `container exited with error` in 12h; last real
bot reply 10:03 while user msgs at 16:11/16:12/16:13 went unanswered under a
live-but-doomed 19.5-min browser-automation turn.

Fix (redesign — needs sign-off; routd turn-lifecycle + chanlib backstop):
1. **Clear typing on every terminal turn outcome, including steered.** The
   steered branch (`dispatch.go` `out.Steered`) marks the turn done but leaves
   typing owned by a `submit_turn` that may never arrive. When a steered turn's
   parent run ends without delivering the steered turn's reply, that turn's
   typing must be cleared by the run-end reconcile, not orphaned.
2. **Make the backstop a real safety net: strictly below RunTTL** (e.g. derive
   it as a fraction of RunTTL, or plumb RunTTL into the refresher), so a stalled
   turn stops "typing" well before the 20-min wall regardless of which clear
   path was missed. A 20-min backstop equal to the 20-min run is not a backstop.
3. **Secondary — fail loud:** the query-timeout graceful-summary path erroring
   with "Not logged in" means the timeout fallback delivers nothing. That resume
   call losing auth is its own bug; the user-facing timeout must still surface a
   notice (dispatch.go:339 `runFailureNotice` covers the non-steered parent, but
   confirm it fires for the steered-follow-up case).

## X1 — capability secrets leak into the agent container env (2026-07-29, proposed)

`5/13` guarantees table secrets are **broker-only** — "credentials never enter the
container" (`specs/5/13-ext-mcp.md:8,231`). The spawn path breaks it:
`routd/dispatch.go:507` resolves `l.db.FolderSecretsForUser(folder, caller)` — the
FULL folder secret set — and runed injects it as container env
(`container/runner.go:263`). So a connector's API token / a pasted PAT (exactly
the credentials `5/13` says the broker holds and the agent never sees) lands in
the agent's environment. `store.EnvProfileKeys` (`store/secrets.go:99`) already
names the ONLY keys that legitimately belong in spawn env (the AI-model creds the
agent's own harness needs); nothing enforces that subset at spawn.

- **Severity:** high (credential exposure to agent-run code)
- **Source:** routd/dispatch.go:507; container/runner.go:263; store/secrets.go:99
- **Status:** proposed — needs the credential-tier decision + sign-off
- **Fix (redesign, sign-off):** at spawn, inject only `EnvProfileKeys`; every other
  secret stays broker-only, resolved at the ext-call boundary (`5/13` broker path),
  never in env. Reconcile `5/13`/`5/14` to state the split explicitly (env-profile
  tier vs capability/broker tier). Verify no connector relies on its key being in
  env before flipping. Found by codex tearing the ext-auth plan (2026-07-29).

## M1 — `mcpc` socat-connect form 502s since mcpc 0.3.0 (2026-07-16, open)

The documented ad-hoc MCP-call form for agents — `mcpc connect "socat
UNIX-CONNECT:$ARIZUKO_MCP_SOCKET -" @s` (`ant/skills/self/mcp.md:128`,
`ant/skills/mcp/SKILL.md:24`, `ant/skills/migrate/SKILL.md:277`) — fails on the
in-container `mcpc` 0.3.0 with `Failed to connect to MCP server: Proxy response
(502) !== 200 when HTTP Tunneling`. The socket is healthy (routd logs `"mcp
server listening"`, `srw-rw---- /run/ipc/gated.sock`); it's mcpc's HTTP-tunnel
wrapper regressing on the socat form, NOT routd. Known since a June diary note.

Impact: an agent that must reach a NON-native MCP tool via mcpc gets a hard 502
and (marinade atlas, 2026-07-16) narrated it to a user as a routd outage +
falsely tied it to an unrelated transient Slack-send 502. For the route-token
class specifically mcpc is now never the answer: minting is root-only by grant
(tier-0 `*` set), a genuine `/root` turn gets every socket tool natively
(worker-side steering + elevated row-ACL gate, 2026-07-16), and list/revoke
ride the tier-1/2 defaults — a non-elevated agent that can't see a mint tool
lacks the grant, and mcpc wouldn't change that. Any genuinely-non-native tool
still hits this.

Fix (deferred, operator): pin/patch in-container `mcpc` to a version whose
`connect "socat …"` form works, OR switch the documented form + the three
skills to a working direct-`UNIX-CONNECT` form — verify against a live socket
before touching the skill docs.

## M2 — elevated (/root) turns bound by static-tier STRUCTURAL gates (2026-07-16, fixed 2026-07-20 except route_tokens)

The 2026-07-16 elevation fix widened both halves of the per-tool grant
(`ServeTurnMCP` rules → `*`, `turnAuthorize` → allow-all), but the resource
gates' STRUCTURAL checks still keyed on `auth.Resolve(folder)` — the folder's
static tier, blind to `turnMCP.elevated`. So `/root network_allow(...)` /
`add_acl` / `register_group` from a tier-2 folder still 403'd "tier N cannot ..."
(the rhias operator hit exactly this on `network_allow('rhias/content', …)`).

**Fixed 2026-07-20 (commit d452d6ef, user sign-off):** `turnIdentity(folder,
elevated)` (tier 0 under /root, else the folder's tier) is computed once in
`ServeTurnMCP` and threaded into the five postBuild structural gates —
`network_rules`, `acl`, `groups`, `routes`, `scheduled_tasks`. `network_rules`
was doubly broken (the only postBuild that never took the elevated `authorize`
either) — now routed through the shared `toolGrant`. Operator-gated: cmdRoot
elevates only for `IsOperator(sender)`. Test:
`TestNetworkRulesMCP_RootElevationManagesEgress`.

**Residual (open):** `route_tokens` mint cap lives in the HANDLER
(`authorizeRouteTokenMint(auth.Resolve(folder), …)` inside `routeTokensHandler`),
not the postBuild Gate — the handler can't see `elevated` without the
Caller/Execution carrying the effective identity. So under /root a tier ≤2 folder
still can't point a token OUTSIDE its subtree, and tier ≥3 can't mint. Only
cross-subtree mint degrades; own-folder /root mint works. Deferred — plumb the
effective identity into `resreg.Execution` (a small resreg contract change).

**Residual 2 (open, HIGH — wider blast radius, fable-found 2026-07-21):** the
SAME elevation-blind pattern lives in the hand-rolled `ipc/ipc.go:831`
`buildMCPServer` — `identity := auth.Resolve(folder)` derived once, blind to
`t.elevated`, for every tool NOT yet migrated to resreg. Under `/root` from a
tier-2+ folder these stay capped: `fork_topic` (own/descendant), the
`inspect_messages`/JID-routing guards, `set_work`'s tier≤2 **registration** gate
(the tool never even appears in `tools/list`), `get_web_presence`. Fix (proposal,
needs sign-off): thread `t.elevated`/`callerID` into `ipc.ServeMCP` →
`buildMCPServer` so `identity` is elevation-aware there too — a change to the
`ipc` package's public signature + every caller/test, so bigger than route_tokens
and out of the d452d6ef scope. This is the remaining half of the class; the
resreg gates are done, the pre-resreg hand-rolled path is not.

## M4 — `vited` runs the Vite DEV server in production (2026-07-16, partly fixed 2026-07-18)

**Crash class fixed (2026-07-18):** the dev server's dep-optimization cache
defaulted into the bind-mounted `/web` root, where host uid ownership drifted to
root and `vite` (USER node) hit `EACCES: unlink …/.vite/deps/_metadata.json` on a
config change → crash-loop → `/pub` 502 (marinade + sloth, twice). Fixed by moving
`cacheDir` out of the mount to a container-local path (`ant/vite.config.js`) — the
cache is now container-owned + ephemeral, so it can't recur. Immediate incident
patched by chowning the stale `/web/**/.vite` caches back to the container uid.

**Residual (still open):** vited still runs `vite dev` (poll-watch + transform)
rather than serving the static `/pub`+`/priv` tree — a steady ~1-core cost even
with the scoped watcher. The clean end-state is a static serve (`vite preview` or
a plain file server) keeping the pub-fallback/trailing-slash middleware; deferred,
needs sign-off.

## S3 — Slack permanent send failures are forced through the retryable 502 path (2026-07-15, open)

`slakd.Send` correctly wraps its known permanent `chat.postMessage` failures
with `chanlib.ErrInvalidRequest`, but the shared `/send` handler maps every
`BotHandler.Send` error to HTTP 502 instead of using the existing
`writeBotResult` classifier. A deleted/archived channel, missing membership, or
failed root fallback is therefore enqueued/retried as a transient delivery.
Even after repairing that boundary, `slackSendClientErrors` omits documented
permanent failures such as `cannot_reply_to_message`,
`restricted_action_non_threadable_channel`,
`restricted_action_read_only_channel`, and
`restricted_action_thread_locked`, so those still take the 502 path.

- **Severity:** high
- **Scope:** Slack outbound error classification
- **Affected:** Slack text sends and thread replies
- **Source:** slakd/bot.go:927-937,1183-1205; chanlib/handler.go:328-340,450-480
- **Status:** open
- **Fix:**

## D4 — Routd's in-memory 502 outbox is discarded and duplicates the durable retry owner (2026-07-15, proposed)

Every transient adapter failure appends an `outMsg` while still returning the
error. Normal turn delivery separately leaves a pending DB row that routd
retries every 30 seconds, so repeated 502s can append up to 1000 copies of one
message. Production never drains them: adapter heartbeat registration creates
a new `HTTPChannel`, `setLive` overwrites the old queued object, then drains the
new empty object. MCP `send`/`reply` has no pending DB row on failure, so its
supposed queued retry is simply lost. A naive drain fix is unsafe too: successful
drains discard the returned platform ID and cannot mark the DB row sent, causing
the durable loop to send it again. There is no infinite loop (24-hour DB cutoff,
1000-item queue, five drain attempts), but there is a bounded retry storm plus
loss.

- **Severity:** high
- **Scope:** routd adapter delivery / retry ownership
- **Affected:** all outbound text adapters, especially MCP sends and 502 responses
- **Source:** chanreg/httpchan.go:146-154,507-534,553-560; routd/deliver.go:58-64; routd/channels.go:114-117; routd/loop.go:348-377
- **Status:** proposed
- **Fix:**

## D5 — Stable delivery key is dropped before adapters, so ambiguous Slack success can double-post (2026-07-15, open)

Routd supplies a stable outbound row ID and `HTTPChannel` serializes it as
`turn_id`, but `chanlib.SendRequest` has no matching field; JSON decoding drops
it before `slakd.Send`. Slack then calls the generic retry helper for the
non-idempotent `chat.postMessage`. If Slack accepts a post but its response is
lost, or returns an error after partial success, the helper and the pending-row
retry can create another platform message. This directly contradicts routd's
claim that the adapter deduplicates redispatch by stable message ID. File
delivery loses the same contract even earlier: `chanDeliverer.Document` accepts
an idempotency key but drops it before `HTTPChannel.SendFile`, whose multipart
request carries no stable key.

- **Severity:** high
- **Scope:** outbound idempotency contract
- **Affected:** Slack text immediately; all retried text/file adapters lack the promised key
- **Source:** routd/turns.go:297-309,337; routd/loop.go:354-375; routd/deliver.go:123-132; chanreg/httpchan.go:183-221; chanlib/handler.go:17-27,328-345; chanlib/retry.go:22-27; slakd/bot.go:891-942,1441-1457
- **Status:** open
- **Fix:**

## A2 — Release guard advances before announcement delivery, making partial failure permanent (2026-07-15, open)

The migrate skill writes `~/.announced-version` before inspecting routes or
sending any destination. A transient route-read failure, authorization error,
adapter 502, or partial multi-destination fanout therefore marks the entire
release announced; the next `/migrate` prints `SKIP` and never retries the
missing destination. The shell loop also has no per-destination success state.

- **Severity:** medium
- **Scope:** v0.59 release announcement delivery
- **Affected:** every group with one or more `#announce` routes
- **Source:** ant/skills/migrate/SKILL.md:243-252,259-299
- **Status:** open
- **Fix:**

## A3 — Announcement target discovery reads an unscoped, truncated route table (2026-07-15, open)

`inspect_routing` asks `ListRoutes(folder,isRoot)` but routd's production
closure ignores both arguments, swallows the DB error, and returns the whole
instance table. The inspect handler then truncates to the first 50 rows before
annotating them. A group's own `#announce` route after global row 50 is
invisible; unrelated groups' routes can be attempted instead, and a DB failure
looks like a successful empty route list. Combined with the prewritten release
guard, all three cases permanently suppress or misdirect the announcement.

- **Severity:** medium
- **Scope:** inspect_routing / announcement target selection
- **Affected:** multi-group instances, especially those with more than 50 routes
- **Source:** routd/mcp.go:407-412; ipc/inspect.go:12-45; ant/skills/migrate/SKILL.md:259-295
- **Status:** open
- **Fix:**

## R2 — Reserving `#announce` silently steals an existing route topic (2026-07-15, proposed)

Before v0.59, every fragment other than `#observe` was a valid pinned topic, so
`folder#announce` meant topic `announce`. The new parser silently reinterprets
that same persisted value as announcement metadata and clears `Topic`; inbound
messages now run in the root topic. No migration detects or rewrites existing
collisions. Parser-produced values otherwise round-trip correctly, and the
`Mode > Announce > Topic` `String` precedence is safe because parsing never sets
more than one field.

- **Severity:** medium
- **Scope:** route-target backward compatibility
- **Affected:** any pre-v0.59 route whose pinned topic is literally `announce`
- **Source:** core/types.go:94-135; routd/loop.go:630-646
- **Status:** proposed
- **Fix:**

## M3 — MCP send, reply, and send_file hide the platform ID their backends return (2026-07-15, open)

All three handlers receive and persist the created platform ID but return plain
`ok`; `post`, `send_voice`, `forward`, `quote`, and `repost` return JSON IDs.
This contradicts the agent self-doc that says `reply` returns `messageId` and
prevents immediate explicit edit/delete/branch targeting without an extra
message inspection. The stronger claim that sequential reply chaining is
impossible is false: `recordOutbound` updates `SetLastReply`, and the next
`reply` without `replyToId` reads that value, so root→child→grandchild works
implicitly on adapters that return IDs.

- **Severity:** medium
- **Scope:** MCP messaging result contract
- **Affected:** send, reply, send_file on all adapters
- **Source:** ipc/ipc.go:607-680,1016-1123; ant/skills/self/mcp.md:5-13
- **Status:** open
- **Fix:**

## X2 — X tweet replies are wired to an unreachable adapter endpoint (2026-07-15, open)

Routd implements every `reply` through `Deliverer.Send`, which always calls the
adapter `/send` endpoint with `reply_to`. The X adapter accepts that field but
ignores it: `/send` always invokes the DM-only verb and rejects tweet JIDs.
Its working tweet-reply implementation is mounted separately at `/reply`, an
endpoint no production routd/chanreg call uses. An agent replying to
`twitter:tweet/<id>` therefore gets a 502 and posts nothing.

- **Severity:** high
- **Scope:** twitd outbound reply contract
- **Affected:** X/Twitter tweet replies and reply chains
- **Source:** routd/mcp.go:47-52,226-248; chanreg/httpchan.go:183-209; twitd/src/server.ts:7-20,123-147; twitd/src/verbs.ts:52-84
- **Status:** open
- **Fix:**

## Egress allowlist NOT enforced — crackbox allows non-allowlisted hosts (2026-07-14, OPEN, HIGH — security)

A contained-folder test (`libtest`, egress set to pypi-only) proved the
per-folder egress allowlist is **not enforced**. crackbox's own logs:
`{"msg":"allow connect","id":"libtest","host":"example.com"}` (also
`webhook.site`, `http-intake.logs.us5.datadoghq.com`) — every one ABSENT from
`libtest`'s resolved allowlist (`arizuko network resolve libtest` returned only
`anthropic.com`/`api.anthropic.com`/`pypi.org`/`files.pythonhosted.org`). The
"default-deny per-folder allowlist" guarantee in `SECURITY.md` + `ant/CLAUDE.md`
("a host not on your allowlist is refused at CONNECT → 403 on every path") does
NOT hold: a tightly-scoped folder reaches arbitrary hosts.

Practical exposure is masked on marinade today (every live folder is `atlas/*`
with `*` egress by config), but the ENFORCEMENT PATH is broken — you cannot
restrict a folder even by setting a tight allowlist. This blocks any
egress-contained deployment — e.g. the in-container librarian with private data
(Surface B works end-to-end, but its data cannot be egress-contained → "must not
leak" fails at the network layer).

Root cause (needs a focused crackbox audit): routd resolves the tight list
(`routd/dispatch.go:493` `allowlist, _ := l.db.ResolveAllowlist(folder)` — the
error is SWALLOWED, and the comment at `:491` says "nil allowlist on error") and
passes it (`:527 EgressAllowlist`) to the spawn; the proxy decision is
`id, ok := p.allow.Allow(src, host)` (`crackbox/pkg/proxy/proxy.go:65`) which
returned `ok=true` for `example.com` under the CORRECT `id=libtest`. So either
the resolved list isn't applied per-container-IP in crackbox (fail-open when the
per-id policy is missing/empty), or a permissive default exists. The
`_`-swallowed `ResolveAllowlist` error → nil allowlist → fail-open is the prime
suspect.

**Fix direction (record-only, needs sign-off — security redesign):** crackbox
must fail-CLOSED (deny all) for a container whose per-id allowlist is
missing/empty; routd must surface (not swallow) a `ResolveAllowlist` error
rather than spawn with a nil allowlist. Add a containment test: a folder with a
tight allowlist gets 403 on a non-listed host (the exact test that just failed).
This is the structural half of the "librarian must not leak" guarantee (with
`secretscan` for reply-content leaks).

## Release announcement is over-delivered: every group blasts all its routes, once per server-per-group — no opt-in, no main-channel, no mute (2026-07-14, OPEN, proposal)

The `/migrate` skill step (e) (`ant/skills/migrate/SKILL.md:224-289`) is run
per-group by each behind group's own agent. It sends the release line to EVERY
non-web route of that group (skips only web/slink/wildcard), deduped only per
telegram-JID / slack-team / discord-guild, guarded by a per-group
`~/.announced-version`. Two symptoms:

1. **Per-group dedup, shared destination.** N groups routing into the same
   Discord server / Slack workspace / Telegram chat each announce once → the
   server sees N copies, one per group, each in that group's first-`seq` route.
2. **No opt-in / no main-channel / no mute.** All routes are targets; the
   within-server dedup lands in an arbitrary (`seq`-first) channel, not a
   designated #general/#random. Routes table (`store/migrations/0022`) has no
   announce/mute column.

Desired: announcements are opt-in per channel, default one main channel per
server, explicitly mutable, and on discord/slack land only in the operator's
chosen (#random) channel — each server sees it exactly once.

**Fix direction (record-only, needs sign-off — redesign):** make announce
opt-in. Candidate mechanism = an `#announce` fragment on the route target
(mirrors the existing `#observe` fragment, `store/migrations/0054`); step (e)
sends ONLY to `#announce`-tagged routes, deduped per server; no tag → no
announce (mute = remove the tag). Operator tags exactly one channel per server
→ naturally once-per-server even across groups. Alternative: a small resreg
`announce_targets` resource (heavier; the "every management entity is a resreg
resource" rule favors this if it grows beyond a flag). Related but opposite
symptom to the "broadcast silently lost / no channel for jid" entry below —
both need the announce target-resolution audited together.

- **Severity:** low-medium (noise/duplicate pings, no data loss)
- **Status:** IMPLEMENTED (code+skill, 2026-07-14), pending release plumbing +
  deploy. Mechanism signed off = `#announce` route fragment. `ParseRouteTarget`
  (`core/types.go`) reserves `announce` (inbound-neutral); `router.Describe`
  annotates `announce`; migrate skill step (e) sends only to `#announce`
  routes, deduped per server (web/slink/wildcard skipped). To reach live
  agents: bump `MIGRATION_VERSION` + rebuild ant image + redeploy. Residual:
  cross-group global dedup is still per-group — two groups tagging the same
  server double-post (operator-controlled: tag one channel per server).

## S2 — Missing SECRETS_KEY permits plaintext secret writes despite the required-key contract (2026-07-14, open)

The configuration contract says `SECRETS_KEY` is required, with no plaintext
mode or `AUTH_SECRET` fallback, but routd only warns when the keyring is empty.
`store.Store.storeValue` then returns the plaintext unchanged, so REST/CLI
secret writes can persist readable values while the service remains healthy.
This also makes imports unsafe to proceed before validating the target key.

- **Severity:** high
- **Scope:** secrets encryption precondition / routd startup
- **Affected:** instances started without `SECRETS_KEY`
- **Source:** core/config.go:136-146 (required-key contract); routd/cmd/routd/main.go:61-69 (warn-and-continue); store/secrets.go:72-78 (plaintext fallback)
- **Status:** open
- **Fix:**

## Web docs claim URL↔kind binding on route tokens; shipped code accepts any token at either URL (2026-07-12, fixed, LOW)

**Location**: `template/web/pub/arizuko/reference/tokens.html` §"/chat/ and /hook/ — each URL bound to its JID prefix kind" (+ echoes in `concepts/tokens.html`, `reference/schema.html`, both howto ledes)

The docs say "a hook: token at /chat/… returns 404; a web: token at /hook/…
returns 404 — binding enforced at token lookup". The shipped contract is the
opposite: spec 5/W §"URL routing" locked "both URL prefixes accept ANY valid
route_token; kind is metadata, not a URL access gate", and
`webd/route_token.go` implements exactly that (lookup does not filter on JID
prefix). Also spec 5/W §"Where this runs" still describes the old filtered
lookup — internal spec inconsistency. Fix: align the four web pages + that
spec section to the any-token contract (docs-only).

- **Status:** fixed 2026-07-13 — all four pages + spec 5/W (lede, "Where this
  runs", Tests) state the any-token contract. Same pass trued
  `reference/tokens.html`'s endpoint table to the shipped handlers (no GET
  `/hook`, no hook SSE row; POST `/hook` → 204) and removed its residual
  headers-delivery claim (same drift the fixed howto/webhooks entry removed —
  ingest forwards body only). `legacy/` snapshot pages left as-is.

## System-turn / broadcast delivery targets a folder-jid or prefix-less room → "no channel for jid" → the broadcast is silently lost (2026-07-13, OPEN)

From the "chats stop responding" eval. System/auto turns (the auto-migrate
release broadcast; some proactive turns) call `deliver.Send(jid, …)` with `jid`
set to the **group folder** (`main`, `atlas/support`) or a **prefix-less/stale
room** (`user/137865889`) instead of the group's actual routed **channel** jid.
`chanDeliverer.resolve` (`routd/deliver.go:78`) maps a jid → adapter via
`lookupSource` (the adapter that delivered the jid's newest inbound); a folder or
a prefix-less room has no inbound source → nil channel → `no channel for jid …`
and the message is dropped. Live: marinade `user/137865889` during
`auto-migrate-atlas/martin` (release note never delivered); sloth `jid:main` on
restart. **Impact:** the migration/announcement broadcast silently fails to reach
users on affected groups; error-spam in logs (51 hits/2d). NOT a user chat dying
(distinct from the stale-session fix). **Fix direction (record-only):** system-turn
delivery should resolve the group's bound channel jid (the routes-table channel /
newest routed inbound), not the folder; or skip+debug-log when a folder has no
bound channel instead of ERROR-spamming a doomed `Send`. Needs the broadcast/send
target-resolution audited — design, not a one-liner.

**v0.59 confirmation:** the new opt-in path still has this exact prefix-loss.
Normal CLI-created routes use `room=` plus `core.JidRoom(jid)`, so the migrate
script strips `room=` and passes values such as `T/channel/C` or `group/42` to
`send`. Adapter ownership requires the original `slack:`/`telegram:`/`discord:`
prefix, and routd returns `no channel for jid`. A normal `room=` route tagged
`#announce` therefore cannot announce. Source: `cmd/arizuko/main.go:403-425`,
`ant/skills/migrate/SKILL.md:280-295`, `chanreg/chanreg.go:41-47`,
`routd/deliver.go:112-120`.

- **Severity:** medium (announcements silently lost on some groups; log noise)
- **Status:** OPEN, record-only.

## rate_limit is the top failure signal (195/2d) — verify it's surfaced to the user, not swallowed (2026-07-13, OPEN, needs verification)

`rate_limit` was the single largest signal in the stop-responding eval (195 hits
in 2 days across instances). The stale-session-adjacent BUGS note records the
session/weekly-limit case as *"SDK threw after result (ignored)"* — i.e.
swallowed. IF the model-API rate-limit/throttle error is swallowed rather than
delivered, the user's chat **silently stops** with no explanation during
throttling (unavoidable externally, but the silence is fixable). **To verify:**
trace `ant/src/index.ts` result handling for the rate-limit / "hit your … limit"
subtype — is it delivered as a "rate limited, back shortly" chat message or
dropped? If dropped, surface a terse user-facing notice (one delivery, not a
retry loop). Distinct from the stale-session fix (that class is now closed).

- **Severity:** medium (silent stop during throttling; most-frequent signal)
- **Status:** OPEN — verify surfaced-vs-swallowed, then a small surface-the-notice fix.

## Resource identity (`Name`/`Table`) restated across the two `resreg.Resource` sites — residual drift the 5/16 single-source model retires (2026-07-13, sweep, record-only)

Swept the code for duplications the `5/16` one-owner + single-source model renders
useless. **Mostly already fixed:** Endpoints / RowType / MCP metadata ARE single-
sourced — every mounted handler imports `resreg/resources/<name>.go`'s
`XEndpoints`/`XMCPArgs`/`XMCPDoc` (audited all 11: **0 inline re-declarations**,
comments say "single source"). So the older `openapi_two_declarations` two-full-
declarations drift is largely closed.

**Residual duplication (retire under 5/16):** each cold-tier resource is
instantiated as `resreg.Resource{}` **twice** — the registry entry
(`resreg/resources/<name>.go` `resreg.Register(...)`, for OpenAPI + the standalone
`arizuko apply`/`export` CLI, no handler) and the mounted handler
(`<owner>/<name>_resource.go`, which adds Store/Handler/Gate). Both **restate
`Name`** (the wire identity, per CLAUDE.md "Name IS wire identity") and some
`Table`. Two sources for one identity string = a drift vector — exactly the class
that let proxyd's live route resource drift to `Name:"routes"` while its catalog
said `proxyd_routes` (fixed 2026-07-01). ~11 resources, Name-restated at:
- routd: `acl_resource.go:54`, `groups_resource.go:58`, `network_rules_resource.go:67`,
  `routes_resource.go:100`, `route_tokens_resource.go:78`, `scheduled_tasks_resource.go:89`,
  `secrets_resource.go:56`, `web_routes_resource.go:50`
- onbod: `gates_resource.go:49` (onboarding_gates), `invites_resource.go:40`
- proxyd: `resource.go` (proxyd_routes)
  (each with a registry twin in `resreg/resources/*.go`)

**Fix (per 5/16 "one owner, single-source declaration"):** the mounted handler
derives `Name`/`Table` from the single registry declaration (import the identity),
never restates it — one source of `{Name, Table, PKFields, RowType, Endpoints}`,
handler adds only `{Store, Handler, Gate/containFn}`. Cheap interim guard: a test
asserting `mounted.Name == registry.Name` per resource catches drift until then.

**Related duplications of the same concept, already logged** (distinct sites, retire
together with the 5/8+5/16 finalization): duplicate `cost_log` tables (routd vs
store — the no-duplication-rule violation, entry below); dashd direct-DB reads that
bypass the owner (`dashd reads SQLite directly`); `arizuko apply` opening the frozen
`messages.db` instead of owner DBs (5/8). All three "bypass or duplicate the
one-owner model."

- **Severity:** low (residual drift vector; the heavy duplications are already fixed/logged)
- **Status:** OPEN, record-only — retire under 5/16 one-owner + single-source.

## Cost attribution is folder-only — per-chat lost, per-user auth-only, plus a duplicate cost_log (2026-07-13, OPEN)

Question raised: is LLM cost attributed completely to individual chats + users? **No.**
- **Cost → folder: yes.** `recordTurnResult` writes `cost_log(folder, turn_id, model, …)` once per model (`routd/turns.go:684-689`; schema `routd/migrations/0001` + `0011-cost-log-user-sub.sql`).
- **Cost → chat: NO (derivable, unwired).** `cost_log` has no `chat_jid`. The turn's `chat_jid` is on `turn_context` (same `turn_id` PK) but nothing joins it — not `CostByTurn` (`routd/db.go:1104`), `/v1/cost` (`routd/reads_http.go:299`), or dashd. A folder with many chats collapses to one folder total. **Min fix:** add `chat_jid` to `cost_log`, set from `tc.ChatJID` at the one write-site (`turns.go:685-689`).
- **Cost → user: PARTIAL.** `userSub := callerSubOfMsg(tc.Trigger)` resolves only `google:`/`github:`/`local:` senders (`routd/budget.go:9-23`); native adapter senders (`telegram:user/…`, WhatsApp, Slack) → `""` → folder-only. Most chat traffic has no user dimension. **Decision needed:** widen `callerSubOfMsg` to key on the raw adapter sender, OR document per-user cost as auth-only by design (currently reads as a bug, not a decision).
- **Duplicate cost_log (no-duplication violation).** A second, structurally different `cost_log` exists in `store/` (`store/cost_log.go` + `store/migrations/0049-cost-log.sql`, cols `ts/cache_read/cache_write`, no `turn_id`) with its own `LogCost`/`GroupUsageBulk` (dashd usage reads this one). Two cost-recording paths that will drift — consolidate to one owner.
- **Reporting:** dashd usage + `GroupUsageBulk` slice by folder only (`dashd/usage_page.go`, `store/cost_log.go:119`); `/v1/cost` is per-turn only. No per-chat or per-user rollup surface exists.

- **Severity:** medium (billing/showback can't split by chat or by unauthenticated user)
- **Status:** OPEN — chat gap is a 1-column + 1-write-site fix; user gap needs a design call; the duplicate cost_log needs sign-off (schema consolidation).

## Codex mount assumes creds readable by the container uid — 0600 operator creds break codex-in-container (2026-07-12, unblocked live, DURABLE FIX OPEN)

`container/runner.go:582-590` RO-mounts `HOST_CODEX_DIR/{auth.json,config.toml}`
into the agent container, which runs as uid 1000 (`mivu`). But those files are
the operator's personal codex creds under `/home/onvos/.codex`, owned
`onvos:1001` mode `0600` → uid 1000 gets `Permission denied` on `config.toml`,
so the in-container `/oracle`/codex path dies (looks like "not logged in", is
actually a uid mismatch). Live on krons 2026-07-12.

- **Unblocked live (fragile):** `chmod o+r /home/onvos/.codex/{auth.json,config.toml}`
  (setfacl not installed → couldn't scope to just uid 1000). uid 1000 now reads
  them; next fresh container's codex works, no restart. **Two caveats:** (1)
  world-readable now exposes the operator's ChatGPT OAuth token to any local
  user — acceptable on a single-operator box, tighten with an ACL (`apt install
acl`; `setfacl -m u:1000:r …`) if the host gains other users; (2) if onvos's
  host codex REWRITES these files (refresh/config change via atomic rename), the
  `o+r` is lost and it breaks again — re-apply, or ship the durable fix.
- **Durable fix (code, needs sign-off):** don't RO-mount the 0600 originals;
  at spawn, COPY `auth.json`/`config.toml` into the per-group writable `.codex`
  dir (`runner.go:583`, already chowned to the run uid) so the container reads
  its own uid-1000 copy. Keeps operator creds untouched, survives host-side
  rewrites, no world-read. Redesign of the codex mount → record + sign-off.

## Onboarding recurs as a manual operator chore — new chat → route-miss → silence → hand-run `arizuko group add` (2026-07-12, OPEN, HIGH)

This keeps popping up. Live datapoint: Nikol (`telegram:user/8177590051`) sent
`/start` + a question at 08:01 2026-07-12 on krons, hit a route-miss, got NO
greeting and NO admission-queue row; the operator had to discover the JID out of
band (sqlite) and hand-run `arizuko group krons add … rhias/niki`. Same story as
adshaus and the earlier telegram groups. Two distinct defects compound it:

1. **`InsertOnboarding` never fires on a genuine route-miss** — **fixed
   2026-07-12**: root cause pinned — TWO drifted route-miss handlers; the
   queue-worker copy that ingest actually drives (`processGroupMessages`)
   had no onboarding and advanced the cursor first, so `pollOnce`'s
   onboarding branch was dead. Full walkthrough in the "Chat-initiated
   onboarding still dead" entry below. One shared `routeMiss` handler now
   serves both paths; an `InsertOnboarding` failure is `slog.Error` + a
   notice delivered to the chat.
2. **No self-serve JID discovery — `/chatid` dead exactly where it's needed** —
   **fixed 2026-07-12.** Correction to the original claim: `/chatid` DOES
   exist (`routd/steer.go handleCommand`, tested by `TestCmdChatID`) — but the
   steer layer only runs on chats that RESOLVE, so on an unrouted chat (the
   one audience that needs it) the command fell into the route-miss drop.
   Live: 3 dead `/chatid` attempts on unrouted `telegram:group/5567410596`,
   2026-07-09. Fix: `routeMiss` intercepts `/chatid` (same `lookupCommand`
   parser, same `ack` path — not a second dispatcher) and replies with the
   chat JID; the miss still queues onboarding. Test: `TestRouteMissChatID`.

**Test gap (operator ask 2026-07-12):** **closed 2026-07-12** —
`tests/onboarding_e2e_test.go TestOnboardingEndToEnd` drives the WHOLE path on
the bootFederation harness (real authd tokens, real ingest→queue→dispatch,
real runed over HTTP): unrouted inbound → route miss → onboarding row
`awaiting_message` in the onbod-owned store via routd's production OnbodClient
→ operator admits (approve + the `arizuko group add` group/route shape) → same
chat's next message routes → agent reply row lands. Verified failing on the
pre-fix queue path (WaitForRow timeout — no onboarding row), green after.

- **Severity:** high (every new user needs manual operator intervention today)
- **Scope:** routd/loop.go onboarding branch; gateway `/chatid`; e2e/anteval
- **Status:** fixed 9fcb0bce (onboarding) + 5f102e0d (/chatid) + 03b6949c
  (e2e) (2026-07-12). NOT yet deployed — ships with the next krons image
  build + restart.

## Steer layer (slash commands / sticky nav / @child) races the queue path — routed-chat slash commands dispatch agent turns (2026-07-12, OPEN, HIGH)

Found while root-causing the onboarding miss (same structural drift, distinct
concern — recorded, NOT fixed). The steer layer (`routd/steer.go` — `/ping`,
`/new`, `/stop`, `/root`, `/invite`, `/gate`, sticky `@group`/`#topic`, @child
delegation) runs ONLY in `pollOnce` (`loop.go l.steer`), but ingest enqueues
straight into the queue worker `processGroupMessages`, which dispatches a turn
WITHOUT consulting steer. So for a routed chat both race: the queue path
usually starts an agent turn on the raw command message within ms, and the
2s-tick `pollOnce` may ALSO steer it while that run is in flight (cursor not
yet advanced) → double-processing (steer ack + an agent reply to the command
text).

**Live evidence (krons):** `/root check rhias direct groups…`
(`telegram:user/1112184352/5771`, 2026-07-12 08:08:31) has a `turn_context`
row on the RAW message (state=done) and NO `root-…` reinjection row — i.e. the
queue path ran it as a plain non-elevated agent turn and the agent replied
ABOUT `/root` instead of `cmdRoot` elevating it. Operator `/root` (and every
routd-serviceable command) is therefore unreliable on the live split: whether
the fixed command response, an agent turn, or both happen is a race.

**Suggested direction (needs design sign-off — control-flow change):** the
same consolidation shape as `routeMiss`: steer must run on the path that
processes the batch (`processGroupMessages`, before `runTurn`), not in a
racing backstop. Care: steer consumes only the LATEST message (`last`) while
the queue path batches; and pollOnce's steer call must not remain as a second
racing site.

- **Severity:** high (operator /root silently not elevating; command double-
  processing burns turns)
- **Scope:** routd/loop.go pollOnce steer site; routd/dispatch.go
  processGroupMessages; routd/steer.go
- **Status:** OPEN — recorded per triage protocol; fix is a dispatch-ordering
  redesign, awaits sign-off.

## Admin cannot log in to the krons dashboard (2026-07-12, OPEN, HIGH — needs root-cause)

Reported: an admin cannot get into krons (`/dash`/`/auth`). Facts gathered:
`auth_users` in krons `auth.db` has **0 rows**; krons `.env` and
`docker-compose.yml` carry **no OAuth provider config** (no `GOOGLE_CLIENT_ID`/
`_SECRET`, no `*_ALLOWED_EMAILS`) — only `AUTH_SECRET` + `AUTH_BASE_URL`. So
either the login flow has no identity provider to authenticate against, or a
successful login isn't being recognized as operator (operator status is a grant
check — `proxyd/main.go:678-681`, and durable once the acl table has a row per
`main.go:236`). No `/auth` deny lines in journalctl (the 401 spam is the known
whapd re-pair loop, unrelated). **Suggested fix (root-cause first, do NOT hack):**
trace the `/auth/login` → callback → `requireAuth` path on krons: (a) is a
provider configured at all — if not, that's an operator config gap (document the
exact `.env` keys), not a code change; (b) if login succeeds but `/dash` 403s,
the first operator needs a bootstrap grant (`**` acl row) — confirm how the
first operator is meant to be seeded and whether that path is broken. Fail loud
on the real cause; if it's config, surface the missing keys rather than
inventing a fallback.

- **Severity:** high (operator locked out of their own instance)
- **Scope:** proxyd/main.go requireAuth + /auth flow; authd; krons `.env`
- **Status:** config-not-code (root-caused 2026-07-12). **Every server-side hop
  verified working on live krons; no code change.** Corrections to the facts
  above: krons `.env` DOES carry an OAuth provider — `GITHUB_CLIENT_ID` +
  `GITHUB_CLIENT_SECRET` (the original check greped only `GOOGLE_*`), and
  compose delivers them (`env/authd.env`). Verified live: `/auth/login` 200
  renders the GitHub button; `/auth/github` 307s to GitHub authorize with a
  valid client_id + PKCE state; GitHub shows the normal sign-in (no
  redirect_uri-mismatch banner → the app's callback URL is registered
  correctly); `/auth/github/callback` is mounted (403 `invalid state` on a
  bogus probe); proxyd has `AUTHD_URL` so the cookie→`/v1/refresh`→JWKS chain
  (`tryRefreshViaAuthd`) is wired; and the first-operator grant is ALREADY
  seeded: routd.db `acl_membership: github:kronael → role:operator` +
  `acl: role:operator | * | ** | allow` (migration-0053). What's actually true:
  **no login has EVER completed** — `auth_users` = 0 rows, `refresh_tokens` =
  0 rows, zero `/auth/github/callback` hits in 11 days of journal. The flow
  dies at the human step, not in the platform. **Exact operator actions:**
  (1) open `https://krons.fiu.wtf/dash/` → redirected to `/auth/login` →
  "Continue with GitHub" → sign in to GitHub as **kronael** (the seeded
  operator sub) → operator scope resolves and `/dash` opens. (2) If the
  admin's GitHub account is NOT `kronael`: log in once (creates the
  `auth_users` row), then seed the grant:
  `arizuko grant krons github:<login> '**'` (→ `role:operator` membership,
  cmd/arizuko/main.go:532). (3) If Google login is wanted (as on marinade):
  add `GOOGLE_CLIENT_ID` + `GOOGLE_CLIENT_SECRET` (and optionally
  `GOOGLE_ALLOWED_EMAILS`) to `/srv/data/arizuko_krons/.env`, then
  `sudo systemctl restart arizuko_krons`.

## GB1 — Redesign: eliminate the "green but broken" class at the cause (2026-07-11, mostly ✅ SHIPPED)

Cause fix for the silent-failure class. fable-reviewed + code-verified; signed
off and shipped L3→L4→L2→L1, each its own commit + test (all `go test ./routd/`
green). NOT deployed yet (ships next image build).

- **Severity:** medium (rare triggers, silent + user-facing when they fire)
- **Scope:** routd dispatch + delivery, container spawn, route write-path, lint
- **Status:** shipped (code); remaining follow-ups below

**Shipped:** L3 silent-turn detect+notice+metric (`d65e73c2`); L4a ghost-group
dispatch guard (`c9979e78`); L4b DeleteGroup route/engagement cascade (`836d3f88`
+ fix `a4c4a68d`); L2 send_file→422+no-row (`d347a0dc`); L1 golangci errcheck+nilerr
config + `lint-strict` (`3aa59fd3`).

**Remaining (open):**
1. **L4b route-write validation** — `routesHandler` add/set + CLI `route add` should
   `GroupExists(target)` before persist. Deferred: needs a shared target parser
   (observe/web/hook suffixes) + confirm no bootstrap adds route-before-group.
   L4a backstops it at dispatch meanwhile.
2. **L1 gate-flip** — wire `lint-strict` into `make lint`/CI once golangci-lint
   builds against the toolchain (x/tools mismatch here), then triage findings.
3. **ant `deliverTurn`** — submit_turn RPC failure should retry once then exit
   non-zero (`ant/src/index.ts:105`) → `OutcomeError` → existing notice path.
4. **Finish `01a61a07`** — home-not-writable should return an error from the runner
   (→ `OutcomeError` → user notice), not log-and-spawn-config-less.

Original plan detail (for the record):

Ship order **L3 → L4 → L2 → L1** (biggest cause-net first). Each = own commit + test.

**L3 — silent-turn reconciliation (KEEP, ~8 lines, highest value).** The clean
epilogue `dispatch.go:314-321` ALREADY computes `TurnResultRecorded(folder,turnID)`
(line 316) and runs synchronously at container exit (no async observer/ledger
needed). Add: `Outcome==OK && !TurnResultRecorded && !TurnHasBotReply` → `slog.Error`
+ deliver a chat notice via the existing `l.deliver.Send` (as `dispatch.go:297-303`)
+ a `silent_turns_total` counter. NO retry (a 0-result run isn't transient). Verified
no false positives: deliberate silence records turn_results (`ant/src/index.ts:328`
calls deliverTurn on every result event incl. think-only); steered turns exit before
the epilogue. Cause-agnostic — catches the three residues L2/L4 miss: 0-result query
(`index.ts:459`), swallowed submit_turn RPC (`index.ts:105`), and the config-less
home fix that still spawns (`01a61a07`).

**L4 — ghost-group guard, TWO commits.** (a) Runtime backstop: guard the HEAD of
`runTurn` (`dispatch.go:102`), mirroring `budgetGate` (`:132`) — `!GroupExists(folder)`
→ ERROR + `deliver.Send` notice + return `true` (consume batch, cursor advances, no
poison replay). One choke covers route/engagement/sticky ghosts. **NOT** in `resolve()`
— there a failed guard becomes a route-*miss* → silently drops (`loop.go:540`) AND fires
spurious `InsertOnboarding` (`:525`). (b) Write-time integrity (the real cause):
`routesHandler` add/set (`routes_resource.go` ~149/171) + CLI `route add`
(`cmd/arizuko/route.go:33`) must `GroupExists(target)` before persist; add a route/
engagement cascade to `DeleteGroup` (`db.go:254`, currently a bare DELETE). Legit flows
traced clean (@child, sticky, web:/hook:, observe, onboarding all pre-register or
pre-guard).

**L2 — `handleDocument` only (~5 lines), NOT a funnel.** The agent surface is already
loud: MCP `send_file`→`mcpAppendDoc`→`toolErr` (`mcp.go:265`,`ipc/ipc.go:1112`), social
REST returns 422 via `relay()` (`turns.go:480`). Only `handleDocument` (`turns.go:336`)
drifted: swallow→HTTP 200 + `pending` row. Fix: return 422 on `Document()` error AND
don't persist the pending row — else the text-only `maybeRetryOutbound` sweep
(`loop.go:373`, sends `m.Content` only) re-sends the caption WITHOUT the file (latent
bug). DROP the `recordDelivery` abstraction and DROP making `appendAndDeliver` non-2xx
(fights `maybeRetryOutbound`, causes double-sends).

**L1 — golangci-lint `errcheck` + `nilerr` only.** Into `make lint` + pre-commit (lint
is bare `go vet`, `Makefile:35`). DROP `errorlint` (unrelated to this class) and DROP
the ast-grep `if err==nil{}` rule (false-positive generator — the with-else form is
legit and used correctly, `turns.go:297`, `dispatch.go:278`). Triage ~15 sweep sites;
benign discards → `//nolint … // benign:`.

**Plus (fable-added cause fixes, cheap):** ant `deliverTurn` submit_turn failure →
retry once then exit non-zero → `outcomeFor`→OutcomeError → existing retry+notice path
(`index.ts:105`,`docker.go:276`). And finish `01a61a07`: home-not-writable → runner
returns error → OutcomeError → existing user notice, not log-and-spawn-config-less.

## Error-hygiene sweep: swallowed / mis-levelled errors in the hot path (2026-07-11, 2 FIXED, rest OPEN)

Sweep for the same class as the no-reply bug below (an error swallowed as
Warn/`_ =` while execution continues → silent degradation). Two shapes recur:
`if err == nil { use }` with the else branch missing, and `_ = err` on
bookkeeping writes that matter next turn.

**FIXED this pass** (both were missing error logs, no control-flow change):
- `routd/loop.go` `resolve()` — a transient `l.db.Routes()` read failure was
  indistinguishable from a clean route-miss, so the poll caller advanced the
  cursor and **dropped the inbound message** with zero log. Kept the
  fail-forward (retry risks a poison-message loop) but made the drop a loud
  `slog.Error`.
- `routd/turns.go:337` `handleDocument` — `s.deliver.Document` error had no
  `else`, so `send_file` failures returned HTTP 200 (row left pending) and were
  never logged, unlike the sibling `handleSend` (`turns.go:308` logs
  "deliver send failed"). Added the matching `slog.Error`.

**VERIFIED, OPEN** (real, left for prioritisation):
- `authd/main.go:108-112` — `core.LoadConfig()` failure → OAuth `/auth/*` not
  mounted, logged only `Warn`, server starts green (JWKS/health OK). Human login
  404s while everything reports healthy. **FIXED 2026-07-11** — elevated to
  `slog.Error` with the "login 404s while health green" symptom in the message.

**NOT A BUG** (verified benign, recorded so it isn't re-flagged):
- `container/runner.go:173` `ipcDir, _ := folders.IpcPath(in.Folder)` — the same
  folder is already resolved+validated via `buildMounts` (`runner.go:170`, which
  handles the error at `:560`) before line 173; `IpcPath` won't diverge for a
  folder that reached dispatch. Discard is a cosmetic asymmetry, not a live gap.
- `routd/prompt.go:29-39` (scout #2) — the store reads there don't surface an
  error at the call site (methods return zero-value/empty context); the one
  explicit `_ = UpdateObservedCursor` is **documented-benign** (comment L24-27:
  re-seeing observed messages is harmless, at-least-once by design).

**REPORTED by sweep, NOT yet independently verified** (read before fixing):
- `ipc/ipc.go:378-386` `writeJSON` — swallows `json.Marshal` error with bare
  `return`, no error frame → tool result vanishes, caller times out. (HIGH if
  confirmed.) **Verified + partially fixed 2026-07-13** — marshal failure now
  `slog.Error` (same log-add shape as the two FIXED items above; no
  control-flow change). Error-frame delivery stays open: writeJSON takes an
  opaque `v`, so a proper JSON-RPC error frame needs the request id threaded
  in — design, not a log-add.
- `ipc/ipc.go:348-361` — peer-cred/UID mismatch on MCP accept only `Warn`; gates
  every tool call for the group. (MED)
- `crackbox/pkg/admin/registry.go:84-107` `flushLocked` — `Warn`+return on
  disk-persist failure inside a void fn; handler still ACKs 200 → stale egress
  allowlist can reappear after restart. (MED)
- `routd/turns.go:670-676` `recordTurnResult` — discards `PutMessage`/`PutSession`
  errors (this path historically broke all replies post-split — verify first). (MED)
- `runed/manager.go:212-214`, `runed/docker.go:66` (steer silently no-ops),
  `ipc/ipc.go:340-345` (conn-limit reject, no error frame),
  `routd/dispatch.go:263,305,320` (terminal SetTurnState unlogged),
  `crackbox/pkg/host/host.go:130-132`, `routd/budget.go:32-43` (spend caps fail
  open on a DB hiccup). (MED/LOW)

Adapters (teled/discd/slakd/mastd/bskyd/reditd/emaid/chanlib) swept clean for
this class.

## Root-owned group home → agent runs config-less, silently never replies ("typing but no reply") (2026-07-11, ROOT CAUSE FIXED on krons, HARDENING OPEN)

Symptom (user-reported): the bot shows a typing indicator then never replies,
intermittently. Reproduced on krons folder `krons/content`: 4 turns (5729–5732)
dispatched + spawned, each ran ~33s, logged `[ant] Query done. Messages: 0,
results: 0` then `[ant] Input empty, exiting`, `code:0` — **no `turn_results`
row, no bot message**. rhias/nemo (healthy) replies normally.

**Root cause.** `groups/krons/content` was hand-created as **root:root** (Jul 7,
manual `mkdir`/host-CLI — the "never mkdir groups manually, use onbod/SetupGroup"
rule, `group_creation` memory). The container runs `user: 1000:1000`
(`compose/compose.go:694`); it can't `mkdir .claude` under the root-owned home, so
`cpDirImpl` (`container/runner.go:1146`) and `writeSettings`
(`container/runner.go:865`) both fail — **settings.json (which carries the
`mcpServers.arizuko` socket + outputStyle) is never written** → the SDK query
runs with no MCP tools and returns 0 messages → no reply. Note `krons/content` is
also a **ghost**: routed-to but NOT in `groups` (registered krons groups:
adshaus, happy, krons, krons/public/marble, mayai, rhias, rhias/content,
rhias/nemo). Fixed live: `chown -R 1000:1000 groups/krons/content`; uid-1000 can
now write `.claude`. Pending end-to-end confirm on next inbound.

**Hardening (OPEN).** Both write-failure sites swallow the error as `WARN` and
continue, producing a silently dead agent — violates CLAUDE.md "strict, not
magical / no silent fallbacks." Fix: when the group home is not writable (mkdir
`.claude` = permission denied, or settings.json write fails), fail LOUD — log
`ERROR` with the folder AND abort the spawn / deliver an error turn, rather than
running a config-less container. Also consider refusing to dispatch a folder with
no `groups` row (ghost-group guard) or auto-`chown` a fresh home to the run uid.

## Services hub: onbod + timed tiles are `Built=true` but their `/dash/` routes 404 (2026-07-10, OPEN)

`dashd/services.go:35-36` marks `onbod` and `timed` `Built=true` (Dash=
`/dash/onbod/`, `/dash/timed/`), so `handleServices` renders them as drill-in
links once the daemon's `/health` is reachable (`services.go:143`). But
`dashd/main.go` only mounts `GET /dash/routd/` and `GET /dash/runed/` — there is
**no `/dash/onbod/` or `/dash/timed/` handler**. Result: on a live instance the
onbod and timed service tiles link to a 404. The slice comment ("Set Built=true
once the per-daemon /dash/ route is shipped") documents the invariant that was
violated. Fix: either ship the two control-plane handlers, or set
`Built=false` on onbod+timed until they exist. Surfaced by the dashd-playwright
audit (`services.spec.ts:32`), 2026-07-10.

- **Status:** fixed 2026-07-11 — set `Built=false` on onbod+timed (restores the
  slice invariant: tiles show as probe-only status, no dead drill-in link).
  Shipping the two `/dash/` control-plane handlers stays a follow-up (flip back
  to `true` then).

## dashd-playwright: 4 drifted test assertions (harness maintenance, not dashd bugs) (2026-07-10, RESOLVED 2026-07-11 — d068eeaa)

The offline dashd playtest (`make play`) has 4 stale assertions that no longer
match verified-correct dashd behavior. Dashboard is sound; fix the tests:
**All 4 fixed in d068eeaa "[tests] dashd-playwright: fix 4 drifted assertions to
match current dashd" — verified this pass.**
- `davd.spec.ts:21` — asserts per-file `/dav/inbox/{MEMORY,CLAUDE}.md` links on
  `/dash/memory/`; they actually render on the group **settings** page
  (`groups_admin.go:306-308`). Point the test there.
- `groups.spec.ts:71` — clicks "delete group" without expanding the collapsed
  `<details>Danger zone` wrapper (`groups_admin.go:330`, added 2026-06-18) →
  button hidden → 30s timeout. Expand the `<summary>` first.
- `invites.spec.ts:44` — POSTs the raw token to `/revoke`; handler keys revoke on
  the short **ref** (`invites_test.go:175,182`) → 404. Revoke by ref.
- `read-only.spec.ts:34` — asserts "Active sessions" on `/dash/status/`; that row
  lives on the chat portal (`main.go:756`). Assert the group-count row instead.

## Stale chat-bound session → `error_during_execution` + silent context loss (2026-07-10, OPEN)

Symptom (reported as "agents dying after some minutes"): a long-lived group's
agent briefly errors mid-turn and appears to "forget" the thread. **Not a
crash** — 24/24 container exits over 12h were `code:0, timedOut:false`; no OOM
(containers have no mem limit, ~218MB usage), no query timeout.

**Root cause.** routd persists one session ID per chat (`sessions` table) and
resumes it next turn via `claude --resume <id>`. The container's SDK transcript
store (`groups/<folder>/.claude/projects/-home-node/*.jsonl`, bind-mounted) is
pruned by Claude Code's **native retention cleanup** (`.last-cleanup`, ~30-day).
When routd resumes a session whose transcript was pruned or never fully
persisted, the SDK returns *"No conversation found with session ID"* → the turn
result is `subtype=error_during_execution`.

Evidence (marinade `atlas`): `.last-cleanup` = `2026-07-09T19:31:48Z` matches an
`error_during_execution` at 19:31:41 to the minute; another at 2026-07-10
07:13:26 resumed `b6c2b287-…` which is absent from disk. On-disk transcripts
span 06-10 → 07-10 (retention window).

**Self-heals but lossy.** `ant/src/index.ts:335,440` catches the session error
and retries with a **fresh** session — the 07:13 case delivered `subtype=success`
~3 min later. Costs: (1) the failed attempt's `error_during_execution` is logged
as `Result #1` and may surface to the user before the retry; (2) the resumed
conversation context is discarded (agent "forgets" the thread); (3) wasted
latency + tokens.

**Candidate fixes (do NOT apply until asked):** (a) before `--resume`, stat the
transcript file for the stored session ID; if missing, start fresh silently
(never surface `error_during_execution` as a delivered result); (b) routd expires
/ nulls a session ID older than the CLI retention window; (c) raise or disable
Claude Code `cleanupPeriodDays` in the container settings so referenced
transcripts aren't pruned while `sessions` still points at them. (a)+(c) together
close both the error-flash and the context loss.

## Chat-initiated onboarding still dead after the compose fix — InsertOnboarding never fires (2026-07-09, OPEN)

A new group posts, hits a route miss, and **nothing happens** — no greeting, no
admission-queue row. The operator must discover the JID out-of-band (grep
routd.db `messages.chat_jid` / journalctl) and hand-run `arizuko group <inst> add
<jid> <folder>`. Witnessed on krons: `telegram:group/5567410596` posted repeatedly
05:34–06:11 2026-07-09, all stored, zero onboarding row created.

**Fix #1 (shipped, `4b4d868c`, deployed to krons):** the route-miss onboarding
insert lives in **routd** (`routd/loop.go:520`, gated by `l.onboardingEnabled` ←
`routd/cmd/routd/main.go:184` `envOr("ONBOARDING_ENABLED","false")`), but
`compose/compose.go` passed `ONBOARDING_ENABLED` only to **onbod**. Added it (+
`ONBOARDING_PLATFORMS`) to routd's env passthrough; rebuilt `arizuko:latest`
(the systemd `ExecStartPre` regenerates env from the image, so a host-side
regen alone reverts on restart — the image MUST be rebuilt); verified
`printenv ONBOARDING_ENABLED=true` inside the routd container.

**Still broken (residual bug).** With the flag confirmed live, a genuine
telegram-group route miss at 06:11:42 was processed (chat `agent_cursor`
advanced to 06:11:42) but produced NO onboarding row and NO error. All
preconditions are met and ruled out:
- `l.onboardingEnabled=true` (in-container printenv),
- `l.onbod != nil` (routd logged "service-token bootstrap via authd" at 06:09:11),
- `ONBOARDING_PLATFORMS` unset → `onboardingAllowed` true,
- no matching/catch-all route, `sticky_group`/`sticky_topic` empty (no engagement),
- `httpOnbod.do` returns an error on any non-2xx and `loop.go:531` logs
  `slog.Warn("insert onboarding", …)` — **no such warn exists**, so a swallowed
  403 is excluded. `InsertOnboarding` is simply never invoked for the miss.

So the `else if l.onboardingEnabled && l.onbod != nil` branch at `loop.go:520` is
not being entered even though both are true. Next step: add a trace log at the
top of the route-miss block (is `!r.ok` even reached, or does `resolve()` return
`ok=true`/`Observe!=""` for an unrouted telegram group?) and confirm the poll
actually walks `pollOnce` for this chat rather than the cursor being advanced at
ingest. Candidate causes: `resolve()` returns a non-miss for a bare group JID; or
`agent_cursor` is advanced before the onboarding branch runs (batching /
`last.Timestamp <= GetAgentCursor` skip at `loop.go:500`).

Note: `/chatid` is not an arizuko command (nothing echoes it) — JID discovery is
meant to BE the onboarding greeting + dashboard queue, exactly what this disables.

See also the onboarding redesign the operator wants (telegram groups = Slack
channels: new group → default staging folder, private-group interaction gated to
some users). That vision is being specced into `specs/5/` — the fix here should
land compatibly with it.

- **Status:** fixed 9fcb0bce (2026-07-12) — **root cause: two route-miss
  handlers, and the one that runs had no onboarding.** Ingest (`routd/server.go`
  `handleMessages`) calls `loop.Enqueue` directly, so the queue worker
  `processGroupMessages` (dispatch.go) consumes the miss within milliseconds —
  its miss branch was a drifted copy (observe + advance only, NO onboarding).
  By the next `pollOnce` tick (2s) the cursor was already current, so the
  `last.Timestamp <= GetAgentCursor` skip suppressed the ONLY onboarding site.
  Matches the live evidence exactly (cursor advanced, no row, no warn, no
  turn_context). Fix: one shared `Loop.routeMiss` (observe ingest → onboarding
  → advance) called from BOTH `pollOnce` and `processGroupMessages`;
  `InsertOnboarding` failure is now `slog.Error` + `onboardingFailedNotice`
  delivered to the chat (fail loud, fail-forward — cursor still advances, no
  poison replay). Tests: `TestQueuePathRouteMissInsertsOnboarding` (failed
  pre-fix), `TestRouteMissOnboardingFailureSurfaces`.

## 5/16 invites fold — agent-forwarder half deferred (2026-07-07, by design)

onbod's `/v1/invites` REST face is folded onto resreg (`154cd17f`, mirrors the
gates fold): create/list/revoke ride the shared handler + one tx + audit, and
invites is on onbod's `/openapi.json`. The AGENT invite tools (`invite_create`/
`invite_list`/`invite_revoke`, ipc/ipc.go registerRaw → GatedFns → routd/mcp.go
→ httpOnbod federation) are deliberately NOT folded yet — same precedent as the
route_tokens operator twin left hand-rolled where the shape doesn't fit resreg.

Blocker for a mechanical forwarder fold (Store nil → forward to onbod): the agent
face carries three things a single HTTP forward can't express —
- **caller-bound issuer** (`issued_by="agent:"+folder`, never a client arg),
- **accept_url synthesis** (routd computes `AcceptURLBase + /invite/<token>`),
- **revoke ownership as list-then-delete** (routd/mcp.go:141 lists THIS folder's
  invites and confirms the token before calling onbod's token-only DELETE — the
  comment: "an agent must not revoke another folder's invite even though the wire
  call would"). A naive forwarded DELETE `/v1/invites/{token}` REGRESSES this into
  a cross-folder revoke.

Safe fold needs onbod's DELETE to gain an `issued_by` scope (`DELETE ... WHERE
token=? AND issued_by_sub=?`) so the forwarder passes the caller-bound issuer and
onbod enforces ownership in one call; create needs the forwarder to inject issuer
+ synthesize accept_url. That's the "one-owner + federation" design step, not a
mechanical fold — left for a focused change on the live agent surface.

## 5/16 two-faces rollout — status (updated 2026-07-06)

All 5 cold-tier agent-MCP faces ride one resreg.Resource via the injected Gate
seam. REST faces folded onto their shared handlers: web_routes (27537500 +
9d649e59 list-all leak fix), acl (44c53cef, closed a cross-folder scope hole),
routes (3195f867), tasks (0b6ca53e). Open:

1. **routes/tasks REST bake the agent tier model in the handler — two failure modes
   (being fixed by the containment decouple).** These handlers run
   `auth.AuthorizeStructural(auth.Resolve(Caller.Folder), …)` — a predicate written for
   the agent tier identity — against a REST operator whose real authority is `ownsFolder`
   (own-or-descendant, tier-independent). The two diverge in BOTH directions:
   - **routes — over-restrictive (no leak).** A tier-1+ REST token managing its OWN
     folder gets 403 (tier-1 needs a STRICT descendant); a tier-2 REST token can't manage
     routes at all. Latent (existing tests use tier-0 folders where the tier cap is a no-op).
   - **tasks — LIVE CROSS-TENANT LEAK (security).** `tasksRESTGate` keys `ownsFolder(jwt,
     Caller.Folder)` where `Caller.Folder==jwt` for per-task ops → a no-op; the only per-task
     check is the handler's tier cap, which is LOOSER than ownsFolder: a **tier-0 REST
     operator can `DELETE /v1/tasks/{anyId}` and cancel ANOTHER tenant's task**; a tier-1
     operator can cancel/patch any same-world sibling's task (`isInWorld`). Uncovered
     (`TestRESTTaskScopedSelfService` uses tier-2, where exact-own ≈ ownsFolder). Present on
     main since the tasks fold (0b6ca53e).
   **RESOLVED 2026-07-06 (0d25b687).** Decoupled containment into a routd-internal
   per-face `containFn(caller,action,target)` — agent→tier `AuthorizeStructural`,
   REST→`ownsFolder` — dropping the baked tier check from the handler (resreg untouched;
   set_routes keeps its face-agnostic `routeTargetWithin` loop). TDD'd: the two tasks
   guards returned 200+deleted pre-fix (leak confirmed), 403 post-fix. Guards:
   `TestRESTTask{Tier0NoCrossTenant,Tier1NoWorldLeak,Tier2Descendant}`,
   `TestRESTRoute{Tier1OwnSubtree,Tier2OperatorOwnSubtree}`. Agent tier semantics
   (`TestRoutesMCP_*`/`TestScheduledTasksMCP_*`) unchanged.
1b. **scheduled_tasks REST (`/v1/tasks`) not OpenAPI-discoverable (minor, follow-on).**
   OpenAPI truthfulness shipped (7c14efd6 — docs emit real Endpoints, no phantom paths).
   But `scheduled_tasks`'s REST face is mounted at `/v1/tasks/{taskId}` (`mountTasks`
   override) with a DIFFERENT action set (list/get/patch/cancel) than its agent tools
   (schedule/pause/resume/cancel/list), so it can't share the resource's registered
   `ScheduledTasksEndpoints` (`/v1/scheduled_tasks`, agent-derivation) — and advertising
   the resource would emit the wrong paths. So it's correctly LEFT OUT of routd's
   `/openapi.json` list (`[routes, web_routes, acl]`). Truthfully advertising `/v1/tasks`
   needs a separate REST resource declaration for the tasks operator face — a follow-on,
   not a phantom.
2. **network_rules: already clean, out of scope.** Its `AuthorizeStructural` lives in the
   injected Gate (network_rules_resource.go:187), the handler is auth-agnostic, and it has
   NO REST twin — the model the decouple brings routes/tasks to. Adopt the same containFn
   only if/when a REST face lands.
3. **Tool-browser drift — RESOLVED 2026-07-11** (df9ebad3 single-sources facade-tool
   MCP metadata; d5023c60 `ipc.ListTools` renders grant-visible cold-tier facade tools
   for dashd; `facade_tools_test.go`). `dashd`'s schema browser now shows the migrated
   cold-tier facade tools. Verified this pass (`ipc/ipc.go:2263`).
4. **`container/runner.go` standalone ServeMCP (minor).** The non-split dev path
   (`!ExternalMCP`) gets no postBuild → no facade tools there. Production (split, routd
   hosts the socket) unaffected.
5. **surrogate refresh: bodyless-4xx nulls the row (5/15).** RESOLVED 2026-07-07 (e813efd5):
   `Engine.Refresh` now signals `ErrReconnect` ONLY on a definitive OAuth error body
   (`tr.Error` set); a bodyless/unparseable non-2xx is a transient error that keeps the
   credential. Test `TestRefresh_BodylessErrorIsNotReconnect`.
6. **flaky test: `TestRefreshRotationRaceSingleWinner` (authd/bugfix_test.go:131).** A
   refresh-rotation concurrency race test that intermittently fails under full-suite parallel
   load but passes 5/5 in isolation. Pre-existing, unrelated to the 5/16/5/15 work. Needs a
   sync point or serialization in the test harness. Low priority (not a product bug).


## oracle skill + examples tell operators to folder-scope CODEX_API_KEY, which the store rejects (2026-07-02, open)

Spec 5/14 put `CODEX_API_KEY`/`OPENAI_API_KEY` in `store.EnvProfileKeys`, so `validateScope`
(`store/secrets.go:128`) rejects them at `scope_kind='folder'` — model creds are user-only (BYOA)
or platform (host `.env`). But `ant/skills/oracle/SKILL.md` "Path B" and several
`ant/examples/*/PRODUCT.md` still show `[[secret]] key = "CODEX_API_KEY"` at **folder** scope.
Under 5/14 enforcement that write 400s.

**Design call needed** (do not silently change either side): (a) keep the model — codex/openai are
user-only like Anthropic — and fix the oracle docs/examples to user-scope or platform `.env`; or
(b) carve `CODEX_API_KEY`/`OPENAI_API_KEY` OUT of the folder-reject as a shared *team* capability
(a folder pays for codex for everyone), keeping only the Anthropic keys user-only. The platform
`.env` fallback for all four now works (`container/runner.go:readSecrets` fixed 2026-07-02); this
is purely about whether folder-scope is allowed. Fixing the docs also requires an ant image rebuild.

- **Severity:** medium (breaks documented operator flow)
- **Scope:** store/secrets.go:128 EnvProfileKeys; ant/skills/oracle/SKILL.md, ant/examples/*/PRODUCT.md
- **Source:** credential refine-review 2026-07-02
- **Status:** RESOLVED 2026-07-05 — user decided codex scopes like Claude: one global (platform
  `.env`) + BYOC (user-scoped), NEVER folder. Code already enforced this (store rejects folder;
  readSecrets carries all 4 keys). Only `oracle/SKILL.md` Path B claimed "folder secret" — fixed to
  env-profile. The `PRODUCT.md` examples were already correct (they use `[[env]]` = platform, not
  `[[secret]]` folder). NOTE: the SKILL.md fix reaches live agents only after an ant image rebuild.

## OpenAPI convention emits phantom/divergent paths for hand-rolled resources (2026-07-02, open)

`resreg/openapi.go:resourcePaths` synthesizes the 5/8 CRUD convention (`/v1/<name>`
GET/POST/GET-one/PATCH/DELETE) from `(RowType, PKFields)`, ignoring each resource's real
`Endpoints`. Truthful only for `proxyd_routes` (the one resource whose live RegisterREST
matches the convention). The others advertise paths their daemons don't serve or serve with
different shapes:

- `timed` advertises `/v1/scheduled_tasks` CRUD — serves none (only `/health`,`/dash`,`/openapi.json`).
- `onbod` advertises `/v1/onboarding_gates` CRUD — actually serves `/v1/onboarding`.
- `routd` advertises `routes`/`web_routes`/`acl` — hand-rolled `server.go` diverges on method/param
  (routes `{id}` not `{seq}`, has `PUT` + no `PATCH`; acl remove is body-`DELETE /v1/acl`; etc).

The `secrets` read-surface leak (convention emitted `GET /v1/secrets/{scope_kind}`) is FIXED by
dropping `secrets` from routd's OpenAPI list (2026-07-02). The rest is the 5/16 ipc→resreg
migration: when each becomes a real resreg resource served via RegisterREST with true `Endpoints`,
change `resourcePaths` to emit from `Endpoints` (empty → schema-only) and the drift closes for good.

- **Severity:** medium (public API doc misleads; not a live leak — phantom paths 404)
- **Scope:** resreg/openapi.go:resourcePaths; timed/split.go, onbod/main.go, routd/cmd/routd/main.go
- **Source:** resreg refine-review 2026-07-02
- **Status:** MOSTLY RESOLVED 2026-07-11 — `resreg/openapi.go` now emits each resource's
  real `Endpoints` (7c14efd6, single-sourced), so routd (routes/web_routes/acl) and onbod
  (gates → `/v1/gates`, 4bd09532) are truthful. **Only `timed` remains**: `timed/split.go:13`
  still lists `scheduled_tasks` in its OpenAPI while serving only health/openapi/dash. Fix is
  a decision (Q8): emit an empty resource list for timed, or give timed a real `/v1` face.

## proxyd_routes list handler returns {routes:[]} envelope; OpenAPI documents a bare array (2026-07-02, open)

`proxyd/resource.go:187` returns `map[string]any{"routes": out}` but `resreg/openapi.go` documents
list `200` as a bare `array<ProxydRoutes>`. The one resource whose paths are correct still serves a
body shape its own doc contradicts. Fix: return the bare `[]Route` (matches the engine convention;
no in-tree consumer indexes the `routes` key) or document the envelope. Deferred: needs a consumer
sweep (webd forwarder relays the body) to confirm the bare-array change is safe.

- **Severity:** low-medium (doc/handler drift on one resource)
- **Scope:** proxyd/resource.go:187
- **Source:** resreg refine-review 2026-07-02
- **Status:** open

## resreg.Caller.Name is write-only (2026-07-02, open)

`resreg/resreg.go:76` `Caller.Name` is set by both adapters (`proxyd/resource.go:366`,
`webd/routes_mcp.go:50`) but read by nobody — `buildEvent` uses `Sub` for both audit `Actor` and
`ActorSub`. Either drop the field + its two assignments, or use it as the human-readable audit
`Actor`. Left this round (the drop cascades through the two call sites' `name` computations; the
use-as-Actor changes audit semantics — neither is a pure cleanup).

- **Severity:** minor
- **Scope:** resreg/resreg.go:76
- **Source:** resreg refine-review 2026-07-02
- **Status:** fixed 2026-07-12 — dropped the field + both assignments (audit Actor
  stays Sub, unchanged); the proxyd `name` computation went with it, webd's `name`
  param remains live for the forwarded X-User-Name header

## ConnectorSecrets resolves folder scope only — user BYOA key never reaches MCP subprocess

**Severity**: medium
**Found**: 2026-06-26
**Location**: `routd/sibling_db.go:ConnectorSecrets` (calls `FolderSecrets(folder)`, not `FolderSecretsForUser`)

A user who sets `GITHUB_TOKEN` in their user-scoped secrets (via `/dash/me/secrets`)
expects it to be used when they invoke a GitHub MCP connector tool. It isn't.
`ConnectorSecrets` reads folder scope only — the user-scoped override is invisible
to the connector subprocess.

Fix requires:
1. `sibling_db.go:ConnectorSecrets(folder, callerSub string)` — add `callerSub` param,
   call `FolderSecretsForUser(folder, callerSub)` instead of `FolderSecrets(folder)`.
2. Thread `callerSub` (the turn's trigger sender) into `ipc/ipc.go:1027` where
   `db.ResolveConnectorSecrets(folder, ...)` is called.
3. `routd/mcp.go:569` pass `callerSub` alongside `folder` into the resolver.

Spec: `specs/5/14-credentials.md § ConnectorSecrets user-scope`.

- **Status:** fixed 0d244973 (2026-06-26) — ConnectorSecrets now takes callerSub, calls FolderSecretsForUser

## Slack threading ignored for in-thread triggers when thread_replies=false (2026-06-25, fixed)

`routd/turns.go:deliverTurn` suppressed `threadRoot` (no new thread) when
`thread_replies=false`, but still passed `tc.Topic` as `threadID` to the platform.
On Slack this means: user types inside an existing thread → topic != "" → bot reply
carries the thread_ts → stays in the thread → invisible in main channel. The
`thread_replies=false` setting only blocked NEW threads, not existing thread chains.
Fixed in 2b2e6062: `deliverTurn` now zeroes `threadID` too when threading is disabled.
All atlas groups on marinade set to `thread_replies=0` (DB update).

- **Status:** fixed 2b2e6062 (2026-06-25)

## Channel adapter silent no-op after auto-deregister (2026-06-25, fixed)

`chanlib/run.go` registered with routd once at startup. After 3 consecutive `/health`
503s (e.g. transient Telegram API Bad Gateway), routd auto-deregisters the adapter.
Adapter recovers and `connected.Store(true)` flips health back to 200, but it never
re-registers. In the split topology, JWT auth and channel registration are decoupled:
inbound messages still reach routd (JWT passes), but outbound delivery silently fails
(`deliverRow` drops the error). 3 bot replies stuck at `status=pending` on krons
today. Fixed in 3625947b: 3-minute re-register heartbeat. Also added error logging
in `deliverRow` (8e61521a) so failures are no longer silent.

- **Status:** fixed 3625947b + 8e61521a (2026-06-25)

## `arizuko network` CLI writes/reads messages.db, not routd.db (2026-06-21, fixed)

`cmd/arizuko/network.go:17` opens the DB via `store.Open(dir/store)`, which targets
`messages.db` (store/store.go:51). But in the split topology `network_rules` is owned by
routd and lives in `routd.db` (store has `OpenRoutd` for exactly this). So `arizuko network
<inst> allow|deny|list|resolve` operates on the wrong DB: writes land in a `network_rules`
table routd never reads, and `resolve` shows stale messages.db rows, not routd's live
allowlist. The operator thinks they edited the egress allowlist but the agent's crackbox
rules are unchanged.

Discovered while fixing sloth main/trading web access: `allow` reported success but the host
stayed denied because the rule went to messages.db. Worked around by inserting directly into
routd.db. The agent-facing MCP path (routd/mcp.go → s.db.AddNetworkRule) is correct — only the
CLI is wrong.

Fix: `cmdNetwork` should call `store.OpenRoutd(dataDir/store)` instead of `store.Open`.

- **Severity:** medium (silent — operator egress edits via CLI are no-ops; no error surfaced)
- **Scope:** cmd/arizuko/network.go
- **Affected:** `arizuko network allow|deny|list|resolve` on all split instances
- **Source:** cmd/arizuko/network.go:17 (`store.Open` → should be `store.OpenRoutd`)
- **Status:** fixed 2026-06-21 (2dfa5670 — store.Open → store.OpenRoutd)

## slakd message IDs are per-channel, not globally unique (2026-06-19, open)

`slakd/bot.go:521` uses `m.TS` (Slack timestamp) as the `InboundMsg.ID`. Slack `ts` is unique
within a channel but not across channels in the same workspace. Two channels posting at the same
microsecond get the same `ts` → `INSERT OR IGNORE` would silently drop the second. Same class of
bug as the teled cross-chat ID collision (fixed 2026-06-19). In practice extremely unlikely
(Slack backend is monotonic per-workspace, microsecond precision), but structurally unsound.

Fix: prefix with the channel JID, e.g. `jid + "/" + m.TS`. Same pattern as teled.
Reaction IDs (`r.Item.TS + ":r:" + r.Reaction`) need the same treatment.

- **Severity:** low (theoretical — Slack TS precision makes collision astronomically unlikely)
- **Scope:** slakd
- **Affected:** slakd/bot.go:521,567 — multi-channel Slack workspaces
- **Status:** fixed 2026-07-11 — message + reaction IDs prefixed with the channel
  `jid`, mirroring teled (`bot.go:291,209`). Raw-TS API targeting uses `TargetID`,
  not `InboundMsg.ID`, so prefixing is safe.

## dashd reads SQLite directly — violates spec 6/1 read-path contract (2026-06-16, open)

`dashd/main.go:106, 170-177, 182-191` opens `routd.db`, `onbod.db`, and `messages.db` directly.
Spec 6/1 §"Read-path: /v1 only, no direct DB" forbids this. Direct DB reads miss all live process
state (runed active runs, routd breaker/queue depth, adapter session health, timed next-tick) —
state that only exists in daemon memory. The cockpit spec was written to force /v1 reads precisely
because snapshots can't see that state.

- **Severity:** high
- **Scope:** dashd, cockpit spec 6/1
- **Affected:** all dashd pages (groups, status, invites, memory, tokens, activity)
- **Source:** dashd/main.go:106,170-191; specs/7/1-cockpit-hub.md
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
- **Source:** dashd/main.go:789-822; specs/7/2-dashd-hub.md
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
- **Status:** resolved d52b2c50 + b99138fb (2026-06-06) — stale entry, verified
  2026-07-13: task detail gates on the task's owner via `visible()`
  (tasks_admin.go), routes list filters each row by `visible(target folder)`
  (routes_admin.go:65)
- **Fix:** add `requireOperator` or `requireVisible(folder)` gate to those GET handlers

## dashd: adminDB() panics if routd.db open fails (2026-06-17, open)

`main.go:226-231` warns and continues if `routd.db` fails to open, leaving `d.dbRoutd = nil`.
`adminDB()` (`main.go:307`) returns `d.dbRoutd` unconditionally. Any request to `handleStatus`,
`handleGroups`, `handleTasks`, `writeActivityRows`, etc. then panics at the `.Query` call.

- **Severity:** medium
- **Scope:** dashd/main.go:226-231, main.go:307
- **Affected:** startup — routd.db missing/corrupt causes panic on first request
- **Source:** bucket A refine review 2026-06-17
- **Status:** resolved a51989cc (2026-07-05) — stale entry, verified 2026-07-13:
  audited all 92 `adminDB()` call sites; every one is nil-guarded directly or
  sits behind a fail-closed gate (`requireVisible`/`requireAdmin` 503 on nil,
  chat portal early-returns on empty folders). Startup keeps Warn+continue by
  design (nil → degraded 503s, comment main.go:223)
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

---
## Codex audit 2026-06-18 — dashd full sweep

---

## [SEC] channels: pairAuth fails open + innerHTML XSS in pair-status polling (2026-06-18, open)

Two issues in `channels.go`. (1) `pairAuth` at line 33: if service-token minting fails, dashd
still sends the call to whapd without Authorization — auth bypass risk on a sensitive operator
action. (2) Line 91: JS polling renders `expires_at`/`since` from whapd JSON directly into
`innerHTML` — admin-page XSS sink if whapd or a proxy returns attacker-controlled strings.

- **Severity:** high
- **Scope:** dashd/channels.go:33, :91
- **Source:** codex audit 2026-06-18
- **Status:** resolved — pairAuth returns error + fail-closed; JS uses textContent/createTextNode (80bd29da)
- **Fix:** (1) fail closed when svc token unavailable; (2) use `textContent` or escape fields

## [SEC] routes: handleRouteUpdate authorizes new target, not existing row (2026-06-18, open)

`routes_admin.go:194`: PATCH only requires admin on the new target folder. A caller who knows a
route ID in another folder can PATCH it into a folder they own — changing match/seq/target on an
otherwise inaccessible rule.

- **Severity:** high
- **Scope:** dashd/routes_admin.go:194
- **Source:** codex audit 2026-06-18
- **Status:** resolved — loads existing row first, requires admin on old + new folder (80bd29da)
- **Fix:** load current row first; require admin on both old and new folder

## [SEC] invites: raw token exposed in revoke URL (2026-06-18, open)

`invites.go:56`: token is a path param in the revoke form action — leaks live bearer into request
logs, browser history, and proxy access logs.

- **Severity:** high
- **Scope:** dashd/invites.go:56
- **Source:** codex audit 2026-06-18
- **Status:** resolved a51989cc — stale entry, verified 2026-07-12: revoke URL
  carries `inviteRef` (sha256 of token); handler resolves ref→token server-side
- **Fix:** revoke by opaque row ID, not token

## [SEC] route_tokens: encodeJID/decodeJID not reversible; webhook label unvalidated (2026-06-18, open)

`route_tokens.go:131`: `decodeJID` maps every `--` back to `/`, so a label containing `--` revokes
the wrong JID. `route_tokens.go:43`: label is unvalidated — can contain `/` or control chars that
break JID namespace.

- **Severity:** high
- **Scope:** dashd/route_tokens.go:43,:131
- **Source:** codex audit 2026-06-18
- **Status:** resolved — label validated against `[a-zA-Z0-9._-]+` (80bd29da);
  encodeJID collision fixed a51989cc, verified 2026-07-13: `encodeJID` =
  `url.PathEscape` (reversible), `decodeJID` deleted (mux PathValue unescapes)
- **Fix:** use `url.PathEscape`; validate label against `[a-zA-Z0-9._-]+`

## [SEC] chat: raw bearer tokens visible to read-scoped dashboard users (2026-06-18, open)

`chat.go:152,262`: session list renders raw chat bearer tokens in Continue links. Any read-scoped
user who can see the portal sees a reusable write-capable token, bypassing the admin gate on token
minting.

- **Severity:** high
- **Scope:** dashd/chat.go:152,:262
- **Source:** codex audit 2026-06-18
- **Status:** resolved a51989cc (2026-07-05) — stale entry, verified 2026-07-13:
  continue links render only for folders the caller admins (`callerAdmins` map
  in the portal, `admin` gate on the group page; `folderSessionTokens` fetched
  only when admin) — read-scoped viewers see rows without tokens
- **Fix:** restrict session listing to admins, or hide the raw token from non-admin views

## [SEC] routd/runed: retry + kill endpoints missing CSRF protection (2026-06-18, open)

`routd_page.go:106`: POST /dash/routd/retry gated by `requireOperator` only — no same-origin
check. `runed_page.go:135`: POST /dash/runed/kill same issue, higher impact (terminates live runs).

- **Severity:** high
- **Scope:** dashd/routd_page.go:106, dashd/runed_page.go:135
- **Source:** codex audit 2026-06-18
- **Status:** resolved — `requireSameOrigin` added to both (1dc9b356)
- **Fix:** add `requireSameOrigin` to both write endpoints

## [SEC] grants: effect silently defaults to allow; nil adminDB in POST paths (2026-06-18, open)

`grants_admin.go:184`: invalid `effect` value silently creates an allow grant. `grants_admin.go:46`
guards GET but not POST — nil adminDB panics on add/revoke.

- **Severity:** high
- **Scope:** dashd/grants_admin.go:184,:46
- **Source:** codex audit 2026-06-18
- **Status:** resolved — invalid effect → 400; nil adminDB guard on add + revoke (91159586)
- **Fix:** reject invalid effect with 400; nil-guard adminDB in POST handlers

## tasks: new tasks have no next_run; cron not validated on create (2026-06-18, open)

`tasks_admin.go:216`: tasks inserted with `status='active'` but no `next_run` — timed only fires
rows with `next_run <= now`, so dashboard-created tasks never schedule. `tasks_admin.go:201`:
invalid cron/interval strings stored silently; timed logs warning and task stays dead.

- **Severity:** high
- **Scope:** dashd/tasks_admin.go:201,:216
- **Source:** codex audit 2026-06-18
- **Status:** resolved — `taskNextRun` computes next_run at create; invalid cron → 400 (91159586)
- **Fix:** compute next_run at create time; validate cron using same parser as timed

## tasks: state-machine transitions not enforced (2026-06-18, open)

`tasks_admin.go:145`: `resume` can revive cancelled/completed tasks without restoring `next_run`;
`pause`/`cancel` can clobber a firing task. Produces zombie active tasks that never fire.

- **Severity:** medium
- **Scope:** dashd/tasks_admin.go:145
- **Source:** codex audit 2026-06-18
- **Status:** resolved — SQL-level guards on pause/resume/cancel; next_run recomputed on resume (91159586)
- **Fix:** enforce valid transitions in SQL or code; recompute next_run on resume

## groups: delete parent leaves orphaned child rows; not atomic (2026-06-18, open)

`groups_admin.go:403`: deleting `corp` removes ACL/routes for the whole subtree and purges files,
but leaves `corp/eng` rows in `groups`. Orphaned child groups become invisible but persistent.
Also not atomic — if ACL cleanup fails after groups DELETE, stale rows resurface.

- **Severity:** medium
- **Scope:** dashd/groups_admin.go:403
- **Source:** codex audit 2026-06-18
- **Status:** resolved — delete wrapped in transaction; descendant group rows purged atomically (91159586)
- **Fix:** delete descendant groups rows in same transaction; forbid non-leaf delete or wrap all in tx

## groups: observe-window and max_children forms corrupt defaults on save (2026-06-18, open)

`groups_admin.go:246,:286`: GET maps stored -1/NULL to `0` for display; POST saves `0` back as a
real override. Opening settings and clicking save changes "use defaults" to "0 msgs/0 chars" (send
nothing) or "0 max_children" (spawning disabled).

- **Severity:** medium
- **Scope:** dashd/groups_admin.go:246,:286
- **Source:** codex audit 2026-06-18
- **Status:** resolved — -1/NULL renders blank; 0 clears to default via ClearGroupMaxChildren (91159586)
- **Fix:** render -1/NULL as blank; treat submitted 0 as clear-to-default in POST handler

## main: writeTaskRows and activity feed apply LIMIT before visibility filter (2026-06-18, open)

`main.go:730`: `writeTaskRows` fetches LIMIT 500 then filters in Go — scoped users miss tasks that
sort after row 500. `main.go:808`: activity feed fetches 1000 rows then filters — same class.

- **Severity:** medium
- **Scope:** dashd/main.go:730,:808
- **Source:** codex audit 2026-06-18
- **Status:** resolved a51989cc (2026-07-05) — stale entry, verified 2026-07-13:
  writeTaskRows pushes the owner filter into SQL (`ownerVisibleSQL`; glob grants
  fall back to a 5000 over-fetch). Activity deliberately over-fetches 1000 for
  non-operators (visibility needs Go-side `jidFolder` route resolution, not
  SQL-expressible); residual: scoped rows older than the newest 1000 stay hidden
- **Fix:** push visibility filter into SQL; or page until N visible rows found

## main: memory write/delete broken for nested group folders (2026-06-18, open)

`main.go:1073`: `parseMemoryPath` splits on first `/` after `/dash/memory/`. For
`corp/eng`, folder="corp" and rel="eng/MEMORY.md" — fails allowlist. Read page shows nested groups
correctly but mutations don't work.

- **Severity:** medium
- **Scope:** dashd/main.go:1073
- **Source:** codex audit 2026-06-18
- **Status:** resolved — parseMemoryPath scans split points right-to-left; nested folders work (91159586)
- **Fix:** route mutations through a wildcard `{folder...}` path param or parse from suffix

## audit: pagination cursor off by one (2026-06-18, open)

`audit_page.go:83`: `lastID` is set on the 51st lookahead row, then page is sliced to 50 and
`before=<lastID>` skips the actual 50th row on the next page.

- **Severity:** medium
- **Scope:** dashd/audit_page.go:83
- **Source:** codex audit 2026-06-18
- **Status:** resolved — cursor taken from last displayed row (29e759f7)
- **Fix:** capture cursor from row 50, not the lookahead row

## routd: retry clears errored on all messages, not just errored=1 (2026-06-18, open)

`routd_page.go:123`: `UPDATE messages SET errored=0 WHERE chat_jid=?` resets all rows, including
non-errored history.

- **Severity:** medium
- **Scope:** dashd/routd_page.go:123
- **Source:** codex audit 2026-06-18
- **Status:** resolved — `AND errored=1` added (1dc9b356)
- **Fix:** `UPDATE messages SET errored=0 WHERE chat_jid=? AND errored=1`

## services: davd probe path wrong; timeout classified as unknown (2026-06-18, open)

`services.go:54`: probe uses `GET /health` for all daemons, but davd's healthcheck is `GET /`.
A healthy davd always shows err. `services.go:60`: timeout mapped to `unknown` — should be `err`
(unknown is for DNS failures; timeout means deployed-but-down).

- **Severity:** medium
- **Scope:** dashd/services.go:54,:60
- **Source:** codex audit 2026-06-18
- **Status:** resolved — per-service Probe field; timeout → statusErr; DNS → statusUnknown (29e759f7)
- **Fix:** per-service probe path; classify timeout as err not unknown

## usage: 7-day query covers 8 days; GroupUsageBulk IN list will fail at scale (2026-06-18, open)

`usage_page.go:111`: `date('now', '-7 days')` includes the full day 7 days ago + today = 8 days.
`usage_page.go:38`: `GroupUsageBulk` builds `IN (...)` from all folder names — hits SQLite param
limit with many groups.

- **Severity:** medium
- **Scope:** dashd/usage_page.go:38,:111
- **Source:** codex audit 2026-06-18
- **Status:** partial — datetime fix applied (29e759f7); GroupUsageBulk IN-list still open
- **Fix:** use `datetime('now','-7 days')` for rolling window; rewrite bulk query with JOIN

## chat: loadChatSessions limits before filtering; nondeterministic continue links (2026-06-18, open)

`chat.go:90`: newest 200 rows fetched then filtered in Go — older sessions disappear once table
grows. `chat.go:361`: `folderSessionTokens` infers topic→token by time proximity; two sessions
without an early message produce nondeterministic continue links.

- **Severity:** medium
- **Scope:** dashd/chat.go:90,:361
- **Source:** codex audit 2026-06-18
- **Status:** partial — LIMIT-before-filter half resolved a51989cc (2026-07-05,
  verified 2026-07-13: `folder IN (...)` pushed into SQL before the LIMIT 200).
  Still open: `folderSessionTokens` topic→token linkage by time proximity
  (nondeterministic continue links) — needs persisted linkage, design change
- **Fix:** push filter into SQL; persist topic-session linkage explicitly

## runed: dead session_id variable in run query (2026-06-18, open)

`runed_page.go:52`: `COALESCE(session_id,'')` selected and scanned but never used — dead code in
the hot path.

- **Severity:** low
- **Scope:** dashd/runed_page.go:52
- **Source:** codex audit 2026-06-18
- **Status:** resolved — dropped from SELECT (1dc9b356)
- **Fix:** drop from SELECT and remove variable

## main: duplicate profile nav link + dead portal .Err field (2026-06-18, open)

`main.go:463`: `profile` appears in both `navLinks` and the identity badge — double aria-current
on `/dash/profile/`. `main.go:540`: portal template `.Err` field is always empty — dead branch.

- **Severity:** low
- **Scope:** dashd/main.go:463,:540
- **Source:** codex audit 2026-06-18
- **Status:** resolved — profile removed from navLinks; .Err branch removed (91159586)
- **Fix:** remove profile from navLinks (keep badge); remove .Err from portal template

## ext tools: InputSchema never populated — agents have no parameter schema (2026-06-26, open)

`ipc.ExtTool.InputSchema` is `json.RawMessage` and `ipc/ipc.go` registration gates on
`len(tool.InputSchema) > 0` to attach it. But `routd/ext.go:LoadExtProviders` never sets
it — the `extToolConfig` struct has no schema field. Every ext tool registers with no
MCP input schema. LLM agents infer args from tool name + description; schema-driven
callers (and accurate UI hint displays) see nothing.

- **Severity:** low (LLM agents guess correctly from description; functional gap for strict callers)
- **Scope:** `ipc/extcall.go:ExtTool`, `routd/ext.go:extToolConfig`, `routd/extproviders/*.toml`
- **Affected:** all REST-descriptor tools (cloudflare, porkbun, gandi, namecheap)
- **Source:** refine review 2026-06-26
- **Status:** RESOLVED 2026-07-11 — `extToolConfig` gained a `[[param]]` array
  (`routd/ext.go:42`), `extInputSchema` builds the MCP schema (`:63`), and it's
  populated on registration (`:157`), consumed at `ipc/ipc.go:990`. Verified this pass.

---

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

## `arizuko apply`/`plan`/`export`/`get` operate on the frozen messages.db post-split (2026-07-01, open)

The resreg YAML-manifest CLI (`cmd/arizuko/apply.go`) opens `store.Open(dataDir+"/store")`
= **messages.db** and drives `resreg.Apply`/`Plan`/`Export`/`ConfigVersion` against it. But
the config resources it manages (groups/acl/acl_membership/routes/web_routes/scheduled_tasks/
network_rules/proxyd_routes/onboarding_gates) all MOVED to routd.db/onbod.db in the split
cutover. So `apply` reads + writes a frozen twin: its CAS counter (`config_meta`) and every
resource DELETE+INSERT hit tables the live daemons no longer read. Effectively `arizuko apply`
is a no-op against production config. `config_meta` is entangled with this — it cannot move to
routd.db until the CLI is repointed at the owner DBs (a per-resource DB routing problem, since
resources now span routd.db + onbod.db). Deferred out of the messages.db-retirement slice.

The same CLI also contradicts 5/8's token exclusion: `resreg.Export` scans every
registered `RowType`, while `resreg/resources/invites.go` registers the raw invite
`Token` and does not set `SkipApplyRebuild`. Therefore today's frozen-DB export can
emit invite bearer tokens, and a naive owner-DB repoint would emit the live tokens.
The 5/8 finalization must exclude imperative token resources from export/apply,
not merely route them to the correct owner DB. `route_tokens` already skips apply
but is still exported as non-secret metadata; decide separately whether that
metadata belongs in a dump.

- **Severity:** medium
- **Scope:** cmd/arizuko resreg apply/plan/export/get, config_meta ownership
- **Affected:** all instances (YAML-manifest config management dead post-split)
- **Source:** resreg/engine.go:627-645 (exports every RowType); resreg/resources/invites.go:14-21,36-43 (raw token registered); store/migrations/0067-config-meta.sql
- **Status:** PARTIAL (verified 2026-08-05) — the messages.db half is DONE: `apply.go` now
  opens routd.db + onbod.db per subsystem via `openSubsystemStores` (`store.OpenRoutd`/
  `OpenOnbod`, apply.go:46-58), so these verbs reach production config. STILL OPEN: (1)
  `config_meta` remains an orphan in messages.db, so the CAS counter has no owner-DB home;
  (2) the 5/8 token-exclusion gap — `resreg.Export` still scans every RowType including the
  raw invite `Token`, which now that the repoint has landed means a live-token export.

## ✅ FIXED 2026-08-05 — dashd reads messages/chats/cost_log/task_run_logs from frozen messages.db (2026-07-01)

Several dashd read paths still query the pre-split messages.db handle (`d.db`) for tables that
moved to routd.db: `routd_page.go` reads `messages`/`chats` (errored + pending outbound counts,
and UPDATEs `messages SET errored=0`), `usage_page.go` reads `cost_log` via `GroupUsageBulk(d.db)`,
`chat.go` reads chat_sessions + messages. These render stale/empty because the live rows are in
routd.db now. Separate concern from the audit_log move (which this pass fixed by repointing
audit.Init + the audit reader to routd.db). These readers should be repointed to `d.adminDB()`
(routd.db) / `d.dbRuned` respectively; after that dashd could drop `store.Open(messages.db)`.

- **Severity:** medium
- **Scope:** dashd routd_page.go / usage_page.go / chat.go direct messages.db reads
- **Affected:** all instances (dashd status/usage/chat views show stale post-split data)
- **Source:** dashd/routd_page.go:50,64,126; dashd/usage_page.go:38; dashd/chat.go:138,369
- **Status:** ✅ RESOLVED — both remaining halves landed. `usage_page.go`'s aggregation was
  rewritten for routd's cost_log columns and moved into dashd (its only consumer);
  `store.GroupUsageBulk`/`GroupUsageSummary` deleted rather than left as a store-schema twin.
  `chat_sessions` became a real routd table (migration 0029) instead of a lazily-CREATEd
  table inside the monolith; it held zero rows on all three instances, so no backfill.
  dashd's `db`/`dbRW` fields are gone — the monolith is unreachable by construction — and
  `resolveDSN` refuses a `DB_PATH` ending in `messages.db` so a stale env fails loudly.
- **Fix:** 3c2b7ad7

## Product PRODUCT.md manifests declare a non-existent `facts` skill (2026-07-09, OPEN)

9 of the `ant/examples/*/PRODUCT.md` manifests list `"facts"` in their `skills`
array (personal, reality, socials, support, creator, pm, slack-team, strategy,
trip), but there is no `ant/skills/facts/` — writing facts is done by `find` +
`recall-memories`. Either the seed path silently ignores unknown skill names
(then the manifest is misleading) or it errors/mis-seeds. Product doc pages
mirror the manifests, so the drift shows up in docs too.

- **Severity:** low
- **Scope:** `ant/examples/*/PRODUCT.md` skills field vs `ant/skills/`
- **Source:** grep — no `ant/skills/facts`; 9 PRODUCT.md declare it
- **Status:** OPEN — decide: rename to a real skill, add a `facts` skill, or drop from manifests
- **Found:** product-docs refine pass (fable), 2026-07-09

## Suffixed web chat token mints a JID that routes nowhere (2026-07-09, OPEN)

`arizuko token <inst> issue chat <folder> <suffix>` builds
`jid = "web:"+folder+"/"+suffix` (`cmd/arizuko/token.go:60`). A group's auto route
is `room=<folder>`, and direct-address resolution keys on the folder — a
`web:<folder>/<suffix>` JID appears to match neither, so a suffixed chat link may
be dead on arrival. The unsuffixed form (`web:<folder>`) works. If suffixed chat
tokens are meant to route (e.g. sub-surface chats), that's a routd gap; if not,
the CLI should reject the suffix for `chat`. Product setup pages were written to
avoid the suffix as a workaround.

- **Severity:** medium (silent dead link if an operator uses the documented suffix arg)
- **Scope:** `cmd/arizuko/token.go` chat JID + routd route/direct-address resolution
- **Source:** `cmd/arizuko/token.go:55-62`; route match `room=<folder>`
- **Status:** OPEN — needs a routing repro to confirm whether `web:X/sub` resolves
- **Found:** product-docs refine pass (fable), 2026-07-09

## howto/webhooks.html claims hook headers are delivered; ingest forwards body only (2026-07-09, OPEN)

`template/web/pub/arizuko/howto/webhooks.html` documents a `"headers": {…}`
envelope on hook ingest, but the route-token webhook path forwards the request
body only (per `webd/route_token.go` ~:268, cited by the refine agent — verify).
Product pages had the same claim removed; the howto page still carries it.

- **Severity:** low (doc-only; misleads webhook integrators)
- **Scope:** `template/web/pub/arizuko/howto/webhooks.html` vs `webd/route_token.go`
- **Status:** fixed 2026-07-12 — verified `webd/route_token.go:handleHookIngest`
  builds `InboundMsg{Content: body}` only (no headers field exists on the type);
  removed all four headers-delivery claims from the howto page, security bullet
  now states headers are dropped at ingest
- **Found:** product-docs refine pass (fable), 2026-07-09

## `arizuko token issue chat|webhook` writes route_tokens to frozen messages.db (2026-07-11, open)

Same class as the fixed `arizuko network` bug (2026-06-21, 2dfa5670): `cmd/arizuko/token.go`
`cmdToken` opens the DB via `store.Open(dir/store)` → `messages.db`, but post-split
`route_tokens` is owned by routd in `routd.db` (`routd/tokens.go` resolves against `d.db`).
CLI-issued chat/webhook tokens land in a table routd never reads — the printed `/chat/<token>`
or `/hook/<token>` URL silently never resolves (webd POST /v1/route_tokens/resolve → 404).
Verified live on krons: routd.db has 3 route_tokens, messages.db has 1 stale CLI-issued row.
Also breaks `token list` (reads stale rows) and the `issue` folder-existence check
(`GroupByFolder` on messages.db misses post-split groups, e.g. `eval`). The REST
(`POST /v1/route_tokens/chat`) and MCP (`issue_chat_link`) paths are correct — only the CLI
is wrong. Note `tokenIssueBearer` (added 2026-07-11) is unaffected: it reads auth.db +
validates the folder via `mustOpenACL` (routd.db).

Fix: route `issue chat|webhook`/`list`/`revoke` through `store.OpenRoutd` (mirror 2dfa5670).

- **Severity:** medium (silent — operator-issued capability URLs are dead; no error surfaced)
- **Scope:** cmd/arizuko/token.go
- **Affected:** `arizuko token issue chat|webhook|list|revoke` on all split instances
- **Source:** cmd/arizuko/token.go:27 (`store.Open` → should be `store.OpenRoutd` for route_tokens)
- **Status:** fixed 2026-07-12 — cmdToken opens routd.db via the existing
  `mustOpenACL` (one CLI open-path, no second `store.OpenRoutd` copy); guarded by
  `TestTokenLandsInRoutdDB` (issue+revoke land in routd.db, messages.db stays empty)
- **Fix:**

## `arizuko group add`/`create` writes the group skeleton to CWD, not the data dir (2026-07-11, open)

`core.LoadConfigFrom(dataDir)` loads the instance `.env` but resolves
`root = envOr("DATA_DIR", mustCwd())` (`core/config.go:157`). Instance `.env` files don't set
`DATA_DIR` (compose injects it per-container), so on the HOST the CLI's `cfg.GroupsDir`
becomes `<cwd>/groups` — `container.SetupGroup` writes the whole agent-home skeleton
(`.claude/`, skills, logs) into a stray `groups/<folder>` under whatever directory the
operator ran the command from. The DB rows still land correctly (routd.db), and routd/runed
re-provision the real `groups/<folder>` on first dispatch, which masks the bug. Observed
live 2026-07-11 creating the `eval` group on krons from a repo worktree: skeleton appeared
in `<worktree>/groups/eval` (root-owned), real dir appeared only when routd provisioned it.

Fix direction: host CLI paths should derive GroupsDir from the instance dir it was given
(`filepath.Join(dataDir, "groups")`), not from `DATA_DIR`-or-cwd. Cross-check other CLI
users of `LoadConfigFrom` (`cmdCreate`, `cmdPair`, `cmdStatus`) for the same cwd fallback.

- **Severity:** low (masked by daemon-side re-provisioning; litters cwd, confuses operators)
- **Scope:** cmd/arizuko/main.go + core/config.go LoadConfigFrom root resolution
- **Affected:** `arizuko create`, `arizuko group add` run on the host
- **Source:** core/config.go:157 (`envOr("DATA_DIR", mustCwd())`)
- **Status:** fixed 2026-07-12 — `LoadConfigFrom(dir)` defaults DATA_DIR to `dir`
  (explicit env/.env still wins), fixing all four callers at the one cause site
  (cross-checked: create/group add/status/secret all pass `mustInstanceDir`);
  daemons use `LoadConfig()` directly, unaffected. Tests:
  `TestLoadConfigFromDefaultsRootToDir` + `TestLoadConfigFromRespectsDataDirEnv`
- **Fix:**

## PROPOSAL — anteval `--mcp` parity face needs an inspect-read on a public MCP surface (2026-07-11, proposed)

Spec 5/9 gap (b): `anteval --mcp` expects an "inspect-compatible MCP-over-HTTP face" —
`HTTPTarget.McpMessages` just GETs `<mcp>/v1/messages/inspect`, i.e. a REST-shaped URL, not a
real MCP client. The platform DOES have MCP-over-HTTP (webd `POST /mcp` session-auth'd, and
`POST /chat/{token}/mcp` chat-token-auth'd, stateless streamable HTTP) but its tools are
`send_message`/`get_round` — get_round returns ASSISTANT frames for one turn, so the
`rest-mcp-parity` sentinel (harness-injected USER message identical via both faces) cannot be
read through it. Closing the gap honestly needs ONE of: (1) an inspect-read tool on the
chat-token MCP face (new public contract on webd, folder-bound like routd's REST inspect);
(2) a real streamable-HTTP MCP client in anteval driving send_message/get_round and a
reshaped parity case (assert the ROUND's frames match via REST + MCP instead of the raw
sentinel). Both change a public surface contract or the spec's case semantics → needs
sign-off before shipping. Until then `--mcp` stays unset and `rest-mcp-parity` fails loudly
("surface not configured") — honest, not silent; `mcp-roundtrip` is unaffected (agent-driven,
callback-checked).

- **Severity:** low (one non-smoke case ungated; documented honest gap)
- **Scope:** anteval/pkg/run/target.go McpMessages + webd chat MCP toolset + spec 5/9
- **Affected:** anteval `rest-mcp-parity` case
- **Source:** anteval/README.md "Two known gaps"; webd/chat_mcp.go toolset
- **Status:** proposed (redesign, needs sign-off)
- **Fix:**

## krons: `/hook/` proxyd route missing — webhook surface dead; route table not hot-reloaded (2026-07-11, open)

Found by anteval's `webhook-in` live case. `POST /hook/<token>` on krons falls through to the
public catch-all (302 → `/pub/krons/hook/<token>`) for EVERY token — the `proxyd_routes` table
(messages.db) had `/chat/` but no `/hook/` row. `compose.coreProxydRoutes` HAS declared
`{/hook/ → webd, public}` since e2d2b5df (2026-05-18), but proxyd seeds the table only when it
is EMPTY, so instances seeded before that date never gain new core routes — a seeded-once vs
evolving-defaults drift class (same class as MIGRATION_VERSION-style drift, no re-seed path).
Spec 5/W webhooks (`issue_webhook` → `POST /hook/<token>`) are silently dead on krons: the
agent mints a token, the POST 302s, no turn fires.

Second facet: the row was INSERTed live (2026-07-11) and a `/zzztest/` probe row confirmed the
running proxyd does NOT pick up proxyd_routes changes without a restart — despite
`proxyd/resource.go snapshot()` reading the DB per request (spec 5/8, d9796a62). Either the
deployed build predates the per-request read or something still caches; verify on next deploy
and re-run `anteval run ... --case webhook-in` (the `/hook/` row is already in place, so the
next proxyd restart activates it).

- **Severity:** medium (a documented public surface is dead on the flagship instance; silent)
- **Scope:** proxyd route seeding / krons data drift
- **Affected:** krons (and any instance seeded before 2026-05-18); spec 5/W `/hook` surface
- **Source:** anteval webhook-in live run 2026-07-11; proxyd request log `status:302 path:/hook/...`
- **Status:** open (row inserted on krons, awaits proxyd restart; re-seed/upsert path undesigned)
- **Fix:**

## Positioning claims "diff, fork, git revert" but nothing versions agent folders (2026-07-14, open)

specs/5/A (grand message, shipped) + specs/17/9 sell ownership as "files you diff,
fork, and `git revert` on your own host". Verified: the platform has NO `git init`
at group creation (onbod/container/cmd grep clean), no auto-commit; live homes show
.git in only 4/8 krons groups + marinade/atlas, and those repos have ZERO commits
(rhias: empty master, 86 dirty files) — agent/operator-initiated shells. So `git
revert` mostly has nothing to revert to. The true claims: plain files, tar = full
backup, git POSSIBLE (ant image ships git + commit skill).

- **Severity:** low (positioning honesty, no runtime impact)
- **Scope:** specs/5/A + 17/9 wording; landing/README ownership sections; the gap itself
- **Fix options:** (a) soften wording to possibility framing (landing sub already
  instructed, 2026-07-14); (b) make it TRUE: `git init` + initial commit at group
  create + a commit hook on /migrate + diary writes — small, but that IS
  specs/9/3-git-as-truth scope creep; decide there, not ad hoc.
- **Status:** open — wording fix in flight; the make-it-true decision belongs to 9/3

## Two hub.js in template — release stamps the one pages don't load (2026-07-14, open)

`template/web/pub/assets/hub.js` and `template/web/pub/arizuko/assets/hub.js` are
byte-identical twins except `ARIZUKO_VERSION`. The docs pages reference relative
`assets/hub.js` → the `arizuko/assets/` copy; the release runbook (root CLAUDE.md
"Tagging" step 3) bumps only the top-level copy. Live footer showed v0.51.0 for
seven releases (v0.51→v0.58) until caught at the v0.58.0 deploy. Same twin risk
for hub.css.

- **Severity:** low (cosmetic version stamp; but a textbook two-paths drift)
- **Scope:** template/web asset layout + release runbook
- **Fix options:** (a) collapse to ONE file (arizuko/assets is the loaded one;
  decide whether the top-level assets/ serves any non-arizuko hub page before
  deleting); (b) release script bumps both (band-aid, applied 2026-07-14 in
  CLAUDE.md wording).
- **Status:** open — versions synced at v0.58.0; collapse decision pending

## `groups` REST twin (`/v1/groups`) — RESOLVED (2026-07-30, shipped option a)

5/16's last resreg rollout item. The agent's `register_group`/`refresh_groups`
already ride `s.groupsResource()`; the spec wants the human REST twin at
`/v1/groups` too. Unlike the other folds (`web_routes`/`routes`/`tasks`/
`route_tokens` — mechanical), this one is NOT mechanical:

- `groups` is a side-effecting **FORWARDER** (Store nil): `register_group` runs
  `s.registerGroup` (git-init a group dir + insert a room route + a DB row), which
  cannot ride a resreg SQL tx (`groups_resource.go` header).
- Operator group-creation ALREADY exists as dashd's FS-managed `/dash/groups/*`
  (`container.SetupGroup`) — the FS-mounted write-discipline path that also seeds
  skills/settings/tasks (the `group_creation` law: never bare-create a group row).

So a routd `/v1/groups` create would be a SECOND group-create door that does LESS
than dashd's (no skill/settings seed) — the exact two-paths drift the platform
forbids. Options:
- **(a) Read-only `/v1/groups`** — mount only the LIST/GET face via resreg (no
  create/delete over REST); operator create stays dashd's `SetupGroup`. Satisfies
  "both faces" for the read surface without a second create door. **Recommended,
  but NOT free:** the `refresh_groups` list handler returns EVERY group folder
  unscoped (`groups_resource.go:71`) — a tier-2 operator must see only its own
  subtree, so this needs result-filtering by the caller's JWT folder first, else
  it's the `rest_listall` cross-tenant leak (see memory `rest_listall_leak`). So
  even (a) is a real change + a leak-risk review, not a one-line mount.
- **(b) Full `/v1/groups` create** — route it through `container.SetupGroup` (not
  `s.registerGroup`), so the one seed path is shared. Larger; routd would need the
  container/setup dependency it currently lacks.
- **(c) Leave groups agent-MCP-only** for create; document that operator
  group-create is dashd-only by design (write-discipline), and drop the `/v1/groups`
  REST item from 5/16.

- **Severity:** low (no runtime bug; a rollout-completeness + orthogonality call)
- **Scope:** routd groups_resource + dashd group forms + 5/16 status
- **Status:** RESOLVED 2026-07-30 (commit 210b8012) — shipped **option (a)**:
  read-only `GET /v1/groups` scoped to the caller subtree (leak closed via the
  surface==REST ownsFolder filter); CREATE stays dashd's `SetupGroup`. No second
  bare create door.

## C11 — Compose generation succeeds after daemon env writes fail (2026-07-30, open)

`Generate` logs `writeEnvFiles` failure and continues, producing compose that
references stale, partial, or missing daemon env files. For `runed`, this can
silently reuse obsolete credentials or recreate the `/login` outage class.

- **Severity:** high
- **Scope:** compose generation / daemon environment integrity
- **Affected:** every generated deployment, especially `runed`
- **Source:** compose/compose.go:241-274,668-672
- **Status:** open — returning the write error is a localized fail-loud fix; no
  redesign sign-off required
- **Fix:**

## P3 — Proxyd dispatch combines routes from two database snapshots (2026-07-30, proposed)

Route matching reads one snapshot, then proxy selection rereads the table and
rebuilds proxies. A concurrent route mutation can pair old authentication policy
with a new backend, return a false 404, and doubles database/proxy construction
work on every routed request.

- **Severity:** high
- **Scope:** proxyd request dispatch
- **Affected:** requests concurrent with `proxyd_routes` mutation
- **Source:** proxyd/main.go:124-136,625-626,654-660; proxyd/resource.go:60-91
- **Status:** proposed — passing one route/proxy snapshot through dispatch changes
  request control flow and needs sign-off
- **Fix:**

## K1 — Security-sensitive identifier generators discard entropy failures (2026-07-30, proposed)

JWT JTIs, OAuth-state nonces, signing-key IDs, run IDs, and session UUIDs are
returned after unchecked `crypto/rand.Read`. Entropy failure therefore emits
predictable or duplicate identifiers instead of aborting issuance or execution.
PKCE and refresh-token generators already return these errors correctly.

- **Severity:** medium
- **Scope:** authentication and execution identifier generation
- **Affected:** JWT lineage, OAuth state uniqueness, key rotation, run/session identity
- **Source:** auth/es256.go:140-145; auth/oauth.go:160-164;
  authd/server.go:95-101; runed/manager.go:364-376
- **Status:** proposed — correcting helper signatures and propagating errors
  crosses authd/runed contracts and needs sign-off
- **Fix:**

## A4 — ACL read errors are indistinguishable from valid empty policy (2026-07-30, proposed)

Query failures intentionally return no rows and therefore deny authorization,
but the API cannot distinguish that failure from an empty policy. User-scope and
list endpoints consequently return successful empty results. Worse, individual
scan failures are skipped, producing a partial policy that can omit a deny row
while retaining allows.

- **Severity:** high
- **Scope:** ACL storage and authorization evaluation
- **Affected:** agent authorization, login scope snapshots, ACL management surfaces
- **Source:** store/acl.go:176-310; auth/authorize.go:59-99; routd/server.go:506-514
- **Status:** proposed — authorization must remain fail-closed, but
  error-returning store contracts and 5xx management responses require
  cross-component sign-off
- **Fix:**

## Adversarial review of the 5/16+surrogate features — deferred findings (2026-07-30)

Bug-hunt on the newly-shipped features. Fixed inline: F3 (Delegate glob-widening,
cc11eace), F4 (Delegate deny/wildcard, cc11eace), F8 (tier-cap on operator REST
mint, 48c7e5ce), F9 (surrogate over-aggressive credential null, 25e0d91c), F16
(OpenAPI omitted route_tokens, 1f66d77f). Remaining, deferred:

- **F1 (fix before wiring Delegate — phase 3).** `role:operator` grant_option=1 is
  set by an UPDATE in routd/0021 + store/0075, but on a split-migrated instance
  `cmd/arizuko/migrate_split.go` copies `acl` (omitting grant_option) at :344
  AFTER routd.Open runs 0021 at :300 → the root ends up grant_option=0, so
  `auth.Delegate`'s root can delegate nothing once wired. Fresh-install seeding of
  role:operator into routd.db also needs grant_option=1. Latent today (Delegate
  unwired). Fix: seed grant_option on the operator row idempotently at the point
  Delegate is wired, after any copy.
- **F6 (verify before relying on /v1/groups for humans).** `groupsRESTAuthz` wants
  a `routes:read`/`acl:read` resource:verb scope, but human JWTs carry folder-glob
  scopes (`handleUserScopes`) that `auth/scope.go` rejects → the read twin may be
  reachable only by service/CLI tokens (folder="" → unfiltered list-all), and the
  subtree filter (`ownsFolder`) inverts for folder=""/"**". Entangled with the
  tier/scope claim model that `5/33` rewrites — resolve there.
- **F7 (pre-existing).** `DELETE /v1/route_tokens/{jid}` matches one path segment;
  hook JIDs (`hook:acme/github`) and nested `web:` JIDs contain `/` → 404. Fix:
  `{jid...}` catch-all (verify resreg path-param binding) or a query param.
- **F5 (by-design, pre-existing).** Both new REST faces (+the pre-existing acl one)
  are open when `verify==nil` (unset AUTHD_URL = local-dev open). Inert in prod
  (AUTHD_URL always set). Changing the open-when-unverified model needs its own
  sign-off.
- **Surrogate robustness (F10–F14, medium).** F10: misconfig (unknown provider / no
  creds) laundered as transient + near-expiry token dropped not stale-used. F11: one
  malformed operator TOML `os.Exit(1)`s routd (routing outage from an optional
  feature) + a shadowing datadir filename silently inherits the built-in's client
  secret. F12: `Provider.AllowedDomain` parsed, never enforced (false egress
  guarantee). F13: unvalidated `secret_key` collisions overwrite/clobber rows. F14:
  a provider removed from the registry leaves a live, unlistable, unrevocable token.

## 5/33 grant-surface flip — attempted, reverted (2026-07-30, adversary-caught)

The role-based grant flip (`600fc408`) shipped broken; reverted (`0f8d956f`). An
adversarial review (fable sub; codex dead this session) found + reproduced:

- **BUG1 (critical):** the "differential" test drove `SeedFolderGrants` — dead code
  the shipped `deriveFolderGrants` never called. "Proven safe by the differential"
  was FALSE. Lesson: an equivalence test must diff the ACTUAL production function.
- **BUG2 (critical):** `deriveFolderGrants` blind-rebound `folder:<path>` → its
  DEPTH role on every call → restriction-by-role impossible (only widening). The
  "decoupled from location" claim was false. Fix: assign the role once at
  create/invite, never re-derive per read.
- **BUG3 (high, live):** `grants.CheckAction` is last-match-wins; `store.ACLRowsFor`
  has no `ORDER BY`; the `acl_by_principal_action` index returns `folder:` before
  `role:`, so an operator DENY sorted before a role ALLOW and was masked. Confirmed
  repro. Live unmitigated consumer: `container/runner.go:553` `share_mount` RO gate
  (fed from `deriveFolderGrants`, no `db.Authorize` pairing). Fix in the correct
  flip: render denies LAST, or add `ORDER BY effect` / deny-wins to the render.
- **BUG4 (medium):** `SeedTierRoles` is INSERT-OR-IGNORE-only — a tightened tier
  bundle never revokes; stale role rows survive a redeploy. Now moot (unwired).
- **BUG5 (medium, pre-existing):** `dashd/tools_admin.go:36` renders the tool list
  from `grants.DeriveRules` directly — a second sink that drifts from the socket's
  `deriveFolderGrants` (overlay/roles). Violates one-renderer-many-sinks. Predates
  the flip; fold both onto one renderer when the flip lands.

Status: BUG1-3 un-shipped by the revert; deriveFolderGrants back to DeriveRules-base
+ expanded overlay (deny appended last = correct precedence). BUG4 moot until the
flip; BUG5 pre-existing. The correct flip must satisfy all five + the tests the
review named (differential over the REAL path; operator-deny-vs-role-verb; restrict-
via-role; multi-platform dedup; dashd/socket parity; reseed-staleness).

## 5/33 grant-flip bugs — RESOLVED (2026-07-30, re-shipped correctly)

The adversary-caught flip bugs (BUG1-5, logged above) are addressed in the
re-shipped corrected flip (`be724eed`):
- BUG1 (differential tested dead code) → RESOLVED: `TestIntegration_GrantSourceDifferential`
  drives the REAL `deriveFolderGrants` and proves it == old `DeriveRules`.
- BUG2 (blind rebind → no restriction) → RESOLVED: `hasTierRole` assign-once guard;
  `TestIntegration_RoleDecouplesDepth` proves a rebind sticks.
- BUG3 (operator deny masked) → RESOLVED: `folderGrantsFromACLOnly` renders denies
  LAST; `TestIntegration_OperatorDenyBeatsRoleAllow` confirms deny-precedence.
- BUG4 (stale role rows) → RESOLVED: `SeedTierRoles` prunes (DeleteACLPrincipal)
  then reseeds.
- BUG5 (dashd renders from DeriveRules) → NOT A LIVE BUG YET: the role bundles ==
  the tier bundles today (differential), so dashd's render matches the socket's.
  It only drifts once grants come from delegation (tier-removal steps b/d/e); fold
  dashd onto the shared render THEN.

## CEO audit — release-readiness doc gaps (2026-07-31)

Release-ready per the CEO audit after these (docs/positioning, not code):

- **Connector/ext-mcp capability is buried (biggest pitch-vs-capability gap).** The
  ext-mcp connector injection (5/13) — arizuko's actual differentiator — has no
  landing-page or concepts-page presence, only `reference/mcp.html:830-871` + one
  line in `security/index.html:44`. Fix: write `concepts/ext-mcp.html` (connectors)
  + link from landing; add `howto/add-connector.html` mirroring
  `howto/oauth-provider.html`. Half-day.
- **Tier-language doc-debt (NOT stale yet — tracked for the 5/33 rename pass).** ~27
  web pages + root docs say "tier"; today they're still factually accurate (roles ==
  the tier bundles), so NOT release-blocking. But when 5/33 phase (b)
  (AuthorizeStructural→scope) + the tier-scalar deletion land, every "tier"-language
  file must update in one coordinated pass. Files to sweep: `security/index.html`,
  `concepts/*`, `reference/mcp.html`+`grants.html`, root `SECURITY.md`/`ARCHITECTURE.md`/
  `ROUTING.md`, ant self-skills reading `ARIZUKO_TIER`. Owner: the 5/33 rename pass.
- **README/landing polish:** SDK-comparison pitch (`README.md:88-102`) should move onto
  `index.html` (done: README "What's planned" 5/17-shipped staleness fixed 2026-07-31).

## CTO audit — 5/33 partial cutover (2026-07-31)

CRITICAL C1+C2 FIXED by reverting step (d) `edd42181` (commit `3a8b8291`): `egress`/`web:publish`
off `tierOf` onto grants was NOT equivalence-preserving — `container.tierOf` (count+1) and
`auth.Resolve().Tier` (min(count,3), floored to 1) diverge below top level, so 18 live sub-groups
would gain unconstrained egress and `/root` turns silently lost web mounts (`CheckAction(["*"],
"web:publish")` is false — `matchGlob` stops `*` at `:`). Real fix (deferred, needs sign-off):
a reviewed backfill migration writing explicit per-folder `egress`/`web:publish` grants that
reproduce the `tierOf` predicate EXACTLY, then delete `tierOf` — the capability was never
derivable from the tier scalar. H1 (test fixtures) + H2/H3 (atomic seed + loud assign) FIXED.

Open (MEDIUM, deferred — none release-blocking; fail-closed or latent):

- **M3 — folderGrantsFromACLOnly drops ACL row Scope.** `routd/seed_grants.go` renders a
  scoped operator DENY (`mcp:send` on `acme/eng/secret`) as a GLOBAL `!send` for the folder.
  Fail-closed (over-denies), but wrong. The flip made the overlay the whole bundle so it bites
  more. Fix: thread scope into the rule render / evaluate scope in CheckAction.
- **M4 — `/root` gets a READ-ONLY share mount (inverted).** `container/runner.go:554`
  `CheckAction(["*"], "share_mount", {readonly:"true"})` matches (rule `*` has no params) so
  elevated gets RO while tier-1 gets RW. Pre-existing `*`-glob-vs-params gap (same as C2).
- **M5 — no un-assign path.** `store.RemoveMembershipBare` has no caller; `remove_acl` scope `**`
  only deletes `role:operator` edges. The operator-rebind rationale isn't reachable via any tool.
- **M6 — role rows are scope `**`.** Bypasses authorize.go's "tier defaults apply only at own
  folder" guard. Harmless today (every db.Authorize passes scope==socket folder) but the `**`
  scope, not the guard, bounds delegation once `AuthorizeStructural` is deleted in step (b).
- **M1/BUG5 — dashd tools display off DeriveRules.** `dashd/tools_admin.go:36`. The revert
  neutralized the "made-it-live" divergence (egress/web back on tierOf); still a second sink to
  fold when step (e) lands. **M2 self-resolved by the revert** — `SECURITY.md:210` "tier ≤ 1
  egress" is accurate again.

## T2 — chat-token MCP `get_round` reads any turn in the instance (2026-08-01, FIXED)

The `/chat/<token>/mcp` tool surface and its HTTP twin disagree on containment.
`authorizeTurn` (the HTTP path) resolves the token, then binds the turn to it via
`MessageTimestampByID(turnID, row.JID)` — a foreign `turn_id` 404s. The MCP
`get_round` tool takes `turn_id` straight to `collectRoundFrames` →
`store.TurnFrames`, whose query is `WHERE turn_id = ? AND is_bot_message = 1`
with no JID or folder predicate. Any holder of any valid route token who learns
a `turn_id` reads that turn's assistant frames verbatim, across folders and
tenants. `GetTurnResult(folder, turnID)` beside it IS folder-scoped, so status
is contained while the content is not — which is why this reads as safe.

Violates CLAUDE.md: "A handler that resolves a `jid`/`folder`/`run_id` param
MUST bind it to the caller's folder." Same class as the 5/44 REST list-all leak.

- **Severity:** high
- **Scope:** route-token containment / cross-tenant read
- **Affected:** `POST /chat/<token>/mcp`, tool `get_round` (and `get_round_status`
  — same shape, verify before fixing)
- **Source:** webd/chat_mcp.go:87,148; store/turns.go:58; contrast webd/turn.go:43
- **Status:** open — found by codex during the three-planes architecture review,
  verified against the code
- **Fix (not applied):** bind at the store boundary, not the caller — give
  `TurnFrames` a `jid`/folder argument so both faces inherit containment from one
  query, rather than adding a second check inside the MCP handler.
## B1 — agent capability token: DELETED (2026-08-01, FIXED 42657378)

The per-spawn brokered token is minted, persisted and GC'd but never reaches
anything. `jti` has zero references in `ipc/` and `routd/mcp.go`; the tool-call
gate identifies the caller from the socket path
(`routd/mcp.go:559`, `callerSub := "folder:" + t.folder`).

Two findings make "deliver the JWS to the agent and authorize from it" the wrong
way to close the gap:

1. **The token cannot name the human.** `runed/broker.go:64` hardcodes
   `typ:"downscoped"`, which forces authd's non-impersonation branch
   (`authd/http.go:~225`), and `Downscope` sets `Sub: parent.Sub`
   (`authd/server.go:233`) — the runed service principal. The `"sub"` field
   runed sends is silently ignored; `authd/tokens_test.go:82` already asserts
   this ("forced to caller"). So every agent token in every folder for every
   user carries `sub=service:runed`. Authorizing from the token's sub would
   collapse all tenants onto one service identity. Spec `5/P` § brokering says
   runed's scope MUST include `tokens:mint` for issuer-mint, but the broker's
   hardcoded `typ` makes that path unreachable — spec and code disagree.
2. **Delivering a bearer to the agent is a net loss.** `container.Input` has no
   token field and `runed/docker.go` never reads `RunSpec.Token` — spec `5/P`
   dropped delivery on 2026-07-11 on purpose. Re-adding it hands a
   prompt-injectable agent (Bash + persistent `$HOME` + egress) a credential it
   can write to disk and replay after the turn: verification enforces no `aud`
   (`auth/jwks.go`), routd's REST surface authorizes from the token's baked
   scope rather than live ACL (`routd/server.go` `authz`), and `mcp_tokens` is
   GC state that no verifier consults, so there is no revocation. Today the
   agent's authority is the mounted socket and dies with the turn.

**Proposal (tokenless).** routd already holds the identity: `turnMCP.trigger`.
Render the turn principal once and use it at the gate, gated by a live `assume`
check — `Authorize(trigger, "assume", folder)` — with the folder's grant rules
still bounding the tool. Failure denies; never fall back to `folder:<path>`.
Turn-less callers (timed/system) keep a folder/service principal and perform no
assumption, which is the rule `dispatch.go:494` already encodes.

Not shipped: this changes the authorization hot path's identity for every
deployment (users with existing deny rows would newly constrain their group's
agent), so it needs sign-off. Landed now as characterization only: the `assume`
lattice position is pinned by `auth/assume_lattice_test.go` (`*` covers
`assume`, `admin` does NOT — impersonation is not resource power) and the
live-revocation property by
`routd/revocation_live_test.go`. Decide: connect it tokenlessly, or delete
`Broker`/`mcp_tokens` as dead weight.

## B2 — Turn principal rendered three ways in routd (2026-08-01, FIXED fc4a5ba2)

One "who is this turn acting as" rule, three renderings — the
one-renderer-many-sinks violation the root `CLAUDE.md` forbids:

- `routd/dispatch.go:494` — spawn/broker identity: trigger, else `service:routd`.
- `routd/mcp.go:487` — connector-secret identity: same rule, retyped.
- `routd/mcp.go:559` — the tool-call gate: `folder:<folder>`, ignoring the
  trigger entirely.

So the secrets the agent gets are resolved for the user while the authorization
decision is made for the folder. Fix is one exported renderer used by all three
sites; the gate's switch to a user principal is the sign-off item in B1.

## B3 — `grantACLTx` turns any `**`-scoped grant into full operator membership (2026-08-01, FIXED)

`routd/acl_resource.go` `grantACLTx` checks `scope == "**"` BEFORE looking at
`action` and routes it to `addMembershipTx(principal, "role:operator")`. The
action is discarded, so granting a deliberately narrow action at tree scope
(`interact` on `**`, or a future `assume` on `**`) silently confers the full
operator role — `IsOperator` reads exactly that membership edge, and operator is
the gate for `/root` elevation. Fix: keep the membership shortcut only for the
grant shape it was written for (`admin`/`*`), and store any other action as a
normal wildcard-scope acl row.

## O1 — onboarding rows stranded in an unknown status (2026-08-01, FIXED-loud fc4a5ba2)

Live data carries `onboarding.status = 'pending'` (sloth 1 row, marinade 1
row) but no code in `onbod/` writes or reads that value — the current status
set is `awaiting_message` / `token_used` / `queued` / `approved`. Every query
that advances a row selects one of those: `promptUnprompted` takes
`status='awaiting_message' AND prompted_at IS NULL`, `admitFromQueue` takes
`status='queued'`. A `pending` row is therefore invisible to the whole
pipeline — it is never prompted, never queued, never admitted, and never
reported. The user who triggered it sees nothing and can never onboard from
that JID, because the row's `jid` PRIMARY KEY blocks a fresh insert.

Same class as the gates-configured-but-none-match dead end: a row reaches a
state the pipeline has no transition out of, and nothing is loud about it.

- **Severity:** medium (2 known users stranded; silent)
- **Scope:** onbod onboarding state machine
- **Affected:** sloth, marinade
- **Source:** `onbod/main.go` (no writer/reader for `pending`); found while
  verifying that the `resetRow` recovery predicate was unreachable
- **Status:** open — data-level fix is the operator's; the code-level fix is
  that the state machine should reject or migrate an unknown status loudly
  rather than ignoring the row

## W1 — world creator's grant cannot reach their own subgroups (2026-08-01, FIXED ebb9991f)

`createWorldTx` grants the creator `acl(sub, 'admin', <folder>)` — a BARE
folder scope (`onbod/main.go`). `auth/acl.go matchSegments` requires an exact
segment count, so that row authorizes `acme` and nothing beneath it: the
creator cannot route a JID into, or administer, their own subgroup. The
operator's stated model ("route the JID into that world or one of its
subgroups") is unreachable today by any shipped path.

The one-line fix — scope `<folder>/**` — was attempted and **reverted**,
because the grant has an exact-match reader: `onbod/main.go:508` lists a
user's worlds with `JOIN acl a ON a.scope = g.folder`. Widening the scope
string silently empties that join, so the creator sees no worlds at all.
Three tests pin the current shape (`TestOnboardingFlow`,
`TestCreateWorldValidUsername`, `TestSplitCreateWorldWritesCrossToRoutd`).

Fixing this means changing every reader from string equality to pattern
matching, which SQL joins cannot express. Storing the grant twice (bare +
subtree) is ruled out by CLAUDE.md — a second parallel path — so the reader
must fetch and filter in Go.

**Second attempt, 2026-08-01, also reverted.** Rewrote the reader as
`firstAdminFolder`: drain `SELECT folder FROM groups`, close the cursor, then
ask `auth.Authorize` per folder — reusing the single evaluator rather than
writing a second matcher, which is the right shape. It hangs. With valid
fixtures the `Authorize` call blocks on onbod's test harness, whose `testDB`
is `sql.Open("sqlite", ":memory:")`: each pooled connection there gets its own
empty database, so a second query on the same handle stalls rather than
reading the rows just inserted. Draining the cursor first did not fix it.

Before a third attempt, settle: does `firstAdminFolder` take an explicit
`*store.Store` built by the caller (so onbod's real file-backed handle is used
and the harness can inject one), and does `onbod`'s `testDB` need
`SetMaxOpenConns(1)` or a file-backed temp DB? The evaluator-based reader is
the correct design; the blocker is connection lifetime, not matching.

Note also that `grant_option=1` — needed so an owner can invite admins —
cannot be written yet: migration 0022 adds the column but krons' live `acl`
table does not have it (`PRAGMA table_info(acl)`), so writing it would fail
world creation with "no such column". That half waits on 5/33 reaching the
instances.

- **Severity:** medium (blocks the world-owner capability, no data at risk)
- **Scope:** onbod world creation / ACL scope shape
- **Source:** `onbod/main.go` createWorldTx + `onbod/main.go:508`; `auth/acl.go` matchSegments
- **Status:** proposed — needs the reader migration designed before the grant changes

## P1 — pairing unreachable outside a route miss (2026-08-01, FIXED by 5/31)

`onbod.linkJID` wrote the correct `acl_membership(jid → sub)` edge, but the only
path to it was a route MISS whose success path creates a world. `specs/5/31`
shipped the extraction: `route_tokens kind='pair'`, `issue_pairing_link`,
`GET/POST /pair/<token>`, `unpair`. Token and edge are both in routd.db, so the
cross-DB ownership problem this entry raised does not arise.

## P1b — fold onbod's greeting onto pairing (2026-08-04, PROPOSED — redesign, needs sign-off; UNBLOCKED 2026-08-06)

**Update 2026-08-06 — blocker 4 is resolved and shipped (`df99e158`).** Option 1
was chosen: webd's pair success page carries the user on to `/onboard`
(`webd/pair.go` `pairContinueURL`). This does not give webd the onboarding
awareness `5/31` rejects — webd names a destination and states no condition.
`/onboard` needs no help: step 6 keys on `unroutedJID` → `membershipJIDs`, which
reads `acl_membership` with **no `added_by` filter**, so an edge `RedeemPairing`
wrote is found exactly as one `linkJID` wrote. The picker was already generic
over who wrote the edge; only the user's route to it was missing.

No new parameter carries it. The flow's `auth_return` cookie is a
*pre-authentication resume* pointer (proxyd's `requireAuth` writes it; authd's
`consumeReturn` reads, validates and clears it during OAuth), so it is already
spent on `/pair/{token}` itself by the time the success page renders — a
different question, asked at a different time. Reusing it would give one cookie
two meanings; a `?next=` would be a parallel mechanism.

Inert until the rest of the fold lands: `pairingTargetFolder` refuses an unrouted
JID, so every pairing mintable today ends on a routed chat where `/onboard` just
shows the user their worlds.

**Blockers 1–3 remain and this entry stays PROPOSED.** The chat-async admission
flow (`IssuePairingLink` + nullable `owner_folder` + the poll observer +
`status='refused'`) is designed in `5/31` and unimplemented; it still needs
sign-off before code moves.

**The lockout half stays deliberately unshipped.** `promptUnprompted`'s `WHERE
prompted_at IS NULL` is still a permanent lockout, and `5/31`'s replacement
cooldown is bound to `store.PairingTTL` (10 min) — which is only a bounded policy
*because* the fold's link IS a pairing token. Today's greeting link is onbod's
own 24h `token_expires`, so a `PairingTTL` cooldown applied now would re-greet
every 10 minutes while a live link sits unused: exactly the spam vector this
entry exists to police. It becomes a two-line change the moment blocker 1 lands,
and not before.


The reverse direction of P1 has NOT shipped: onbod still mints
`onboarding.token`, posts `/onboard?token=…`, carries the JID across OAuth in
the unsigned `onboard_jid` cookie, and writes its own edge stamped
`added_by='linkJID'` — a second writer into the same table.

Investigated 2026-08-04 as a deletion; it is a **redesign of onboarding's
control flow**, not a swap. Three blockers, each an addition:

1. **The shipped mint structurally refuses the JIDs onbod greets.**
   `pairingTargetFolder` (`routd/route_tokens_resource.go`) 403s when
   `DefaultFolderForJID(jid) == ""`, and `router.ResolveRoute` has no
   catch-all. onbod's row is inserted only in routd's route-MISS branch
   (`routd/loop.go:557`), so every greeted JID is unrouted by construction.
   onbod would need its own exported `store` mint with a different gate — a
   second minting rule, replacing the second edge writer it removes.
2. **10-minute TTL vs a once-only greeting.** `store.PairingTTL` is 10 min;
   the greeting link is 24 h and `promptUnprompted` prompts each JID exactly
   once (`WHERE prompted_at IS NULL`). A user who clicks late is permanently
   locked out — the `jid` PRIMARY KEY blocks a fresh onboarding row. Needs a
   re-prompt policy that does not spam the chat.
3. **`linkJID` is also the admission advance.** It queues (gate match),
   approves (no gates), or refuses to the user's face (`errLinkRefused` → 403
   via `writeLinkErr`), and emits the `onboarding.queue`/`.approve` audit
   rows. `RedeemPairing` writes only the edge and webd's success page is
   terminal, so the whole admission half loses its trigger and its way to
   report the outcome. Needs a new trigger (a tick scanning for newly-paired
   `awaiting_message` rows, plus a chat follow-up) — the flow moves from
   browser-continuity to chat-async.

Live evidence that the stated harm is not yet biting: **zero
`added_by='linkJID'` rows** on krons, sloth and marinade
(`SELECT added_by, COUNT(*) FROM acl_membership GROUP BY 1` →
`cli:kron-all`, `cli:kron-discord`, `migration-0053`, `admin`,
`cli:ondra-grant` only). So nothing in production is currently out of reach of
`unpair`.

Restamping `linkJID`'s edge to `'pairing'` is NOT a safe one-liner either: it
would let `unpair` delete an edge while `onboarding.user_sub` still names that
sub and the row still reads approved/queued, leaving the admission row stale.

**Update 2026-08-06 — a FOURTH blocker, and it invalidates the designed fold.**
`5/31` answered blockers 1–3 in its "Onboarding — the fold, designed" section.
That design was written against an onbod that AUTO-PICKED a world on claim, and
`5/18` step 6 landed underneath it (`d9e57288`, 2026-08-04) replacing the
auto-pick with a **browser choice**: `handleDashboard` (`onbod/main.go:622`)
asks `unroutedJID` → `adminFolders`, then renders `renderWorldPicker`
(`onbod/main.go:1261`, a form POSTing to `handleAddRoute`) for ≥2 worlds,
`insertRoute` for exactly one, `renderNoWorld` for none. `5/18` is explicit:
"empty set is a terminal page — **never auto-pick**".

Pairing writes the edge; it does not route the JID. So the greeted user's chat
is STILL silent after redemption — `5/18`'s opening problem — until step 6
runs. Today they reach it because `handleTokenLanding` sets
`auth_return=/onboard` (`onbod/main.go:474`) and redirects there. Under the
fold the return target is webd's `/pair/{token}`, whose success page is
terminal (`webd/pair.go:87`), and nothing carries the user to `/onboard`.
`5/31`'s own flow (a) confirms it by omission: it ends at "queued / approved /
the refusal" and never routes the JID.

The poll observer cannot substitute — the multi-world case is a form a human
fills in and a tick has no browser. Closing this needs one of three, each a
`5/18` decision: (1) webd's pair page redirects into `/onboard`, giving webd
onboarding awareness that `5/31` rejects twice; (2) the observer chat-sends a
SECOND link to `/onboard`, a two-link flow specified nowhere; (3) the observer
auto-picks, reverting step 6.

Verified stale pointers in `5/31`'s fold section, corrected in the same pass:
`firstAdminFolder` no longer exists (it became `adminFolders`,
`onbod/main.go:941`), `onbod/main.go:585` is `linkJID` not the auto-route, and
`onboarding.token` / `idx_onboarding_token` were replaced by `token_ref` /
`idx_onboarding_token_ref` (`onbod/migrations/0004`).

Also blocked, and worth naming because it looks independent and is not:
`promptUnprompted`'s permanent lockout (blocker 2). Swapping `WHERE prompted_at
IS NULL` for a cooldown is a two-line change touching nothing else, but "does
not spam the chat" is the policy this entry says is missing — an unbounded
re-greet on a 24h timer is a spam vector, and a bounded one is the design call.

- **Severity:** low today (two writers, but one has written nothing)
- **Source:** `onbod/main.go` linkJID/handleTokenLanding/handleDashboard;
  `specs/5/31-identity-pairing.md` §"Onboarding — the fold, designed" +
  §"What step 6 broke"; `specs/5/18-onboarding-model.md` step 6
- **Status:** proposed — needs sign-off on the chat-async admission flow AND on
  where step 6's picker lives after redemption moves to webd, before any code
  moves

**Adjacent, checked and filed here rather than separately:** two stale lines in
`5/18`, out of this entry's scope to edit — `specs/5/index.md:92` says "Steps
1–6 ship … step 7's explicit, attributed route act does not", but `5/18` itself
records step 7 SHIPPED (`120d5461`); and `5/18`'s schema listing still names the
`token` column that `onbod/migrations/0004` replaced with `token_ref`.

## P2 — three identity-link mechanisms; authd owns two of them (2026-08-02, decided)

"One human, many logins" is implemented three times. Operator decision
2026-08-02: **collapse to one, exposed by authd in the system's canonical way**
(a resreg resource — REST + MCP from one handler, per root CLAUDE.md), with
proxyd and dashd consuming that surface rather than reading a table.

| mechanism | DB | writer | read surface | live rows |
|---|---|---|---|---|
| `identities` + `identity_claims` | auth.db | `store/identities.go:33` | **yes** — `GET /v1/identities/{sub}` (`authd/http.go:384`), consumed by routd's `inspect_identity` | 0 everywhere |
| `auth_users(user_id)` + `oauth_identities` | auth.db | `authd/store.go:122` (the live OAuth login path) | **none** | 1 (marinade) |
| `auth_users.linked_to_sub` | **routd.db** | none (`LinkSubToCanonical` has no non-test caller) | read by `proxyd/main.go:885`, `dashd/profile.go:81` | 0 everywhere |

So authd holds one model that is exposed but never populated, and one that is
populated but never exposed; routd holds a third that is neither. The two
readers in row 3 therefore always get the empty answer — proxyd canonicalises
to the input sub unchanged, dashd renders an empty linked-accounts list.

**Trap worth knowing**: `auth_users` exists in BOTH routd.db and auth.db with
DIFFERENT schemas — `(sub, username, hash, name, created_at, linked_to_sub)`
versus `(user_id, name, created_at)`. Same name, two databases, two meanings.

### Target

1. `oauth_identities` (+ `auth_users(user_id)`) becomes the single model: it has
   the live writer and a first-class person entity, so no sub is "elected"
   canonical and losing one provider account cannot orphan the rest.
   `UNIQUE(provider, provider_sub)` already prevents one login mapping to two
   people.
2. Re-point `GET /v1/identities/{sub}` at it and register it as a resreg
   resource so the MCP twin is derived rather than hand-rolled. The endpoint
   already exists as the seed; today it reads the empty model.
3. proxyd and dashd consume that surface. Note the accepted cost: this puts an
   HTTP hop on proxyd's per-request cookie path, so authd being unreachable
   degrades session canonicalisation — decide the failure mode (fail closed, or
   fall through to the raw sub) as part of the work.
4. Delete `identities`, `identity_claims`, `store/identities.go`,
   `auth_users.linked_to_sub`, `CanonicalSub`, `LinkSubToCanonical`,
   `SubsLinkedTo`, and `dashd`'s `linkedSubs` — additive DROP migrations in
   both schemas.

Deleting before step 2 lands would remove `inspect_identity`'s backing store,
so the order matters.

- **Severity:** medium (account linking non-functional on two surfaces)
- **Source:** `store/identities.go`; `authd/store.go:122`; `authd/http.go:384`;
  `store/auth.go:60,73`; `proxyd/main.go:885`; `dashd/profile.go:81`
### Step 1 SHIPPED (90e46d62)

`GET /v1/identities/{sub}` now reads `auth_users`+`oauth_identities`. The
endpoint had been answering `{identity:null,subs:[]}` for every sub, so routd's
`inspect_identity` reported everyone as unclaimed.

### Step 2 is NOT a deletion — correction to this entry

The old model has two more live surfaces, found while attempting the delete:

- **an operator CLI** — `arizuko identity list/create/link/unlink`
  (`cmd/arizuko/main.go:815-870`), the only way to bind a platform sub to a
  person by hand;
- **`inspect_identity`** (`ipc/inspect.go:112`) reads
  `GetIdentityForSub` straight from the DB, NOT through authd's endpoint — so
  re-pointing the endpoint did not re-point the tool.

So the two models are not redundant in the way this entry first claimed. They
differ by WRITER: `identities`/`identity_claims` is manual and
operator-driven and accepts any sub (`tg:42`); `oauth_identities` is written
only by the OAuth login path (`upsertOAuthUser`) and has no manual entry point.
Deleting the first would remove the operator's ability to link a platform
identity by hand — which is the pairing capability `5/31` is about.

**Revised step 2**: give `oauth_identities` a manual writer, migrate the CLI and
`inspect_identity` onto it, THEN delete `identities`, `identity_claims`,
`store/identities.go`, `linked_to_sub` + accessors, and dashd's `linkedSubs`.
`provider`/`provider_sub` already holds a platform sub fine — the endpoint test
seeds `tg:42` that way.

### Step 2 SHIPPED as a deletion (2026-08-04, fcd845cb + 54125cbd)

The revision above was itself wrong on one point: `inspect_identity` had
already moved. `routd/identity.go` resolves through authd's
`GET /v1/identities/{sub}` (`b6aabfd2`), which step 1 re-pointed at
`auth_users` + `oauth_identities` — so the tool needed no work.

That left `arizuko identity link|unlink` as the only writer, filling a table no
reader ever looked at, which is not a capability to preserve. And the manual
bind it nominally offered is what `5/31` pairing now does with consent and an
inverse. So step 2 landed as a straight delete, no manual writer added:
`store/identities.go`, the CLI subcommand, `linked_to_sub` + `CanonicalSub` +
`LinkSubToCanonical` + `LinkedSubs` + `AuthUserBySub`, dashd's `linkedSubs`,
and migrate-split's copy of the two tables. Dropped by authd `0006` and routd
`0027`.

**Step 3 is what remains** and is an addition, not a deletion: dashd's
"Linked accounts" section was removed rather than left rendering an
always-empty table, so showing a real list means dashd consuming authd's
`GET /v1/identities/{sub}` — including the failure-mode decision this entry
already flagged. proxyd no longer reads `CanonicalSub` at all.

- **Status:** steps 1 + 2 shipped; step 3 (dashd consumes authd's endpoint) open

## P3b — rejected minimization phases, recorded so they are not retried (2026-08-01, closed)

Two phases from the 2026-08-01 minimization plan were rejected on review and
should NOT be reattempted as written:

- **Fold `onboarding.token` into `invites`** — forbidden by `specs/5/31`: an
  invite carries a SCOPE and is issued by a granter; a pairing nonce carries a
  JID and proves chat control. Merging them yields a table whose redemption
  semantics depend on which columns are NULL, with a bug class where a pairing
  token grants. If they ever share storage it must be via an explicit `kind`
  column, never NULL-inference.
- **Drop `onboarding.status` as derivable from its timestamps** — it is not
  derivable, and the fail-loud work (`c6bfe2b0`) added a refusal outcome that
  makes it less so.

- **Status:** closed — recorded to prevent a repeat, no action wanted

## L1 — role membership read as a pairing claim (2026-08-02, FIXED 4e831f10)

`acl_membership` carries two edge kinds — pairing (`jid → canonical sub`) and
role membership (`jid → role:operator`) — and a JID may legitimately hold both.
`linkJID`'s claim check selected any parent, so `QueryRow` could return the role
row and refuse a valid re-pair with "already linked to another account", naming
a role as the other account.

Order-dependent, which is why it never surfaced: the `(child, parent)` index
sorts by parent, so a `google:…` pairing happens to precede `role:operator` and
hides it. sloth carries exactly this shape —
`telegram:user/1183985669` paired to a google sub AND granted operator directly.

- **Fix:** the claim check excludes `parent LIKE 'role:%'`. Test uses a sub
  sorting after `role:` so the ordering is deterministic; falsified against the
  unfiltered query.

## S1 — retired `CHANNEL_SECRET` still documented as the live adapter credential (2026-08-02, fixed; helm split to S1b)

`CHANNEL_SECRET` is gone from the code — `chanlib.RouterClient.svcToken` carries
a `service:<daemon>` ES256 JWT on every authenticated routd call, and five call
sites carry the comment "no `CHANNEL_SECRET` remains". Three places still tell a
reader otherwise:

- Root `CLAUDE.md ## Config` lists it as an anchor env var an operator must set.
- `template/web/pub/arizuko/security/index.html` states adapter ingress is
  "authenticated by `Authorization: Bearer $CHANNEL_SECRET`" and lists it under
  operator anchors — this is the **live** security page on krons.
- Two legacy product setup pages emit it in their `.env` sample.

An operator following the security page sets a variable nothing reads, and
believes ingress is guarded by a shared secret when it is guarded by a rotating
signed token pinned to a verified `sub`. Spec-side copies were corrected with
the `5/34` move (`6ae3e954`); these three were left because they are a docs
concern, not a spec one.

- **Severity:** medium (no exploit — the real gate is stronger than documented)
- **Scope:** operator docs accuracy
- **Affected:** `CLAUDE.md`, `template/web/pub/arizuko/security/index.html`,
  `template/web/pub/arizuko/legacy/products/{reality,slack-team}/setup.html`
- **Fix:** replace with `AUTHD_SERVICE_KEY` (the one surviving symmetric
  bootstrap secret) and describe the ES256 exchange; redeploy `/pub`.
- **Status:** docs fixed 2026-08-05; **helm split out to S1b, needs sign-off**.
  - `CLAUDE.md` + `security/index.html` — the two live pages the entry named
    were already done in `d81cc2d2` (2026-08-04).
  - The sweep the entry did not do found the var alive on **17 more live
    pages**, including the one an operator is likeliest to follow:
    `reference/env.html` documented `CHANNEL_SECRET` as "Required in
    production" plus TEN per-daemon `<DAEMON>_CHANNEL_SECRET` overrides, all
    dead (`grep -rn '_CHANNEL_SECRET' --include='*.go'` → 0). Those eleven
    `env-entry` blocks are deleted, not reworded — there is no surviving var to
    redirect them to, and `AUTHD_SERVICE_KEY` already had its own entry.
  - The wording fix is not a rename. The pages described a **symmetric HMAC**
    ("routd verifies inbound HMACs, the adapter verifies outbound HMACs"). Both
    directions are ES256 bearers verified against authd's JWKS: routd requires a
    `service:` subject, and the adapter's `chanlib.authGate` (`chanlib/auth.go:67`)
    requires exactly `service:routd`. Symmetric, but no shared secret exists to
    verify — so the sentence had to be rewritten, not have a token swapped.
  - `howto/index.html` and `reference/cli.html` claimed `arizuko create`
    generates a `CHANNEL_SECRET` into `.env`. It writes `AUTH_SECRET` and
    `SECRETS_KEY` (`cmd/arizuko/main.go:270-284`) and never wrote this one.
  - `template/web/pub/arizuko/legacy/**` deliberately untouched (~60 hits). The
    changelog page calls it "the previous docs are preserved at /legacy/" — an
    archive that also documents `gated` and `emaid`. Editing one var inside a
    snapshot of a retired topology makes it neither the old docs nor the new.
  - NOT deployed: `template/web/pub/` is source-of-truth, the `/pub` deploy is
    the operator's step (`template/web/CLAUDE.md`).

## S1b — the helm chart deploys the deleted `gated` daemon (2026-08-05, PROPOSED — redesign, needs sign-off)

Split out of S1, whose fix ("wire `AUTHD_SERVICE_KEY` instead of the dead
`CHANNEL_SECRET`") cannot be applied to `deploy/helm` as a config change. The
chart is pre-split and cannot deploy arizuko at all:

- `templates/gated.yaml` runs `command: ["gated"]`. `gated` was deleted in
  v0.50.0 (`45e4ab0d` + `24d57a3e`); it is not in the Makefile's `DAEMONS` and
  there is no `gated/` directory.
- `_helpers.tpl:89-97` points every adapter's `ROUTER_URL` at
  `<release>-gated:8080`, so the whole chart's routing target does not exist.
- There is no `authd`, `routd`, or `runed` template, and **zero** occurrences of
  `AUTHD_URL` / `AUTHD_SERVICE_KEY` / `AUTHD_SERVICE_NAME` anywhere in the chart.

That last point is why this is not S1's one-line swap. Wiring
`AUTHD_SERVICE_KEY` into `adapters.yaml` would inject a credential with no
`authd` to exchange it at and no `routd` to present it to — every adapter would
still `os.Exit(1)` on the required-env check (`chanlib/run.go:51-55`), but the
chart would now *look* current. That is worse than the honest breakage.

- **Severity:** medium (k8s install path is dead; no runtime effect on the three
  compose instances)
- **Scope:** deploy/helm, and `template/web/pub/arizuko/howto/kubernetes.html`
  which documents it (left at `CHANNEL_SECRET` on purpose — the page and the
  chart must move together)
- **Affected:** anyone installing via helm
- **Source:** deploy/helm/arizuko/templates/gated.yaml:57;
  deploy/helm/arizuko/templates/_helpers.tpl:89-97;
  deploy/helm/arizuko/values.yaml:119; deploy/helm/arizuko/templates/secret.yaml:13;
  deploy/helm/arizuko/templates/adapters.yaml:68-75
- **Status:** PROPOSED — redesign, needs sign-off. The real fix is a split-topology
  chart (authd/routd/runed deployments + services, repoint `routerService`, add
  the `AUTHD_*` env, drop `gated.yaml` and `secrets.channelSecret`), or deleting
  the chart if helm is not a supported install path. Do not half-wire it.

## O2 — two adapters down on platform credentials, not code (2026-08-02, open)

Fleet eval + redeploy of all three instances (2026-08-02 15:17–15:20). All units
active, all daemons healthy, and the agent path is verified end-to-end on krons —
a real bot row landed in `routd.db` at `2026-08-02T15:17:49Z`
(`telegram:user/1112184352`), 35s after restart. Two adapters are nonetheless
dead, both on expired platform credentials, both pre-existing and unaffected by
the deploy:

- **krons `whapd`** — `Restarting`, ~65-70s cadence, 30+ restarts observed.
  Logs: `"session invalidated, delete auth dir and re-pair","code":401`. routd
  auto-deregisters the `whatsapp` channel every ~3 failures. Needs the
  `--pair <phone>` pairing-code re-auth, not a restart. A pairing code was
  issued 2026-05-21 and never redeemed, so this has been down ~10 weeks.
- **marinade `emaid`** — `health: starting` indefinitely, `RestartCount=0` (it
  does not crash-loop, so it never gets flagged as failing). Logs:
  `imap idle error … [AUTHENTICATIONFAILED] Invalid credentials` every ~60s;
  routd deregisters the `email` channel every ~3 min. Needs the correct IMAP
  password in secrets.

Both are operator credential fixes, not code fixes — recorded so they stop being
rediscovered by each fleet check. The second one is also a monitoring gap worth
noting: an adapter stuck in `health: starting` forever looks less alarming in
`docker ps` than a crash loop, while being equally down.

- **Severity:** medium (two channels dead; no data loss, no security exposure)
- **Scope:** operator credentials
- **Affected:** `arizuko_whapd_krons`, `arizuko_emaid_marinade`
- **Fix:** re-pair whapd per `reference_whapd_pairing`; set marinade's IMAP
  password. Neither requires a deploy.

## D6 — `make test-all` is red: the services-hub link test cannot pass as written (2026-08-02, open)

`tests/dashd-playwright/tests/services.spec.ts:32` ("built tiles link to their
control plane") fails, so `make test-all` exits non-zero. Confirmed pre-existing
— it reproduces on a clean tree, unrelated to the modernizer sweep (`a5645e4d`).

Two independent mismatches, and the second makes the test unsatisfiable:

1. **Stale list.** The spec asserts `BUILT = ['onbod','timed']` and puts
   `routd`/`runed` in `UNBUILT`. `dashd/services.go:31-38` says the opposite —
   `routd` and `runed` carry `Built: true`, `onbod` and `timed` `false`.
2. **Unreachable assertion.** `dashd/services.go:142` renders the link only
   when `s.Built && statuses[i] != statusUnknown`. The spec's own header comment
   states that in tests no daemon hostname resolves, so every tile is
   `unknown` — under its own stated conditions **no tile can ever render a
   link**. Fixing only the BUILT list would leave it red.

This also explains why the sibling test at :42 ("unbuilt tiles render name as
text") passes: it passes trivially, because nothing links.

Behind it is a real design question, hence proposal not fix: should a tile for a
daemon that is built but currently unreachable link to its control plane? The
current answer is no — which is arguably backwards, since an unreachable daemon
is exactly the one an operator wants to click through to. Changing that is a
dashd UX decision and needs sign-off.

- **Severity:** medium (CI red; masks any real regression in the same suite)
- **Scope:** dashd services hub + its playwright contract
- **Affected:** `tests/dashd-playwright/tests/services.spec.ts:32`,
  `dashd/services.go:31-38,142`
- **Fix:** decide the link-when-unreachable question first, then align the test's
  BUILT list with `services.go` and make the probe condition reachable in tests
  (stub a healthy probe, or drop the status gate).

## D7 — `make test-e2e` verifies nothing: no `webd` test matches `-run E2E` (2026-08-02, open)

The target is `go test ./webd/... -count=1 -run E2E -timeout 300s`
(`Makefile:73`), but no test function in `webd` has a name matching `E2E`, so it
reports `ok ... [no tests to run]` and exits 0. The repo's actual `E2E`-named
tests live elsewhere — `crackbox/test/egress_e2e_test.go` and
`teled/e2e_test.go` — neither of which the target selects.

This is a green light that never checks anything, and it is wired into release
discipline: root `CLAUDE.md` and `specs/5/36` both say "run `make test-e2e`
before tagging". Every such run has passed vacuously. It surfaced while shipping
`specs/5/36` step 4, whose `strings.CutLast` conversions touch folder-path and
reply-ID parsing — exactly the routing-adjacent code the target claims to cover.
Those were verified by running the touched packages' full suites directly.

Pre-existing and unrelated to the Go 1.27 work: `Makefile:71-74` is unchanged
since well before it.

- **Severity:** medium (false confidence at the release gate, not a live defect)
- **Scope:** release verification
- **Affected:** `Makefile:71-74`; cited by `CLAUDE.md` "Build & Test" and
  `specs/5/36-go-1.27-adoption.md`
- **Fix:** decide what the release e2e gate is meant to cover. Either write the
  `webd` route-token E2E tests the target's comment describes ("release-only
  webd slink E2E tests"), or repoint it at the suites that exist. A target that
  selects zero tests must fail, not pass.

## ✅ FIXED 2026-08-04 Y1 — `5/8` is not implementable as specced: three undecided questions (2026-08-02, proposal)

**Fix:** `83cccc78`. Built exactly the "DECIDED (2026-08-02, user)" resolution
below: `resreg.Resource.DB` carries the SUBSYSTEM (`routd`|`onbod`, a logical
tag, not a filesystem path) as content-hash CAS replaces `config_meta`
per-subsystem (`resreg.Checksum`, no table); `cmd/arizuko/apply.go`'s four
`store.Open(messages.db)` sites are repointed to `store.OpenRoutd`/`OpenOnbod`,
proven end-to-end by `TestCLI_ExportApply_RealFiles` (drives the actual
`cmdExport`/`cmdApply` against real on-disk `routd.db`/`onbod.db` files, not a
frozen schema). `proxyd_routes` stays `routd`-owned per the resolved Q1 (no
`proxyd.db` is ever opened by this code). One operational item NOT done here
(out of scope — never touch a live instance/`.db`): deleting each live
instance's empty `proxyd.db` file remains an operator cleanup task.

`specs/5/8-yaml-manifests.md` is marked `partial` with the remaining work
described as a mechanical repoint of `cmd/arizuko/apply.go` from the frozen
`messages.db` to each resource's split owner DB. A codex readiness review, with
its load-bearing claims verified against live krons, says otherwise. Three
decisions are missing; an implementer would have to invent them.

**1. `proxyd_routes` has no unambiguous owner.** `5/16` (canonical, duplicated
into `5/8`) maps it to `proxyd.db`. On krons, `/srv/data/arizuko_krons/store/`
does contain a `proxyd.db` file — but it is **empty**, holding neither
`proxyd_routes` nor any table, while `proxyd_routes` actually lives in
`routd.db` (`routd/migrations/0015-auth-sessions-proxyd-routes.sql`, read by
`proxyd/main.go:1000`). An empty `proxyd.db` makes this worse than a missing
one: an implementer following the spec writes to a DB nothing reads, and the CLI
silently applies nothing. Decide — follow deployed reality (owner = `routd.db`)
or move the table.

**2. `config_meta` does not exist in any owner DB.** `5/8:316` states it is "a
single-row table **in each owner DB**". Verified across all six krons DBs:
`config_meta` exists **only** in the retired `messages.db`. So the entire
optimistic-locking envelope the spec builds on has no home in the split layout,
and no split-daemon writer bumps a per-owner version today.

**3. Partial-apply recovery is self-contradictory.** Per-owner transactions are
clear, but after owner A commits, its version moves N → N+1; re-running the same
manifest stamped N now CAS-fails on A. The spec calls re-run "idempotent", which
it cannot be. Undecided: whether identical content bypasses a stale CAS, whether
committed owners are skipped, whether `--force`/re-export is the recovery path,
plus owner ordering, mixed-success exit status, and what `plan` reports when one
owner DB is ahead.

Also noted: `resreg.Resource` carries `Table` but no owner field
(`resreg/resreg.go:155`), so ownership has nowhere to live as data — it is prose
in two specs. And `get` emits no version while strict `apply` rejects a touched
owner lacking one, which breaks the promised get→apply round trip.

- **Severity:** medium (blocks `5/8`; the commands stay inert meanwhile)
- **Scope:** manifest CLI ownership + versioning model
- **Affected:** `specs/5/8-yaml-manifests.md`, `specs/5/16-mcp-rest-unification.md`,
  `cmd/arizuko/apply.go`, `resreg/resreg.go:155`
- **Fix:** answer the three questions in the spec first (they are design calls,
  not implementation details), then the repoint is mechanical.

### Recommended answers (2026-08-02, awaiting sign-off)

Second codex pass, asked to decide rather than diagnose. Its load-bearing
premise verified: `arizuko_proxyd_krons` mounts the whole instance data dir
read-write at `/srv/app/home`, so proxyd reaching `routd.db` directly is
FS-mounted write-discipline, not a violation.

1. **`proxyd_routes` stays in `routd.db`.** proxyd reads it there, its REST
   handler writes there, package install writes there, and it has the FS mount
   to do so legitimately. Moving the table buys migration + federation work and
   nothing else. **Delete the empty `proxyd.db`** (after confirming it is empty
   per instance) and stop opening it — an empty DB that the spec names as an
   owner is a false authority that makes `apply` silently no-op.
2. **`config_meta` goes into every manifest-owning DB** via that daemon's own
   migrations — with (1), that is `routd.db` and `onbod.db` only. Each starts at
   version 1; `messages.db`'s counter has no lineage and is ignored. Every
   manifest-addressable mutation must bump its owner's counter **in the same
   transaction**, resreg-shared and direct FS writers alike, or CAS is blind. A
   single central counter is rejected: it cannot be updated atomically with a
   mutation in a different SQLite file. Consequence to accept: scalar
   `config_version:` manifests break and must be re-exported as a per-owner map.
3. **Partial-apply recovers by content-aware skipping.** Version equality is
   required only when an owner has a non-empty diff; stale version + empty diff
   = already applied, skip without re-bumping. `--force` is reserved for stale
   version + different content. Preflight all owners before committing any,
   commit in deterministic owner order, stop at the first runtime failure.
   Mixed-success `apply` exits 3 and prints which owner applied and which rolled
   back; the rerun skips the applied owner and exits 0. `plan` exits 2 only on a
   real conflict (stale + non-empty diff).

Plus: add `Owner` to `resreg.Resource` as a typed logical enum
(`OwnerRoutd`/`OwnerOnbod`) — **not** a filesystem path and not the serving
daemon, so `proxyd_routes` is `OwnerRoutd` though proxyd serves it. Consumed by
`apply`/`plan`/`export`/`get` to group resources and pick the DB opener;
registry validation rejects an engine-managed resource with no owner.

### DECIDED (2026-08-02, user)

**Q2 — yes, and exports are per subsystem.** `config_meta` lands in each
manifest-owning DB via that daemon's own migrations, and the manifest carries a
per-owner version rather than a scalar. Scalar `config_version:` manifests are
rejected and re-exported.

**Consequence: Q3 dissolves.** The partial-apply recovery design (preflight all
owners, deterministic commit order, exit 3 on mixed success, content-aware skip
on rerun) existed only to serve ONE global manifest spanning several DBs. With
export scoped per subsystem there is one file, one DB, one transaction, one
version — nothing to recover across. Delete that section from the spec rather
than implementing it; it is machinery for a problem we no longer have.

**Consequence for Q1.** Per-subsystem export separates the LOGICAL grouping
(proxyd's routes belong to proxyd's export) from the PHYSICAL file (they live in
`routd.db`, the shared cold-tier store). So the resreg tag is a *subsystem*, and
the DB opener is a separate, much smaller mapping. No per-daemon-DB fiction is
needed, and the empty `proxyd.db` has no reason to exist.

**Q1 — RESOLVED by `5/16`'s own rule, not by preference.** `5/16` § "One owner
+ federation" states: *"A resource is owned by the DB that migrates its table,
never by whichever daemon reads it."* `proxyd_routes` is migrated by
`routd/migrations/0015-auth-sessions-proxyd-routes.sql` → **owner is
`routd.db`**. The owner-DB map three paragraphs below that sentence says
`proxyd.db`, contradicting the rule it just stated. Fix the map, not the rule.

The precedent is in the same section: `timed` owns no DB, reads
`scheduled_tasks` from `routd.db`, routd owns it because routd migrates it.
proxyd is that shape plus writes — permitted, since proxyd is FS-mounted
(verified: `arizuko_proxyd_krons` mounts the instance data dir rw at
`/srv/app/home`) and split write-discipline lets FS-mounted daemons write owned
tables directly.

So the two axes are: **subsystem** (who serves the face — proxyd) and **owner
DB** (who migrates the table — routd.db). Per-subsystem export groups by the
first and opens by the second. `proxyd.db` is never created; delete the 0-byte
file.

Separate federation remnant, not part of this: proxyd still opens `messages.db`,
but only for the `audit_log` sink (`proxyd/main.go:993`). `routd.db` and
`onbod.db` already carry their own `audit_log`, so retiring `messages.db`
reduces to repointing that one sink. Corroborating: `routd/` contains **zero** references to
`proxyd_routes`; the table sits in `routd.db` because proxyd resolves cookie →
user → scopes → route from `auth_sessions`/`acl`/`auth_users`/`route_tokens` in
one per-request decision (`proxyd/main.go:1001`). Splitting costs a second DB
open per request and buys nothing. `proxyd.db` on krons is 0 bytes, dated
Jul 11, referenced by no Go code at all.

## A1 — the split is deployment-shaped only: auth data lives in the router's DB (2026-08-02, proposal)

The daemons look like microservices and are coupled through a shared filesystem.
Three verified facts:

**1. Every daemon mounts every DB.** `compose/compose.go:812`, in the *generic*
`writeSvc` emitter, writes `- <dataDir>:/srv/app/home` for every service with no
per-daemon condition. Verified on live krons: `arizuko_timed_krons` has the
instance data dir mounted `rw`. So CLAUDE.md's "non-mounted daemons (slakd,
timed) write via the owner's HTTP API" describes a boundary that does not exist —
the split write-discipline is a convention, honored voluntarily in code.

Honoring it: `timed`, `davd`, `teled` open no DB at all. Not honoring it:
`proxyd` (routd.db), `webd` (routd.db), `dashd` (routd.db + onbod.db +
runed.db), and `slakd` (`slakd/main.go:44` opens routd.db) — which CLAUDE.md
names as an HTTP writer. `timed` owns no DB because it chose the wire, not
because it lacks access.

**messages.db is no longer in that list (2026-08-05, `e5e75bd5` + `3c2b7ad7`).**
routd's two boot-time legacy copiers are deleted and dashd is repointed, so no
daemon opens the monolith — it was the one file legitimately shared by several
daemons and therefore the stated blocker on per-owner `store/` subdirectories.
That blocker is lifted; the remaining ones are enumerated in Q1 (three CLI verbs
still open it) and the cross-DB reads catalogued above (onbod→routd.db,
dashd→3 DBs), which a per-owner mount would have to keep reachable. The
messages.db FILE stays on all three instances — it holds pre-split history, and
disposing of it is an operator decision, separate from retiring the code path.

**2. Authorization lives in the router.** `acl`, `acl_membership`,
`auth_sessions`, `auth_users` are in `routd.db`. authd owns `auth.db` and holds
`identities`, `oauth_identities`, `refresh_tokens`, `signing_keys`,
`identity_claims`. So the daemon whose entire job is auth does not hold the
authorization rows, and `auth.Authorize` — the sole evaluator — reads them out of
the router's file.

**3. `auth_users` is duplicated across two owner DBs.** It exists in BOTH
`auth.db` and `routd.db`, and five packages write "it": `onbod/main.go`,
`proxyd/main.go`, `authd/http.go`+`store.go`, `dashd/profile.go`,
`routd/budget.go` — each hitting whichever DB its handle opened. Both are empty
on krons today, so this is a latent split-brain, not live divergence. Nothing
prevents divergence the moment both get rows.

This is the root cause of Y1's confusion and of proxyd reading another daemon's
tables: a daemon needing an auth answer must go where the auth rows happen to
sit, which is routd.

**Proposal.** Move `acl`, `acl_membership`, `auth_sessions`, and the single
canonical `auth_users` into `auth.db` under authd. proxyd then answers identity
by offline-verifying authd's ES256 token — the mechanism every other daemon
already uses (`5/1`) — instead of opening routd.db. The one thing forcing a DB
read is the opaque **session cookie**; have authd's login set a JWT-bearing
cookie and proxyd's identity reads disappear entirely. Afterwards, narrow the
mount per daemon at `compose/compose.go:812`, which turns the write-discipline
from a convention into an enforced boundary.

- **Severity:** high (architectural; latent split-brain on `auth_users`)
- **Scope:** cross-daemon schema ownership + container mounts
- **Affected:** `compose/compose.go:812`, `proxyd/main.go:869-890`,
  `slakd/main.go:44`, `store/auth.go`, `authd/store.go`, CLAUDE.md's
  write-discipline paragraph, `specs/5/16` owner-DB map
- **Fix:** needs sign-off — cross-daemon schema move + migration per instance.
  Do not start piecemeal; `5/8` and `5/16` should be rewritten against the
  outcome, not before it.

### The `auth_users` collision decomposes — there is nothing to merge (2026-08-02)

The two tables are not drifted copies of one table; they model different
things and were wrongly given the same name.

`auth.db.auth_users` — `user_id` PK, `name`, `created_at`. The OAuth identity
record, FK'd from `oauth_identities`.

`routd.db.auth_users` — `sub`, `username`, `hash`, `name`, `created_at`,
`linked_to_sub`, `cost_cap_cents_per_day`. Checked column by column:

- **`hash` is dead; `username` is NOT.** CORRECTED 2026-08-02 — the original
  claim here ("username + hash are dead") was wrong, produced by running the
  evidence grep through `| head -6`, getting six test-file hits, and concluding
  from a truncated list. `onbod/main.go:784` writes `UPDATE auth_users SET
  username = ?` inside `createWorldTx`, and `:593` reads it on every
  `GET /onboard` — a failed scan renders "User not found." and returns, so
  dropping the column breaks onboarding for every user rather than degrading a
  dashboard. `hash` is genuinely dead: no `SELECT` anywhere, written only by
  `store/auth.go:29,96` to satisfy `NOT NULL`. Delete `hash`; keep `username`
  and carry it into `user_profiles`.
- **`cost_cap_cents_per_day` is live but is not identity** —
  `store/cost_log.go:87,98` read/write it as a per-user spend cap (the same
  column also exists on `groups`). It belongs on a budget table keyed by sub,
  not on an identity table.
- **`sub`/`name`/`created_at`/`linked_to_sub`** are identity, duplicating
  authd's row.

So the resolution is not a merge: strip the dead credential columns, move the
budget column to its own table, and the remainder consolidates into authd's
`auth_users` — reconciling `sub` with authd's `user_id`. The name collision
disappears instead of being resolved.

**Timing.** CORRECTED — "both tables hold zero rows" was checked on krons ONLY
and generalised. Real counts (read-only):

| instance | `routd.db` `auth_sessions` | `routd.db` `auth_users` | `auth.db` `auth_users` |
| -------- | -------------------------- | ----------------------- | ---------------------- |
| krons    | 0                          | 0                       | 0                      |
| sloth    | 5                          | 3                       | 0                      |
| marinade | 3                          | 2                       | 1                      |

sloth's rows are real Google identities; marinade has `local:admin` plus one
Google row. `cost_cap_cents_per_day` is 0 everywhere. So the migration must
carry data on two of three instances.

### `auth_sessions` is not writer-less either (CORRECTED 2026-08-02)

`routd/db.go:110` `copyLegacyProxydTables` `INSERT OR IGNORE`s into
`auth_sessions` from the legacy `messages.db` on **every routd boot** — that is
production code writing the table, contradicting the claim that nothing writes
it. What does hold: nothing MINTS a session post-split (`CreateAuthSession` has
only test callers), every row is backfill (counts match `messages.db` exactly),
and all rows are long expired — newest `expires_at` is 2026-06-05 — so the
cookie branch still cannot match in production.

**Correct order, which the drop-first plan had backwards:** remove
`auth_sessions` from `copyLegacyProxydTables` first, so the backfill stops and
the rows go inert; then drop the table in a later migration once every instance
reads zero. `specs/5/16`'s own migration gate already forbids dropping while an
instance is non-zero.

Note what that writer implies more broadly: routd still opens the frozen
`messages.db` at every boot to copy legacy rows forward. That is the real
blocker for retiring `messages.db`, and it is bigger than any single table.

## M1 — agent-facing MCP tool descriptions still promise dissolved tiers and a dropped table (2026-08-02, open)

Four tool descriptions in `ipc/ipc.go` tell the agent about authorization that
no longer exists. These are not comments — they are the strings the model reads
when deciding whether it may call a tool, so this is a behaviour bug in the
agent surface, not documentation drift.

- `:1177` (`post`) and `:1291` (`delete`) end with **"Tier 0-2 only."**
- `:1874` (`invite_create`) and `:1956` (`invite_revoke`) end with
  **"Tier 0-1 only."**

Depth-derived tiers were removed; `auth/identity.go:11` states the principal
"carries ZERO authorization (no tier, no world rank)". Authorization is a single
`auth.Authorize` over ACL rows. An agent told "Tier 0-1 only" may decline a call
it is in fact permitted to make, or reason wrongly about its own authority —
the failure is silent and looks like model reticence rather than a bug.

Same site, second defect: `invite_create` promises the recipient "gets a
**`user_groups`** row matching target_glob". That table was cut over by
`store/migrations/0053-acl-cutover.sql` and does not exist — `sqlite_master`
count is 0 on krons. The row a recipient actually gets is an `acl` /
`acl_membership` row.

Found during the phase-1..4 spec minimization pass, which flagged it as live
misinformation rather than stale prose. Verified independently against the code
and the live DB before logging.

- **Severity:** medium (silent under-use of granted authority; no privilege
  escalation — the real gate is `auth.Authorize`, which is correct)
- **Scope:** agent MCP tool descriptions
- **Affected:** `ipc/ipc.go:1177,1291,1874,1956`
- **Fix:** delete the tier sentences; restate `invite_create`'s effect in terms
  of the `acl`/`acl_membership` row it actually writes. Grep the whole file for
  other tier/`user_groups` references before calling it done — these four were
  found by a full-file read, not a truncated grep.

## ✅ FIXED 2026-08-03 Y2 — fixing Y1 would activate an invite-token wipe and a raw-bearer export (2026-08-02, open)

**Fix:** `5c6a5421` (shipped BEFORE Y1's repoint, as this entry's own "Fix"
line required) — `SkipApplyRebuild: true` on `invites`, and `InvitesRow`
carries no `token`/bearer field, only `ref`. Found stale (still marked open)
during the Y1 repoint pass 2026-08-04; re-verified live with two new tests
(`TestApply_NeverRebuildsInvites`, `TestExport_OmitsInviteToken`,
`resreg/resources/resources_test.go`) against the NOW-repointed real
`onbod.db`, not the frozen schema this entry originally worried about.

Hard ordering constraint on Y1, found during the phase-5 spec pass and verified
independently.

`resreg/resources/invites.go` does **not** set `SkipApplyRebuild`. Its two
siblings in the same directory do — `route_tokens.go:111` and `secrets.go:54` —
each with a comment giving the reason ("tokens are runtime-minted, not
manifest-managed"; "enc_value blobs are set imperatively, never via apply").
`resreg/engine.go:523` skips exactly those resources in the apply rebuild loop:

```go
for _, r := range All() {
    if r.RowType == nil || r.SkipApplyRebuild { continue }
```

Two consequences, both currently latent:

1. **`arizuko apply` destroys live invites.** Unguarded resources are
   DELETE+INSERTed from the manifest, so every unredeemed invite token not in
   the YAML is wiped.
2. **`arizuko export` writes raw bearers to disk.** `invites.go:15` declares
   `Token string \`db:"token" yaml:"token" json:"token"\`` — the token value
   itself is serialized into the dump, which is then a credential file.

Both are inert today **only** because the manifest CLI opens the frozen
pre-split `messages.db` and therefore touches no live table (Y1). That means
**Y1's fix activates this bug**: the moment `apply`/`export` are repointed at
the owner DBs, the first `export` leaks live invite bearers and the first
`apply` can wipe them.

- **Severity:** high once Y1 ships; latent until then
- **Scope:** manifest apply/export safety for token-bearing resources
- **Affected:** `resreg/resources/invites.go`, `resreg/engine.go:523`
- **Fix:** set `SkipApplyRebuild: true` on `invites` and drop `token` from the
  YAML projection, matching `route_tokens`. **Do this BEFORE the Y1 repoint**,
  not after — and while there, audit every resreg resource carrying a secret or
  bearer field for the same omission rather than fixing only this one.

## E1 — five adapters keep state in unmounted paths; it dies on every recreate (2026-08-03, open)

Found by the mount audit. `teled`, `bskyd`, `reditd`, `linkd` and `emaid` each
persist state under a data-dir path, and their compose fragments declare
**`volumes:` zero times** — verified on `template/services/{teled,bskyd,reditd,linkd,emaid}.yml`,
and `sudo docker inspect arizuko_teled_krons` returns an EMPTY mount list. The
state lives inside the container layer and is destroyed on every recreate.

| daemon | state | code |
| ------ | ----- | ---- |
| teled  | Telegram update offset | `teled/bot.go:60` (`loadOffset`), written `:66-101` |
| bskyd  | `bluesky-session.json` | `bskyd/client.go:80`, written `:92,95` |
| reditd | `cursors.json` | `reditd/client.go:78`, written `:88,90` |
| linkd  | `linkd-state-<name>.json` | `linkd/client.go:95,122`, written `:143,161` |
| emaid  | `emaid.db` (a whole SQLite DB) | `emaid/store.go:22`, created `:19-38` |

**teled is the user-visible one.** `loadOffset` is
`data, _ := os.ReadFile(b.cfg.StateFile)` — the error is swallowed, so a missing
file silently returns offset 0. Telegram treats `getUpdates` offset 0 as "send
everything still queued" and retains ~24h, so a recreate can re-deliver up to a
day of updates and the agent answers them all again. Silent by construction: no
log line marks the reset.

`emaid` is the structural oddity — it opens a SQLite database at a path nothing
mounts, so it silently starts empty each time.

Note `reditd` and `emaid` are not even given `DATA_DIR`, so their paths resolve
relative to the container working dir.

Correcting a related claim recorded earlier in **A1**: the universal
`<dataDir>:/srv/app/home` rw mount comes from `compose/compose.go:811` in
`writeSvc`, which emits ONLY the eight core daemons (authd, routd, runed, timed,
onbod, dashd, proxyd, webd). The 10 channel adapters plus davd/ttsd/vited are
NOT emitted there — they are standalone fragments included verbatim
(`compose.go:384`), each declaring its own volumes or none. So "one line hands
every daemon every database" is true of the core eight only, and narrowing the
adapters is a per-fragment edit, not one code change.

- **Severity:** high for teled (duplicate agent replies after any recreate);
  medium for the rest (re-auth / re-scan / lost dedup state)
- **Scope:** compose fragment volumes for channel adapters
- **Affected:** `template/services/{teled,bskyd,reditd,linkd,emaid}.yml`,
  `teled/bot.go:60`
- **Fix:** give each fragment the narrow rw mount it needs (`whapd.yml:9` and
  `twitd.yml:9` already show the pattern — a single subpath, not the data dir),
  set `DATA_DIR` where absent, and make `loadOffset` fail loud instead of
  swallowing the read error.

## R1 — codex review of the 2026-08-03 fix batch: six gaps (2026-08-03, open)

Adversarial review of `50af5f50..e37a084b` (the Y2/M1/E1/D6/D7 fixes). Findings
verified before logging. Ordered by consequence.

**1. CRITICAL — the adapter-mount fix is inert on every live instance.**
`compose/compose.go:383-402` includes each instance's OWN
`/srv/data/arizuko_<inst>/services/*.yml`, not `template/services/`. Confirmed
on krons: that directory holds installed copies ("Converted from
services/teled.toml by `arizuko generate`"), including a second adapter
`teled-rhias.yml`. Editing the template fixes NEW installs only —
krons/sloth/marinade still have no adapter state mounts, so teled still replays
~24h of Telegram updates on recreate. `cmd/arizuko/packages.go:347-375` copies
templates only on `packages add`; there is no update path for installed
fragments. The installed copies are also still emitting the retired
`CHANNEL_SECRET`.

**2. teled's swallow moved from read to write.** The startup read now fails
loud through `chanlib/run.go:90-94`, but `teled/bot.go:92-94` only warns when
the SAVE fails, advances the in-memory offset, and continues. A full or
read-only mount looks healthy until restart, then replays from the last
offset that actually persisted.

**3. The dissolved-tier class is incomplete.** `resreg/resources/route_tokens.go:86`
still tells `issue_webhook` "Tier rules match issue_chat_link", and — worse —
the agent's own global instructions assert tier ceilings at `ant/CLAUDE.md:28`
and `:165-166`. Those reach the model on every turn.

**4. The invite-bearer fix is YAML-only.** `yaml:"-"` does stop `export`
(`resreg/engine.go:359-375`), but `json:"token"` remains, so the live token is
still returned by `GET /v1/invites` (`onbod/admin.go:53-72`), rendered in full
by dashd (`dashd/invites.go:72-84`), printed by the CLI
(`cmd/arizuko/main.go:732-743`), and published in OpenAPI
(`resreg/openapi.go:122-143`). Y2 fixed the manifest path, not credential
exposure generally.

**5. `SkipApplyRebuild` now yields false advice.** `cmd/arizuko/apply.go:165-169`
assumes every skipped resource is a secret and prints "set via `arizuko secret
set`" — nonsense for invites.

**6. The vacuous-target class remains.** `webd/Makefile:10-11` still runs
`-run E2E` with no zero-match guard; only the root Makefile was hardened.

Minimality notes (not defects): `shouldLink` is a one-line wrapper with a
dedicated 21-line test the renderer test already covers; commit `32b180da`
bundles three narrowings with the persistence fix and `e37a084b` bundles dashd
with Make.

- **Severity:** high (1 leaves the reported bug live on all three instances)
- **Fix:** 1 needs an installed-fragment update path — a real `arizuko` command,
  not a manual edit, since it must run on every instance and survive the next
  `generate`. 2-6 are small and independent.

## authd `TestRefreshRotationRaceSingleWinner` is flaky (2026-08-03, open)

Found while verifying the R1 fragment-relink fix (unrelated package —
`compose`/`cmd/arizuko` are untouched by this test). Fails intermittently
(1/5 runs) in isolation with no other tests running:
`bugfix_test.go:168: successor must be revoked after a concurrent-reuse
family kill`. Timing-sensitive race assertion, not reproducible on demand.

- **Severity:** low (test-only; no evidence of a production race, just an
  assertion that sometimes loses a real race in the test harness)
- **Scope:** authd refresh-token rotation test
- **Source:** authd/bugfix_test.go:168, TestRefreshRotationRaceSingleWinner
- **Status:** open, record-only — needs a root-cause pass on the race timing,
  not a fix on sight

## ✅ FIXED 2026-08-03 — F1 — onbod's dashboard forms never emit the CSRF field they are checked against (2026-08-03, fixed)

`handleOnboardPost` rejects any request whose `csrf` form value is missing or
does not match the `onbod_csrf` cookie, but no rendered form contains that
field: `renderUsernamePicker` and `renderDashboard` emit `<form method="POST"
action="/onboard">` with no hidden `csrf` input. Every real dashboard submission
— create_world, add_route, delete_route — should therefore 403 with "csrf token
invalid". Only the tests pass, because `postOnboard` injects the pair itself.

Found while extracting the double-submit helpers into `auth.EnsureCSRF` /
`auth.CheckCSRF` for spec 5/31's pairing confirm page. The extraction preserved
onbod's behavior exactly (the shared `EnsureCSRF` now RETURNS the token, which
is what a renderer needs to embed); it did not add the missing field, because
that is a behavior fix on a surface this work does not otherwise touch.

Narrower than recorded: `renderUsernamePicker` is the ONLY form onbod renders
for `POST /onboard`. `renderDashboard` renders read-only tables — the
`add_route` / `delete_route` actions `handleOnboardPost` dispatches have no
rendered form on any onbod page, so `create_world` was the whole live breakage.

- **Severity:** high — onboarding self-service dashboard is unusable if confirmed
- **Scope:** onbod HTML forms
- **Affected:** `POST /onboard` action=create_world
- **Source:** onbod/main.go `checkCSRF`, `renderUsernamePicker`
- **Status:** fixed — not deployed
- **Fix:** 4def8f36 — the token `auth.EnsureCSRF` returns is threaded through
  `handleDashboard` into the renderer, plus a test that replays the rendered
  form's own inputs against the handler instead of injecting the pair.

## N1 — linked OAuth logins do not share authority (2026-08-04, FIXED)

**Approved by the operator**, recorded here because it is an authorization-model
change and the tree was busy when it was decided.

> **Shipped with a different design than the one below.** The operator
> superseded the `Caller.Extra` union with **alias resolution at mint**: authd
> resolves the presented login to the account's canonical provider sub and
> stamps that as the JWT subject. Everything below the "The design" heading is
> the rejected plan, kept for the record. Canonical spec section: `5/32`
> § "Alias resolution — one account, one principal".
>
> Why the union was dropped: it makes one person N principals at every
> evaluation, spreads their grants across N rows, and leaves the union stale
> until token expiry. Resolving to one sub keeps one person one principal and
> leaves `expandPrincipals` untouched — a strictly smaller change than the one
> that was billed as "no new mechanism in the authorization path".
>
> **Canonical = `auth_users.user_id`** — set to the first login's
> `provider:providerSub` at account creation, never rewritten. Not earliest
> `linked_at` (unlink would move it and silently transfer authority); not a new
> `canonical_sub` column (a second carrier of a fact `user_id` already holds).
> No schema migration, nothing to backfill. Verified on copies of all three live
> `auth.db`: krons 0/0 rows, sloth 0/0, marinade 1/1 — 0 principals move.
>
> **Two things the entry got wrong.** (1) The symptom is not "a principal with
> no grants" — the alias login returned **HTTP 500**, because `dispatch` tried
> to create a second account for an identity `UNIQUE(provider, provider_sub)`
> already owned. (2) The trade-off "unlinking takes effect at token expiry" does
> not apply; there is no union to go stale. The real unlink hazard is different
> and is now a spec constraint: **unlinking the canonical identity must be
> refused**, or the account is anchored to a login nobody holds. No unlink path
> exists today.
>
> **Blocked on a prerequisite the entry did not see:** `?intent=link` put the
> raw JWT subject (`user:google:g-1`) into `StateIntent.LinkFrom`, which
> `dispatch` wrote into `auth_users.user_id` — a column `5/1` pins as bare. So
> linking forked a new account instead of attaching to one, and the next mint
> double-prefixed to `user:user:google:g-1`. Fixed first, in `beecf595`; no
> test covered the link path at all.
>
> **What resolution costs:** the subject no longer says which credential was
> presented (proxyd's `X-User-Sub` and `5/I`'s audit actor both derive from it).
> Carried as an `authn`/`login` audit row in authd — NOT a JWT claim, since
> `refresh_tokens` stores only the canonical sub and the claim would vanish on
> the first refresh.
>
> - **Fixed:** `beecf595` (link prefix + resolution + tests)
> - **Deployed:** no
> - **Follow-up (not done):** propagate the login identity past authd —
>   proxyd would need an `X-User-Login` header for `resreg`'s audit actor to
>   distinguish "Alice via Google" from "Alice via GitHub". Out of scope here
>   (proxyd + resreg are not authd).

`oauth_identities` (auth.db) already models what is wanted: one `auth_users` row
per person, many provider logins, with `UNIQUE(provider, provider_sub)` making a
given provider account belong to exactly one person. The `?intent=link` flow
(`authd/oauth.go:108`, binding via the signed `LinkFrom` state) populates it.

**But authorization does not consume it.** `acl.principal` and
`acl_membership.parent` key on *provider subs* (`google:alice`), not on the
account. So linking a second OAuth yields a login alias, not shared authority:
Alice pairs Telegram to `google:alice`, later links GitHub, and logging in via
GitHub she is a principal with no grants — the same person, recorded as such,
with none of her authority.

The step that nominally covered this was `Store.CanonicalSub`, which read
`auth_users.linked_to_sub`. It was **never written in production** (its writer
had no non-test caller; `auth_users` is 0 rows on krons), so it was an identity
function, and it is being deleted as superseded. `auth/authorize.go:10` still
refers to it.

**The design — no new mechanism in the authorization path.**
`auth/authorize.go:15` documents `Caller.Extra` as "Extra principals to fold
into the expansion set **without a DB lookup**", and `expandPrincipals` already
seeds its frontier from it (`:136`). authd already queries a user's linked subs
(`authd/store.go:126`). So:

1. authd mints the account's sibling provider subs into the token as a claim.
2. The verifier populates `Caller.Extra` from that claim.
3. `expandPrincipals` folds them in — unchanged.

routd never reads `auth.db`, so this adds no cross-daemon coupling. The
alternative — having `expandPrincipals` walk `oauth_identities` directly — was
rejected for exactly that reason (it is the `A1` pattern).

**Trade-off to accept:** unlinking an OAuth takes effect at token expiry (~15
min), not instantly. Same class as the pending scopes-in-token decision;
unlinking is rare and is de-escalation.

- **Scope:** `authd` token minting + the verifier's `Caller.Extra` population
- **Affected:** `authd/store.go:126`, `authd/oauth.go`, `auth/jwks.go:181`,
  `auth/authorize.go:15,136` (read-only — the expansion needs no change)
- **Belongs to:** `specs/5/32` (it owns the principal namespace and
  `expandPrincipals`); write the section there before implementing.

## V2 — dashd duplicates ~4,500 LOC of control planes phase 7 moves away (2026-08-04, proposal)

Codex review of the dashd surface against the post-phase-5 end state. Almost
nothing is *dead* — the identity deletions left only textual rot. The problem is
**duplication**: dashd hosts control planes that `specs/7/3` moves to each
daemon's own `/dash/<daemon>/`.

| target | LOC | why |
| ------ | --- | ---- |
| `chat.go` + test | ~1,019 | duplicates webd's `/me/chats/new`; **records the RAW reusable route bearer** (`chat.go:25,38,450`) |
| routes / route_tokens / errored-retry | ~867 | duplicates routd's planned `/dash/routd/`. The mint form (`route_tokens.go:103`) should not survive anywhere — cockpit may list/revoke, minting belongs to webd and agent tools |
| `runed_page.go` | 526 | reads `runed.db.spawns` directly and proxies kill; both are runed's |
| tasks (`main.go:771`, `tasks_admin.go`) | ~505 | duplicates timed's dashboard, including a SECOND cron parser and next-run computation |
| invites + whapd pairing | ~782 | belong on `/dash/onbod/` and `/dash/whapd/` |
| usage / audit / packages | ~842 | usage reads the frozen `messages.db` (`usage_page.go:38`); audit reads only routd's `audit_log` while claiming to be global; packages duplicates the CLI |

Plus in-page duplicates: the `/dash/` tile portal repeats `/dash/services/`;
`/dash/status/` repeats counts and calls them health while the hub probes real
health; groups repeats usage and route rows; the 53-line tool browser duplicates
MCP `tools/list`.

**`chat.go` is deletable NOW, ahead of the rest.** `chat_sessions` does not exist
on krons, sloth or marinade — the portal has never been used, so there is no data
to preserve and no capability to lose. Deleting it also removes a plaintext
bearer store of exactly the class Y2/I1 just closed for invites.

**Everything else is SEQUENCED, not free.** Deleting dashd's routes page before
routd serves `/dash/routd/` loses the capability. Order: build the per-daemon
page, then delete dashd's copy. That ordering is the whole proposal.

**The one real gap** is not an edge list — it is an **authority-provenance view**.
`grants_admin.go:52` renders only `acl` rows and consumes `acl_membership` merely
as autocomplete (`:92`), so an operator cannot answer "why does this Telegram
account carry Alice's authority?". The view needs child → parent, `added_by`,
role chains and the resulting effective grants, wired to the existing unpair
action.

Explicitly NOT to add: an outstanding-pairings page (a 10-minute single-use token
offers an operator no action) and an archive page (`arizuko archive` is a CLI
operation).

- **Severity:** medium (duplication + one unused plaintext bearer store)
- **Fix:** delete `chat.go` now; sequence the rest behind phase 7; add authority
  provenance.

## N2 — an operator can grant to a non-canonical alias, silently (2026-08-04, open)

Residual risk from N1 (`beecf595`), verified as having **zero live instances**
today but no guard against acquiring one.

authd now resolves a linked OAuth login to the account's canonical sub
(`auth_users.user_id`) at mint. Grants are keyed on provider subs. So if Alice's
canonical is `google:X` and she links `github:Y`, an operator who grants to
`github:Y` creates a row that **can never match**: Alice's JWT always carries
`google:X`, and `expandPrincipals` compares exact strings.

Same shape for pairing: an `acl_membership.parent` naming a non-canonical alias
is unreachable — and unreachable to unpair too, since the contain seam compares
the caller's canonical sub against the stored parent.

Verified fleet-wide: `oauth_identities` is EMPTY on krons and sloth, and
marinade's single row is its own canonical (`user_id` = `provider:provider_sub`).
sloth's two granted principals (`google:114015391913260915382`,
`discord:user/811295670702047272`) appear in no link table, so each resolves to
itself. **Nothing is stranded today**, and nothing can be until someone links a
second OAuth identity — which no one has.

- **Severity:** low now, medium once anyone links a second login
- **Fix:** reject (or loudly warn on) a grant or pairing whose principal is a
  known non-canonical `oauth_identities.provider_sub`. authd already answers
  this — `oauthIdentityForSub` returns the owning account — so the check is a
  lookup, not new state. Do NOT silently rewrite the principal: moving a grant
  without the operator asking is the failure mode N1 chose an immutable
  canonical to avoid.

## Z1 — all three live instances run a routd/onbod image ~5 days stale; a chunk of shipped migrations is not live (2026-08-04, open)

Found verifying `83cccc78` (the 5/8 CAS/repoint) against read-only `.backup`
copies of the three live instance DBs (never touched the originals). Every
instance's `migrations` table stops at `routd` version **19** and `onbod`
version **2**, dated 2026-07-12 and 2026-06-06 respectively — but the
codebase's `routd/migrations/` goes to **0027** and `onbod/migrations/` to
**0003**, with 0021 (2026-07-30), 0023/0025/0026/0027 (2026-08-01 through
2026-08-04) and onbod 0003 (2026-08-03) all added AFTER the running image.
`sudo docker inspect arizuko_routd_krons` shows image `30c7bd65…`, tagged
`arizuko:latest`, **created 2026-07-30 07:42**; `sudo systemctl status
arizuko_krons` shows the container restarted 11h ago (2026-08-04 10:39) —
so the SERVICE restarted recently, but on the SAME stale image, and no new
migration ran because the binary inside it doesn't contain them. Identical
version numbers (19 / 2) on krons, sloth, AND marinade — one shared stale
image, not a per-instance drift.

Concrete consequence for code shipped this week: `route_tokens.kind`
(migration 0026, 5/31 pairing-vs-route distinction) and onbod's invites
hash-at-rest `ref` PK (migration 0003) do not exist on any live routd.db/
onbod.db yet — `resreg.Export`/`Checksum` fail outright on those two
resources against live data today (`no such column: kind` / `no such column:
ref`), confirmed by the same verification run. This is NOT a bug in
`83cccc78`'s `route_tokens`/`invites` RowFilter changes — the code is correct
against the schema the codebase defines and against a freshly-migrated DB
(`routd.OpenMem()`, which the CI-equivalent tests use) — it is a **deployment
gap**: this week's migrations, and everything else shipped since the 2026-07-30
image build, are not protecting any live user.

- **Severity:** high (a growing list of shipped fixes — pairing grants,
  role-member 4R, user-profiles, drop-linked-to-sub, invites hash-at-rest —
  is not live on any instance)
- **Scope:** deployment / image freshness, all three instances
- **Affected:** krons, sloth, marinade (`routd`, `onbod`)
- **Source:** `.backup` copies verified 2026-08-04 21:4x UTC; live `migrations`
  table + `docker inspect`/`systemctl status` (read-only checks)
- **Fix:** rebuild + redeploy (`sudo make images` then `sudo systemctl restart
  arizuko_<instance>` per instance) — an operator action, not attempted here
  per "never touch a live instance." Once redeployed, re-verify `route_tokens`/
  `invites` export against real data (this session's synthetic-schema tests
  already cover the post-migration shape; only the live-data path was blocked).

## Z2 — proxyd_routes round-trip is not a no-op: `[]string{}` vs `nil` breaks Diff on every row with no preserve_headers (2026-08-04, open)

Found in the same live-DB verification pass (read-only, all three instances):
exporting `proxyd_routes`, re-parsing the emitted fragment, and diffing against
the live table reports EVERY row as `update` — 12/12 on krons, 10/10 on sloth,
11/11 on marinade — never `add`/`remove`, so it is a payload-equality bug, not
a scope/PK bug, and it is instance-independent (same shape on all three).

Root cause, verified: every live row has `preserve_headers = '[]'` (confirmed
via direct query, not assumed) — `ProxydRoutesRow`'s `BeforeInsert`
(`resreg/resources/proxyd_routes.go`) always writes `'[]'\'` for a nil/empty
`PreserveHeaders`, never NULL or `''`. `AfterScan` then
`json.Unmarshal([]byte("[]"), &r.PreserveHeaders)`, which yields a **non-nil
empty** `[]string{}`. But `PreserveHeaders` carries `yaml:"preserve_headers,
omitempty"`, so a row with no headers is OMITTED from the emitted YAML
entirely; re-parsing that fragment leaves the Go zero value, **`nil`**.
`engine.go`'s `payloadEqual` falls through to `reflect.DeepEqual`, which
treats `[]string{}` and `nil` as unequal — the classic Go slice-identity trap.
Every existing test exercises either the live value alone (never round-trips
through EmitYAML/ParseYAML) or a row with a genuinely non-empty
`PreserveHeaders`, so this never surfaced until compared against real rows.

Pre-existing — `83cccc78` did not touch `proxyd_routes.go`'s hooks or
`payloadEqual`; found during general verification of that commit, not fixed
per CLAUDE.md's bug-triage protocol (don't fix on discovery).

- **Severity:** medium — `arizuko plan`/`get` misreport every proxyd route as
  changed when nothing changed; an operator `apply` of an unmodified export
  would still be a correct no-op DELETE+INSERT (same values back), so this is
  a round-trip-honesty violation (spec 5/8 §"Round-trip honesty"), not a data
  -loss risk.
- **Scope:** `resreg` engine (`payloadEqual`) or `proxyd_routes.go` (`AfterScan`)
- **Affected:** `proxyd_routes` resource, live on krons/sloth/marinade
- **Source:** live-DB verification 2026-08-04 21:4x UTC (`.backup` copies)
- **Fix:** either normalize in `AfterScan` (empty JSON array → `nil`, matching
  what a round-trip through YAML always produces) or teach `payloadEqual` to
  treat a nil and a zero-length slice as equal generically — the general form
  is probably right, since any future `[]string`-typed field with `omitempty`
  hits the identical trap.

## Z3 — `onboarding` (onbod) still not a resreg resource; archiving it as speced would leak a live plaintext bearer (2026-08-04, fixed)

- **Status:** fixed `461661fe` (hash at rest, then registration), 2026-08-05.

Closed exactly as the entry's own **Fix** line prescribed, in that order.
`store/migrations/0080` + `onbod/migrations/0004` replace `onboarding.token`
with `token_ref` = `hex(sha256(token))`, indexed, `jid` still the PK.
`store.BackfillOnboardingTokenRefs` (Go — SQLite has no `sha256()`) hashes
every existing plaintext token FORWARD from `onboarding_legacy`, so links
already sitting in users' chats keep resolving; a NULL token stays NULL rather
than hashing `""` into one ref shared by every consumed row. Redemption still
takes the RAW token and hashes internally (`handleTokenLanding`,
`jidForToken`, `claimByToken`), mirroring `GetInvite`/`ConsumeInvite` after I1.

Verified against `.backup` COPIES of all three live `onbod.db` through the
real `openOwnedDB` path — krons 15→15 rows (12 live tokens→12 refs), sloth
21→21 (18→18), marinade 8→8 (7→7), `onboarding_legacy` dropped in each, and a
sampled pre-migration token from each instance still redeems to its JID.

The registration then landed with both hazards this entry raised closed:
`OnboardingRow` OMITS `token_ref` outright (not `yaml:"-"`), and
`SkipApplyRebuild: true` stops any apply from DELETE+INSERTing the table from
a RowType lacking the column — which is what would have nulled every live
setup link. No separate archive-only document was needed: with the bearer
hashed, `token_ref` is a verifier like `route_tokens.token_hash`, so if
`archive` wants to carry admissions forward it can follow
`ArchiveRouteTokenRow` without a new secrecy story. **Spec 5/8's text still
needs the correction this entry asked for** — it says "register it the same
way `onboarding_gates` is", which remains wrong; the table needed a
hash-at-rest migration first.

Not chased (still open): O1's stranded `pending` rows. sloth and marinade each
carry one, confirmed live — `pending` is absent from `knownStatuses`, so
`warnStrandedRows` logs them every tick and nothing advances them.

## Z3b — `migrate-split` bootstraps the FINAL onbod schema but records no migration rows, so a reshaping migration re-runs against it and fails (2026-08-05, fixed)

Found while closing Z3, **not introduced by it — `invites` already has this
bug at `961e5b68`** and it is the reason `onbod/migrations/0001` had to give
up its `CREATE INDEX ... ON onboarding(token)`.

`migrateSplit` (`cmd/arizuko/migrate_split.go:306`) execs `onbodSchema` —
which deliberately mirrors the tables' FINAL shape so the copy has somewhere
to land — but never inserts `migrations(service='onbod', version=N)` rows.
`db_utils.Migrate` keys off `MAX(version)`, so on the next onbod boot it sees
0 and re-runs the whole chain over already-final tables. `IF NOT EXISTS`
CREATEs make that harmless, which is what the file's comment assumes; a
migration that RESHAPES does not:

```
$ sqlite3 x.db "CREATE TABLE invites (ref TEXT PRIMARY KEY, ...);"   # onbodSchema
$ sqlite3 x.db "ALTER TABLE invites RENAME TO invites_legacy; CREATE TABLE invites (ref ...);"  # 0003
$ sqlite3 x.db "SELECT token, ... FROM invites_legacy;"              # BackfillInviteRefs
Error: in prepare, no such column: token
```

onbod then refuses to boot. Reachable only on a monolith→split conversion, so
no live instance is exposed today (all three are long since split) — but the
next reshaping migration silently arms it again for anyone converting.

- **Severity:** medium (blocks a fresh monolith→split conversion; no live
  instance affected)
- **Scope:** `cmd/arizuko/migrate_split.go`, `store/invites.go`,
  `store/onboarding.go`
- **Affected:** `BackfillInviteRefs` (live at base), `BackfillOnboardingTokenRefs`
- **Source:** reproduced on a scratch DB 2026-08-05; `db_utils/db_utils.go:24-28`
  is the `MAX(version)` read
- **Status:** fixed 2026-08-05 (a6649e24)
- **Fix:** `migrateSplit` stamps `migrations` rows 1..`onbodBootstrapVersion`
  (=4) for service `onbod` right after the bootstrap, so `Migrate` resumes after
  them. Claiming FEWER is not an option — `Migrate` demands contiguous versions
  (`ver != cur+1` → "migration gap"), so a short claim still replays the
  reshaping ones. Claiming 4 required `onbodSchema` to genuinely embody 0002,
  which was missing its four `audit_log` indexes; they are added. Rows are
  written only when onbod has recorded none of its own, so a DB onbod migrated
  itself keeps its version rather than being claimed past a migration it never
  applied. `TestOnbodSchemaMatchesMigrations` diffs the bootstrap DDL against the
  migration chain's shape, so a new onbod migration fails the build instead of
  silently re-arming this. **No fleet migration:** conversion-only path, all
  instances are long since split.

## Z3c — `store.InviteRef` and `store.HashRouteToken` are one scheme under two names (2026-08-05, fixed)

Both are `sha256` over the raw bearer; they differ ONLY in output encoding —
`InviteRef` hex-encodes for a TEXT column, `HashRouteToken` keeps the 32 raw
bytes for a BLOB. `resreg/archive.go:314` already notes the equivalence in
prose ("hex-encoded … matching invites' Ref"). `onboarding` now makes three
tables on the same scheme, and Z3 reused `InviteRef` rather than adding a
third helper — but it reads wrong at an onboarding call site.

- **Severity:** low (naming/clarity; no behavior at risk)
- **Scope:** `store/invites.go`, `store/route_tokens.go` + 5 call-site files
  outside store/onbod (`dashd/invites.go`, `ipc/ipc.go`, `tests/`, `cmd/`)
- **Fix:** promote the digest to one generically-named helper (`store.TokenRef`)
  with the BLOB form deriving from it, and rename the ~55 call sites. Purely
  mechanical, but a repo-wide rename — recorded rather than shipped inside a
  bearer-hashing change.
- **Status:** fixed 2026-08-05 (`7ac3401e`) — `store/token_ref.go` owns the
  scheme. `TokenRefBytes` is the only `sha256` call; `TokenRef` is a hex
  wrapper over it, so the encodings cannot drift. 25 files, 63 call sites
  (57 `TokenRef` + 7 `TokenRefBytes`, of which one is the `resreg/archive.go`
  prose that first noted the equivalence). Blast radius was 21 code files, not
  the 5 recorded above — `ipc/ipc.go`, `onbod/` (10), `resreg/` (3), `dashd/`,
  `cmd/`, `tests/`, `webd/`. `ErrInviteRefUnknown` + `BackfillInviteRefs` keep
  their names (invites-specific, not the shared scheme). No stored value or
  column type touched; `store/token_ref_test.go` pins byte-identical output
  against pre-rename vectors.

### Z3 — the original finding (2026-08-04)

Spec 5/8 §"Decided, not merely deferred: pending onboarding admissions must be
archived state" calls registering `onboarding` "a Blocking precondition, same
as the acl_membership one above... register it the same way `onboarding_gates`
is... rides this spec's existing mechanism for free." Verified against the
code before attempting it (as this pass's own instructions required) and found
the prescription does not actually work safely:

1. **`onboarding.token` is a live PLAINTEXT bearer, not a hash.**
   `onbod/main.go:403-419` mints it (`core.GenHexToken()`), stores it in the
   clear (`UPDATE onboarding SET token = ?`), and `onbod/dash.go:50` states
   outright: "the token column is never read or rendered (a live onboarding
   token is a bearer...)". `onboarding_gates` — the resource the spec says to
   copy — has no such column; the two tables are not the same shape the spec's
   analogy assumes. A plain `resreg.Register` with `RowType` including `Token`
   would put that bearer straight into `arizuko export`/`get` YAML the moment
   the resource lands — the exact class of bug Y2 (`5c6a5421`) just fixed for
   `invites`, reopened for a table Y2 never touched.
2. **The table has no folder scope**, so ANY manifest apply mentioning
   `onboarding` — even an unrelated one, since `resreg.Apply`'s loop rebuilds
   every mentioned resource wholesale when `HasScope()==false` — would
   `DeleteAll`+`InsertAll` the WHOLE table. Since `Insert`'s column list is
   derived purely from the registered `RowType`'s `db:` tags, and a
   bearer-safe `RowType` would have to OMIT `token`/`token_expires` (matching
   `route_tokens`' precedent of omitting `token_hash` from its config
   `RowType` entirely) to avoid ever writing/reading the bearer via the
   manifest path, that same omission means every rebuild via a normal
   `arizuko apply`/`archive apply` config step would silently NULL out every
   live chat-onboarding magic link instance-wide — a correctness regression
   `onboarding_gates` (no live-credential column) never risks.

Both are novel to `onboarding` specifically — `route_tokens`/`invites` already
solved this exact shape (hash-at-rest PK, `SkipApplyRebuild`, a
`yaml:"-"`/omitted bearer field, a SEPARATE archive-only value-carrying doc)
and the same treatment would work here too, but it is real, unbuilt design
work, not "register it the same way" as the spec currently states. Not
attempted under this pass's time budget — logged instead of risking a bearer
leak or a live-link wipe shipping in `cmd/arizuko/archive.go`.

- **Severity:** medium (archive/config-manifest correctness + a credential-
  leak class, not yet reachable — `onboarding` isn't registered, so nothing
  ships broken today; it blocks doing this right later, not a live bug now)
- **Scope:** `resreg/resources/` (registration), spec 5/8 (the analogy needs
  correcting, not just the code)
- **Affected:** `onbod.db` `onboarding` table; archive's `onbod.yaml` document
  (pending admissions are NOT part of the archive built in this pass)
- **Source:** verified against `onbod/main.go:403-419`, `onbod/dash.go:50`,
  `resreg/resources/route_tokens.go` (the hash-at-rest precedent),
  `resreg/engine.go`'s `Apply`/`Insert` column-list derivation, 2026-08-04
- **Fix:** mirror `route_tokens`/`invites`: a folder-less, `SkipApplyRebuild`
  registration with `token`/`token_expires` omitted from `RowType` entirely
  (config manifest never sees them), plus an archive-only
  `ArchiveOnboardingRow`-with-token document and its own UPSERT-not-rebuild
  import lane — the same shape `resreg/archive.go`'s `ArchiveRouteTokenRow`/
  `ArchiveInviteRow` already establish. Needs sign-off before landing (spec
  correction + new archive document), not attempted here.

## Z4 — archive apply's filesystem restore has no live run-slot claim (2026-08-04, RESOLVED 2026-08-05 — 43cf6d7a)

Spec 5/8 §"Filesystem restore claims the folder's run slot" specified
`groups.tar` extraction should claim the folder's `runed` spawn slot so no
live agent turn can start mid-extraction. `397bc16f` shipped the CLAIM
MECHANISM (`spawns.kind`, `Manager.RegisterExecutor`, `Run`'s per-kind
dispatch) but nothing used it: no executor, and no wire contract for an
offline CLI to reach a live `runed`.

**Resolved as a GENERIC folder hold, not a restore verb** (operator
direction). The proposal's shape — a `kind='backup'` executor that extracts
the tar itself, plus a `RunRequest` field naming WHAT to restore — was
rejected: it would have put the restore's payload semantics into the
cross-daemon contract, so the next folder-exclusive job needs a second
design. What shipped instead:

- `KindHold` + `POST /v1/holds` (`runed/hold.go`, `43cf6d7a`). runed does
  no work for the caller; it just holds the folder. `Manager.Hold` goes
  through the same `admit()` claim-or-reject step an agent turn does —
  extracted from `Run`, not copied — so it inherits per-folder exclusion,
  the busy→routd-requeues backpressure, the RunTTL wedge protection, and
  `spawns` visibility. No lease table, no pause flag, no lock column.
- Holds get their own endpoint rather than a `kind` on `POST /v1/runs`
  because the response semantics differ: `RunOutcome`'s pinned contract is
  "returned when the run completes", and a hold hands back a handle while
  the run is still open. Release reuses the existing
  `DELETE /v1/runs/{run_id}` — a hold IS a run, and `Kill` already
  dispatches by kind. Gate is `POST /v1/runs`' gate unchanged (`runs:run` +
  folder containment; `runs:kill` to release).
- `arizuko archive apply` claims each to-be-written folder between
  `extractGroups`' two passes and releases in a defer (`43cf6d7a`). An
  unreachable `runed` is FATAL — it dies naming both remedies rather than
  proceeding unguarded. `--stopped` is the operator asserting the instance
  is down, the apply-side counterpart of export's `--quiesced`.

**RunTTL needed no new work**: `397bc16f` had already moved the arming to
the shared dispatch site (`spawn()` wraps ctx with
`context.WithTimeout(ctx, m.runTTL)`), so a containerless kind gets wedge
protection by honoring ordinary ctx cancellation. Verified rather than
assumed — `TestHoldExpiresOnRunTTL` asserts the slot is REUSABLE after an
abandoned hold expires, not merely that the row went terminal.

Two races found by the tests and fixed at the cause, both in `43cf6d7a`:
the hold executor now creates its release signal on first touch by either
`Run` or `Kill` (an immediate release used to close a channel nobody had
created, wedging the folder until RunTTL), and `StartSpawn` is guarded on
`state='queued'` (a DELETE landing between claim and start flipped
`'killed'` back to `'running'`, resurrecting a terminal row — reachable for
ANY kind, only easy to hit via a hold).

19 tests: 13 in `runed/hold_test.go`, 6 in `cmd/arizuko/archive_hold_test.go`
(two of which drive the CLI's holdFn against a REAL `runed` Server).
52 ok / 0 fail, unchanged from baseline.

- **Not done, left open:** `dashd/runed_page.go` hardcodes "Stop the agent
  currently working for %s" in its kill-confirm text — misleading for a
  hold. Spec 5/8 already names it; `dashd` was outside this pass's
  territory. One-line fix: read `kind`, vary the label.
- **Not verifiable live:** per Z1 all three instances run a 2026-07-30
  image at routd migration 19 of 29, so `runed` migration 0004
  (`spawns.kind`) and the `/v1/holds` route are not deployed anywhere. The
  end-to-end CLI→HTTP→Manager path is proven in-process against the real
  `runed.Server`; it has NOT been exercised against a running instance.
- **Deploy note:** `runed` publishes no host port, so `arizuko archive
  apply` on the host needs `RUNED_URL` pointed somewhere reachable (or must
  run inside the compose network). Without that an operator's only path is
  `--stopped`. Whether to publish the port is an operator decision, not
  taken here.

## Z5 — `network_rules`' seed migration uses `CURRENT_TIMESTAMP`, making two fresh instances' checksums permanently unequal (2026-08-04, open)

Found chasing a flaky archive test (a cross-agent report of ONE failure led to
a 50-run `-count` sweep, not dismissed as a fluke). `routd/migrations/0005-
network-rules.sql` seeds two default rows (`anthropic.com`, `api.anthropic.
com`) with `created_at = CURRENT_TIMESTAMP` — SQLite's own wall-clock `now()`,
evaluated at MIGRATION-RUN time, second resolution. `network_rules` is a
registered `resreg` resource (`resreg/resources/network_rules.go`) whose row
content — `created_at` included — is part of `resreg.Export`/`Checksum`'s
projection for every `routd.db`, unconditionally (not just for resources a
given manifest mentions). Two independently-bootstrapped `routd.Open` calls
(the exact shape `cmd/arizuko/apply_test.go`'s `openInstance` helper uses
everywhere) therefore get DIFFERENT `network_rules` seed timestamps whenever
the two migration runs cross a wall-clock second — same-second runs
coincidentally match, which is why this was never caught before: every
existing cross-instance test (`TestCLI_ExportApply_RealFiles` and this
session's own archive tests) already uses `--force` for its apply, which
skips the checksum comparison entirely and never exercises this path.

`CreatedAt` is declared `StampedFields: []string{"CreatedAt"}` on this
resource, so `Diff`/`plan`'s comparison already correctly ignores it — this
is NOT a live bug in ordinary `apply`/`plan` output today. It only bites the
content-hash CAS `Checksum` computes (spec 5/8's gate for whether `apply`
requires `--force`), which hashes the raw `Export` projection with no
StampedFields exclusion. `routd/migrations/0022-seed-operator-grant-
option.sql` seeds a comparable default row (`role:operator`'s base ACL grant)
and gets this right — a fixed literal `'2026-07-30T00:00:00Z'`, not
`CURRENT_TIMESTAMP` — so the inconsistency is between two migrations solving
the same "seed a default row" problem, not a property of seeding itself.

- **Severity:** low (no live-instance impact — a real instance's
  `network_rules` seed timestamp never changes after first boot, so its OWN
  checksum stays stable across repeated exports; this only bites a scenario
  comparing checksums ACROSS two independently-bootstrapped instances without
  `--force`, which no shipped code path does today) — but a real round-trip-
  honesty gap in the same class as Z2, and a landmine for the next test or
  tool that tries a no-force cross-instance config compare
- **Scope:** `routd/migrations/0005-network-rules.sql`; the general question
  of whether `Checksum` should exclude `StampedFields` the way `Diff` already
  does (Z2's `payloadEqual` fix, if it lands, is the natural place to also
  fix this — both are "the hash is more sensitive than the comparison the
  rest of the engine already treats as canonical")
- **Affected:** any code comparing two independently-migrated `routd.db`
  checksums without `--force`; `cmd/arizuko/archive_test.go`'s
  `TestArchive_Apply_SkipsNonEmptyFolderUnlessForce` worked around it by
  testing `tarGroups`/`extractGroups` directly instead of the full checksum-
  gated `applyArchive` pipeline
- **Source:** `routd/migrations/0005-network-rules.sql`,
  `resreg/resources/network_rules.go:73`, verified 2026-08-04 by a 50-run
  `-count` sweep with the migration file moved aside vs restored
- **Fix:** re-seed via a new migration with a fixed literal timestamp
  (matching 0022's pattern) to normalize future installs — cannot rewrite an
  already-applied migration; separately, decide whether `Checksum` should
  exclude `StampedFields` generically (would also close part of Z2's gap).
  Needs sign-off (schema + a content-hash semantics change), not attempted
  here.

## Q1 — three `arizuko` CLI verbs still open the frozen messages.db (2026-08-05, fixed)

With dashd repointed (`3c2b7ad7`) and routd's legacy copiers deleted (`e5e75bd5`), no
DAEMON opens the pre-split monolith. Three CLI verbs still do, via `store.Open` — the
only remaining function that resolves `messages.db`:

- **`arizuko group list`** (`cmd/arizuko/main.go:360,368`) reads `s.AllGroups()` off the
  frozen DB, so it prints the pre-split group set. Every OTHER `group` verb (add/rm/grant/
  ungrant) already routes through `mustOpenACL` → routd.db, so this one read is the odd
  one out and silently disagrees with the writes beside it.
- **`arizuko budget set|show`** (`cmd/arizuko/budget.go:23`) is entirely on the monolith:
  `SetFolderCap`/`SetUserCap` write caps routd never reads, and `SpendTodayFolder`/
  `SpendTodayUser` sum the frozen `cost_log`. routd has its OWN `SpendTodayFolder`/
  `SpendTodayUser`/`FolderCap`/`UserCap` on routd.db (`routd/db.go:1032-1047`,
  `routd/sibling_db.go:92`) — the pair that the pre-spawn budget gate actually consults.
  So `budget set` appears to succeed and changes nothing enforced.
- **`arizuko create`** (`cmd/arizuko/main.go:291`) CREATES an empty `messages.db` in every
  new instance's `store/`, then chowns it. A fresh install ships a monolith no daemon
  opens — which also makes "messages.db is retired" false for new instances.

Same class as the already-fixed `arizuko network` (2026-06-21) and `arizuko apply`
repoints: the verb was never moved to the owner DB at cutover. `store.OpenRoutd` already
exists, so `group list` and `budget` are a handle swap plus a `cost_log` column rewrite
for the spend queries (routd's shape is `recorded_at`/`cost_cents`, the store's is
`ts`/`cents`). `create` should simply stop making the file.

Not fixed here: out of the messages.db-retirement slice's scope (daemon reads), and the
budget swap changes an operator-facing surface, so it wants its own tests.

- **Severity:** medium (`budget set` is a silent no-op; `group list` prints stale truth)
- **Scope:** cmd/arizuko target-DB routing
- **Affected:** all instances — `arizuko group list`, `arizuko budget`, `arizuko create`
- **Source:** cmd/arizuko/main.go:291,360; cmd/arizuko/budget.go:23; store/store.go:51
- **Status:** fixed 2026-08-05 (7a8f4c2b budget, 29e3fb8e group list, eedae3bf create)
- **Fix:** `budget` now takes `*routd.DB` (`mustOpenRoutd`), so it calls the SAME
  cap readers `budgetGate` calls and the wrong-DB bug is unrepresentable at the
  type level; routd gained the two missing write halves (`SetUserCap` delegating
  to store beside `UserCap`, `SetFolderCap` beside its native `FolderCap`). This
  also fixed `budget show`'s spend, which summed the frozen `ts`/`cents` and
  would have errored on routd.db's `cost_cents`/`recorded_at`. `cmdGroup` now
  uses ONE routd.db handle for every action, so `list` agrees with the writes
  beside it and the three duplicate per-case handles collapse (`auditCLI`'s rows
  move to routd.db, where `audit_log` actually lives). `create` seeds the default
  `main` group + tasks into routd.db and makes no messages.db — it was putting
  them in a DB no daemon opens, so a fresh instance had no `main` group at all.
  A `store.Open(` grep now leaves only the playwright seed; `migrate-split` reads
  messages.db by read-only `ATTACH`, not `store.Open`.

## Q2 — dashd's audit-free writers outlived their reason, so admin mutations are unaudited (2026-08-05, open)

Five sites in dashd justify using audit-FREE store variants with the same stale claim —
"routd.db has no audit_log table, so the audited writer would roll back":
`groups_admin.go:122,412`, `route_tokens.go:69,131`, `routes_admin.go:157`,
`grants_admin.go:216,267`, `chat.go:416`. routd migration **0016** added `audit_log` to
routd.db, and dashd's own sink has pointed there since; `audit.Init(dbRoutd, …)` runs at
boot. The premise is false, and the consequence is that operator grant/route/route-token/
group mutations through the dashboard persist with no audit row — while the same
mutations through the CLI and MCP surfaces are audited.

Found while retiring messages.db (the comments are adjacent to the repointed reads).
Left in place: switching to the audited writers is a behavior change on seven mutation
paths and belongs in its own commit with per-path tests, not smuggled into a repoint.

- **Severity:** medium (operator mutations invisible to the audit trail; surfaces disagree)
- **Scope:** dashd admin write paths vs audit/log.go
- **Affected:** all instances — dashboard grant/route/token/group/task mutations
- **Source:** dashd/groups_admin.go:122,412; route_tokens.go:69,131; routes_admin.go:157; grants_admin.go:216,267; chat.go:416; routd/migrations/0016-audit-log.sql
- **Status:** fixed 2026-08-05
- **Fix:** per-site, since the audit-free choice was not uniformly wrong. Five
  sites moved to the audited writer (`AddACLRow`, `RemoveACLRow`, `AddRoute`,
  `InsertRouteToken` ×2, `RevokeRouteToken`). The two group handlers KEPT their
  raw multi-table transactions — `PutGroup`/`DeleteGroup` open their own tx and
  cannot carry the admin grant or the acl/routes cascade — and emit via
  `audit.EmitInTx` inside that same tx instead; group delete's pre-existing emit
  also moved from post-commit into the tx, so an audit row can no longer outlive
  a rolled-back delete. The audited writers hardcoded `Actor: "system"` /
  `Surface: gateway`, which would have recorded an operator action as the
  system, so `store.Store` gained `AsUser(sub)` + a single `auditIdentity()`
  renderer generalizing the actor seam `AddACLRow` already had via
  `row.GrantedBy`. Falsifiability proven per site: reverting each to its
  audit-free writer fails its test with `actor_sub = ""`.
  Tests: `dashd/audit_mutations_test.go`.

## Q3 — dashd tests wire a STORE-schema DB as routd.db (2026-08-05, open)

`testutils.NewInstance` runs `store.Migrate` — the pre-split messages.db schema — and
dashd tests pass that handle as `dbRoutd`. The two schemas overlap enough (messages,
groups, acl, routes) that this passed unnoticed, but they diverge on `cost_log`
(`ts/cents/input_tok` vs `recorded_at/cost_cents/input_tokens`) and on tables routd added
after the split (`chat_sessions`, migration 0029), which the store schema simply lacks.

Two dashd tests now compensate in-line — `TestGroupListUsage` DROPs and recreates
`cost_log` in routd's shape, and the chat tests CREATE `chat_sessions` themselves. That
works but means the fixture can assert against a schema production does not have, so a
genuine column-name regression can pass. The durable fix is a routd-schema instance
helper (`testutils.NewRoutdInstance` running routd's migration FS) for daemons that read
routd.db; `store.Migrate` should stay only where the frozen schema is the subject.

- **Severity:** low (test-fidelity; can mask a real schema drift)
- **Scope:** tests/testutils vs routd/migrations
- **Affected:** dashd test suite (and any future daemon test wiring inst.DB as routd.db)
- **Source:** tests/testutils/testutils.go:49; dashd/integration_test.go (cost_log swap); dashd/chat_test.go:createChatSessions
- **Status:** fixed 2026-08-05
- **Fix:** `testutils.NewRoutdInstance` migrates a real routd DB; every dashd
  fixture uses it. Both in-line compensations deleted (the `cost_log`
  DROP+recreate and `createChatSessions`). A SECOND fixture had the same defect:
  `dashd/admin_routd_test.go`'s hand-rolled `adminSchema` const claimed to mirror
  routd/migrations but omitted `audit_log`, `messages.sender NOT NULL`, and
  `route_tokens.owner_folder`'s FK to `groups` — deleted in favour of the real
  schema, which immediately caught three test rows production would reject.
  `splitAdminDash` now also calls `audit.Init` as dashd's main does, so handlers
  emitting through the package sink are actually observable in tests.

## Q4 — the same false "routd.db has no audit_log" justification survives in cmd/arizuko and onbod (2026-08-05, fixed)

Q2 retired the claim in dashd, but two more sites still carry it. `cmd/arizuko/route.go:16`
states "routd.db has no audit_log table, so we use PutRouteRow / DeleteRouteRow (the
audit-free twins) — same discipline as `arizuko grant` and `arizuko secret`" — false
since routd migration 0016, so `arizuko route add|rm` mutates the live route table with
no audit row. `onbod/main.go:1147` writes the invite-accept acl grant via the audit-free
`PutACLRow`, justified as "the same FS-direct discipline as dashd's routd.db writers" —
a reference that is now stale, since dashd's writers audit.

Not fixed here: `cmd/arizuko/` is owned by another agent this session, and onbod is
outside the dashd brief. Both are the same one-line-per-site change Q2 made, plus the
`AsUser` seam for the CLI's operator identity (the CLI has no `X-User-*` header, so its
actor would need to come from the invoking operator or stay `system` deliberately).

- **Severity:** medium (CLI route mutations invisible to the audit trail; same class as Q2)
- **Scope:** cmd/arizuko route CLI, onbod invite-accept
- **Affected:** all instances — `arizuko route add|rm`, onbod invite acceptance
- **Source:** cmd/arizuko/route.go:16; onbod/main.go:1147; routd/migrations/0016-audit-log.sql
- **Status:** fixed 2026-08-05 (`7ac3401e`). Both sites were defects — the
  predicted "onbod is legitimately audit-free" verdict did NOT hold.
  - `cmd/arizuko/route.go` — **defect**. Audit-free writers used purely on the
    false premise; now `AddRoute` / `DeleteRoute`. `DeleteRoute` did not exist
    (only `AddRoute` had an audited form) so it was added as the twin: reads
    `target` before the DELETE (it is the audit row's folder), emits via
    `EmitInTx`, records nothing when the delete matched nothing. A third copy of
    the false claim was in `route_test.go`'s own comment, asserting the audited
    writers "would roll back against routd.db" — removed.
  - `onbod/main.go` — **defect, not a comment fix**. The proposed cross-DB
    justification (`audit.Init`→onbod.db vs grant→routd.db) does not apply:
    `AddACLRow` emits with `audit.EmitInTx`, which writes the TRANSACTION's DB,
    not the `Init`-configured one, so grant + audit row commit together in
    routd.db. The `group create`/`delete` precedent cited for it is not
    audit-free either — `81521b18` gave both an `EmitInTx` row
    (`dashd/groups_admin.go:150,474`).
  - **Actor:** `AsUser` could not be reused as-is — it renders `surface=rest`,
    and stamping a CLI mutation REST is a lie the trail cannot detect. The
    existing `auditIdentity` seam gained the surface instead of a second
    mechanism: `AsCLI` → `cli:<osUser>` / `SurfaceCLI` (the actor form
    `LogCLIAudit` already established). `AsUser` output is unchanged, so dashd's
    rows do not move.
  - **Falsifiable:** reverting each writer one at a time fails only its audit
    case (no row found / `actor=system`) while `TestRouteLandsInRoutdDB` and
    `TestSplitInviteGrantLandsInRoutd` stay green.

## Q5 — `arizuko grant`/`ungrant` and `arizuko packages` carry the same false audit_log claim (2026-08-05, open)

Found while fixing Q4, not fixed with it (separate concern, and outside that
brief). `cmd/arizuko/main.go:518` states "runGrant writes acl rows audit-free
(routd.db has no audit_log table — same discipline as routd's own grant
endpoint)" — the identical claim Q2 and Q4 already retired. `arizuko grant` /
`ungrant` therefore mutate `acl` and `acl_membership` with no audit row, while
the same grant through dashd (`81521b18`) and MCP is recorded.

The Q4 fix makes this strictly mechanical now: `AddACLRow` / `RemoveACLRow`
exist and audit into routd.db, and `AsCLI` supplies the operator, so these are
one-line swaps plus the same falsifiable test shape as `route_audit_test.go`.

`cmd/arizuko/packages.go:200` applies a whole grant bundle through `PutACLRow`
in a loop — same swap, but it wants a verdict of its own first: a package apply
may deserve ONE audit row for the bundle rather than N for the rows.

Not checked here: `arizuko secret` (`PutSecretRow`, `cmd/arizuko/secret.go:108`)
carries the same audit-free shape and the audited `SetSecret` exists. Give it
its own verdict — a secret's audit row must not carry the value.

- **Severity:** medium (operator grant mutations invisible to the audit trail;
  same class as Q2/Q4, and grants are the higher-privilege surface)
- **Scope:** `cmd/arizuko/main.go` (runGrant/runUngrant), `cmd/arizuko/packages.go`,
  possibly `cmd/arizuko/secret.go`
- **Affected:** all instances — `arizuko grant|ungrant`, `arizuko packages apply`
- **Source:** cmd/arizuko/main.go:518,526,532,547,553; cmd/arizuko/packages.go:200
- **Status:** fixed 2026-08-05 (`877ea615`, `f445b639`, `703f6e75`). All three
  sites were defects; none was legitimately audit-free.
  - `main.go` grant/ungrant — **defect**, the mechanical swap the entry
    predicted: `AddACLRow`/`RemoveACLRow` for a scoped grant,
    `AddMembership`/`RemoveMembership` for the `**` operator grant, `AsCLI` for
    the actor. `AddMembership`/`RemoveMembership` had to move onto
    `auditIdentity()` first — they rendered Actor/Surface by hand
    (`addedBy`-or-`system` over `gateway`), so an `AsCLI` store wrote an
    operator grant as anonymous gateway traffic. One renderer now serves both
    acl writers; unattributed output is byte-for-byte unchanged.
  - `packages.go` — **defect, and the verdict is N rows, not one**. A bundle row
    would say "installed grantpkg" and hide WHICH authority it handed to whom;
    the question an audit trail answers is per-grant, and the bundle is already
    recorded durably in `installed_packages`. The objection to a per-row trail
    (N anonymous rows at one timestamp do not read as one act) is defeated by
    naming the package in each row, not by collapsing them — so each grant
    writes `granted_by: package:<name>` and `AddACLRow`'s ParamsSummary gained
    `granted_by`, without which the actor (`cli:alice` either way) cannot
    distinguish a typed grant from one a package brought in. Settled
    independently by an asymmetry the entry did not predict: `packages remove`
    ALREADY reversed grants through the audited `RemoveACLRow`, one row each —
    install was the silent half, so grants appeared from nowhere and were
    recorded only on the way out.
  - `secret.go` — **defect, and its comment was wrong twice**: it claimed
    routd.db has no audit_log AND that this matched "routd's POST /v1/secrets
    endpoint", which calls the audited `SetSecret`
    (`routd/secrets_resource.go:126`). The CLI was the last silent secret
    writer, not a peer of routd's. `SetSecret`/`DeleteSecret` are behaviourally
    identical to the Bare twins (same `validateScope`, same sealing, same
    `ErrSecretNotFound`), so this adds a row and changes nothing else.
  - **No secret reaches an audit row.** The payload was already safe — the row
    holds `secrets/<scope>/<id>/<key>` plus `"encrypted": bool`, delete holds no
    params, and `audit.marshalParams` redacts key names matching
    `token|secret|api_key|^key$`. So the fix is the CALL SITE, not the payload
    shape. Nothing asserted the invariant, though, so
    `TestSecretAuditNeverCarriesValue` now scans EVERY column of both rows for
    the plaintext, across folder/user scope and sealed/unsealed stores. Proven
    non-vacuous: adding `"note": value` to the ParamsSummary fails it (the
    redaction regex does NOT save a badly-named key).
  - **Falsifiable, per writer:** reverting each of the six writers one at a time
    fails only its own audit case (no row / `actor=system`) while the behavior
    tests (`TestGrantThenList`, `TestGrantLandsInRoutdDB`,
    `TestPackagesInstallGrants`, `TestSecretLandsInRoutdDB`) stay green.
  - `RemoveACLRowBare` and `RemoveMembershipBare` are deleted — this was their
    last caller, and they were the false claim in executable form.
    `PutACLRow`/`PutMembership` stay (test seeding; the `role:member` seed at
    group creation, the 4r migration backfill) with the missing-table
    justification struck from their comments.
## ✅ FIXED 2026-08-05 — F1 — `installed_packages` is a management table with no resreg resource (2026-08-05, fixed)

Every cold-tier management entity is required to register a resreg `Resource`,
so one handler wears a REST face for operators and a derived MCP face for
agents. `installed_packages` registers none: `resreg/resources/` has no packages
resource, `dashd/packages_page.go` is read-only by its own comment ("install /
upgrade / remove is the `arizuko packages` CLI"), and the only mutation path is
`cmd/arizuko/packages.go` writing through `routd/packages_store.go`. Root
`CLAUDE.md` names this exact shape: *"A management resource without a resreg
registration is a review-blocker."*

The consequence is the drift the rule exists to prevent — an agent cannot see or
manage what an operator installed, and the two surfaces disagree about what a
package is. Registering it is not a mechanical addition: package install runs a
multi-step side-effecting lifecycle (git fetch at a pinned revision, asset
copy, grant apply, route apply) that does not fit the CRUD convention resreg
emits, so the resource shape has to be designed — probably a narrow
`list`/`get` face plus explicit install/remove actions, not a table CRUD.

Signed off and shipped as a READ-ONLY resource — the entry's own instinct
("probably a narrow `list`/`get` face") was right, and the "plus explicit
install/remove actions" half was dropped on inspection. Install writes host
files (compose fragments under `<datadir>/services/`, skills under
`<datadir>/skills/`), shells out to `git clone`, applies acl + `proxyd_routes`
rows, and needs `arizuko generate` + a compose restart afterwards. Putting that
behind a routd handler means either a second implementation of a ~200-line
pipeline (the drift CLAUDE.md forbids) or a write that does less than the CLI
while looking like it did the same thing. Remove is worse: the record is what
`packages remove` reads to find the identities to reverse, so a `DELETE` orphans
the routes, grants and files it named. The lifecycle stays CLI-owned; the
resource publishes the read both faces were missing.

Containment turned out not to be folder containment at all: the table has no
folder column and the record names cross-folder identities, so there is no
per-folder slice to hand a tenant. Both faces bind the same instance-wide target
(`**`) and run `auth.Authorize` on it — a folder-scoped grant does not match, so
only the operator role or an explicit `**`-scoped delegation reads it.

- **Severity:** medium (agent/operator surfaces disagree; documented review-blocker)
- **Scope:** packages lifecycle vs resreg
- **Affected:** all instances — `arizuko packages`
- **Source:** resreg/resources/ (no packages resource); dashd/packages_page.go:8-25; cmd/arizuko/packages.go; routd/packages_store.go; CLAUDE.md:71-73
- **Status:** fixed — not deployed
- **Found:** specs/5 frontmatter audit; `5/28` demoted to `partial`.
- **Fix:** `308a2f05` — `resreg/resources/installed_packages.go` (catalog decl:
  RowType → OpenAPI schema, two GET Endpoints, `list_packages`/`get_package`)
  + `routd/packages_resource.go` (one handler, REST gate + agent gate, both on
  `**`) + `routd.OpenAPIResources` (so a test can assert doc and mux agree) +
  `store/migrations/0081` (the shared schema library never mirrored routd's
  0020, so `resreg.Export(routd)` could not scan the resource). 11 tests, each
  falsified by breaking exactly the mechanism it covers.
- **Leaves `5/28` at `partial`:** F2 (unaudited route install) and F3
  (`products.toml` composition unbuilt) are untouched by this.

## F2 — package route installs write no audit row (2026-08-05, fixed)

`applyPackageRoutes` (`cmd/arizuko/packages.go:230-253`) installs a package's
public routes through `st.PutProxydRoute` (`store/proxyd_routes.go:51-79`),
whose whole body is DELETE + INSERT + Commit with no audit call.
`applyPackageGrants` runs from the SAME install path twenty lines earlier
(`packages.go:448` and `:453`) and audits per grant. So installing a package
records which authority it handed out but not which public URL it opened —
the half with the larger blast radius is the silent one.

This is the **sixth** instance of the unaudited-mutation class, and the first
one that is not a grant or a secret. Five were closed 2026-08-05: `81521b18`
(dashd admin mutations), `7ac3401e` (store bearer-hash), `877ea615` (`arizuko
grant`/`ungrant`), `f445b639` (package grants, per grant not per bundle),
`703f6e75` (`arizuko secret`). Q5 fixed the grant loop in this very file and
left the route loop beside it untouched.

Adjacent, checked and NOT filed: `arizuko group add` also writes routes through
the audit-free `PutRouteRow` (`cmd/arizuko/main.go:411-431`), but `auditCLI(s,
"group add", …)` records the act itself, so the cascade is attributable. The
package path has no such covering row.

- **Severity:** medium (public route changes invisible to the audit trail)
- **Scope:** package install route apply
- **Affected:** all instances — `arizuko packages add` for any package shipping `*-routes.json`
- **Source:** cmd/arizuko/packages.go:230-253,448,453; store/proxyd_routes.go:51-79
- **Status:** fixed 2026-08-05 (`06c98611`). Three departures from the entry's
  own Fix line, each deliberate:
  - **Amended in place, no audited twin.** `AddACLRow`/`PutACLRow` came in pairs
    because the bare form had real audit-free callers. `PutProxydRoute` and
    `DeleteProxydRoute` have exactly ONE production caller each — package install
    and package remove — and both need the row, so a twin would be a dead second
    path. Both moved onto the existing `runAudited` + `auditIdentity()` seam;
    `packageACLStore` became `packageStore` and now serves the route writers too,
    without which the rows land as anonymous `system`/gateway traffic.
  - **Delete audited in the same commit.** `packages remove` was silent too, so
    fixing only install would have inverted the asymmetry rather than closed it.
    A trail that shows routes opening but never closing reads as "still live" for
    a route that is gone — worse than silence, because it looks complete.
    `deleted` in ParamsSummary separates a real withdrawal from a no-op.
  - **No `package:<name>` stamp.** `granted_by` worked for grants because `acl`
    HAS a grantor column the audit row merely surfaces. `proxyd_routes` has none;
    adding one is a routd migration for provenance alone, and there is nothing to
    disambiguate — every `proxyd_route.set` IS a package install, since that is
    the only caller, and `installed_packages` already maps path to package.
    ParamsSummary carries what the path cannot say instead: backend, auth,
    gated_by, redirect_to.
  - **Strengthens the case, found while fixing:** proxyd's own
    `/v1/proxyd_routes` resource already audits this mutation (resreg emits via
    `EmitInTx` in the handler's tx, `proxyd/resource.go:212`). The CLI was not
    following a different discipline — it was the only silent writer left, the
    same shape `703f6e75` found for secrets.
  - **Falsifiable:** restoring the pre-fix bodies leaves both mutations
    byte-identical (`store.TestPutDeleteProxydRoute` and
    `TestPackagesInstallRoutesHotApply` stay green) and fails exactly the three
    new tests in `cmd/arizuko/packages_route_audit_test.go` with "rows = 0,
    want 2". Verified, not asserted.

## F3 — `5/28`'s composition section is written as shipped and is unbuilt (2026-08-05, FIXED 2026-08-06)

`specs/5/28-packages.md` §"Composition — blending an ordered product list"
(lines 156-181) is in the present tense among shipped material, but
`grep -rln "products.toml" --include="*.go" .` returns zero files — nothing
reads a product list, and the per-payload-kind collision rule it specifies has
no implementation. The spec's own "## Deferred" section (216-219) lists other
unbuilt items and omits this one, so the reader has no signal.

This is the decision that moved here when `20-ant-portability.md` dissolved
(`specs/5/index.md:126`), which is likely how it arrived already sounding
settled.

- **Severity:** low (spec-accuracy; no runtime effect)
- **Scope:** specs/5/28 composition
- **Affected:** readers of 5/28
- **Source:** specs/5/28-packages.md:156-181,216-219
- **Status:** FIXED 2026-08-06. Took the entry's second option — the section
  keeps its place (the design is worth keeping) and gains the `5/5`
  unbuilt-section marker plus a "Composition's unresolved lock" subsection
  under Deferred; the stale status blockquote (which still listed the
  now-closed `F2` as open) was corrected in the same pass, as was the
  `specs/5/index.md` row.
- **Found while fixing, filed separately as `F29`:** the section is not merely
  unbuilt, it is unimplementable as written — its lock is instance-keyed and
  its subject is group-scoped. An implementer taking this entry at face value
  would have built the contradiction.

## F4 — `5/10`'s grant inheritance does not exist; two callers reinvent it (2026-08-05, FIXED 2026-08-05)

`specs/5/10-web-access.md` justifies `/priv` access with segment traversal — a
grant on `atlas` covering `atlas/search`. `auth.MatchGroups` (`auth/acl.go`)
does not do that: `matchSegments` requires equal segment count, so pattern
`atlas` matches target `atlas` only; reaching the subtree needs `atlas/**` or
`**`. The stated mechanism is fiction.

The promised *behaviour* does happen, which is why nobody noticed — but through
two independent bolt-ons, neither documented, which is precisely the "two paths
drift" shape the one-gate rule bans:

- `dashd/authz.go:56-80` loops `strings.HasPrefix(folder, a+"/")`, with a
  comment admitting *"MatchGroups requires equal segment depth… so subtree is
  checked explicitly."*
- `proxyd/main.go:589-610` truncates the request path to its first segment with
  `strings.Cut` before calling `MatchGroups`, so `/priv/atlas/search/x` is
  authorized against the literal `atlas`. Same outcome, third mechanism.

Two sub-claims from the audit were checked and are FALSE: proxyd does not
"lack inheritance" (it truncates instead), and coverage is not thin —
`auth/acl_test.go` has 14 table-driven cases including cross-folder denial and
`**`, and `proxyd_internals_test.go` adds 6 more.

Deciding whether depth-inheritance belongs in `MatchGroups` changes what every
existing `acl` row means — a grant on `atlas` would silently start covering
every descendant. That is an authorization-semantics change and is not shipped
without sign-off. The alternative (keep exact-depth matching, delete both
bolt-ons, require explicit `atlas/**` rows, migrate existing rows) is also a
semantics change.

- **Severity:** medium (spec misdescribes the gate; three matching behaviours in tree)
- **Scope:** auth.MatchGroups semantics; dashd + proxyd (`/priv` and `/dav`) subtree reach
- **Affected:** all instances — `/priv` and dashd folder visibility
- **Source:** specs/5/10-web-access.md; auth/acl.go matchSegments; dashd/authz.go:56-80; proxyd/main.go:589-610
- **Status:** FIXED (`6409b14a` + follow-up). Signed off; shipped as
  keep-the-glob. The scope glob IS the containment (`5/33` d8, `5/33` d2, root
  CLAUDE.md "no parent-folder inheritance"), so `atlas` is one folder and
  `atlas/**` is the subtree. `auth.Authorize` already read it that way through
  the same `matchPattern`, so teaching `MatchGroups` to inherit would have
  silently widened every grant on every surface; `5/10`'s claim was the outlier
  and is corrected. dashd's prefix loop deleted; its SQL prefilter twin
  (`ownerVisibleSQL`'s `LIKE 'a/%'`) deleted with it.

  **Live-row impact: none, verified by enumeration**, not assumed. Every `acl`
  row on all three instances: krons 15, sloth 15, marinade 13. Only 4 are not
  scoped `**` — krons' two `folder:eval/…  web_route.set` rows (scope == the
  folder exactly, no child groups), sloth's `google:114015…  admin  coach`
  (`coach` has no subfolders), and sloth's `discord:user/811295…  admin  main`
  (`main` has 5 children, but `acl_membership` binds that principal to
  `role:operator`, so `callerScope` short-circuits on `**` and never reached the
  deleted loop). Everyone else holds `**`. No migration; no live row rewritten.

  The audit's framing that all three mechanisms compensate for `MatchGroups` was
  2/3 right. proxyd's truncation answers a DIFFERENT question — a slot path is
  not a folder — and answered it wrong in both directions: folders are
  multi-segment, so `atlas/search` was 403'd on its own `/priv` page, and a
  scope of `atlas/*` reached nothing. Replaced by `auth.MatchSlot`, one helper
  defined in terms of `MatchGroups`, documented as `5/V` filesystem containment
  (the parent's `~/private_html` bind-mount physically holds the child's slot),
  never grant inheritance. Pure widening over the old behaviour, and no live
  principal holds a deep enough grant to notice.

  A FOURTH site turned up while fixing the third, in the same file: proxyd's
  WebDAV gate (`davRoute`) did the identical segment-one cut. dufs serves
  `<data>/groups` as `/data`, so `/dav/<folder>/<file>` walks a tree that nests
  exactly like the web slots — and folder `atlas`'s container home IS
  `groups/atlas`, which holds `groups/atlas/search`. Same question, same
  `auth.MatchSlot` answer. Fixing `/priv` and leaving its twin two hundred lines
  down would have been the drift this bug is about.

  Coverage: `auth/acl_test.go` 14 → 41 cases, plus `MatchSlot` and the
  malformed-glob case; `dashd` predicate rewritten; `proxyd` +7 `/priv` cases
  and +8 `/dav` cases. Each mechanism was broken one at a time and only its own
  test failed.
- **Found:** specs/5 frontmatter audit; `5/10` demoted to `partial`, now shipped.

## F5 — `store.UserScopes` decides folder visibility without action or deny (2026-08-05, PARTIALLY FIXED 2026-08-05)

`specs/5/32` makes `auth.Authorize` the one question the ACL answers.
`store.UserScopes` (`store/acl.go:200-228`) is a second reader:
`SELECT DISTINCT scope FROM acl WHERE effect='allow' AND principal IN (…)`.
It ignores the `action` column entirely, has no deny-wins precedence against a
scope that also carries an allow row on a different action, and is blind to
wildcard-principal rows that `auth.Authorize` honours.

Its callers split, and only one half is a defect:

- **Inert** — `dashd/authz.go` feeds the result into `auth.Caller.Extra` as
  principal aliases, and a full `auth.Authorize` downstream re-checks action,
  deny and wildcards. The blindness cannot escape here.
- **Live** — `dashd/authz.go` `visible()` / `requireVisible()` /
  `requireOperator()` and `onbod/dash.go:20-29` treat the derived set as the
  terminal 200/403 decision with no downstream `Authorize`. A deny row that
  `auth.Authorize` would honour does not hide a folder from the dashboard.

So the violation is real but confined to the read/visibility surface, not the
mutation surface. Routing visibility through `auth.Authorize` changes which
folders appear for existing users — a live authorization-behaviour change.

- **Severity:** medium (deny rows do not suppress dashboard visibility)
- **Scope:** store.UserScopes vs auth.Authorize
- **Affected:** all instances — dashd + onbod folder lists
- **Source:** store/acl.go:200-228; dashd/authz.go visible/requireVisible/requireOperator; onbod/dash.go:20-29
- **Status:** PARTIALLY FIXED. The second READER is gone: `UserScopes` moved to
  `auth.UserScopes` and now projects the same rows `Authorize` loads (expanded
  principals + wildcard-principal rows) instead of its own
  `SELECT DISTINCT scope … principal IN (…)`. That closes the wildcard-principal
  blindness outright — a `google:*` grant authorized fine yet produced an EMPTY
  `X-User-Groups`. Documented and tested as a LISTING that can never be a
  verdict, with the deny/action gaps pinned as tests rather than left implicit.
  Live impact nil: no instance has a wildcard-principal row or a deny row (all
  43 rows fleet-wide are `allow`).

  **STILL OPEN, still needs sign-off:** dashd's read gate remains a scope-list
  match rather than an `auth.Authorize` call, so a deny row still cannot
  suppress dashboard visibility. That last step is a policy decision, not a
  refactor — it must pick WHICH action dashboard read requires (`interact` is
  the natural floor), and a `[]string` header is structurally incapable of
  carrying deny, so the gate has to move off the header entirely. Not shipped
  inline. Note the original entry overstated onbod: `onbod/dash.go`
  `requireOperator` only tests for `**`, it does no per-folder visibility.
- **Found:** specs/5 frontmatter audit; `5/32` demoted to `partial`, now shipped.

## ✅ FIXED 2026-08-06 F6 — `5/K`'s backend contract claims more than `ant/` implements (2026-08-05, RESOLVED BY DELETION)

Four statements in `specs/5/K-ant-backend-codex.md` are not backed by the code:

1. **Graceful degradation is unimplemented.** `capabilities()` has exactly two
   call sites, both tests (`codex.test.ts:67`, `select.test.ts:32`); nothing in
   `ant/src/index.ts` branches on it. The spec (lines 57-58) promises the
   runtime "degrades gracefully" when a capability is `false`.
2. **The backends disagree on a capability the spec says both satisfy.**
   `claude.ts:443` sets `setModelLive: false`, `codex.ts:323` sets it `true`;
   spec lines 59-60 say *"Both claude and codex satisfy every field today."*
3. **`claude.ts` has no dedicated test file** — the default backend, driving
   every production turn, is exercised only indirectly. `codex.ts` has
   `codex.test.ts`.
4. **`ARIZUKO_BACKEND` is undocumented.** Read at `ant/src/index.ts:444-445`
   and `ant/src/backend/index.ts:1,14,25`; it appears in no README, no
   ARCHITECTURE/EXTENDING section, and no `template/` env file — the switch
   that picks the harness is discoverable only by reading the source.

- **Severity:** medium (the default backend is untested; the selector is undiscoverable)
- **Scope:** ant backend abstraction
- **Affected:** all instances running ant
- **Source:** ant/src/backend/claude.ts:443; ant/src/backend/codex.ts:323; ant/src/index.ts:444-445; ant/src/backend/index.ts:1,14,25; specs/5/K-ant-backend-codex.md:57-60
- **Status:** RESOLVED 2026-08-06 — the operator chose deletion over wiring.
  `13c9d12d` removed the whole `Backend`/`Caps`/`Session` seam, `codex.ts`
  and `ARIZUKO_BACKEND`; `8e2e563f` deleted `5/K`. Every item is moot: there
  is no `capabilities()` to wire (1), no second backend to disagree with (2),
  and no selector to document (4). Item 3's fix survives — `claude.test.ts`
  moved to `ant/src/claude.test.ts` and still covers `normalize()`
  row-by-row, verified falsifiable by mutation.
  Root cause the audit missed: `ARIZUKO_BACKEND` was unreachable, not merely
  undocumented — `container/runner.go` `buildArgs` never emitted it into the
  container, so codex could never have been selected in production.
- **Fix (2, 3, 4):** `cd6ebc0b`. The spec no longer claims both backends
  satisfy every field; `setModelLive` is now pinned by a test on BOTH sides
  (`claude.test.ts` false, `codex.test.ts` true) so the divergence is a
  recorded fact. `claude.test.ts` covers `normalize()` row-by-row against
  `5/K`'s mapping table — deliberately not re-testing `claudeResultStatus`,
  which `index.test.ts` already owns. `ARIZUKO_BACKEND` documented in
  `ant/README.md` + `EXTENDING.md`. (`reference/env.html` is another agent's
  lane this round and is NOT done.)

### ✅ F6.1 — SIGNED OFF + SHIPPED 2026-08-06: delete `Caps` and four dead `Session` methods

The audit called item 1 "wire or drop `capabilities()`". It cannot be wired as
specced, and the reason generalizes past `capabilities()`:

- `interrupt`, `sendUserMessage`, `setModel`, `setPermissionMode` have **zero
  call sites** in `ant/src` outside the two backend implementations. Four of
  `Caps`' eight fields exist to gate exactly those.
- `5/K`'s worked example — *"no live interrupt → close+respawn"* — is what
  `runQuery` does **unconditionally**. It always ends in `session.close()`;
  continuation is always a fresh `spawn()` with `resume`/`resumeAt`. The
  degraded path is the only path, so the capability check would gate nothing.
- Building the undegraded half would add a **third** live-steering mechanism
  beside the respawn loop and each backend's native wiring (claude's
  `createIpcDrainHook` PostToolUse drain of `/run/ipc/input`; codex's
  `turn/steer`) — the parallel second path root `CLAUDE.md` forbids.
- `streaming` / `toolUse` have no branch to gate: a backend that cannot stream
  cannot implement `events()`. The runtime's two real backend branches
  (`resolveResumeSession`, MCP rendering) key on `backend.name()`, because they
  are about one harness's id format and config format, not a capability.

**Proposed:** delete `Caps`, `Backend.capabilities()`, and the four unused
`Session` methods; the seam becomes `name()` + `spawn()` + `events()` +
`close()`, which is what the runtime actually calls. `codex.ts`'s `turn/steer`
and `turn/interrupt` implementations stay — they are reachable from inside the
backend, which is where steering belongs.

**Shipped 2026-08-06 — and the operator went further than the proposal.** Not
just `Caps` and the four methods: the entire in-process seam is gone, `codex.ts`
with it. The proposal's own reasoning generalized one step past where it
stopped — if the runtime only ever calls `spawn`/`events`/`close` on a single
implementation, the interface is not a seam, it is a one-implementation
indirection.

Harness independence was never lost, because it never lived here. It lives at
the **process boundary**: the MCP tool surface + `submit_turn` on the unix
socket `routd` serves in-process, which ant already speaks every turn as a
socat client. A TypeScript interface compiled into ant was a second, weaker
copy of a genericity the wire protocol already provides — the parallel second
path root `CLAUDE.md` forbids. The durable idea moved to `5/P` § "ant wraps a
harness, it never is one"; `5/K` is deleted.

## ✅ FIXED 2026-08-06 F7 — runed has no `audit_log` table at all (2026-08-05, FIXED)

`specs/5/I`'s Layer A is "state-changing REST call writes an `audit_log` row in
its own tx", and every other daemon that owns a DB implements it — authd, onbod
and routd each carry an `audit_log` migration and `EmitInTx` call sites. runed
carries neither: `grep -ri audit runed/` is empty, and `runed/migrations/`
(0001-0004) never creates the table. This is deeper than unwritten calls — there
is nowhere to write.

The two endpoints that need it are the highest-consequence ones runed has:
`handleRun` (`runed/server.go:86-115`) spawns a container, `handleHold`
(`server.go:123-147`) claims a folder's run slot. Both call the manager
directly.

Precision on the spec: `specs/5/I-tool-call-logging.md:32` mentions runed once,
under Layer B (the stderr tool-log tap, explicitly "no DB write"). So the spec
does not name runed as a Layer A participant — but Layer A is stated as an
unconditional rule for state-changing REST, which `/v1/runs` and `/v1/holds`
are.

- **Severity:** medium (container spawns and run-slot claims leave no trail)
- **Scope:** runed audit instrumentation
- **Affected:** all instances
- **Source:** runed/migrations/0001-0004 (no audit_log); runed/server.go:86-115,123-147; specs/5/I-tool-call-logging.md:32
- **Status:** resolved-not-yet-removed
- **Fix:** `1d4f33b9`. Migration `0005` adds the table (the routd-0016 shape)
  and `main.go` wires `audit.Init` on runed's own handle. **The report's
  proposed fix was not taken, on two counts.**

  *`POST /v1/runs` is deliberately NOT audited.* A row per turn duplicates the
  `spawns` row, which already carries kind, state, outcome, exit_code, steered
  and all three timestamps and is rendered by dashd — strictly more than an
  audit row, at the same volume. That is `audit/PLAN.md` § SKIP's own rule for
  `messages` (the row IS the record) applied to runed's record.
  `TestRunEmitsNoAuditRow` pins it. What `spawns` cannot answer is who ASKED,
  so the audited calls are the ones that are pure intent: `run.hold` (POST
  `/v1/holds`) and `run.kill` (DELETE `/v1/runs/{id}`, POST `/v1/runs/stop`).
  Both also have outcomes that write no `spawns` row at all — a busy hold, a
  kill of an already-terminal run, a `/stop` on an idle folder — where the
  audit row is the only trace the call happened.

  *`EmitDB`, not `EmitInTx`.* Manager.Kill's mutation lands on the docker
  daemon and Manager.Hold's detaches into a goroutine; runed's writers are
  autocommit throughout (`runed/db.go`). There is no `*sql.Tx` to join — the
  same reasoning as `75cc1a6b`.

  The report's `turn_id` note does not apply either: neither audited call
  carries one. A hold is external (a restore, a vacuum) and an operator kill
  arrives by run_id or folder, not from inside a turn. The join key is
  `resource = runs/<run_id>` → `spawns`.

  Still open, tracked in `specs/5/I`'s status rather than here: **denied**
  runed calls are unrecorded. That is `authz.deny`, it applies to every runed
  endpoint including the reads, and it wants one uniform gate — a kills-only
  denial row would be an arbitrary slice of it.

## ✅ FIXED 2026-08-06 F8 — the OpenAPI aggregator page misstates routd and omits two daemons (2026-08-05, FIXED)

Two errors on `template/web/pub/arizuko/reference/openapi.html`, the page
`specs/5/17` designates as the discovery surface:

1. Line 89 states `secrets` is *"excluded… to keep enc_value blobs off any read
   surface"*. routd includes it — `routd/cmd/routd/main.go:268-269` lists
   `secrets` in the `OpenAPIHandler` allowlist, with a write-only verb set. The
   protection is real, the description of it is not. (The audit also claimed
   `route_tokens` was falsely described as excluded; it is not mentioned on the
   page at all, so that half is DISPROVED.)
2. The aggregator table (lines 55-81) lists six daemons — routd, proxyd, onbod,
   webd, dashd, timed. Eight mount `/openapi.json`: **authd**
   (`authd/main.go:114`) and **runed** (`runed/cmd/runed/main.go:116`) are
   missing. No reverse drift — nothing listed is unmounted.

- **Severity:** low (discovery gap + one false security claim in operator docs)
- **Scope:** web docs vs routd/authd/runed mounts
- **Affected:** operators using the aggregator
- **Source:** template/web/pub/arizuko/reference/openapi.html:55-81,89; routd/cmd/routd/main.go:268-269; authd/main.go:114; runed/cmd/runed/main.go:116
- **Status:** resolved-not-yet-removed
- **Fix:** `016d3d0b` (secrets wording + the authd/runed rows) and `d8b0ecd4`.
  The second pass found three more drifts on the same page that the audit had
  not: `installed_packages` was missing from routd's row (it IS in
  `routd.OpenAPIResources`), emission was described as a fixed five-operation
  CRUD convention (`resreg/openapi.go:217` emits one operation per declared
  `Endpoint`, falling back to the convention only when none are declared), and
  `x-mcp-when` — the whole point of `5/17` — was absent. `routd/README.md:51`
  carried the same stale `secrets`-is-excluded claim and was corrected too.
  NOT deployed: `template/web/pub/` is source-of-truth; the rsync is the
  operator's step.

## F9 — `dashd/me_env.go` writes credentials with zero tests (2026-08-05, fixed)

`dashd/me_env.go` has four handlers, three of which write — create, update and
delete a named credential, all reaching `PutSecretRow`. No test anywhere in
`dashd/*_test.go` references those handlers or the `/dash/me/env` path. Its
twin `dashd/me_secrets.go` has twenty tests across `me_secrets_test.go` (11),
`me_secrets_byoa_test.go` (7) and `me_secrets_routd_test.go` (2) — the
asymmetry is larger than the audit's "11 vs 0" suggested.

WISDOM requires tests in the same commit as any new write path; this one shipped
without them, on the credential surface.

- **Severity:** medium (untested credential write path)
- **Scope:** dashd user-env credential handlers
- **Affected:** all instances — the `/me` portal
- **Source:** dashd/me_env.go; dashd/me_secrets_test.go, me_secrets_byoa_test.go, me_secrets_routd_test.go
- **Status:** FIXED 2026-08-06 — `dashd/me_env_test.go`, nine cases mirroring
  the `me_secrets` shape: the cross-guard on every verb, `store.validateScope`
  through the handle the page writes with, CSRF + auth wiring, seal-at-rest,
  caller-sub binding, and no credential value in a log line or an `audit_log`
  column. Each verified falsifiable by breaking its path.
- **Also found:** `TestMeSecrets_AuditOmitsValue` was VACUOUS — it never called
  `audit.Init`, so `audit.Emit` was a no-op, `audit_log` stayed empty, and the
  test asserted that the empty string does not contain the secret. It passed
  with the plaintext planted straight into the emitted event. Fixed in the same
  pass: sink wired, row existence asserted, every column read.
- **Left open:** `/dash/me/env`'s PATCH and DELETE reject a capability key with
  a bare `not an env-profile key`, omitting the `— use /dash/me/secrets`
  pointer POST carries (and that `me_secrets` carries on all three verbs). Cosmetic
  — the page only ever POSTs — but the three messages should agree. See F20.

## F10 — two of `5/6`'s acceptance criteria have no test (2026-08-05, fixed)

`routd/proactive_test.go` tags acceptance criteria 1, 2, 4, 5 and 7 by comment
and never covers 3 or 6:

- **#3** — a silent outcome still arms the cooldown. The string `outcome:
  "silent"` appears nowhere in the tree, so nothing asserts the silent branch
  records anything.
- **#6** — a proactive turn does not bump engagement, so the next human message
  routes normally. No assertion exists on engagement state after a proactive
  fire.

Both are the criteria that keep the feature from becoming a nuisance — the
cooldown and the not-hijacking-the-conversation guarantees.

Checked and NOT a defect: `CHANGELOG.md:967` (v0.47.0) still says the feature is
"not yet switched on", and that remains true — `PROACTIVE_ENABLED`
(`routd/proactive.go:39`) defaults false via `boolEnv("")` and no
`template/services/*.yml` sets it. Stale-sounding, factually correct.

- **Severity:** low (untested guard rails on a feature that is off everywhere)
- **Scope:** routd proactive tests
- **Affected:** any instance that enables `PROACTIVE_ENABLED`
- **Source:** routd/proactive_test.go; routd/proactive.go:39; specs/5/6-proactive-interjection.md
- **Status:** FIXED 2026-08-06 — `TestProactiveSilentOutcomeArmsCooldown` (#3)
  and `TestProactiveTurnDoesNotBumpEngagement` (#6) in `routd/proactive_test.go`,
  tagged to the criteria the way the other five are. Both drive the real loop
  (scan → dispatch → re-scan), not just `evalProactive`.
- **Re-confirmed 2026-08-06:** `CHANGELOG.md:968`'s "not yet switched on" is
  still factually correct — `PROACTIVE_ENABLED` is read only at
  `routd/proactive.go:39` and no template, compose fragment or `.env` sets it.
  Left as written.
- **Spec still `partial`:** the test gap is closed but three definition-of-done
  items are not — no operator page under `template/web/pub/`, no `dashd`
  surface for mode/cooldown, no migration entry. See the spec's status block.

## F11 — proxyd's route surface is documented under routd's resource name (2026-08-05, FIXED 2026-08-06)

The resource is `proxyd_routes` (`resreg/resources/proxyd_routes.go:90-91`,
`proxyd/resource.go:311`), so the wire path is `/v1/proxyd_routes` and — with no
`MCPNames` override — the derived tools are `proxyd_routes.*`. Both operator
documents still say `routes`, which is routd's message-routing resource:

- `proxyd/README.md` lines 16, 48, 58-62, 65, 105, 148 say `/v1/routes`, and
  line 52 names the MCP tools `routes.list/get/create/update/delete`.
- `template/web/pub/arizuko/components/proxyd.html:60,104,130` and its `legacy/`
  twin say `/v1/routes`.

This is the doc half of the drift fixed in code 2026-07-01, when proxyd's live
resource had drifted to `Name: "routes"` while its catalog already said
`proxyd_routes`. The rule it violates — a resource name is its globally unique
wire identity, and two daemons must never claim one — is in root `CLAUDE.md`.
An operator following the README calls the wrong daemon.

Separately, `dashd/services.go:33` declares the proxyd tile `Built:false` and no
`proxyd_routes` view or control exists in `dashd/`.

- **Severity:** medium (documented endpoint does not exist; collides with routd's name)
- **Scope:** proxyd docs + dashd surface
- **Affected:** operators managing proxyd routes
- **Source:** proxyd/README.md:16,48,52,58-62,65,105,148; template/web/pub/arizuko/components/proxyd.html:60,104,130; resreg/resources/proxyd_routes.go:90-91; dashd/services.go:33
- **Status:** FIXED 2026-08-06. `proxyd/README.md` had already been renamed by
  the time this was picked up; the surviving slip there was the ACL action
  string on line 88 (`routes.<action>` → `proxyd_routes.<action>`). The web half
  is `c52fa771`: `components/proxyd.html` now says `/v1/proxyd_routes` and
  `proxyd_routes.*`, and `reference/openapi.html`'s webd row no longer calls the
  forwarded resource `routes`. The dashd half closed separately — `/dash/proxyd/`
  shipped 2026-08-06 and is documented on `components/dashd.html`.

  The rename uncovered two further errors on the same page, fixed in the same
  commit: it described the **retired HMAC identity model** (`PROXYD_HMAC_SECRET`,
  `auth.RequireSigned`, `auth.StripUnsigned`, `X-User-Sig` — none of which exist;
  `auth/middleware.go` exports only `ProxydTransit`), and it linked
  `specs/5/5-uniform-mcp-rest.md`, a file that has never existed under that name
  (`5/5` is worlds-agents-sessions; the mechanism spec is `5/17`).

  The `legacy/` twins are deliberately NOT fixed — `legacy/` is the archived
  pre-redesign site, linked only from the changelog entry that announces it.

  Not fixed, tracked as **F25**: the same HMAC drift on
  `components/dashd.html`, which also carries the dead `5/5-uniform-mcp-rest`
  link and omits five shipped control planes.

## ✅ FIXED 2026-08-06 F12 — the engagement TTL in the operator docs is not routd's default (2026-08-05, FIXED)

`template/web/pub/arizuko/concepts/engagement.html:65` tells operators the TTL
defaults to `20m`. routd defaults it to 30m in both places it sets one:
`routd/cmd/routd/main.go:203` `durOr("ENGAGEMENT_TTL", 30*time.Minute)`, and
`routd/server.go:144-145` re-defaults to 30m when the value is zero. An operator
reasoning about why the bot is still replying is off by 50%.

`core/config.go:248` does carry a `20*time.Minute` default, which is probably
where the number came from — but that is the removed-monolith config path and
nothing routd runs reads it, so it does not rescue the doc.

No dashd surface shows or clears engagement windows either, so the operator can
neither see who is engaged nor end it.

- **Severity:** low (wrong number in operator docs; no operator control)
- **Scope:** web docs vs routd default; dashd surface
- **Affected:** all instances
- **Source:** template/web/pub/arizuko/concepts/engagement.html:65; routd/cmd/routd/main.go:203; routd/server.go:144-145; core/config.go:248
- **Status:** FIXED 2026-08-06 — cause fix, not just the number.
- **Fix:** the dead `core.Config.EngagementTTL` field is DELETED. It had zero
  readers repo-wide (`go build ./...` proves it) yet carried the 20m that every
  wrong page cited, and `env.html` named it as the reader. Its 30m twin was
  also spelled twice, so both now read one `routd.DefaultEngagementTTL`
  (`routd/server.go`), used by `cmd/routd`'s `durOr` fallback and by
  `NewServer`'s zero-branch. Three pages corrected to 30m:
  `concepts/engagement.html` (also dropped `now − last_activity < TTL`, which
  describes a `last_reply_at` column the spec explicitly does not have — the
  column stores the deadline), `reference/env.html` (default, plus a "Read by"
  pointing at the deleted `core/config.go:217`), and a claim on both that the
  window is extended by "each user or bot message" — only bot outbounds bump it
  (`routd/turns.go:226`, `ipc/ipc.go:636`; `routd/loop.go` only reads).
  `routd/README.md:154` already said 30m and was left alone.
  Guarded by `TestDefaultEngagementTTL` (`routd/engagement_http_test.go`),
  which writes `30*time.Minute` out as a literal so bumping the constant fails
  it and the docs get revisited. Proved falsifiable twice against the whole
  `routd` package: making the zero-branch unconditional, and setting the
  constant back to 20m — each failed that test and no other.
- **Split off:** the "no dashd surface shows or clears engagement windows"
  half of this entry is NOT closed; it is `F31`.

### F12a — the dashd engagement view needs API that does not exist (2026-08-06, FIXED 2026-08-06 — shape (a))

Attempted while closing `5/G`'s item-6 gap. routd does have a REST pair —
`GET /v1/engagement` (`routd/reads_http.go:198`) and `POST /v1/engagement`
(`:217`), mounted at `routd/server.go:272-273` — so "force-disengage over
HTTP" is buildable today (`ttl_seconds <= 0` clears the window,
`reads_http.go:243`, the same semantics as the `disengage` MCP tool at
`ipc/ipc.go:1571`; neither touches an in-flight turn). The **view** half is
not. Three blockers, none fixable inside dashd:

1. **No list, at any layer.** Every `chat_reply_state` read in the tree is
   `WHERE jid=? AND topic=?`. The only statement that is not is
   `DELETE ... WHERE engaged_folder=?` (`routd/db.go:175`, the group-delete
   cascade). `grep -rn "ListEngaged\|AllEngaged\|engaged_until IS NOT NULL"`
   is empty. So "who is engaged right now" cannot be answered — not over
   `/v1`, not through a store helper. A page can only look up a jid the
   operator already knows.
2. **The deadline is never returned.** `EngagementResponse` is
   `{folder, last_reply_id}` (`routd/api/v1/types.go:318-321`);
   `DB.Engaged` returns `(folder, bool)` (`routd/db.go:686`) and the handler
   discards the bool. `engaged_until` — the one number the page exists to
   show — does not cross the wire.
3. **dashd cannot authenticate to it.** Both handlers require
   `routes:read` / `routes:write`, and `serviceGrants` (`authd/http.go:26-62`)
   has **no `service:dashd` entry**, so dashd's token is minted with empty
   scope. See F15a — same root cause.

Building the view therefore means new API surface on a spec that does not
describe it, which is the sign-off case. Two shapes, and they are not
equivalent:

- **(a) Extend the hand-rolled pair** — add a list (`GET /v1/engagement` with
  no `jid`) and put `engaged_until` in the response. Smallest change; leaves
  engagement as the one operator-managed thing with no resreg registration.
- **(b) Promote engagement to a resreg resource** — what root `CLAUDE.md`
  says every cold-tier management entity must be, and it would derive the MCP
  face for free. But `engage`/`disengage` are already hand-authored hot-tier
  tools with a three-arm authorization (`ipc/ipc.go:1509-1527`), so this
  creates the second path the same rule forbids unless those tools are
  retired onto it.

Either way `serviceGrants["service:dashd"]` must exist first.

- **Severity:** medium (blocks the `5/G` item-6 surface; `5/G` stays partial)
- **Scope:** routd `/v1/engagement` shape + authd serviceGrants + dashd page
- **Affected:** all instances
- **Source:** routd/reads_http.go:198,217,243; routd/api/v1/types.go:318-321; routd/db.go:175,686; authd/http.go:26-62; ipc/ipc.go:1509-1527,1571
- **Status:** FIXED 2026-08-06 — blockers 1 and 2 closed. Blocker 3
  (`serviceGrants["service:dashd"]` is missing, so dashd's token is minted with
  empty scope) is NOT re-filed here: `F15a` already owns it, in authd. `5/G`
  stays `partial` on that plus the dashd page (item 6) and `F12`'s TTL doc
  (item 5), none of them this spec's package.
- **Decision: shape (a).** Engagement is HOT-TIER conversational state, not a
  cold-tier management entity, so it does NOT become a resreg resource — the
  same carve-out root `CLAUDE.md` already makes for the hand-authored
  `engage`/`disengage` tools. Shape (b) would put a second path into a seam
  those tools own, and their three-arm authorization is not something resreg's
  `Gate` expresses. The hand-rolled pair was extended instead.
- **Shipped** (`routd/engagement_http.go`, the engagement surface lifted out of
  `reads_http.go` into its own file):
  - `EngagementResponse` gained `engaged_until`. `DB.Engaged` now delegates to
    a new row-shaped `DB.Engagement` rather than a second query, so the
    deadline comes off the read that already ran.
  - `DB.ListEngaged(folder, all)` + `GET /v1/engagement` with no `jid`
    (`EngagementListResponse`) — the "who is engaged right now" read that no
    layer could answer. Live windows only, newest deadline first.
  - Containment: `all` keys on an EMPTY folder claim ONLY — a top-level tenant
    has a non-empty folder, and widening on depth is the leak
    `rest_listall_leak` records. Per row the predicate is
    `descendant(row.engaged_folder, caller)`, NOT `ownsFolder`, which counts an
    empty target as owned by everyone and would expose unclaimed windows.
- **Guards** (`routd/engagement_http_test.go`), each proved falsifiable by
  breaking its own code path and running the whole `routd` package — every
  mutation failed its test and nothing else:
  - `TestEngagementGet_ReturnsDeadline` — deleting the `out.EngagedUntil`
    stamp: `live window returned no engaged_until`.
  - `TestEngagementList_EnumeratesLiveWindows` — widening the live-window
    filter by 24h: `list returned 3 windows, want 2`.
  - `TestEngagementList_NoCrossFolderLeak` (content-level, asserts on the raw
    response BODY) — swapping `descendant` for `ownsFolder`: `tenant alice sees
the unclaimed window`; disabling containment entirely: `contains
    slack:c/bob-1 — cross-folder leak` ×3. It also asserts a root token sees
    all 5 seeded rows, so it cannot pass on an empty result.

## ✅ FIXED 2026-08-06 F13 — webd resolves route tokens in-process, and the endpoint `5/W` specifies is dead (2026-08-05, FIXED)

`specs/5/W-webhook-routes.md:164-167` states, in bold, that **webd does not open
`routd.db`** because cross-daemon direct DB reads are barred, and that it
resolves a URL token through `POST /v1/route_tokens/resolve` so routd does the
hashing and webd never sees the table.

Neither half is true. `webd/main.go:64` opens `routd.db` via `store.OpenRoutd`,
and `lookupRouteToken` (`webd/route_token.go:55-58`) calls
`s.stRoutd.LookupRouteToken(token)` in-process. The resolve handler exists —
`routd/tokens_http.go:31` — and has zero production callers repo-wide; the only
thing exercising it is `routd/contract_test.go:314`.

So there is an unused HTTP surface maintained as if it were the contract, and a
documented daemon boundary that is not enforced. Either webd's direct read is
correct and the spec plus the dead endpoint should go, or the boundary is
correct and webd should move onto it — but the current state teaches the wrong
model to anyone reading either the spec or the endpoint.

- **Severity:** medium (spec describes a boundary the code does not have; dead endpoint)
- **Scope:** webd ↔ routd route-token resolution
- **Affected:** all instances — `/chat/<token>/` and `/hook/<token>`
- **Source:** webd/main.go:64; webd/route_token.go:55-58; routd/tokens_http.go:31; routd/contract_test.go:314; specs/5/W-webhook-routes.md:164-167
- **Status:** FIXED 2026-08-06 — the code was the truth; the endpoint is deleted
  and `5/W` now describes the direct read. `5/W` is `shipped`.
- **Both keep-the-endpoint objections were checked and both failed.**
  - Not an MCP tool: `deriveMCPTools` walks `Resource.Endpoints`, and resolve
    was never declared there — 0 of 61 tools in `ipc.ListTools` match
    `resolve`, and `route_tokens` derives exactly its five declared tools. No
    `ant/`, `dashd/`, or `template/` path reached it either.
  - No containment lost: `handleTokenResolve` took a raw token and NO folder,
    and applied no folder check — the same shape as `LookupRouteToken`. It
    added only a `routes:read` scope check on webd's own service token, which
    authenticates the daemon, not the tenant. The HTTP path enforced strictly
    less than nothing extra.
  It was also absent from `/openapi.json` (never an `Endpoint`), so this was
  the INVERSE of the advertised-but-unmounted class: mounted but undocumented.
  Deleting it makes mux and doc agree.
- **Shipped:** `routd/tokens_http.go` (mount + `handleTokenResolve` gone),
  `routd/api/v1/types.go` (`ResolveRequest`/`ResolveResponse` gone),
  `specs/5/W` § Resolution (names both readers, records why the hop is
  refused), `routd/README.md`, `webd/README.md`, and the two web-doc pages.
- **Tests:** `TestRouteTokens_NoHandRolledResolve` (re-mounting the path fails
  it and nothing else), `TestRouteToken_ResolvedInProcess` (router client on a
  closed port; making resolution depend on routd reachability fails it and
  nothing else), `TestRouteTokenStream_FolderMismatchForbidden` (deleting the
  `X-Folder == group` bind fails it and nothing else — while the pre-existing
  `TestSlinkStream_SlinkSigOK` still PASSES, since it sends only the honest
  folder; that is the vacuous-guard shape this repo keeps hitting).
  Getting the second test to isolate required pinning `routerURL` in webd's two
  fixtures: both built `config{}` with it empty, so every fixture server looked
  like one whose routd is unreachable.

## ✅ FIXED 2026-08-06 F14 — `5/12` and `5/24` never reached the operator web docs (2026-08-05, FIXED)

Definition-of-done item 5 requires an operator-facing page under
`template/web/pub/`. Two shipped specs have only a changelog line:

- **`5/12-turn-retry`** — no `concepts/` or `reference/` page, and
  `MAX_TURN_RETRY` is absent from `reference/env.html`. Everything else holds:
  tests at `routd/fixes_test.go:437,475`, repo docs at `routd/README.md:128`,
  and no dashd row is owed (the spec makes it global config with no per-folder
  override). The similarly-named retry button at `dashd/routd_page.go:104`
  clears an unrelated `messages.errored` flag — different mechanism, not this
  one.
- **`5/24-live-tasklist-status`** — no web page, and additionally
  `routd/README.md` never mentions `submit_status` even though routd owns
  `mcpSubmitStatus` / `deliverStatus` per the spec's own pointers. That is a
  second, independent gap (item 3, repo docs).

Checked and DISPROVED: `5/27-compose-native-packaging` was claimed to have the
same gap and does not — web docs
(`components/channels.html:136`, `reference/env.html:720-721`), tests
(`compose/compose_test.go:97`), repo docs (`ARCHITECTURE.md:697,709`,
`EXTENDING.md:209`) and a dashd surface (`dashd/packages_page.go`) all exist.
It keeps `status: shipped`.

- **Severity:** low (undiscoverable behaviour; no runtime effect)
- **Scope:** web docs
- **Affected:** operators
- **Source:** specs/5/12-turn-retry.md; specs/5/24-live-tasklist-status.md; routd/README.md
- **Status:** resolved-not-yet-removed
- **Fix:** `a86a3716` (`concepts/retries.html` + `concepts/progress.html`, slotted
  into the curriculum after `topics`, nav + tour + pagers restitched, plus the
  `MAX_TURN_RETRY` row in `reference/env.html`), `93201091` (the `submit_status`
  paragraph in `routd/README.md`), `97f5c365` (both specs flipped to `shipped`).
  Every definition-of-done item was re-checked against the call paths first, not
  taken from the audit. NOT deployed: `template/web/pub/` is source-of-truth.

## F15 — authd has no cockpit tile and no operator token revocation (2026-08-05, open)

Two item-6 gaps on the daemon that signs every token in the system:

- `dashd/services.go:33` declares the authd tile `{"authd", …, false, ""}` —
  `Built:false`. The sole ES256 signer has no operator surface.
- Nothing lets an operator revoke another user's refresh-token family.
  `revokeFamily` (`authd/store.go:256`) exists but fires only from reuse
  detection (`authd/server.go:285,300`, `authd/oauth.go:413`).

Nuance the audit did not state: self-service revocation *does* exist —
`POST /auth/logout` (`authd/oauth.go:409-410`) revokes the caller's own family
via their own cookie, which satisfies the spec's own "logout" case (lines
63-64). The gap is specifically admin-initiated revocation — the incident-
response verb, when a session must be killed and the user cannot or will not do
it.

- **Severity:** medium (no way to cut off a compromised session)
- **Scope:** dashd cockpit + authd admin surface
- **Affected:** all instances
- **Source:** dashd/services.go:33; authd/store.go:256; authd/server.go:285,300; authd/oauth.go:409-413
- **Status:** open
- **Fix:** two concerns, two changes — the cockpit tile, and an authorized
  revoke endpoint. The endpoint is the one that matters; it needs its own authz
  decision (who may revoke whose session) rather than defaulting to operator.

### F15a — authd has no admin API at all, so the tile has nothing to render (2026-08-06, PROPOSED — needs sign-off)

Attempted while closing `5/1`'s item-6 gap. The tile cannot flip to
`Built:true` honestly, because item 6 asks for **view AND control** and authd
publishes neither. Verified absent, with the greps:

- **No signing-key metadata.** `GET /v1/keys` (`authd/http.go:110`) serves
  the JWK Set, and `auth.PublicJWKS` (`auth/jwks.go:196-207`) emits only
  `{kty,crv,x,y,kid,alg,use}` — `active`, `created_at` and `retired_at` are
  dropped in `servingKeys()` (`http.go:121-129`). The only reads of
  `signing_keys` are in-process (`authd/store.go:40`) or direct-file from the
  CLI (`cmd/arizuko/token.go:163`).
- **No session/refresh listing.** `grep -rn "FROM refresh_tokens"` returns
  exactly one line — `authd/store.go:226`, `WHERE token_hash = ?`. There is
  no query by `sub` or by `family_id`.
- **No operator revoke.** `revokeFamily` (`authd/store.go:256`) is unexported
  package-`main`, reachable only from reuse detection
  (`authd/server.go:285,300`) and the user's own cookie
  (`authd/oauth.go:413`). `RevokeAllNow` (`authd/server.go:107`) — the
  emergency key-revoke lever — has **zero callers** outside its definition.
- **dashd holds no scope.** `serviceGrants` (`authd/http.go:26-62`) has no
  `service:dashd` key, so `handleServiceToken` (`http.go:155`) mints dashd a
  valid token with a **nil** grant slice. Same root cause as F12a. This also
  means dashd's existing runed-kill and whapd-pair proxies carry an
  empty-scope bearer today.

Two decisions the audit did not surface, on top of the authz question F15
already names:

1. **Where does the revoke audit row land?** authd writes `audit_log` into
   **auth.db** (`authd/migrations/0003-audit-log.sql`), and dashd's
   `/dash/audit/` reads **routd.db** (`dashd/main.go:213-215`). An
   operator-initiated revoke recorded only in auth.db is invisible on the
   page that exists to show it — the one surface where that matters most.
   Note also that today *nothing* in authd is audited except boot and login
   (`authd/main.go:78-85`, `authd/oauth.go:356-366`): not mints, not service
   -token exchanges, not refresh rotations.
2. **Is it a resreg resource?** Root `CLAUDE.md` says every cold-tier
   management entity is one. `signing_keys` and `refresh_tokens` are exactly
   that, and authd's `/openapi.json` currently declares **zero** resources
   (`authd/main.go:114`). Hand-rolling three endpoints instead would be the
   drift `5/16` exists to reverse.

- **Severity:** medium (no way to cut off a compromised session; `5/1` stays partial)
- **Scope:** authd admin API + serviceGrants + audit sink + dashd cockpit
- **Affected:** all instances
- **Source:** authd/http.go:26-62,110,121-129,155; authd/store.go:40,226,256; authd/server.go:107,285,300; authd/oauth.go:413; authd/migrations/0003-audit-log.sql; dashd/main.go:213-215; dashd/services.go:33
- **Status:** PROPOSED — needs sign-off
- **Fix:** decide (i) who may revoke whose session, (ii) which DB the row
  lands in, (iii) resreg resource vs hand-rolled. Then the endpoints, the
  `service:dashd` grant, and only then `Built:true`.

## F16 — the tier-drift sweep never scanned the web docs, and root docs still drift (2026-08-05, open)

The sweep that removed numeric tiers from the docs reported taking stale
references from 114 to 1. The measurement is not sound: its grep used
`--include="*.md"`, and the 169 files under `template/web/pub` are all `.html`
— so the entire operator-facing docs site scored zero by construction, whether
or not it drifted.

Hand-classified against the current code (excluding hot-tier/cold-tier, which is
a different and still-valid concept, and excluding CHANGELOG/BUGS history where
past state is the point), roughly eight stale tier-as-authority references
remain, concentrated in the root documents the sweep evidently did not re-check:
`CLAUDE.md:54`, `ARCHITECTURE.md:332,594-595,864`, `EXTENDING.md:59`,
`SECURITY.md:39,131`, and `specs/4/P-personas.md:45` (still "Tier 2/3: ro
mounts"). Tiers are gone from the code — no `Tier` field, no `auth/policy.go`,
no `AuthorizeStructural` — so each of these describes an authority model that
does not exist.

The three specs the sweep named — `5/33`, `5/A`, `5/M` — read clean in full and
keep `status: shipped`.

- **Severity:** low (docs describe a removed authority model)
- **Scope:** root docs + web docs tier references
- **Affected:** readers of CLAUDE/ARCHITECTURE/EXTENDING/SECURITY
- **Source:** CLAUDE.md:54; ARCHITECTURE.md:332,594-595,864; EXTENDING.md:59; SECURITY.md:39,131; specs/4/P-personas.md:45
- **Status:** open
- **Fix:** re-run the sweep with `--include='*.html'` included, then fix the
  root-doc sites. Note `CLAUDE.md` is a project-instruction file — that edit
  wants the user's eye, not a bulk rewrite.

## F17 — `obs/metrics.go`'s header comment undercounts its own metrics (2026-08-05, open)

`obs/metrics.go:3` says "nine families". The file defines fifteen
(`obs/metrics.go:25-104`). `specs/5/O-observability.md` says "Fifteen families"
and is correct — the spec is right and the code comment is stale, the reverse of
the usual direction. Worth recording because an earlier audit asserted the count
was eleven and the spec wrong; both halves of that were incorrect.

- **Severity:** low (comment-only)
- **Scope:** obs package doc comment
- **Affected:** readers of obs/
- **Source:** obs/metrics.go:3 vs obs/metrics.go:25-104
- **Status:** open
- **Fix:** one word. Or drop the count — a number in a comment beside the list
  it counts will drift again.

## F18 — two live copies of the false "routd.db has no audit_log" claim survive (2026-08-05, fixed)

Q2, Q4 and Q5 retired this claim across dashd, `cmd/arizuko` and onbod. Two
copies remain, both justifying an audit-free writer with a premise that has been
false since routd migration 0016:

- `store/groups.go:85` — `DeleteGroupRow` is documented as the audit-free twin
  "for callers writing to routd.db (which has no audit_log table)". Its caller
  `cmd/arizuko/main.go:439` (`group rm`) does emit an `auditCLI` row beside it,
  so the behaviour is covered; the justification is what is wrong.
- `routd/dark_tools_test.go:82` — "Uses the audit-free `PutTaskRow` (routd.db
  has no audit_log table)." Test seeding only, so **comment-only fix**.

Checked and NOT defects — these two say something different and true:
`resreg/engine.go:553` refers to the engine's own minimal isolation-test schema,
and `cmd/arizuko/migrate_split.go:166` refers to routd.db *before its migrations
run*, which is the state that code operates on.

- **Severity:** low (comment-only; both writers are otherwise accounted for)
- **Scope:** stale justification comments
- **Affected:** future readers choosing an audit-free writer
- **Source:** store/groups.go:85; routd/dark_tools_test.go:82
- **Fix:** strike the parenthetical from both, as Q5 did for
  `PutACLRow`/`PutMembership`. Do not change the writers — `DeleteGroupRow`'s
  caller audits, and the test seeds.
- **Status:** fixed 2026-08-05 (`d35cab4d`). The entry found two copies; a sweep
  of the non-migration tree found **four**, and the two it missed sit in the same
  two files as the two it named:
  - `store/groups.go:58` (`PutGroupRow`) — "an audit_log-less DB (routd.db …)",
    three lines above the `DeleteGroupRow` copy the entry did name.
  - `store/tasks.go:100` (`RemoveTask`) — same claim, in the very file the entry
    cites as stating the real reason correctly (`PutTaskRow`, :53-55).
  Verified false per context rather than assumed from the pattern: all four
  describe writers acting on routd.db AFTER its migrations run, and
  `routd/migrations/0016-audit-log.sql` creates the table. The two the entry
  cleared (`migrate_split.go:166` = routd.db BEFORE migrating,
  `resreg/engine.go:553` = the engine's own isolation-test schema) were re-checked
  and left untouched.
  Comment-only as prescribed; each now states a reason checkable at the call
  site — `group add`/`group rm` (`cmd/arizuko/main.go:409,440`) emit `auditCLI`,
  so a second row would double-count one act, and test seeding is not an act.
- **Found while fixing, not filed as its own entry:** `RemoveTask`'s comment was
  stale twice — it also claims to back `cancel_task`, which deletes through
  `routd/scheduled_tasks_resource.go`'s own `deleteTaskTx` (resreg-audited).
  `RemoveTask` has ZERO production callers. Left in place: dead code is a
  separate concern from a false comment, and `877ea615` deleting
  `RemoveACLRowBare` is the precedent for removing it under its own verdict.

## F19 — Q4 and Q5 record fix commits that are not in HEAD's history (2026-08-05, fixed)

The Status lines of two closed entries cite SHAs that
`git merge-base --is-ancestor … HEAD` rejects: Q4 cites `015b0e6d`, Q5 cites
`5c8abc3e`, `ddc53d59` and `84ea34d5`. The commits they describe are real and
present under different SHAs with identical subjects — `877ea615` ("audit
arizuko grant and ungrant"), `f445b639` ("audit package grants, per grant not
per bundle"), `703f6e75` ("audit arizuko secret set and delete") — so the work
shipped and only the citation is wrong, presumably recorded before a rebase.

This matters because those Status lines are the audit trail for an audit-trail
fix, and the recorded evidence cannot be checked by anyone else. (The entry
predicted `git show 5c8abc3e` would fail; it does not — see Status. The objects
survive in the shared object DB, reachable from no ref, which is what makes the
citation look verifiable while being unreachable for every other checkout.)

- **Severity:** low (record accuracy)
- **Scope:** BUGS.md Q4/Q5 Status lines
- **Affected:** anyone verifying the closed entries
- **Source:** BUGS.md Q4, Q5
- **Status:** fixed 2026-08-05. The entry named four dead SHAs; a sweep of every
  backticked SHA in this file found **eleven**, all worktree-local commits whose
  diffs were reconciled into differently-hashed ancestors. Repointed after
  verifying each mapping by content (`git log -S` on a line the dead commit
  added), not by subject — four of the eleven had no ancestor with a matching
  subject at all, because reconciliation collapsed pairs into one commit:
  - `015b0e6d` (Q4 route+invite audit) and `71db5b8e` (Z3c bearer-hash) →
    `7ac3401e`, which carries both `AsCLI` and `TokenRefBytes`.
  - `5c8abc3e` → `877ea615`, `ddc53d59` → `f445b639`, `84ea34d5` → `703f6e75`
    (Q5's three; subjects match and content confirms).
  - `c9860e3e` (hash at rest) and `c140ef78` (registration) → `461661fe`, which
    adds both `store/migrations/0080-…` and `resreg/resources/onboarding.go`.
  - `b2d70368` (link prefix) and `53049d70` (alias resolution) → `beecf595`,
    which carries `bearerSub`.
  - `fedfa065` (POST /v1/holds) and `22f22b39` (archive hold) → `43cf6d7a`,
    which adds both `runed/hold.go` and `cmd/arizuko/archive_hold_test.go`.

  The habit the entry asked for: `git merge-base --is-ancestor <sha> HEAD`
  before recording a SHA. `git cat-file -t` is NOT enough — all eleven still
  resolve to commit objects in the shared object DB, so `git show` succeeds on a
  SHA no one else can reach. The check is reachability, not existence.

## ✅ FIXED 2026-08-06 F23 — dashd's `RUNED_URL` is read but never written, so the runed kill button is dead in every deploy (2026-08-06, FIXED)

`dashd/main.go` reads `RUNED_URL` into `d.runedURL`, and `handleRunedKill`
returns **503 "RUNED_URL not configured"** when it is empty
(`dashd/runed_page.go:153-156`). Nothing writes it: `compose/compose.go`
never emits `RUNED_URL` (`grep -n "RUNED_URL" compose/compose.go` is empty),
and it is absent from dashd's env allowlist at `compose/compose.go:191-194`
(`AUTH_SECRET, DASH_PORT, WHAPD_URL, AUTHD_URL, AUTHD_SERVICE_KEY,
SURROGATE_GITHUB_CLIENT_ID, SURROGATE_GITHUB_CLIENT_SECRET`). So unless an
operator hand-adds it to the instance `.env`, `/dash/runed/`'s only control
503s on every click.

`TestRunedKillNoURL` covers the 503 branch, so the code is doing what it was
written to do; nothing tests that the deploy supplies the variable. Found
while wiring `/dash/proxyd/`, which avoids the same trap with a code default
(`PROXYD_URL` → `http://proxyd:8080`, matching `webd/main.go:45`).

- **Severity:** medium (a shipped operator control that cannot work as deployed)
- **Scope:** compose generation vs dashd
- **Affected:** all instances
- **Source:** dashd/main.go (runedURL read), dashd/runed_page.go:153-156; compose/compose.go:191-194
- **Status:** FIXED 2026-08-06
- **Fix:** shipped as proposed, but as **one renderer rather than a second
  copy of the proxyd expression**: `dashd/main.go`'s new `backendURL(envKey,
service)` returns the env override when set and `http://<service>:8080`
  otherwise, and BOTH `runedURL` and `proxydURL` now go through it. Mirroring
  proxyd's inline `chanlib.EnvOr` would have left two places for the same rule
  to drift; the bare `os.Getenv` that caused this bug is now gone from dashd's
  wiring entirely. `TestBackendURLDefaultsToComposeService` pins both defaults
  plus override-wins and trailing-slash-trim; reverting `backendURL` to
  `os.Getenv` fails that test and only that test.
- **Note:** this was one of two independent faults on the same button. The
  other — `service:dashd` missing from authd's `serviceGrants`, so the bearer
  was minted with empty scope — was fixed separately in `fd697e99`.
## F22 — `/dash/me/env`'s three rejection messages disagree (2026-08-06, open)

`handleMeEnvCreate` rejects a capability key with

    GITHUB_TOKEN: not an env-profile key — use /dash/me/secrets for capability keys

but `handleMeEnvUpdate` (`me_env.go:208`) and `handleMeEnvDelete`
(`me_env.go:258`) stop at `GITHUB_TOKEN: not an env-profile key` — no pointer at
the page that does accept the key. The twin `me_secrets.go` carries
`— use /dash/me/env` on all three verbs (`:211`, `:260`, `:316`), so the
asymmetry is `me_env`'s alone.

Cosmetic today: the page's form only ever POSTs or DELETEs a key it already
listed, so the truncated message is unreachable from the UI. It is reachable
from `curl` and from any future client, and one guard answering three different
ways is how the two pages drift apart.

Found writing the F9 tests: the test first asserted the pointer on all three
verbs, which is what the spec's "Write paths" section implies, and failed.

- **Severity:** low (message consistency on an unreachable-from-UI path)
- **Scope:** dashd env-profile handlers
- **Affected:** API callers hitting PATCH/DELETE with a capability key
- **Source:** dashd/me_env.go:208, :258; dashd/me_secrets.go:211, :260, :316
- **Status:** open
- **Fix:** give both the same suffix as the POST branch. One string constant for
  all three so a fourth verb cannot invent a fifth wording.
