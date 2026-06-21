# BUGS.md — open issues queue

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
- **Status:** open

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
- **Status:** open
- **Fix:** revoke by opaque row ID, not token

## [SEC] route_tokens: encodeJID/decodeJID not reversible; webhook label unvalidated (2026-06-18, open)

`route_tokens.go:131`: `decodeJID` maps every `--` back to `/`, so a label containing `--` revokes
the wrong JID. `route_tokens.go:43`: label is unvalidated — can contain `/` or control chars that
break JID namespace.

- **Severity:** high
- **Scope:** dashd/route_tokens.go:43,:131
- **Source:** codex audit 2026-06-18
- **Status:** partial — label validated against `[a-zA-Z0-9._-]+` (80bd29da); encodeJID/decodeJID collision still open
- **Fix:** use `url.PathEscape`; validate label against `[a-zA-Z0-9._-]+`

## [SEC] chat: raw bearer tokens visible to read-scoped dashboard users (2026-06-18, open)

`chat.go:152,262`: session list renders raw chat bearer tokens in Continue links. Any read-scoped
user who can see the portal sees a reusable write-capable token, bypassing the admin gate on token
minting.

- **Severity:** high
- **Scope:** dashd/chat.go:152,:262
- **Source:** codex audit 2026-06-18
- **Status:** open
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
- **Status:** open
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
- **Status:** open
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
