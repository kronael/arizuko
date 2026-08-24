# BUGS.md — open issues queue

> **Standing policy — every fix here adheres to WISDOM** (`~/.claude/CLAUDE.md`):
> minimality (smallest change at the root cause), orthogonality (one concern per
> fix, no parallel second path — amend the original), fail loud to the user on
> user-facing paths, retry only transient errors, fix causes not symptoms.
> Redesigns (new contract, changed cross-daemon control flow, auth-model or
> schema changes) stay recorded as proposals and ship only after user sign-off.

## ✅ FIXED 2026-08-24 L91 — dashd's env page omits ANTHROPIC_OAUTH_TOKEN, and its own test says so (2026-08-24)

`TestMeEnv_HTMLPage` (`dashd/me_env_test.go:420`) iterates `store.EnvProfileKeys`
and asserts every key renders on `GET /dash/me/env`. It fails:

```
me_env_test.go:420: html page omits ANTHROPIC_OAUTH_TOKEN
```

`9e55216e` ("ANTHROPIC_OAUTH_TOKEN forwards to the container (pi's credential
name)") added the key to `store.EnvProfileKeys` without the dashd page that
lists them. A user running the pi backend therefore has no operator surface to
set the one credential that backend needs — the same class as
`reference_runed_model_credentials`, where an omitted canonical key cost the
fleet-wide `/login` outage.

**Fixed 2026-08-24.** The page held a SECOND literal of the same facts
(`dashd/me_env.go` `envProfileKeyList`), so adding a key to the map could never
reach it. `envProfileKeyList` is now DERIVED from `store.EnvProfileKeys`, sorted
for a stable display order — one declaration, two readers, and a key added to the
map appears on the page by construction. `TestMeEnv_HTMLPage` is the guard and
passes.

## ✅ FIXED 2026-08-24 L90 — a stale test, not a fork bug: the assertion outlived the rule it asserted (2026-08-24)

`TestEnsureTopicWithFork_CopiedSessionKeepsForkCursor`
(`routd/session_fork_test.go:161`) fails on `main`:

```
observed cursor cleared even though the child inherited the parent transcript
```

**The premise above was wrong** — read the call path before believing it. Nothing
clears the cursor. `L87` (fixed 2026-08-23, `64aae191`) deliberately replaced
"a fork stamps observed_cursor=now" with "the child inherits the PARENT's
cursor", because stamping now discarded the channel's ambient backlog at the
moment a thread was created. The test asserted the pre-L87 rule
(`ObservedCursor != ""`) and its fixture gave the parent no cursor, so it
failed from `64aae191` onward while the code was correct.

**Fixed 2026-08-24** by updating the test, NOT the code — "fix" here would have
reintroduced `L87`. `TestEnsureTopicWithFork_CopiedSessionKeepsParentCursor`
now seeds a parent cursor and asserts the child inherits that exact value, and
a second case pins that a cursorless parent hands down nothing. A test that
outlives its rule reads as a regression in the code it guards.

The assertion had a TWIN in the integration suite —
`TestFeature_ForkContinuity/fork-with-parent-session-resumes-a-restamped-copy`
(`tests/feat_continuity_test.go`), red since `64aae191` for the same reason and
fixed the same way. One rule change, two suites: grep every assertion of a rule
before believing one file carries it.

## L89 — release announcements stopped 2026-07-15: nobody opted in, and the CHANGELOG is not in the image (2026-08-24)

The last release line to reach a real chat is `v0.51.0` on 2026-06-11. Thirteen
releases have shipped since. `/migrate` itself is healthy — every group but the
stragglers below is at v203, and each posts a migration report — but step (e),
the announce, has produced nothing for ten weeks. Two independent causes, either
one sufficient.

**1. No destination.** `v0.59.0` (2026-07-15, `b3bdccac`) made announcing
opt-in: step (e) sends only to routes whose target carries the `#announce`
fragment, and "a group with no `#announce` route announces nowhere". The change
shipped without tagging anything, and nothing has been tagged since:

```
krons     52 routes, 0 #announce
sloth     16 routes, 0 #announce (4 #observe)
marinade  21 routes, 0 #announce (6 #observe)
```

Opt-in with an empty opt-in set is off. The `#observe` counts show operators do
use fragments, so the mechanism works — the release just never asked for one.

**2. Nothing to announce.** Step (e) reads
`/opt/arizuko/CHANGELOG.md`. That path is right; the file is not there:

```
$ docker run --rm arizuko-ant:latest head -1 /opt/arizuko/CHANGELOG.md
head: cannot open '/opt/arizuko/CHANGELOG.md': No such file or directory
```

The agent's `/opt/arizuko` is `MaterializeAppSrc`'s copy of runed's own
`/opt/arizuko` (`container/runtime.go:58`), and the `Dockerfile` populated that
with `ant/skills`, `ant/CLAUDE.md`, `ant/output-styles` and
`template/services` — no `CHANGELOG.md`. Before the `M1` fix the agent got a
bind of `HOST_APP_DIR`, the repo root, which does carry it; closing M1's
dev-tree hole silently closed the announce path with it. Live agents said so
plainly, and it read as success: *"No CHANGELOG present, so announce is a no-op
(nothing to announce)"* — `krons eval/etk57vv-priv-grant-g`, 2026-08-22.

**3. And the destination it would build is not a JID.** Step (e) read the
route's `match` and took `room=<r>` as the address to `send` to. `room` is not
an address: `router.msgField` defines `chat_jid` as the whole JID and
`platform`+`room` as its two halves, so `room=group/5069669817` reaches no
adapter and `platform=discord room=<guild>/<chan>` loses the `discord:` half.
Only a match written `chat_jid=<jid>` ever produced a working destination —
one route on the fleet, on marinade. Cause 3 was invisible behind cause 2.

**4. And it recorded the non-delivery as delivered.** With no destination the
`for` loop had nothing to iterate, so control fell through to
`echo "$latest" > ~/.announced-version`. Every guard on the fleet reads
`v0.63.0` — the version at which the step last "succeeded" — and two of them
hold the malformed value `## v0.63.0 — 2026-07-31`. This is `A2` again in a new
place: `A2` moved the guard write after the sends, but an empty destination list
walks past the sends without failing. The same `test -n "$jids"` that surfaces
cause 3 closes this: zero destinations is now an error, so the guard stays put.

Fixed here (v204):

- `Dockerfile` copies `/src/CHANGELOG.md` to `/opt/arizuko/CHANGELOG.md`, next
  to the other four, because that directory is the agent's whole view of the
  release.
- `ant/skills/migrate/SKILL.md` builds the destination as `<platform>:<room>`,
  uses `chat_jid=` verbatim when present, and **fails** — leaving the guard
  unadvanced — when a tagged route names no platform. An announce route that
  resolves to nothing is now an error, not a quiet skip.
- One route per instance tagged `#announce` (below). `Announce` is read only by
  `router.Describe` for the `inspect_routing` annotation, so the fragment
  changes nothing about inbound matching.

Related, found in the same sweep: eleven groups are frozen below v203 —
`sloth main/1000` v126, `main/coach` v126, `coach/1000` v126, `main` v170;
`krons 1000` v170, `mayai/1000` v162, `krons/1000` v162, `share` v165,
`krons/groups/mayai` v0; `marinade share` v134, `atlas/research` v129. Every
`1000` folder is stale on both instances, which is a pattern, not eleven
accidents. Untriaged.

## L69 — every cost row on the fleet is attributed to nobody: 7188 rows, 0 user subs (2026-08-22)

`cost_log` already carries the three dimensions spend must be sliced by: `folder`
(the group), `model`, and `user_sub` (who triggered the turn). Two of the three
work. The third is empty in every row that has ever been written.

```
krons    2711 rows, 0 with a user_sub
sloth    2209 rows, 0
marinade 2268 rows, 0
```

The cause is one list. `routd/budget.go:12`:

```go
var authSubPrefixes = []string{"google:", "github:", "local:"}
```

`callerSubOfMsg` returns the trigger sender only when it carries one of those
prefixes, and `recordTurnResult` writes that return value into `cost_log.user_sub`.
No sender on any instance has ever carried one. The senders that actually spend
are platform ones — krons' busiest human is `telegram:user/1112184352` with 779
turns — plus `anon:<hex>` from the web chat. Every one of them writes `''`.

The list is not wrong for what it was written for. Its comment says platform
senders are "not bound to a `user_profiles` row, so they carry no per-user cap",
and that is true. But ONE function serves two concerns:

- **attribution** — record who caused this spend. Wants every human sender.
- **cap lookup** — does this sub have a configured daily limit. Needs a profile row.

Restricting attribution to satisfy the cap lookup is what emptied the column.
`SpendTodayUser` and `idx_cost_log_user_day` exist and answer nothing;
`arizuko budget show user <sub>` returns 0 for every sub on the fleet.

A second defect sits under the same code. `store.UserCap` returns
`sql.ErrNoRows` when the sub has no `user_profiles` row, so the moment
attribution starts working `budgetGate` logs a warning per turn for every
platform user. "No profile" means uncapped — it is the documented default, not
an error.

**Fix**: split the two concerns. Attribution writes the trigger sender verbatim
for every sender that is a person (excluding `system`, `timed`, `timed-isolated:`,
`delegate` and `operator`, which are the platform triggering itself); the cap
lookup keeps requiring a profile row and treats its absence as uncapped.

Not a redesign: no new contract, no changed control flow, no schema change. The
column, the index and both queries already exist for exactly this.

Ties: `specs/6/23` (a gateway makes the NUMBER trustworthy; this makes the row
attributable — orthogonal halves of the same question).

## ✅ FIXED 2026-08-23 L83 — webd read failures render as success: the SSE says the round finished (2026-08-23)

`store.GetTurnResult` maps only `sql.ErrNoRows` to `"pending"`; a real error
returns the zero `TurnInfo`, whose `Status` is `""`. The SSE handler discarded the
error and tested `Status != "pending"`:

```go
if info, _ := s.stRoutd.GetTurnResult(folder, turnID); info.Status != "pending" {
    …writeSSE("event: round_done" …)   // "" != "pending" → the round "finished"
    return
}
```

So a transient sqlite `busy`/`locked` under WAL load ends the browser's round
with an empty status and closes the stream. The sibling JSON handlers
(`handleTurnSnapshot`, `handleTurnStatus`) return 500 on the identical query.

Four more reads on the same page discarded their error and rendered a blank page
at HTTP 200 — `webd/me.go` (thread x2, topic list) and `webd/partials.go`
(topics, thread) — while their JSON twins in `webd/api.go` return 500. The SSE
replay query (`TurnFrames`) discarded its error with no log at all, so a client
resuming from `Last-Event-Id` silently skipped frames.

**Fixed**: the SSE ends the round with `status:"error"` and an ERROR log (headers
are already sent, so the truth rides the status field the client already reads);
the replay failure logs; the five HTML reads return 500 like their JSON twins.

Covered by a test pinning the zero-value collision that made it possible — a
failed read is neither an error the caller sees nor `"pending"`. The handler's
own emission is NOT covered end to end; the SSE fixture needs an authorized turn
plus a hub topic, and that is worth adding.

Remaining, same class, not fixed: `webd/chat_mcp.go:148` `collectRoundFrames`
logs its error but returns `nil` frames, so `get_round` answers with a successful
empty snapshot and `wait=true` blocks the caller for the full 5-minute cap. The
fix changes its signature to return the error to the MCP tool, which is a
contract change on the chat-MCP face — recorded, not shipped.

## ✅ FIXED 2026-08-23 L81 — an operator's edit to a package-owned proxyd route succeeds, then silently reverts (2026-08-23)

Two predicates answered one question and disagreed about the third owner value.

```go
store/proxyd_routes.go  (SyncProxydRoutes keep loop)  if owners[r.Path] == RouteOwnerOperator   // allowlist
proxyd/resource.go      (PATCH guard)                 if routes[idx].Owner == RouteOwnerArizuko // denylist
```

`package:<name>` (introduced by `L46`) is neither, so a PATCH to `/slakd/` or
`/teled/` returned 200 and the next `arizuko generate` overwrote it — with no
log, no 409, nothing. That is precisely the "config stops applying and looks
healthy" failure the guard was written to make loud (`L45`), reappearing through
the owner the guard never learned about.

**Fixed**: `store.RouteSurvivesGenerate(owner)` is the one predicate — exactly
`operator` survives — and both the keep loop and the PATCH guard call it. The 409
now names the actual owner instead of hardcoding "arizuko".

## ✅ FIXED 2026-08-23 L82 — a folder cannot manage its own `#observe` route (2026-08-23)

A route target legally carries a fragment (`main/trading#observe`), and the
containment predicates compared the RAW string:

```go
func routeTargetWithin(target, owner string) bool { return groupfolder.Contains(owner, target) }
```

`Contains("main/trading", "main/trading#observe")` is false, so `set_routes`,
`add_route` and `DELETE /v1/routes` 403'd on the caller's OWN row — while the
resource's own delete SQL (`target LIKE ?||'#%'`), `core.ParseRouteTarget` and the
tool description all treat that row as belonging to `main/trading`. A
list→edit→set round-trip therefore either refused the caller's existing observe
route or dropped it, and it could not be re-added: the channel flips from
"archived as observed context" to a route miss, and messages stop being stored.

Live rows in this shape: sloth 4, marinade 6, krons 0. Sub-folder targets
(`atlas/general#observe`) were unaffected, which is why it hid.

**Fixed**: both `routeTargetWithin` and `routeTargetOwned` compare
`core.ParseRouteTarget(target).Folder`. A target PREFIX
(`folder:`/`daemon:`/`builtin:`) is still not a folder — only the fragment comes
off — and the test pins that.

## ✅ FIXED 2026-08-23 L79 — `remove_acl` reported a grant revoked while the grant stayed live (2026-08-23)

The agent/REST revoke never checked what it removed:

```go
// routd/acl_resource.go, before
_, err := tx.ExecContext(ctx,
  `DELETE FROM acl WHERE principal=? AND action=? AND scope=? AND effect=? AND params='' AND predicate=''`, …)
return err                       // …and the handler returned apiv1.OK{OK:true} unconditionally
```

Two things compound. The DELETE pins `params` and `predicate` to `''`, so a row
an operator created WITH a narrowing — `dashd/grants_admin.go:160` offers exactly
that (`jid=telegram:*`), and package-installed grants carry a predicate — cannot
be matched. And with no rows-affected check the call answered `{"ok":true}`
regardless. `list` DOES return `params`/`predicate`, so a caller could see a row
it could not revoke and be told the revoke worked.

The store twin gets it right (`store/acl.go:84-90`, full PK), as do
`onbod/invites_resource.go:104` and `routd/membership_resource.go:79`, which both
check rows-affected.

**Fixed**: `revokeACLTx` returns the row count and the handler refuses `404` when
it is zero, naming why — a revoke is the one operation where "did nothing" must
never read as success. The refusal text points the operator at where a narrowed
grant can be revoked.

NOT changed, deliberately: this face still addresses only un-narrowed rows.
`grantACLTx` writes `params='', predicate=''` too, so grant and revoke stay
symmetric; widening both to carry a narrowing is an additive contract change,
recorded as `L80`, not smuggled into a fail-loud fix.

Live rows affected: zero — no `params`-bearing acl row exists on any of the three
instances. The defect is the shape, on an auth surface.

## L84 — dashd's "Delete group" is a fourth delete renderer L71 missed, and it strands tasks (2026-08-23, proposal)

`L71` unified three group-delete paths into `store.DeleteGroup`. It missed the
one an operator is most likely to use: the dashboard button
(`dashd/groups_admin.go:437-464`), which opens its own transaction and cascades a
DIFFERENT set. The two operator-reachable paths now strand disjoint things:

| cascade | `store.DeleteGroup` (CLI `group rm`) | dashd handler |
| --- | --- | --- |
| child groups | no | **yes** |
| acl | no | **yes** |
| routes | yes | yes |
| `chat_reply_state` | yes | no |
| `scheduled_tasks` | **yes** | **no** |
| `sessions`, `network_rules`, `acl_membership`, `installed_packages` | no | no |

So a dashboard delete leaves the group's `<folder>-mem-N` rows `active`, timed
fires them forever at a folder that no longer resolves, and onbod reads each
unroutable jid as a new account to onboard — the exact `L66`→`L67` chain, still
reachable. Tasks at risk per dashboard-delete: krons 61, sloth 37, marinade 36
active rows across 12/7/7 task-owning folders.

The handler's own comment is now false: "store.DeleteGroup can't stand in: it
opens its own tx and touches only groups" (`:428`) — untrue since `ae559c7a`
earlier the same day.

**Proposal (needs sign-off — this changes dashd's control flow and widens
store.DeleteGroup's contract):** dashd delegates to the unified delete for each
folder in the subtree, and `store.DeleteGroup` absorbs the subtree + acl cascade
dashd has, so ONE renderer owns the whole thing. The four tables neither path
touches (`sessions`, `network_rules`, `acl_membership`, `installed_packages`)
should be decided in the same pass rather than left to a fifth caller.

Note `L78` already fixed the shared half — all these cascades now use
`groupfolder.SQLSubtree` instead of `LIKE`.

## L85 — the smoke is 6/9 after the SDK jump L73 exposed; the three failures are all child-spawn callbacks (2026-08-23, open)

`L73` fixed the ant image so it ships the SDK it installs. The practical effect
is that the fleet moved from `@anthropic-ai/claude-agent-sdk` **0.3.153 to
0.3.237** in one step — 84 patch versions — because the host copy had been
winning silently for an unknown length of time.

The last smoke on 0.3.153 was 9/9 (11:57). The first full smoke on 0.3.237 is
6/9:

```
child-delegate  FAIL  240s  1765 tok  no callback
priv-grant      FAIL  180s  6112 tok  GET probe: unsupported protocol scheme ""
webhook-in      FAIL  150s  1913 tok  no callback
(the other six pass, including two that DO receive sink callbacks)
```

What is ruled out: delegation itself works — both child groups were created and
both child turns reached `done`. The sink is reachable — `fresh-context` and
`self-skill` received callbacks in the same run. Grants are intact. The eval
folder's egress rule is `*`.

What is NOT ruled out, and is the obvious suspect: the SDK jump. `priv-grant`
returning the literal string `probe` where a URL belongs reads like changed agent
behaviour, not a Go defect.

An earlier run in the same session scored 1/9 and was NOT this: the harness was
pointed at a docker gateway that a `systemctl restart` had re-allocated
(192.168.160.1 → 192.168.192.1). That is fixed — anteval now refuses a run whose
`--sink` it cannot reach.

**Next test, not yet run**: build the ant image with the SDK pinned to 0.3.153,
re-run the three cases. If they pass, the jump is the cause and the decision is
whether `@latest` stays in the Dockerfile — it is deliberate and documented
there, so that is the operator's call, not a fix to make silently.

Until then the fleet runs an SDK version no smoke has passed at 9/9.

## L86 — one Discord channel is three sessions, and a triggered message is never ambient for its siblings (2026-08-23, proposal)

Reported from sloth as "before observed worked well, now it's selective and
unusable" and, in the channel itself, `dont yiu see the history?`.

Measured on sloth's `#trading` channel (`main/trading`), 2026-08-23:

```
15:58  topic 1541055376747659294   user      15:59 bot replies in that topic
16:04  topic (root)                user      16:05 bot replies
18:13  topic 1540726266351722526   user      18:13 bot replies in THAT topic
18:13  topic 1541055376747659294   user      18:14 bot replies in the OTHER topic
18:14  topic 1540726266351722526   "dont yiu see the history?"
```

Three live sessions for one channel — two Discord threads plus the root. Each
`sessions` row carries its own `session_id` and its own `observed_cursor`, so a
turn in thread A cannot see a word said in thread B. In the Discord UI the human
sees one interleaved conversation and the desk answering the wrong thing.

The threads are real: `discd/bot.go:187-200` sets `topic` only when the channel
IS a thread and rewrites the JID to the parent. Discord names a thread after the
message it was started from, which is why 57 `sessions` rows on sloth have a
topic equal to a root message's id. That part is correct.

The gap is what crosses between them. `<observed>` carries `is_observed=1` rows
only (`specs/5/B`), and a message that TRIGGERED a turn is `is_observed=0` — so
sibling topics never become ambient for each other by construction. `5/F` made
the cursor per-`(folder, topic)` for a good reason (topic A used to eat topic B's
window); nothing was added to let a topic see its siblings' primary traffic.

`observe_group` already implements exactly this shape — "pick up source's
primary-delivered (`is_observed=0`) messages as ambient" — but only across
FOLDERS. The same question intra-folder has no answer.

Second finding — and it RETRACTS the first version of this entry. I claimed the
envelope's budget had become inverted (22KB of `<episodes>` against 349 bytes of
conversation) and read that as the regression. Measuring every turn envelope on
`main/trading` back to June says otherwise:

```
day         turns  avg_prefix  avg_conv  avg_msgs
2026-06-14    209        2016      1399       1.1   <- when it worked
2026-07-15     14       15747      1102       1.0
2026-08-08      8       22014      1239       1.0
2026-08-23     46       22474      1409       1.1   <- now
```

`avg_conv` and `avg_msgs` are FLAT since June. The `<messages>` block has always
carried ~1 message; the agent has always relied on SDK session resume for
history. So the envelope did not regress and the inverted ratio is not the cause.

What did move is `avg_prefix`: **2016 → 22474 bytes, 11x since June**, gradually,
with no step at any deploy. Worth its own entry, not this one.

The measured step change is topics: the bot replied in 1–2 topics per day for
months, then 12 on 2026-08-21 and 4 on 08-23. Since history lives in the SESSION
and not the envelope, splitting a channel into N sessions is exactly what costs
the history — which is what `L87` fixes on the fork axis.

**Proposal (needs sign-off — this changes the prompt-context contract):**

1. A folder's OTHER topics' primary messages become ambient for a turn, capped by
   the existing `observe_window_*` knobs and tagged so they are distinguishable
   from same-topic history. This is `observe_group`'s mechanism applied
   intra-folder, not a new one.
2. Cap `<episodes>` growth — 11x since June with no step is a slow crowd-out,
   not the reported break. Separate change, separate evidence.

Both are behaviour changes on every turn on every instance, so neither ships
inline. `5/B` and `5/F` are the specs to amend.

## ✅ FIXED 2026-08-23 L87 — a forked topic inherits the parent's transcript but not its ambient backlog (2026-08-23)

`EnsureTopicLineage` stamped the child's `observed_cursor = now`:

```sql
INSERT OR IGNORE INTO sessions(..., forked_at, observed_cursor) VALUES(?,?,?,?, now, now)
```

The fork's other half copies the parent's Claude transcript (`L54`, 2026-08-20),
and the stamp is only correct while that transcript carries the rest. It does
not. A transcript holds the turns that RAN in the parent, and a busy channel's
traffic is `is_observed=1` rows that never ran one — on sloth's `#trading`, 20 of
37 messages in 24h. So a new Discord thread opened blind to everything said in
the channel before it existed.

Measured: thread `1540726266351722526` forked at 18:13:05 with
`observed_cursor = 18:13:05`, while the channel conversation it needed ran
15:58–18:13. Its parent (the root topic) sat at cursor 15:10:44.

**Fixed**: the child inherits the PARENT's cursor, falling back to empty when the
parent has no row — the normal cold path, where `ObservedSince` reads the whole
trailing window already capped by `observe_window_messages` / `_chars`.
Re-showing a message the parent saw is benign; the standing rule is "observed
messages are context, not requests", which the cursor's at-least-once advance
already depends on.

Test fails on the old behaviour (verified by mutation: child cursor came back as
`now`, not the parent's).

## ✅ FIXED 2026-08-23 L88 — `/resolve` recalls files, never the channel; the agent has no instruction to read it (2026-08-23)

Asked whether the agent researches and recalls before starting a topic. It runs
the triage — `resolve` fires 90 times across `main/trading`'s transcripts, so the
`[resolve]` nudge works. What it recalls is the problem.

`resolve` step 2 "Recall" reads `~/diary`, `~/facts`, `~/users` — files the agent
wrote. Nothing in it reads the CHAT. Measured tool use over the folder's entire
transcript history:

```
compact-memories 157 · resolve 90 · migrate 44 · market-context 35
inspect_messages  33 · fetch_history 12 · recall-memories 1
```

`fetch_history` — whose own description is "reconstruct context before replying,
especially on first contact or after a reset_session" — ran **12 times ever**.
`recall-memories` ran **once**.

Two causes, both prompt-level:

1. `resolve`'s recall step never names a channel-history tool, and step 1 tells a
   continuation to skip recall entirely.
2. `ant/CLAUDE.md` did name a tool for this — the WRONG one. It said "call
   `inspect_messages` for recent history", while `inspect_messages`'s own
   description says "Not for conversation context before replying
   (fetch_history)". The instruction and the tool contract disagreed.

**Fixed**: `resolve` step 2 now calls `fetch_history` on the chat FIRST, before
the file recall, with the reason stated — a thread forked off a busy channel
inherits the transcript of turns that RAN there, which is not what people
actually said. `ant/CLAUDE.md` now names `fetch_history` and says why
`inspect_messages` is not it.

Pairs with `L87` (the mechanical half: a fork now inherits the parent's observed
backlog instead of stamping `now`). `L87` gives the turn the ambient window;
this makes the agent go and read the conversation when it starts fresh.

Threading itself is NOT at fault and was checked: every bot reply on
`#trading` since 2026-08-01 carries its trigger's topic, and the only cross-topic
rows are the expected root→new-thread creations.

## L80 — the agent/REST acl face cannot express a narrowed grant (2026-08-23, proposal)

`grantACLTx` hardcodes `params='', predicate=''` on INSERT and `revokeACLTx`
matches the same, so the resreg acl resource can neither create nor address a
narrowed grant. Only `dashd`'s form and the CLI can, through the store writer.
`list` returns `params`/`predicate`, so the face shows what it cannot manage.

Since `L79` the mismatch is at least loud (a revoke that matches nothing 404s).

**Proposal (additive contract, needs sign-off):** `add_acl`/`remove_acl` and the
REST twins accept optional `params` and `predicate`, defaulting to `''` so
existing callers are unaffected, and both writers bind them. That makes the
resreg face the one renderer for the acl table instead of a lossy subset of it.

## ✅ FIXED 2026-08-23 L78 — group-delete cascades used SQL `LIKE`, so deleting one folder deleted another's rows (2026-08-23)

Five DELETE statements expressed folder containment as `col = ? OR col LIKE ?||'/%'`.
SQL `LIKE` is the wrong predicate twice over: it is case-INSENSITIVE for ASCII,
and `_` and `%` are wildcards. Measured on sqlite 3:

```
$ DELETE FROM routes WHERE target='a_b' OR target LIKE 'a_b'||'#%';
  → also deleted axb#observe        (`_` matched `x`)
$ DELETE FROM routes WHERE target='coach' OR target LIKE 'coach'||'#%';
  → also deleted Coach#observe      (LIKE folds case)
```

The case half is not hypothetical: sloth carried a `Coach`/`coach` pair for
3.5 months (`L66`). The codebase already knew — `routd/db.go:753` avoids LIKE in
`ListEngaged` for exactly this reason — but the five cascades did not.

`register_group` made the metachar half reachable: it was the ONLY create path
that skipped `IsValidFolder` (the CLI, dashd, invites and archive import all call
it), so an agent could create `acme/a_b`. Worse, `registerGroup` git-inits
`filepath.Join(groupsDir, folder)` and `Join` resolves `..` — a mutation check
confirms `world/../../etc` registered with `outcome=ok`, because the authz glob
`world/**` matches the raw string and containment never parses it.

**Fixed.** `groupfolder.SQLSubtree` / `SubtreeArgs` is the SQL twin of
`groupfolder.Contains` — an exact `substr` comparison with no metacharacters and
no case folding — and all five sites use it (`store/groups.go`,
`routd/routes_resource.go`, three in `dashd/groups_admin.go`). No destructive
`LIKE` remains: `grep "DELETE FROM" | grep LIKE` is empty. `register_group`
validates the folder. Both are covered by tests that fail on the un-fixed code,
the SQL one against a real sqlite.

Live blast radius today: zero rows — no metachar or case-variant folder exists on
any of the three instances.

## ✅ FIXED 2026-08-23 L74 — 14 recurring tasks are `active` with no next run, dead since June (2026-08-23)

```
$ sqlite3 routd.db "SELECT COUNT(*) FROM scheduled_tasks
                    WHERE status='active' AND COALESCE(cron,'')<>'' AND COALESCE(next_run,'')='';"
sloth 9 · marinade 5 · krons 0
```

The due claim is `WHERE status='active' AND datetime(next_run) <= datetime(?)`
(`store/tasks.go:138`). `datetime('')` is NULL, and `NULL <= …` is NULL, so such
a row is never claimed again while still reporting itself as scheduled. Nothing
recomputes `next_run`, so the state has no exit.

`main/content-mem-0..4` were created 2026-05-25 and have **zero** rows in
`task_run_logs` — that group's memory compaction has never run. sloth `main`'s
daily and weekly compactions last fired 2026-06-09/15; marinade's whole atlas
daily set last fired 2026-06-16.

Creation is not the cause: `store.SeedDefaultTasks` writes `next_run = now`
(`store/tasks.go:269`), and every stranded cron here parses fine. The rows date
to the June split cutover.

**Fixed, two halves.** Data: the 14 rows were backed up
(`scratchpad/stranded-tasks-backup.sql`) and given their real next occurrence;
0 were due on write. Producer: `timed/split.go` could still reach the same state
— `computeNextRun` returns `""` on a cron parse failure (`timed/main.go:43`) and
the old code wrote `('', 'active')` for it. A bad cron is a misconfiguration, not
a transient fault, so it now sets `status='error'` and logs ERROR instead of
leaving a row that lies about being scheduled.

## L75 — a dispatch failure loses the trigger silently: the retry never retries, and pending mentions get demoted to "observed" (2026-08-23, proposal)

Two faults with one root: a dispatch failure path advances the cursor past the
work it did not do.

**(a) The retry is a dead promise.** `routd/dispatch.go:449-460` logs
"scheduling retry 1/3", then returns `(false, false, false, "", nil)`. That shape
is indistinguishable from "batch handled", so the caller falls through to
`l.advance(chatJID, last)` at `:144`. The `time.AfterFunc` fires 10s later,
calls `ResetTurnForRetry` + `Enqueue`, and the re-poll reads `NewMessages` from
the **already-advanced** cursor — nothing to dispatch. The turn strands `running`
until `SweepExpiredRunning` (`db.go:964`) flips it to `expired`.

Measured: every retry the fleet logged in 7 days died this way.

```
marinade 2026-08-21 02:01:00 WARN "turn failed without reply, scheduling retry" retry=1 max=3  (x2)
turn_context: sched-atlas/general-mem-0@… → expired
              sched-atlas/search-mem-0@…  → expired
(no further log line for either turn_id, ever)
```

The `busy` branch two hundred lines earlier gets this right and says why: "do NOT
advance the cursor … the batch is re-fed … turn_context stays 'running' so the
re-dispatch is live" (`:126-135`).

**(b) A pending mention is demoted mid-failure.** During the 2026-08-22 disk-full
outage a user @-mentioned sloth three times over 1.5h. Each created a turn, each
spawn failed, the cursor correctly did not advance — and then ordinary chatter
arrived. `dispatch.go:51` resolves a re-fed batch by `newestUserMsg` alone, so
the newest non-mention row selected the `#observe` fallthrough route and the
WHOLE batch, mentions included, was stored `is_observed=1` and the cursor moved
past it. The three turns were swept to `expired`. No reply, no error notice; the
next bot contact was 21h later.

```
1540738584259207179 main/trading 2026-08-22T15:04:58 expired  "@sloth what do you think?"
1540739822694440980 main/trading 2026-08-22T15:09:53 expired  "@sloth ?"
1540759670833160263 main/trading 2026-08-22T16:28:45 expired  "@sloth Hi"
```

This is not outage-specific: any chat with the shipped mention-route +
observe-fallthrough pair loses pending mentions whenever a dispatch failure
overlaps ordinary chatter.

**Proposal (needs sign-off — both change dispatch control flow):**

- (a) the retry branch returns the `busy` shape (no advance, re-feed) and drops
  the `AfterFunc` + `ResetTurnForRetry` entirely: the poll re-feed already IS the
  retry, exactly as the transport-failure path works. One return value, but it is
  the dispatch lifecycle, so it is recorded here first.
- (b) route resolution must not demote a message that already owns a live
  `turn_context`. Either resolve per-message and split the batch by resolved
  route (it already splits by sender), or exclude rows with a live turn from the
  newest-message resolve.
- Independently of both: `SweepExpiredRunning` expiring a turn with no bot reply
  must mark its trigger errored and deliver `runFailureNotice`. Today the turn
  dies with no user-visible trace at all, which is the fail-loud rule broken on a
  user-facing path.

## ✅ FIXED 2026-08-23 L76 — a message no one can receive is stamped `sent` (2026-08-23)

`routd/turns.go` `deliverRow` skips delivery for a JID with no `:` prefix and
sets `row.Status = MessageStatusSent`. No channel owns a prefixless JID, so the
row records a delivery that never happened.

```
sloth  mcp-1786912578405198742  chat=coach                  status=failed attempts=10
       "/migrate v199→v201 done — but a new guard is breaking skills. Needs operator."
krons  mcp-1787239846679434451  chat=krons/public/marble     same shape
later migrate reports to `coach` (08-20/21): status=sent, attempts=0 — delivered nowhere
```

The skip itself is deliberate and documented (it stops breaker spam). Stamping
`sent` is the defect: at least two operator alerts are provably lost, one of them
reporting a skills-breaking guard. The agent-side twin fails loud
(`no channel for jid coach`); only the `submit_turn` result path swallows.

**Fixed**: the row lands `failed`, not `sent`, and the skip logs WARN instead of
DEBUG. No new status was invented — `failed` IS the dead-letter state and
`delivery_attempts=0` already means "refused permanently", so the content becomes
visible on the dead_letters surface the operator already has. Nothing re-sends
it: `PendingOutbound` selects `status='pending'` only, so the breaker the skip
was written to protect is untouched.

The real fix is proactive-operator delivery via `role:operator` routing, which
`ARCHITECTURE.md` already lists as unshipped; that is a redesign and not
proposed here.

## L77 — four shipped skills tell the agent to use subagent types the container does not have (2026-08-23)

`ant/skills/sonnet/SKILL.md:8` instructs `ALWAYS use subagent_type: "sonnet"`,
and `opus/`, `haiku/`, `fable/` say the same for their tiers. The agent container
has no such types:

```
sloth transcripts, 7d: 3x  Agent type 'sonnet' not found.
  Available agents: claude, Explore, general-purpose, Plan, statusline-setup
```

There is no `ant/agents/` directory, no `.claude/agents/` in group homes, and
`ant/src/claude.ts` passes no `agents` map to the SDK — only
`DEFAULT_MODEL='claude-opus-5'`. The skills were synced from the host toolkit
where those definitions exist; the definitions were not ported.

Every tier dispatch therefore runs at the parent's model and effort instead of
the cheaper one the skill promises — a cost leak, unmeasured.

**Fix**: ship the definitions (add `agents` to the SDK options in
`ant/src/claude.ts`, or seed `.claude/agents/*.md`), which keeps the effort
pinning the skills promise. Rewriting the four SKILL.md files to use `model:` on
the default type is the smaller alternative and loses the effort pin.

## ✅ FIXED 2026-08-23 L71 — the operator's only group delete cascades nothing and writes no audit row (2026-08-23)

Three functions delete a group, and the one an operator can actually run is the
weakest:

| path | reachable by | cascades | audit |
| --- | --- | --- | --- |
| `store.DeleteGroupRow` | `arizuko group rm` | nothing | no |
| `store.DeleteGroup` | nothing (tests only) | nothing | yes |
| `routd.DB.DeleteGroup` | `registerGroup` rollback | routes, engagement, tasks | no |

So the L66 cascade shipped on 2026-08-22 lives on a path no operator can invoke,
and `arizuko group rm <folder>` leaves exactly the wreckage L66 was about:
routes aimed at a folder that no longer resolves, `chat_reply_state` pinned to
it, and `scheduled_tasks` timed keeps firing forever (which then reaches onbod
and mints a pairing token for an unroutable jid — `L67`).

There is no foreign key to fall back on. Neither `routes` nor `scheduled_tasks`
declares one, and the live DBs run with `foreign_keys=0`:

```
$ sudo sqlite3 .../krons/store/routd/routd.db "PRAGMA foreign_keys;"
0
```

Live damage, measured 2026-08-23:

```
instance   orphan routes   orphan engagement rows
krons      0               0
sloth      0               0
marinade   1               4
```

**Fixed**: one renderer. `store.DeleteGroup` now does the whole cascade in ONE
transaction (a partial cascade IS the orphan state) and stamps the audit row
through `auditIdentity()`, so a CLI delete is attributed to the operator instead
of to "system". `routd.DB.DeleteGroup` delegates to it; `DeleteGroupRow` is
deleted; `arizuko group rm` calls the one function.

`route_tokens` is deliberately NOT cascaded — a token for a deleted target is
refused at webd's ingress (`L5`).

`TestRegisterGroupRollsBackOnAddRouteError` forced its failure by dropping the
`routes` table, which then aborted the now-transactional rollback and left the
orphan it guards. It fails the INSERT with a trigger instead; the invariant is
unchanged.

## ✅ FIXED 2026-08-23 L73 — every agent image shipped the host's stale Anthropic SDK, not the `@latest` it installed (2026-08-23)

`ant/Dockerfile` installs the SDK deliberately and says why:

```dockerfile
# Anthropic SDK auto-updates to latest on every image build; overrides
# the version pinned in package-lock.json. ...
RUN npm install @anthropic-ai/claude-agent-sdk@latest
COPY . ./
```

`ant/` is its own build context (`ant/Makefile`: `docker build -t arizuko-ant .`),
so the repo-root `.dockerignore` never applied to it and there was no
`ant/.dockerignore`. `COPY . ./` therefore copied the host's 521MB
`ant/node_modules` straight over `/app/node_modules` — including the SDK the
line above had just fetched.

Measured before and after adding `ant/.dockerignore`:

```
host ant/node_modules  → 0.3.153
image, before          → 0.3.153   (the host's copy won)
image, after           → 0.3.237   (what @latest actually resolves to)
```

84 patch versions behind, on every agent turn the fleet has run. Two live turns
on krons completed on 0.3.237 after the rebuild, so the jump is not itself a
regression.

**Fixed**: `ant/.dockerignore` excluding `node_modules`, `.next`, `dist`. The
repo-root `.dockerignore` also gained `.claude/` and `.refs/` — 6.2GB of
subagent worktrees and reference checkouts that `COPY . .` was transferring on
every build (2.8GB context) and that busted the layer cache whenever a session
wrote a file.

Related, NOT fixed: 37 stale subagent worktrees under `.claude/worktrees` (6.2GB)
that nothing cleans up. Removal is recursive and some may hold uncommitted work —
operator's call.

## L72 — anteval leaks a group per case and every release pays an LLM turn for each (2026-08-23, proposal)

`anteval` cases create child groups (`register_group` / `delegate_group`) and
nothing ever deletes them. 31 of krons' 43 groups are eval leftovers:

```
$ sqlite3 routd.db "SELECT COUNT(*), SUM(folder LIKE 'eval/%') FROM groups;"
43|31
```

They are not inert. `routd.checkMigrationVersion` spawns an auto-migrate turn
per group on every version bump, so each leaked folder bills a real LLM turn
forever:

```
$ sqlite3 routd.db "SELECT SUM(cost_cents), COUNT(DISTINCT turn_id) FROM cost_log
                    WHERE turn_id LIKE 'auto-migrate-eval/%';"
4538|113
```

**$45.38 across 113 turns**, for folders that exist only because the harness
never cleaned up — and it grows by two folders per smoke run. Each also leaves a
docker network behind (`docker network ls` shows 28 `arizuko_krons_eval-*`).

**Proposal (needs sign-off — anteval gains a write it does not have today):**
anteval deletes the child groups its run created, at end of run. `L71` makes the
delete correct; what is missing is a surface — the groups resource registers
`list` and `register` only (`routd/groups_resource.go:41`), so there is no REST
or MCP delete for anteval to call. Either register the delete action on that
resource (one resource, the containment gate it already has), or have anteval
shell out to `arizuko group rm` on the instance host.

Not in scope here: whether leaked folders should auto-migrate at all. They
should not, but the cause is the leak, not the migration hook.

## L70 — spend is recorded on three dimensions and readable on none of them (2026-08-22, proposal)

With `L69` fixed, `cost_log` carries folder, model and user_sub on every row.
Nothing can read them together.

`GET /v1/cost` answers exactly one question — one turn, summed across models:

```
GET /v1/cost?turn_id=<id> → {turn_id, folder, input_tokens, output_tokens, cost_cents}
```

The aggregate readers are `SpendTodayFolder` and `SpendTodayUser`, and their only
callers outside `budgetGate` are `arizuko budget show`, which reads the DB file
directly. So per-group and per-user spend has:

- no REST surface and no MCP tool — an agent cannot answer "what has this group
  spent", and neither can an external tool;
- no window but today — the UTC-day sum the gate needs, nothing else;
- no breakdown — never per model, never per user within a folder, never per day.

That fails `CLAUDE.md`'s standing rule directly: **every cold-tier management
entity is a resreg resource, no exceptions.** `cost_log` is one, and it is
reachable only through a CLI that opens the file.

**Proposal (needs sign-off — this is a new contract):** register a `spend`
resreg resource on routd, read-only, per `5/17` (REST + derived MCP, the injected
gate binding `folder` to the caller's folder exactly as `/v1/cost` already does):

```
GET /v1/spend?folder=&user_sub=&model=&since=&until=&group_by=folder|user_sub|model|day
    → [{folder, user_sub, model, day, input_tokens, output_tokens, cost_cents}]
```

One resource, one handler, the containment check the other cold-tier resources
already use. `idx_cost_log_folder_day` and `idx_cost_log_user_day` already index
both hot paths.

Explicitly NOT in this proposal: whether the numbers can be trusted. That is
`specs/6/23` — cost is still self-reported by the spender, and a gateway is what
makes it authoritative. The two are orthogonal: `L69` made the row attributable,
this makes it readable, `6/23` makes it true.

## ✅ FIXED 2026-08-22 L68 — anteval starves its own tail: one overrunning case times out every case after it (2026-08-22)

anteval runs cases sequentially into ONE folder, and arizuko correctly runs one
turn per folder at a time. `Drive` calls `ensurePristineRoot` before each case,
which sends `/new` and polls until it is consumed — a session reset, NOT a wait
for the folder to be idle. Nothing waits for the previous case's TURN to end.

So a case whose agent keeps working past its own `max_wall_ms` leaves that turn
running, and every later case queues behind it and times out having never
started. The signature is a run whose failures are a contiguous ALPHABETICAL
tail, each reporting `0 tokens`.

Measured on krons 2026-08-22 (nonce `etk5t9p`): `priv-grant` spent 9265 tokens —
by far the run's most — and anteval abandoned it at its 180s budget while the
agent kept going. The `eval` folder then had NO dispatch between 07:14:39 and
07:22:21, 7m42s. `rest-roundtrip` (90s), `self-skill` (120s) and `webhook-in`
(150s) all expired inside that window with 0 tokens each.

The inverse correlation across four runs is the tell: when the heavy case ends
early the tail passes (smoke3: 8/9, only `child-delegate` failing), and when it
runs long the same four fail (smoke2, smoke4: 5/9). The product is behaving
correctly in both.

This has now cost two investigations in one day — the same four FAILs were read
first as an ipc regression, then as an expired token (which was a REAL second
cause, fixed by the startup check in `a17bccec`).

Proposed fix (harness contract change, needs sign-off): before each case, wait
for the folder to hold no in-flight turn, with its own bounded cap and a loud
error when the cap is hit. A case that cannot get a quiet folder must say so
rather than report the next case's starvation as that case's failure.

- **Source:** `anteval/pkg/run/run.go` (`Drive`, `ensurePristineRoot`).

**FIXED 2026-08-22** (`10969e54`): `Drive` waits for a case's own turn to record
its cost before injecting the next case. A turn that never records one does not
hang the suite — the wait is bounded and the result carries a NOTE that the next
case may have started behind it.

**Verified 2026-08-22**: krons ran 9/9 in a single 12-minute smoke, the first
clean run. One case (`fresh-context`) spent the full 3-minute fallback and said
so — its injected message id never became a turn id, because routd batches a
folder's pending messages and the turn takes the LAST id in the batch. The
bounded wait plus the loud NOTE is exactly what that case is for; the real
signal (does this folder have a live turn) has no read surface, recorded in
`specs/6/23`.

## ✅ FIXED 2026-08-22 L66 — a removed group strands its 5 seeded tasks; they fire forever and route nowhere (2026-08-22)

`SeedDefaultTasks` writes 5 `<folder>-mem-N` rows per group, but
`scheduled_tasks.owner` carries NO foreign key to `groups.folder`, so removing
or renaming a group leaves its tasks behind. They stay `active`, `timed` keeps
firing them on cron, and every firing targets a folder that does not exist.

Two live instances carry the shape:

| instance | orphan tasks | real group |
|----------|--------------|------------|
| sloth    | `Coach-mem-0..4` (`owner='Coach'`) | `coach` exists; `Coach` does not |
| marinade | `libtest-mem-0..4` | no `libtest` group at all |

sloth's pair is a CASE mismatch: `groups` holds exactly one row, `coach`, and
zero `Coach` (checked with `=`, not `LIKE` — sqlite's `LIKE` is
case-insensitive and hides this). Nothing in the folder path lowercases, so the
two names were written by two different creation events.

`turn_results` dates it exactly: folder `Coach` holds ONE turn ever
(2026-05-05T21:29Z, its creation day) and folder `coach` holds 123, the most
recent 2026-08-21. The group ran once under the capitalised name, was recreated
lowercase, and the 5 tasks stayed pointed at the name nothing answers to.

The damage runs both ways:

- The orphans never execute — `Coach-mem-0` has fired daily since
  2026-05-05 and has never once run.
- The REAL group gets nothing: `coach` has no `-mem-` tasks, so its memories
  have never compacted.

Cause fix (needs sign-off, it is a schema change): a foreign key
`owner REFERENCES groups(folder) ON DELETE CASCADE`, matching the FK
`task_run_logs` already has on `scheduled_tasks`. That makes the orphan
impossible instead of detecting it later. Operator cleanup of the 10 existing
rows is separate and is the operator's call.

- **Source:** `store/tasks.go:261` (`SeedDefaultTasks`), `scheduled_tasks` schema.

**FIXED 2026-08-22** (`9b68f8e0`): `DeleteGroup` now cascades `scheduled_tasks`,
following the route/engagement pattern already there rather than adding a
foreign key — that cascade is deliberately non-fatal so registerGroup's rollback
can always remove the group row. Live rows cleared: sloth `Coach-mem-0..4`,
marinade `libtest-mem-0..4` (backed up first). The groups that had lost their
tasks were re-seeded with real next-cron times, not `now`, so nothing fired on
insert: sloth `coach`, marinade `atlas/strengths`, krons `krons/content` and
`rhias`.

## ✅ FIXED 2026-08-22 L67 — onbod mails a pairing link to an unroutable task JID, then logs success over the failure (2026-08-22)

Downstream of `L66`, and a fail-loud violation in its own right.

When `timed` fires an orphan task, its JID (`Coach`) matches no route. onbod's
`promptUnprompted` reads it as a brand-new account to onboard, mints a pairing
token and POSTs a pairing link at it. routd answers `502`. On sloth this has
run every night; the log pair is always:

```
WARN "send reply non-2xx" jid=Coach status=502
INFO "sent pairing link"  jid=Coach
```

`sendReply` returns nothing, so the caller cannot tell a `502` from a delivery
and logs the success line unconditionally — the failure is one line above the
claim that it worked. Worse, the row is CLAIMED before the send
(`prompted_at` is stamped first, deliberately, so two ticks cannot both send),
so a failed send is never retried: the pairing link is minted, lost, and the
row now reads as prompted.

That claim-first order is correct for the race it defends against; the defect
is that `sendReply` swallows its own outcome. It should return an error, and a
failed send should release the claim it took.

Neither the firing nor the 502 writes an `audit_log` row: sloth's audit surface
records exactly ONE non-ok outcome in 7 days (a discord like 502) while this
fails every night. The audit log reads clean through a repeating user-facing
failure.

- **Source:** `onbod/main.go:1421` (`sendReply`), `onbod/main.go:482`
  (`promptUnprompted`).

**FIXED 2026-08-22** (`54994318`): `sendReply` returns its outcome and the
pairing tick logs one error instead of a success line under the failure. The
claim still survives a failed send ON PURPOSE — releasing it would re-mint a
pairing token every tick for a permanently unroutable jid, and WISDOM retries
only transient errors. The two other call sites (refusal, queue notice) now
report a user who was refused or queued in silence.

## L62 — marinade's emaid IMAP login fails, unhealthy for 3 days (2026-08-21, open)

`arizuko_emaid_marinade` has been `Up (unhealthy)` since 2026-08-18. Every IMAP
IDLE cycle logs once a minute:

```
{"level":"ERROR","msg":"imap idle error","err":"login: imap: NO [AUTHENTICATIONFAILED] Invalid credentials (Failure)"}
```

The daemon retries a NON-transient error forever — WISDOM says retry only
transient failures, and an authentication rejection is a configuration fault,
not a network blip. Two separate items:

- **Operator:** the stored IMAP password is wrong or revoked. Email ingest on
  marinade has been dead for three days and nothing surfaced it to a user.
- **Code:** `AUTHENTICATIONFAILED` should stop the loop and surface once, not
  re-log every 60s. Same cadence class as `L61`.

- **Source:** `emaid/`, marinade instance `.env`.

## L63 — krons whapd 401-loops, needs a phone re-pair (2026-08-21, open)

`arizuko_whapd_krons` is `Restarting` continuously. Known cause: the WhatsApp
link is gone and the pairing must be re-established from the user's phone
(pairing-code path, not QR — the recipe is in the whapd pairing memory). Not a
code defect; recorded so the fleet's one permanently-red container has an entry
rather than being treated as normal.

- **Source:** `whapd/`, operator action required.

## Error-hygiene sweep: swallowed / mis-levelled errors in the hot path (2026-07-11, 8 FIXED, rest OPEN)

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
  every tool call for the group. **FIXED 2026-08-15** — both the peer-cred
  read failure and the UID mismatch are now `slog.Error` with the "connection
  rejected, group gets no tool calls" symptom (log-add only, no control-flow
  change).
- `crackbox/pkg/admin/registry.go:84-107` `flushLocked` — `Warn`+return on
  disk-persist failure inside a void fn; handler still ACKs 200 → stale egress
  allowlist can reappear after restart. **FIXED 2026-08-15** — all five
  failure branches (marshal/tmp-create/tmp-write/tmp-close/rename) elevated to
  `slog.Error` naming the "egress allowlist change not persisted, will revert
  on restart" symptom.
- `routd/turns.go:670-676` `recordTurnResult` — discards `PutMessage`/`PutSession`
  errors (this path historically broke all replies post-split — verify first).
  **FIXED 2026-08-15** — verified safe (log-add only, the delivery itself is
  already done via `appendAndDeliver` before the discarded `PutMessage`, so
  logging doesn't touch the reply path that broke in the 2026-06-08 outage).
  `PutMessage`, `PutSession`, `PutCost` (per-model), and `SetTurnState("done")`
  now each log `slog.Error` naming their specific symptom (missing from
  history/audit, lost session continuity, missing cost attribution, turn
  stuck in-progress).
- `runed/docker.go:66` `steerInto` — **FIXED 2026-08-15**: `writeIPCInput`
  failure now logs `slog.Error`, distinguishing a real disk-write failure from
  the documented SIGUSR1 race the function's doc comment describes (both
  previously returned `false` silently).
- `routd/dispatch.go:263,305,320` (renumbered ~300/349/379 at HEAD) terminal
  `SetTurnState("done")` unlogged — **FIXED 2026-08-15**, all three call
  sites (steered, terminal-failure, clean-completion) now log `slog.Error`
  naming "turn may appear stuck in-progress" (or, for the steer site, "may be
  re-dispatched, duplicating output").
- **Still open, unverified:**
  - `runed/manager.go` (steer-related discards near the admit/spawn path,
    line numbers drifted from the original 212-214 citation — not
    independently re-audited this pass).
  - `ipc/ipc.go:340-345` (conn-limit reject, no error frame) — already logs
    `slog.Warn`, so not silent; the gap is a missing JSON-RPC error frame to
    the caller, which needs the request id threaded through `writeJSON` —
    design work, not a log-add.
  - `routd/budget.go:32-43` spend caps fail open on a DB hiccup — already
    logs `slog.Warn` on `FolderCap`/`UserCap` read failure, so not silent
    either; the open question is whether failing OPEN (allow the turn) is the
    right policy when the cap check itself errors, vs. failing closed — a
    policy call, not a log-add.
  - `crackbox/pkg/host/host.go:130-132` — egress-proxy unregister failure on
    VM stop logs to stderr via `fmt.Fprintf`, not `slog` — inconsistent
    structured-logging but not silent. Low priority, left open.

Adapters (teled/discd/slakd/mastd/bskyd/reditd/emaid/chanlib) swept clean for
this class.

## Root-owned group home → agent runs config-less, silently never replies ("typing but no reply") (2026-07-11, ROOT CAUSE FIXED, HARDENING 2/3 DONE, spawn-abort residual OPEN)

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

**Hardening — two of three pieces now DONE, re-verified 2026-08-15:**

- **Fail loud on write failure — DONE** (`01a61a07`). `container/runner.go`
  `seedSettings`'s `os.WriteFile` failure now logs `slog.Error` naming the
  exact symptom: "group home not writable — agent will run config-less and
  not reply; chown the home to the run uid" (`runner.go:899`).
- **Ghost-group guard — DONE** (`c9979e78`, GB1 L4a). `routd/dispatch.go:147`
  `runTurn` now refuses to dispatch when `!l.db.GroupExists(folder)`: logs
  `slog.Error("dispatch refused: unregistered group", ...)`, sends
  `ghostGroupNotice` to the chat, and consumes the batch instead of spawning.
  This directly closes the reproduced `krons/content` scenario (a ghost,
  routed-to but not in `groups`) — that exact case can no longer reach a
  config-less spawn.
- **Abort spawn / deliver an error turn on write failure — still OPEN.**
  `seedSettings` (void fn) and `cpDirImpl` (still `slog.Warn`, not `Error`,
  for its own mkdir/readdir/lstat/read failures) log and `return`;
  `buildMounts`/`container.Run` still have no error return, so a
  **registered** group whose home is nonetheless mis-owned (permission drift
  unrelated to being a ghost) still spawns config-less silently past the
  loud log — an operator watching logs catches it, one polling only
  `turn_results` does not. Propagating a write failure into an aborted spawn
  + delivered error turn crosses the `container`→`runed`→`routd` call chain
  (`Input`/`RunResult` contract change) — a redesign, needs sign-off, not
  attempted here.

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
  **Re-verified 2026-08-15 at `bcb5bb83`; still exactly as described, still a
  redesign.** `store.UserScopes` is gone — `store/acl.go:196` is now only a
  pointer to `auth.UserScopes` — so the second READER really did close. The
  live half is unchanged: `dashd.callerScope` (`dashd/authz.go:44`) reads the
  `X-User-Groups` header, falls back to `auth.UserScopes(sub)`, and treats
  `slices.Contains(allowed, "**")` as operator; `visible()` then decides
  200/403 by `auth.MatchGroups` over that list. There is no `auth.Authorize`
  anywhere in dashd's read path. Not fixed here: closing it picks which action
  dashboard read requires and moves the gate off a `[]string` header that
  cannot represent deny — an auth-model change, which is precisely the class
  that ships only after sign-off.
- **Found:** specs/5 frontmatter audit; `5/32` demoted to `partial`, now shipped.

## F77 — skill-guard's per-line scan is evadable by splitting a payload (2026-08-08, PARTIALLY FIXED 2026-08-08)

The demonstrated case is fixed: shell line-continuations are joined before
scanning, so `curl \` + newline + payload no longer scans `safe` while the
one-line form scans `dangerous`. Reproduced by codex and independently before
the fix; regression test in `ant/src/skillguard.test.ts`.

The CLASS remains open. A payload assembled from string concatenation, built in
a variable, or split across a here-doc still passes a regex table that reads one
line at a time. Closing it means AST or dataflow analysis, which
`specs/5/23` explicitly puts out of scope.

Not a containment boundary — Docker, the crackbox egress allowlist and the gated
MCP socket are, and `SECURITY.md` states this. Recorded so the residual is not
mistaken for coverage.

**Re-verified 2026-08-15 at `bcb5bb83`: accurate as written, no action.** The
demonstrated fix is present and is the one described — `logicalLines`
(`ant/src/skillguard.ts:46`) joins a line ending in `\` with the next before
scanning. The residual class is genuinely out of scope: closing it needs AST or
dataflow analysis, which `specs/5/23` excludes by decision, so there is no
contained fix to make and nothing to close. Left open deliberately as a
recorded residual, which is what this entry is for.

## ✅ FIXED 2026-08-22 L65 — `delegate_group` and `escalate_group` were dead in the split routd (2026-08-21)

Caught by anteval `child-delegate`, which is exactly what that suite is for.

Evidence from krons routd.db + journald for run `etk57vv` (2026-08-21T23:29Z,
eval turn `82bc4d61`):

- `register_group` succeeded at 23:29:52 — the child folder
  `eval/etk57vv-child-delegate` exists.
- the agent called `delegate_group` at 23:29:56 — the tool-log PRE line is
  there, and **no POST line**.
- `ipc/ipc.go:1957`'s `"delegating to child"` INFO never fired; no child
  message row, no child dispatch, no error surfaced at transcript-visible level.
- the agent then fell back to a plain `send` to
  `web:eval/etk57vv-child-delegate` (23:30:17). That writes a bot message, which
  **wakes no turn**, so the child never ran and the callback never came.

Reproduced 2026-08-21T23:52Z (`etk58og`): still FAIL, 150s cap, same shape.

**Triage candidates** (not yet distinguished): the granted-row authz on the tool
(scope glob vs the target folder), or the handler erroring before its first log
statement. The tool log's `args_summary` is empty for this call, which hides the
arguments and is its own small gap.

**A wrong theory, recorded so it is not retried:** I proposed that the child had
no `egress` capability because 5/33 says a path carries zero authorization. That
is wrong for egress — egress is NOT an `acl` row. It lives in `network_rules`,
and `routd/network.go` `ResolveAllowlist` walks the full ancestry at dispatch,
so a child DOES inherit its parent's allowlist. Amending the case to grant the
child egress did not fix it (the reproduction above ran with that prompt).

**ROOT CAUSE, found 2026-08-22 — two faults stacked, and my first two theories
were both wrong:**

1. **The tool was not in the agent's list at all.** `folder:eval` held
   `mcp:delegate_group` scoped `eval/**`, which covers the subtree BELOW eval,
   not `eval` itself. The agent's own transcript said it: *"gated socket shows
   no register_group/delegate_group/routing tools, just the 22-tool member
   floor."* Granting scope `eval` made the tool appear.
2. **Then it failed on its own wiring guard:** `delegate_group not configured`.
   Both `escalate_group` and `delegate_group` guarded on
   `gated.EnqueueMessageCheck != nil`. routd NEVER sets that field — its loop
   polls new messages by timestamp, which `routd/mcp.go:121` states outright for
   `InjectMessage`. So both tools were registered, advertised to the agent, and
   returned "not configured" on every call since the split.

**FIXED**: the guard now requires only `db.PutMessage`, and the enqueue is
called only when present — it is a latency optimisation, not a dependency.
Test `TestDelegateGroupWorksWithoutEnqueueHook` drives the real tool over the
socket with `GatedFns{}` (nil enqueue), exactly as routd constructs it.

- **Source:** `ipc/ipc.go` `delegate_group` + `escalate_group`;
  `routd/mcp.go:121`; `anteval/cases/anteval.toml` `child-delegate`.

## ✅ FIXED 2026-08-21 L64 — isolated crons re-enter the chat through the ambient window (2026-08-21)

`L54` closed the SESSION path: an isolated scheduled run gets its own one-off
container and never resumes or persists the chat's session, so a folder's crons
stop writing themselves into the conversation. Their rows still land
`routed_to = <folder>` with `is_observed = 1`, and `ObservedSince` filters by
folder with no sender exclusion — so the ambient window served every cron prompt
back to **every topic in that folder** on its next turn. The same hole, through a
second door.

Measured on sloth `main/trading` (2026-08-21): the `<observed>` block of a live
Discord-thread turn was dominated by 8-a-day `timed-isolated:` "Silent cron: …
stay silent" prompts. 113 observed entries across recent transcripts.

**FIXED**: `core.IsolatedSenderPrefix` / `core.IsIsolatedSender` declare the
marker once; `routd/prompt_db.go`'s ambient query excludes those senders from
both UNION arms, and `routd/dispatch.go`'s three literal prefix tests now read
the same declaration. Test `TestObservedSinceExcludesIsolatedCronRuns` asserts
the cron row qualifies on every OTHER predicate first, so it cannot pass
vacuously.

**NOT fixed, and not a bug**: the folder-scoped ambient window itself. `5/F`
records that decision — the cursor is per-`(folder, topic)` precisely so every
topic sees the folder's ambient traffic once. A forked topic also inherits its
parent's transcript by design (that is what `L54` restored). Two sloth session
files legitimately carry root turns plus one thread's turns.

- **Source:** `routd/prompt_db.go` `ObservedSince`; `core/types.go`; `specs/5/F`.

## ✅ FIXED 2026-08-21 L61 — stranded onboarding rows re-log at ERROR forever (2026-08-21)

**FIXED** by `warnedStranded` (a `sync.Map` in `onbod/main.go`): each
`(jid, status)` pair is reported once per process, and a row whose status
CHANGES warns again because that is a new fact. Confirmed on TWO instances, not
one — `telegram:user/1112184352` on sloth and `telegram:user/1612831875` on
marinade, 6 ERROR lines a minute each. The rows themselves still need an
operator to set or delete them; arizuko will not guess a status for a row of
unknown provenance. Test: `TestStrandedRowWarnsOncePerProcess`.

sloth carries an `onboarding` row for `telegram:user/1112184352` with
`status='pending'`, which is not in `knownStatuses`
(`awaiting_message`/`token_used`/`queued`/`approved`). `warnStrandedRows`
(`onbod/main.go:387`) scans the whole table on a timer and logs one ERROR per
unknown row per scan — 6 in a 60-second window after a restart.

The warning is deliberate and correct: its comment says arizuko will not pick a
status for a row of unknown provenance, because that is the operator's call,
and "silence is what let them sit unnoticed". The defect is the CADENCE, not
the decision. An operator-actionable data condition that cannot change without
human action re-logs at ERROR indefinitely, which trains the reader to filter
ERROR on the one daemon where onboarding failures surface.

- **Not a code-path bug**: no turn, invite or grant is affected. The row simply
  never advances, which is exactly what the log says.
- **Fix shape**: log each stranded jid once per process (a seen-set), or once
  per status transition, and keep the count on the existing health/metrics
  surface. Loud once beats loud forever — the same distinction `L45` draws
  between a config silently ignored and one whose drop is named.
- **Operator fix for the live row**: decide what that jid should be and set it,
  or delete the row if the onboarding attempt is abandoned. arizuko will not
  guess it.
- **Source:** `onbod/main.go:387-408`; live on sloth 2026-08-21.

## ✅ FIXED 2026-08-21 L60 — `ARIZUKO_IS_ROOT` never reached ant: the `/var/lib/share` skip was dead (2026-08-20)

`ant/src/claude.ts` (`extraDirs`, moved 2026-08-20 from `index.ts:251`) reads
`process.env.ARIZUKO_IS_ROOT` to keep an elevated `/root` turn from adding
`/var/lib/share` to `additionalDirectories`. But `seedSettings` writes
`ARIZUKO_IS_ROOT` only into the settings.json **env block**
(`container/runner.go:838`), which reaches the SDK subprocess — never ant's own
process.env; no `-e ARIZUKO_IS_ROOT` exists in `buildArgs`. So the check is
always false and every elevated turn gets `/var/lib/share` as an extra dir
anyway. Same class as `ARIZUKO_MODEL`/`RUNED_RUN_TIMEOUT`/`ARIZUKO_BACKEND`
([ant_env_delivery]): a var ant reads that nothing emits is silently dead.

- **Impact**: cosmetic-to-low — root already mounts `/var/lib/groups` RW; the
  extra read-only context dir is redundant, not an escalation. The agent-side
  `$ARIZUKO_IS_ROOT` doc claim ("echo $ARIZUKO_IS_ROOT shows privilege") holds
  only inside the SDK's shells, not for ant.
- **Fix shape**: either emit `-e ARIZUKO_IS_ROOT=` in `buildArgs` (one line,
  symptom-level) or fold it into the three-layer config render
  (`specs/6/7` § "ant owns the capabilities", next increment) where ant renders
  the settings and therefore knows the flag. The render is the cause fix.
- **Source:** `container/runner.go:694-728` (the `-e` list), `:838`;
  `ant/src/claude.ts` `extraDirs`.
- **Fixed 2026-08-21**: `buildArgs` takes `elevated` and always emits
  `-e ARIZUKO_IS_ROOT=` (`"1"` or empty), so the value is present-and-false
  rather than absent-and-ambiguous — the shape that made the first two
  instances of this class hard to spot. Pinned by
  `TestBuildArgs_EmitsIsRootBothWays`. The cause fix (ant renders the settings
  itself and therefore knows the flag) still belongs to `specs/6/7`'s next
  increment; this makes the documented behaviour true today.

## ✅ FIXED 2026-08-21 L59 — the `5/23` skill-guard cannot gate what codex loads (2026-08-20)

`codex` discovers skills natively from `$HOME/.claude/skills`. In the container
the group dir IS `$HOME` (`container/runner.go:552` mounts it at `/home/node`),
so codex loads the group's skills whether the connector wants it to or not.
Measured on codex-cli 0.147.0 (2026-08-20): a skill written to
`cwd/.claude/skills` was NOT loaded, every `$HOME/.claude/skills` entry WAS,
and `--ignore-user-config` did not change it. codex exposes no flag to restrict
or disable that discovery.

`ant/src/codex.ts` runs `scanSkills` and renders `scan.clean` into a prompt
index. That index is duplicate work — codex already read those files — and the
guard's verdict is advisory: a skill the scan EXCLUDES is still loaded by codex,
it is merely absent from the index the connector appends.

- **The agent writes `$HOME`.** A SKILL.md the agent writes itself is loaded on
  the next turn. That is the self-grant class `strictMcpConfig` closes for
  claude and `--ignore-user-config` / `--no-extensions` close for codex config
  and pi extensions. Skills are the hole left open.
- **CORRECTION 2026-08-20, same day: codex HAS a gate — I missed it.** The
  entry above reasoned "no flag to restrict discovery, therefore no gate".
  codex ships a full hook system: `$CODEX_HOME/hooks.json` declares
  `PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `PreCompact` and `Stop`,
  using the SAME schema as Claude Code's settings hooks (`matcher` plus
  `{type:"command", command, timeout}`), gated by hook trust
  (`--dangerously-bypass-hook-trust` for vetted automation). So the `5/23`
  guard can be a WRITE-TIME gate for codex exactly as it is for claude — a
  `PreToolUse` matcher on the write tools — rather than the advisory load-time
  scan. The absence of a `--skill` flag was never the relevant question.
  Same error shape as [[our-flag-their-limit]] filed an hour earlier: I
  concluded a capability was missing without looking for the mechanism that
  provides it.
- **The real fix is one hook set, rendered per harness.** ant declares the guard
  (and the mid-turn steering drain) ONCE and renders it into each harness's
  native config: claude's settings/SDK callbacks, codex's `hooks.json`, pi's
  extensions. One declaration, many readers — not three guards. That subsumes
  options (a)/(b)/(c) above and is tracked with the seam inversion in
  `specs/6/7` § "ant owns the capabilities" (next increment; the seam itself
  shipped 2026-08-20).
- **FIXED 2026-08-21** by that increment. `ant/src/policy.ts` holds each
  capability as a harness-neutral `(ToolEvent) => Verdict` and one `BINDINGS`
  table; three renderers read it — claude SDK callbacks, a generated
  `$CODEX_HOME/hooks.json` whose commands run `policy-cli.js`, and a pi
  extension loaded by `-e`. Two measurements changed the shape and both are in
  the spec: codex writes with **`apply_patch`**, not `Write`/`Edit`, so the
  write-time matcher never fires there — `skill-rescan` therefore binds to every
  tool and is the gate on codex; and codex **swallows a hook's stderr**, so the
  CLI spools its log lines to a file the harness drains. Verified end to end
  against codex 0.147.0: a turn told to write a SKILL.md carrying `curl … | bash`
  left `…/skills/evil/SKILL.md.quarantined` behind. The guard is a mechanism on
  codex now, not advice.
- **Source:** `ant/src/codex.ts` `CodexSession.events`; `ant/src/skills-scan.ts`;
  `specs/5/23`; the matrix note in `specs/6/7-environment-contracts.md`.

## ✅ FIXED 2026-08-21 L58 — a re-fed turn lost its observed window: the cursor advanced before the container spawned (2026-08-20)

`routd/prompt.go:38` advances the per-topic observed cursor inside
`buildAgentPrompt`, immediately after `ObservedSince` reads the rows and
**before** anything spawns. Nothing rolls it back. The prompt is built at
`dispatch.go:283`; every failure after that point discards the rendered prompt
and re-feeds the batch on the next 2s poll (`dispatch.go:117` transport
failure, `:126` the growing backoff, `:211` runed rejecting admission at
capacity). The re-fed turn calls `buildAgentPrompt` again — and now
`ObservedSince` returns nothing, because the first attempt already consumed the
watermark.

The trigger messages survive (their cursor advances separately, on success).
The **ambient observed context does not**: it is read once and lost for that
topic forever.

- **The comment states the opposite of the code.** `prompt.go:20-25` says
  "at-least-once — the cursor advance is not transactional with the turn; on
  crash recovery a topic may re-see an observed message, which is benign".
  Read-then-advance-then-spawn is at-MOST-once, and the failure is a silent
  loss, not a benign duplicate. A comment is a claim, not evidence.
- **`L55` made this constant.** The starved instance produced 4,704 dispatch
  runs against 18 spawns. Nearly every one of those was a re-feed, and every
  re-feed burned whatever observed rows had arrived since the last one.
  Candidate second cause for the sloth "context is bad or even missing"
  reports — `L54` was the fork half; this is the ambient half. Not yet proven
  against a live transcript.
- **Backend-independent, and worse without a transcript.** For `claude` an
  attempt that reached the model leaves the rows in the resumed transcript, so
  the loss is partial. `pi` runs `--no-session` (`ant/src/pi.ts`), so layer 1
  is always empty and nothing covers it.
- **Source:** `routd/prompt.go:27-39`; re-feed paths in `routd/dispatch.go`.
- **Fixed 2026-08-21**: `buildAgentPrompt` is now pure with respect to the
  cursor — it RETURNS the watermark and writes nothing. `processGroupMessages`
  collects the per-topic marks and applies them after `l.advance(chatJID, last)`,
  the one place a dispatch already commits its progress. That is the
  `serialization_point` move: the second bookkeeping point was removed rather
  than guarded. Every early return above that line (transport failure, busy
  backoff, steer ack) now leaves the window intact for the re-fed turn, which
  makes the read genuinely at-least-once as the comment always claimed.
  A failed `UpdateObservedCursor` is logged, not swallowed. Pinned by
  `TestBuildAgentPrompt_DoesNotConsumeTheObservedWindow`, which renders twice
  and requires the same window both times.

## L57 — the resreg routes face re-implements the route INSERT without the web-JID guard (2026-08-20, open)

`store.AddRoute`/`SetRoutes`/`UpdateRoute` all refuse a `chat_jid=web:*`
match (`ErrWebJIDRouted`, store/routes.go:48,150,170) because web JIDs are
direct-addressed and the route table never applies to them. The agent/REST
face on the routes resreg resource does NOT: `addRouteTx` and `setRoutesTx`
(routd/routes_resource.go) are their own INSERTs on resreg's tx and carry no
`matchesWebJID` check, so an agent's `add_route` accepts a `chat_jid=web:*`
rule that `resolve()` will never consult (direct address precedes the table).
The rule lands, `inspect_routing` annotates it as if it governed, and the
agent believes a web chat is redirected or observe-silenced when nothing
changed — the exact silent-message-loss confusion the skill doc closes with
"`chat_jid=web:*` is rejected" (ant/skills/self/chat-routing.md), which is now
true on only one of the two write paths. Classic one-renderer violation: two
writers of `routes`, one guarded. Found while defusing the anteval
`route-observe-silent` landmine.

- **Severity:** medium
- **Scope:** routd routes resource / store write-discipline
- **Affected:** agent `add_route`/`set_routes`, REST `/v1/routes` add/set
- **Source:** routd/routes_resource.go `addRouteTx`/`setRoutesTx` vs store/routes.go:48
- **Status:** open
- **Fix:**

## ✅ FIXED 2026-08-20 L56 — a bot reply swallows the user's slash command

**First diagnosis was WRONG and is kept here as the correction.** `web:eval`'s
`agent_cursor` read `2026-08-15T21:32:31Z` while its neighbours were current, and
that looked like a permanently stuck chat. It was not stuck — it was lagging one
long batch. Once the last anteval case finished, the cursor advanced to
`12:16:58Z` on its own. Correct measurement, wrong interpretation.

The real fault is narrower and much more general. `processGroupMessages` steered
the batch's **newest** row:

```go
last := msgs[len(msgs)-1]
…
if l.steer(chatJID, last, folder) { … }
```

A slash command is consumed by `steer` and nowhere else, and `handleCommand`
refuses a bot row (`msg.BotMsg` → false). So whenever the agent answers between
the command and the next poll, `steer` inspects the reply, refuses it, and the
command falls through into the turn as plain text. Nothing logs.

Measured on krons, `web:eval`, both attempts identical in shape:

| time     | row                        |
| -------- | -------------------------- |
| 11:58:42 | user `/new`                |
| 12:04:20 | **bot** "Page is published…" |
| 12:08:29 | user `/new`                |
| 12:12:20 | **bot** "⏳ publishing…"     |

Neither `/new` was consumed — no `"Session cleared."` ack was ever delivered, and
`sessions(eval,"")` moved from `948b6564` to `675edf03` because a turn replaced
it rather than `cmdNew` clearing it. anteval's baseline guard then aborted,
correctly.

This is not an anteval quirk: the same hole loses a user's `/stop`, `/root`,
`/gate` or a sticky `#topic` in any chat busy enough to answer first.

- **Severity:** medium (silent; a user's command is executed as chat text)
- **Source:** routd/dispatch.go — `steer(chatJID, last, folder)` on the batch's
  newest row
- **Fix:** steer the newest NON-bot row (`newestUserMsg`), and advance past that
  row only — anything newer has not been consumed.
- **Tests:** `routd.TestNewestUserMsg_{SkipsBotRowsAfterACommand,TakesTheLatestUserRow,AllBotBatchSteersNothing}`

## ✅ FIXED 2026-08-20 L55 — a restart starves the instance: 4,704 dispatches produced 18 spawns (2026-08-20)

Restarting `arizuko_krons` fires `routd.checkMigrationVersion` for every folder
below `MIGRATION_VERSION`, queueing an `auto-migrate-<folder>` turn each. krons
runs `MAX_CONCURRENT_CONTAINERS=2`, so the queue drains two at a time while the
dispatch loop re-feeds every waiting turn on its 2s poll (`L51`).

Measured over 15 minutes after the 11:42Z restart:

| signal                | count |
| --------------------- | ----- |
| `dispatch run`        | 4,704 |
| `mcp server listening`| 4,704 |
| `spawning container`  | **18** |

A 260:1 ratio, and the re-feed is not free: every cycle resolves capabilities and
**starts an MCP server**. In one 60s window four turns — `auto-migrate-mayai`,
`-krons`, `-fiu`, `-eval/etjty2l-child-delegate` — were each dispatched 30 times,
once per poll.

Two things it broke, both diagnosed rather than assumed:

- **anteval scored 0/5 with 0 tokens on every case.** The turns dispatched
  (`topic=etk2gpt-*`, `trigger=user:anteval`) but never got one of the two slots
  inside a case's 120-180s timeout. Not an agent failure and not a deploy defect.
- **The `L54` per-case root reset could not win.** `auto-migrate-eval` runs on
  `folder=eval, topic=""` and re-persists a root session; anteval's new baseline
  guard correctly aborted rather than score inherited history — the guard worked,
  the instance was simply not quiet.

Also visible and separate: `session: resetting on idle expiry` logged 270 times
in the window, once per re-fed dispatch for the same topic, because the reset
decision is re-evaluated every poll on a chat whose cursor is weeks old.

- **Severity:** medium (self-clearing, but it starves real user turns for minutes
  after every restart and burns model budget on migrate turns nobody asked for)
- **Source:** routd `checkMigrationVersion`; the 2s re-feed poll (`L51`);
  `MAX_CONCURRENT_CONTAINERS=2` in `env/runed.env`
- **Status:** resolved-not-yet-removed. Of the shapes costed (backoff / stagger
  the migrate wave / operator-gated auto-migrate / the `L51` push path), the
  per-chat backoff shipped: it removes the busy-wait for EVERY starvation source
  (migrate waves, task bursts, a slow folder), not just the restart one, and is
  contained in routd — no new cross-daemon flow, no changed delivery semantics
  (cursor and turn_context handling untouched; at-least-once holds).
- **Fix:** `routd/dispatch.go` — a batch runed refuses as Busy retries on a
  per-chat exponential backoff (4s base, ×2 per consecutive refusal of the same
  batch, 60s cap) instead of every 2s poll; `busyDeferred` skips the re-feed
  BEFORE the expensive setup (capability resolve, MCP socket, prompt build). A
  new inbound message clears the backoff (fresh activity and slash commands are
  never delayed); any cursor advance clears it too. The 15-minute krons window
  drops from ~450 dispatch cycles per starved chat to ~19.
- **Tests:** `routd.TestBusyOutcomeRetriesWithoutAdvanceOrBreaker` (a busy
  result must not produce another dispatch on the very next poll; the same turn
  completes after the window); `routd.TestBusyBackoffGrowsAndNewMessageBypasses`.
- **Deploy:** routd only — rebuild + `systemctl restart`. The `session:
resetting on idle expiry` re-log and the `L51` push path stay open as `L51`.
- **Do NOT** read an anteval score taken while a migrate wave is draining.

## ✅ FIXED 2026-08-20 L54 — every forked topic answered its first turn with no context (2026-06-01)

Reported from the sloth desk 2026-08-19 19:45Z, verbatim: *"Vidí nějak tu diskusi,
nebo vždy začíná znovu bez kontextu?"* — "Does it see the discussion at all, or does
it always start over without context?" It did start over. A user replying inside a
Discord thread opens a new topic, and the first turn on that topic carried no
transcript and no observed window: the bare @mention and nothing else.

Three faults on one call path, each hiding the next:

1. `core.NewSessionID()` minted `sess-<unixnano>`. `claude --resume` takes a UUID
   and nothing else, so `ant/src/index.ts` `resolveResumeSession` refused the id
   and started fresh.
2. `copyParentSession` had already copied the parent transcript to
   `sess-<nano>.jsonl` — 934 KB on the live case — a file the agent could never
   open. The fork's whole purpose, silently void.
3. `EnsureTopicLineage` stamps `observed_cursor = now` so the child reads only
   post-fork context. Correct when it inherits the transcript; with (1) and (2)
   it removed the last source of context the child had.

Dead since `dcf29e03` (2026-06-01), which added the UUID guard to stop `--resume`
burning a turn on the retry. That fixed the symptom and left the cause: routd kept
minting an id the agent would always reject. The guard then logged the loss at
INFO and answered anyway — a user-facing failure that never surfaced.

Live proof (sloth, 2026-08-19): 19:40:02 topic `""` resumes `a2fb6376` with the
full 10-message observed window; 19:44:58 topic `1539720616280727582` forks;
19:45:01 `session id sess-1787168698153112071 is not a resumable UUID (arizuko
placeholder) — starting fresh`; 19:45:02 new session `dcac785c`, prompt = one
`<message>`, no `<observed>`. The complaint lands at 19:45:17.

- **Severity:** high (fleet-wide, user-visible; krons 26 hits/7d, sloth 4/3d)
- **Source:** core/types.go:282; routd/dispatch.go:411-455; routd/mcp.go:97;
  container/runner.go:1298; ant/src/index.ts:21-42
- **Fix:** `NewSessionID` returns a UUIDv4, so a minted id is resumable by
  construction and ant's guard goes back to being a safety net for a pruned
  transcript. `CopySession` restamps `"sessionId":"<src>"` → `"<dst>"` on every
  line so the child transcript names the child. `CopySession` reports whether it
  copied, and `ensureTopicWithFork` clears the child's `observed_cursor` when it
  did not — no fork can start with zero context on both axes.
- **Tests:** `core.TestNewSessionIDIsResumable` pins the id against ant's own
  regexp; `container.TestCopySession_RestampsSessionID`;
  `routd.TestEnsureTopicWithFork_{NoParentSessionKeepsObservedWindow,CopiedSessionKeepsForkCursor}`.
- **Deploy:** routd + the agent image both carry half of this. Rebuild and restart
  both. Topic rows already holding a `sess-<nano>` id keep starting fresh once
  each — the next turn stores the real UUID, so no migration is needed.

### The other half: an isolated task resumed the chat it was isolated from

The same report also covers *"context is bad"*, and that is a second defect found
in the same transcript. `specs/4/8` §"Implemented beyond base spec" defines
`context_mode`: `"group"` resumes the session, `"isolated"` means **no
`--resume`**. `routd/dispatch.go` passed `SessionID: l.db.SessionID(folder,
topic)` unconditionally and set `Isolated` only as a separate flag, which
suppressed the `session_log` row and the container reuse but never the resume.

So every isolated cron opened the chat's transcript, ran, and appended its whole
tool-heavy turn to it. sloth's `main/trading` transcript holds 13 turns: **8
crons** (`/migrate`, `/compact-memories` ×6, the MacroHype monitor) against **4
human ones**, at 1 MB. Claude Code compacts a transcript that size, and it was
compacting a conversation that was mostly cron.

All 10 of sloth's active tasks already declare `context_mode = 'isolated'`. The
operator asked for this and the code did not do it.

The write-back is the same defect mirrored, and closing only the read half makes
it worse: runed mints a throwaway session for a spawn that arrives with none, and
both `dispatchRun`'s backstop and `recordTurnResult` then installed that id as the
topic's session. Before the fix an isolated cron already replaced the chat's
session with whatever it ended in; after a read-only fix it would replace it with
a transcript holding nothing but the cron. Both writers now skip an isolated turn.

- **Source:** routd/dispatch.go:637,384 (was); routd/turns.go:708 (was);
  specs/4/8-scheduler-service.md:39-41
- **Fix:** `dispatchRun` resolves `isolated` once and sends `SessionID: ""`.
  The backstop moved into `persistSessionBackstop`, which skips an isolated
  trigger, and `recordTurnResult` applies the same rule on its own path.
- **Tests:** `routd.TestDispatchRun_{IsolatedTaskGetsNoSession,GroupTaskResumesSession}`
  assert on the `RunRequest` routd builds, not on the call site;
  `routd.TestPersistSessionBackstop_{IsolatedDoesNotOverwriteSession,ChatTurnPersistsSession,RecordedTurnWins}`
  cover the write-back.
- **Deploy:** routd only. Existing chat transcripts keep the cron turns already
  written into them; only new runs stop adding to them.

### ✅ FIXED 2026-08-20 Known consequence: the fork fix turned anteval's case isolation OFF

`5067cfdf` (2026-08-08) fixed cross-case contamination in anteval by giving each
case its own topic: eight smoke cases had shared one conversation, so case 1
answered "I can't, that tool is root-only" and cases 2-8 inherited it — the run
scored 4/8 off history rather than off the live tool list.

That fix worked only because a new topic started blind, which is the defect above.
`specs/5/F:48` is explicit that the first turn in any non-main topic forks the
folder's main, so with the fork repaired every anteval case now inherits the eval
chat's root transcript. On krons `sessions(eval, "")` holds `56952d01` at 167 KB
of prior eval runs. The contamination `5067cfdf` removed comes back on the next
run, and anteval scores the history again.

**Fix (2026-08-20):** `5/F` unchanged; anteval establishes a pristine baseline
itself. Before EVERY case the driver reads `GET /v1/topic_sessions` for the
root topic (`--folder`, now required) and, when a session exists, sends a bare
`/new` (steer-consumed: deletes the root sessions row, spawns no turn) and
holds case injection until the session is observably gone — per-case because a
group-context scheduled follow-up (state-persist) fires topic-less on the ROOT
topic and re-persists a session mid-run. A root session that will not clear
aborts the run loudly. Verified while fixing: with main sessionless the fork
copies nothing and clears the case topic's observed cursor, and the ambient
window cannot carry one case into another — `ObservedSince` takes only
`is_observed=1` rows and `PutMessage` hard-codes injected rows to 0 — so there
is no second hole. Tests:
`anteval/pkg/run.TestDrive{ResetsDirtyRootBeforeCases,PristineRootSendsNoNew,AbortsWhenRootWontClear,ResetsRootDirtiedMidRun}`,
`TestHTTPTargetSession`. Spec: `5/9` §"Session isolation".

Litter this also quantifies: `groups/eval/.claude/projects/-home-node/` holds **38
orphaned `sess-*.jsonl` copies, 7.9 MB**, one per forked topic — every one a
transcript routd copied and the agent then refused to open. The fix stops new ones
accruing; the existing 38 are dead files.

- **Severity:** medium (no live agent affected; it silently invalidates the one
  tool that certifies the agent)
- **Source:** routd/dispatch.go:173-177; specs/5/F-topic-lineage.md:48;
  anteval/pkg/run/run.go (topic per case, `5067cfdf`)
- **Status:** OPEN, needs a decision. anteval must isolate explicitly instead of
  relying on a broken fork. Cheapest option with no contract change: clear the eval
  chat's root session before a run, so main holds no transcript and each case forks
  from nothing. The alternative — a topic that declares "do not inherit" — amends
  `5/F` and needs sign-off.
- **Do NOT** run anteval and trust the score until this is settled.

## L53 — the secret broker writes `surface: "crackbox"` on every secret read (2026-08-18)

`store/secret_use_log.go:57` stamps `audit.SurfaceCrackbox` on the `audit_log`
row it emits per secret read, and `audit/log.go:34` defines that constant as the
string `"crackbox"`.

The secret never went near crackbox. `LogSecretUse` is called by the MCP broker
middleware — the resolver `ipc/ipc.go:247` calls the audit seam — so the surface
is `mcp`. The label was wrong before `6/24` and is now worse: it names a
component production does not run at all, so an operator filtering
`surface = 'crackbox'` in `audit_log` finds secret reads and nothing else,
while filtering `surface = 'mcp'` misses them.

**Severity: low, but it corrupts the one query an audit table exists for.**
Nothing breaks; the answer is just wrong.

**Why this is not an inline fix.** `surface` is a closed enum whose values are
written into `audit_log` rows and kept. Changing the constant makes historical
rows and new rows disagree about the same event, and the spec that defines the
closed set is `specs/5/I`. Either the write changes and old rows stay (a query
must then union two values), or a migration rewrites them. That is a data
decision, so it is recorded here for sign-off rather than shipped.

Recommended: change the write to `audit.SurfaceMCP`, delete `SurfaceCrackbox`
from the enum, and migrate existing rows — the string appears on exactly one
write path, so the rewrite is `UPDATE audit_log SET surface='mcp' WHERE
surface='crackbox'` with no ambiguity about which rows are meant.

## L52 — `arizuko token revoke <jid>` cannot revoke a suffixed chat token (2026-08-18)

`cmd/arizuko/token.go:222` derives the owner folder with
`groupfolder.JidFolder(jid)` when the operator does not pass one, and
`groupfolder/folder.go:110` implements the `web:` case as
`strings.CutPrefix(jid, "web:")` — the whole remainder is returned as the
folder.

But `token issue chat <folder> [<suffix>]` mints `web:<folder>/<suffix>`. So for
every token that has a suffix the derived folder is `<folder>/<suffix>`, which
is not a folder at all, `RevokeRouteToken` matches no row, and the command dies
with **"token not found or wrong owner"** — a message that names the wrong
cause. Measured live on krons while verifying the `6/24` deploy: revoking
`web:eval/egress624` failed, and the same command with `eval` passed explicitly
succeeded on the same row.

The `hook:` branch immediately below it already strips the last path segment
(`folder.go:114`). The `web:` branch does not, and that asymmetry is the bug.

**Severity: medium, operator-facing.** Every suffixed chat token is
unrevokable by its documented command unless the operator happens to know to
pass the owner folder as a second argument. A token that cannot be revoked is a
standing credential.

**This is the [identity_derived_not_read] class**, and it is a leftover of the
fix that already closed the same class elsewhere: migration `200` added
`target_folder` and states "nothing parses it back out of the JID any more".
The mint stopped parsing; `revoke` did not. The owner folder is a column on the
`route_tokens` row (`owner_folder`), and the command parses it out of the JID
instead of reading it. The cause fix reads the row — `RevokeRouteToken`
already looks the token up, so the folder need not be supplied at all when the
caller is the operator CLI. Do NOT "fix" `JidFolder` to strip a segment: `web:`
JIDs for folder-level chats have no suffix, so stripping would break the
common case and merely move the asymmetry.

Not fixed: separate concern from the `6/24` egress split it was found during.

## L51 — every turn waits ~1s for a poll tick that could be a channel send (2026-08-18)

`routd/loop.go:325` drives dispatch from `time.NewTicker(l.pollEvery)`, and
`pollEvery` defaults to **2 seconds** (`routd/pending_actions_http.go:12`
documents it). There is no push path — grepping `loop.go` for a channel, a
notify or a wake finds nothing.

So an inbound message lands in `messages`, and then nothing happens for up to 2s
(≈1s average) until the next tick reads it. That latency is paid on **every
turn**, before the container is even spawned, and it is invisible: no log, no
metric, nothing that says "we sat here".

For scale: the per-spawn proxy readiness poll costs ~10ms and I declined a
push-callback for it as not worth the machinery. This is ~100x that, and it
already has the machinery.

**A channel, not a webhook.** routd's ingest handler and its dispatch loop are
the same binary — the handler that writes the row can signal the loop directly.
An HTTP callback would add a listener, a retry policy and a delivery guarantee
to cross a process boundary that does not exist. Webhook is cross-process push;
this is in-process push.

Shape: a buffered `chan struct{}` of size 1, non-blocking send after the message
row commits, and the loop selects on it alongside the ticker. The ticker STAYS —
it is the self-healing path for anything that writes a row without signalling
(a migration, a direct DB write, a future daemon), which is exactly the property
that makes polling forgiving. Push for latency, poll for correctness.

Other tickers checked and deliberately NOT included: `runed` hourly
housekeeping, `timed`'s 60s scheduler tick (inherently time-driven),
`webd/hub.go` SSE keepalive, `webd/turn.go` 15s status. None of them sit on the
per-turn hot path.

Not yet measured end to end — before building, instrument one turn to confirm
the tick is really the dominant wait rather than container spawn. If spawn is
300ms and the tick is 1000ms, this is the biggest single latency item in the
system.


## L50 — LoadConfigFrom leaks one instance's .env into the process (2026-08-18, found while fixing L47)

`core.LoadConfigFrom` (`core/config.go:185`) does two things a loader should not:

- `godotenv.Load(dir/.env)` writes every var of THAT instance's `.env` into the
  process env, and nothing ever unsets them;
- `os.Setenv("DATA_DIR", dir)` fires only when `DATA_DIR` is empty, so the FIRST
  instance a process touches pins the data root for every later call.

`compose.envOr` (`compose/compose.go:1344`) falls back to `os.Getenv`, so the
leaked vars become silent fallbacks for a LATER `compose.Generate` — a route
gated on `ONBOARDING_ENABLED` can be emitted for an instance whose own `.env`
never mentions it.

Not currently reachable in production: one CLI command is one process, one
instance. It is reachable the moment anything loops over instances in-process,
and it is already reachable in the test binary — wiring `syncPackageSkills`
(`L47`) into `generate` made `TestGenerateSkipsGatedOffRoute` fail from a
neighbouring test's `.env`. Worked around there by pinning `DATA_DIR` and
`ONBOARDING_ENABLED` per test, which is a test fix, not the cause fix.

Cause fix (redesign, needs sign-off): a non-mutating loader — read the `.env`
into a map like compose already does, resolve the Config from it, and let a
caller that genuinely wants process env opt in. Touches every `LoadConfigFrom`
caller, so it is a contract change, not an inline patch.

Recorded, not fixed (Bug Triage Protocol).


## L49 — proposal: an LLM gateway, so the container never holds the real model key (2026-08-18, operator's idea, needs sign-off)

**Today the agent container holds the real provider key.** `ANTHROPIC_API_KEY`
is in `store.EnvProfileKeys` (`store/secrets.go:115`), which `5/14` defines as
the one table-backed credential class that reaches container env by design. The
agent can read its own environment. There is also **no base-URL override
anywhere in the tree** — no `ANTHROPIC_BASE_URL`, no seam to point model traffic
elsewhere.

**A gateway is not a proxy, and that is the point.** Egress proxying cannot
meter model calls without MITM, which `L48` argues we should drop entirely. A
gateway avoids interception: the client is CONFIGURED to call it
(`ANTHROPIC_BASE_URL`), so the gateway IS the endpoint. No TLS termination, no
CA in the container, nothing spliced.

The shape:

- arizuko mints a **per-spawn virtual key** with a budget and a TTL, and emits
  it plus the base URL into the container.
- The gateway holds the real provider keys. The container never sees one.
- **Revocation is deleting the virtual key** — immediate, and it needs no
  cooperation from a running turn.
- **Metering is per-key by construction**: tokens, cost and limits land against
  the spawn, so per-folder and per-user cost attribution is a read, not an
  estimate.

**OAuth is a hard requirement, not a preference.** `store.EnvProfileKeys`
already carries `CLAUDE_CODE_OAUTH_TOKEN` beside the API keys, and subscription
billing is flat-rate where API keys bill per token — at arizuko's turn volume
the API-key path is impractically costly. A gateway that only speaks API keys is
disqualified.

Candidates (verified 2026-08-18):

- **Bifrost** (Maxim AI) — Apache-2.0, single Go binary, self-hosted.
  Purpose-built for Claude Code: `ANTHROPIC_BASE_URL` points the CLI at it,
  `ANTHROPIC_AUTH_TOKEN` carries the virtual key (sent as `Authorization:
  Bearer` automatically). Four-tier budget hierarchy, per-request token counts,
  per-virtual-key cost attribution, 20+ providers with failover, ~11µs overhead.
  Documented to detect an Anthropic MAX account instead of API-key auth and work
  with it out of the box. Matches arizuko's stack and the minimal requirement.
- **LiteLLM** — MIT, 100+ providers, the reference virtual-key/budget model.
  Handles subscription auth by FORWARDING the client's OAuth header upstream
  (`forward_client_headers_to_llm_api`). Python + Postgres; heavy for a solo
  instance.

**THE ONE THING TO VERIFY BEFORE COMMITTING — where the OAuth credential
lives.** It decides whether this proposal is a security change or only a
visibility one:

- Credential **at the gateway**, spawn gets a revocable virtual key → this
  entry's actual goal: isolation AND metering, subscription billing preserved.
- Credential **forwarded from the client** → the container still holds the real
  token. Metering and cost attribution, but the security half evaporates.

LiteLLM's documented mechanism is explicitly the second. Bifrost's phrasing
suggests the first, but "detects if you're using a MAX account" comes from a
vendor summary, not its docs. Read Bifrost's own documentation on MAX-account
configuration before building anything.

**Coverage is bounded, and the gap is the worst-isolated path.** A gateway can
front a provider only where BOTH hooks exist: a base-URL override and a token in
env. arizuko's two subscription routes differ exactly there:

- **Claude Code** — `CLAUDE_CODE_OAUTH_TOKEN` in env + `ANTHROPIC_BASE_URL`.
  Both hooks present; a gateway fronts it.
- **Codex on ChatGPT-OAuth** — `ant/skills/oracle/SKILL.md:39` documents it as a
  HOST MOUNT: `HOST_CODEX_DIR` bind-mounted to `/home/node/.codex`, "no env vars
  needed". No token to swap, no base-URL redirect. A gateway cannot front it.

So metering would cover the Anthropic subscription and every API-key provider,
and MISS codex-on-ChatGPT-OAuth. Its meterable alternative
(`CODEX_API_KEY`/`OPENAI_API_KEY`) is the per-token billing this entry exists to
avoid.

Worth noting the host-mount path is also the weakest isolation arizuko has — a
bind-mounted `~/.codex` gives the container the operator's whole ChatGPT
session, not a scoped token — and it is the one case a gateway structurally
cannot improve, because nothing in the request path can be intercepted or
replaced. That is its own problem, not this entry's.

**NOT iron-proxy.** That is an egress MITM proxy for injecting credentials into
arbitrary HTTP — a different mechanism at a different point in the path. It
meters nothing.

Wiring cost is small: emit `ANTHROPIC_BASE_URL` and the virtual key as `-e` in
`buildArgs`. Note the trap — anything not emitted there is silently dead in the
container (three prior instances: `ARIZUKO_MODEL`, `RUNED_RUN_TIMEOUT`,
`ARIZUKO_BACKEND`).

Ties: `5/14` (credential tiers — this moves the model key OUT of the env tier),
`L48` (the egress proxy stays MITM-free because metering does not live there),
`X2` (the same "container must not hold the raw secret" argument, other half).


## L48 — proposal: one proxy per spawn, instead of one registry keyed by source IP (2026-08-18, operator's idea, needs sign-off)

`6/8` §"Security invariants" makes egress safety depend on an ordering the
consumer must honour:

> Consumers MUST register before the spawned container starts outbound traffic,
> and MUST unregister on exit. The "register before traffic" ordering is the
> consumer's invariant, not egred's — egred just enforces "no entry → deny".

Late registration fails closed, which is fine. **Late UNregistration does not.**
Docker reuses source IPs; if runed dies between a container exiting and
`/v1/unregister`, the stale `(source_ip, id, allowlist)` entry survives and the
next tenant on that IP inherits the previous folder's allowlist. A cross-folder
egress leak held off by a convention, which root `CLAUDE.md` says to replace
with a design that makes the bad state impossible.

**The proposal: give each spawn its own proxy instance**, configured with that
folder's allowlist at spawn time and dying with the turn. No registry, no IP
keying, no ordering obligation, nothing to reuse. The allowlist is static for
the proxy's whole life.

What it buys beyond the leak:

1. **An off-the-shelf proxy becomes usable unmodified.** The one thing that
   ruled out Squid and tinyproxy was needing dynamic per-source rules without a
   restart; a per-spawn proxy never needs a rule change at all. Envoy's control
   plane stops being necessary too.
2. **~890 lines of arizuko-specific code stop existing** — `pkg/admin` (572),
   `pkg/match` (144), `pkg/client` (175) are all in service of the registry.
3. **The failure domain shrinks to one turn** instead of every container.

Costs, honestly:

- One more process per turn. Note `6/20` already DECLINED warm pools, so
  per-turn ephemeral is the accepted shape and the spawn machinery exists.
- The proxy must be listening before the agent's first egress — an ordering
  problem, but a local one inside a single spawn rather than a distributed one
  across a shared registry.
- Network plumbing per pair, unless the proxy runs as a sidecar in the agent's
  own netns, which avoids most of it.

**"Static config precludes the MITM/header-injection work" — checked, it does
not.** Two reasons:

- **Nothing dynamic exists to preclude.** `runed/docker.go:127` builds the
  EgressConfig "from runed's own config + that static per-folder list", once, at
  spawn. There is no re-register and no mid-run update anywhere. The registry's
  dynamism serves MULTIPLEXING one proxy across many containers — not live rule
  changes. Per-spawn removes the multiplexing, which is all it was doing.
- **Per-spawn is BETTER for header injection, not worse.** Injection needs
  this tenant's credential; a shared proxy must map source IP → whose secret,
  the same fragile keying that causes the leak above. One proxy per spawn has
  exactly one tenant and no mapping. TLS interception needs a CA trusted inside
  the container — per-spawn that CA can be minted for one turn, instead of a
  fleet-wide MITM CA whose leak compromises every container ever spawned.

The credential and the lifetime become the same scope, which is what makes the
injection half safe rather than merely possible.

**Shape, measured (2026-08-18).** Running one per turn means it must be tiny,
and it nearly is already. `pkg/proxy` is 638 non-test lines — `peek.go` 279 (SNI
extraction, the hard part), `proxy.go` 164 (CONNECT + HTTP splice),
`transparent.go` 106 and `origdst_*.go` 89 (transparent mode). The registry
coupling is one constructor parameter, `func New(allow *admin.Registry)`; for a
single-tenant proxy that becomes a plain allowlist.

Drop transparent mode — a sidecar does not need it, the container gets
`HTTP_PROXY` pointing at its own proxy, so nothing is intercepted — and the
whole thing is **peek + splice + match, roughly 450 lines.**

So: SHRINK rather than adopt. tinyproxy is smaller in C, but not smaller
operationally once a config renderer and a second build language are counted,
and it does nothing the 450 lines cannot. Go suits the per-turn model —
millisecond start, a few MB resident.

It also improves the "ship one component to a stranger" story (`6/16`): a
450-line single-tenant proxy with a static allowlist can be read in an afternoon
and trusted. A 2,400-line system with a registry, an admin API and a source-IP
trust model cannot.

Not considered in `6/8`, `6/9` or `6/20`. Worth costing before any further work
on the registry, because it would delete the registry rather than improve it.


## L47 — a package's skills never reach a group that already existed (2026-08-17, FIXED)

`seedSkills` (`container/runner.go:1125`) copies a package's `skills/<n>/` into a
group's `~/.claude/skills/`. It has exactly one non-test caller:
`container.SetupGroup` (`runner.go:1076`). `SetupGroup` is reached only from
group/instance CREATION — `cmdCreate` (`cmd/arizuko/main.go:335`), `cmdGroup`
(`:448`), `onbod/main.go:869`, `dashd/groups_admin.go:114` — and from
archive-apply. **`cmdGenerate` is not among them.**

So a package's skill lands in a group only if the package was already installed
when that group was created. Verified by walking the call graph, not inferred.

`packages install` / `upgrade` / `sync` all print "run `arizuko generate
<instance>` to apply" (`cmd/arizuko/packages.go:521,588,622,678`). The operator
does exactly that, sees success, and no group that already existed ever receives
the skill — on first install or on any later revision.

Doubly closed: `cpDirFresh` (`runner.go:1271`) skips files that already exist,
so even if `seedSkills` did run again it could not UPDATE a skill, only create
absent ones.

**The spec says the opposite.** `specs/5/28-packages.md:203` lists `skills/<n>/`
as `managed: sync`, and `packages.go:323`'s own comment says "seedSkills layers
into every group". Code, spec and comment disagree, and the code loses.

**No reconcile exists** — checked before claiming. Stock skills have two: the
`MIGRATION_VERSION` comparison in `prepareInput` annotates the prompt "[pending
migration]" every turn, and `/migrate` does a real 3-way merge against
`/opt/arizuko/ant/`. Neither covers packages: `/opt/arizuko/ant/` is the baked
image, not the host `<datadir>/skills/` tree, and that tree is never
bind-mounted into any container, so the agent-side merge cannot see it.

**Severity: high, and structurally worse than `L41`/`L45`.** Same shape — an
operator-facing "apply this" command that silently does not — but `L41` had a
partial reconcile on a later generate and `L45` at least reached new deploys.
Here there is no path at all, ever, for a group that predates the install. A
package shipping a skill fix, a corrected instruction, or new capability wording
freezes at whatever content existed when each group was created.

Untested as well as unnoticed: `TestSeedSkillsPackageLayer`
(`container/container_test.go:938`) calls `seedSkills` directly and proves only
the first copy. The update-into-an-existing-group case has no test.

**SIGNED OFF 2026-08-17, fixed 2026-08-18.** `generate` reseeds package skills
into every existing group (`cmd/arizuko/skills.go:syncPackageSkills`, the
delivery half that `syncProxydRoutes` is for routes), and the package layer is
extracted out of `seedSkills` into `container.SeedPackageSkills` so both callers
run ONE implementation. `MIGRATION_VERSION` was not used: that signal announces
staleness to the agent and asks it to merge, which is right for `ours` content
and wrong here — nothing needs merging when the operator owns no copy.

The overwrite boundary, verified before choosing it rather than assumed:
`<datadir>/skills/<n>/` is written ONLY by `packages install` and deleted only by
`remove` — no operator authoring path, no `.merge-base` entry, so `/migrate`
never touches it and the package owns the directory NAME. That makes it
upstream-managed (`5/28` `managed: sync`) and `cpDirOverwrite` correct for it.
`cpDirFresh` is unchanged and still guards the stock `ours` tree beside it.

Two guards the overwrite needed, neither in the original entry:

- **`.disabled` is honoured**, as it is for stock skills. dashd's per-group skill
  toggle already writes the marker for package skills and nothing acted on it;
  under skip-existing that was merely ineffective, under overwrite it would have
  made disabling impossible.
- **a package skill whose name a shipped skill holds is skipped, loudly.** The
  package layer copies into the same `.claude/skills/` tree, so overwrite alone
  would let a package edit a shipped skill — a shadowing hole skip-existing had
  been hiding.

`TestSeedSkillsPackageLayer` was also asserting on a placement the real caller
never produces (a free-standing `claudeDir`, not the group's own); corrected.


## L46 — a package may grant itself operator authority and claim core routes (2026-08-17, FIXED)

`arizuko packages install <git-url>` applies a package's declared grants and
routes with **no bound on what it may ask for**.

**Grants.** `applyPackageGrants` (`cmd/arizuko/packages.go:210`) validates only
`checkGrantPredicate(row)` — the predicate SYNTAX — then stamps
`GrantedBy = "package:<name>"` and writes the row. Scope and action are never
checked. A package shipping

    [{"principal":"folder:evil","action":"*","scope":"**","effect":"allow"}]

gets instance-wide authority over every folder, because `auth/authorize.go:231`
matches `**` against everything and operator-ness in arizuko is emergent from
exactly that row (there is no operator role to check against).

**Routes.** `applyPackageRoutes` applies any path. A package declaring `/dash/`
or `/mcp` re-points the operator console or the agent socket surface at its own
backend. `collectProxydRoutes` will happily emit it.

**Owner.** After `L45`, `store.ProxydRoute` has an `owner` field and the package
decoder is lenient, so a package can stamp `operator` and opt out of ever being
rewritten by generate. (Flagged by the L45 implementation, deliberately left.)

**Severity.** Not remotely exploitable — it needs the operator to run `install`.
This is supply chain, `curl | bash` class. What makes it a bug rather than a
known property is that packages are DOCUMENTED as a bounded, reversible unit
("remove reverses exactly what the record names"), and the install summary
prints the grants it applies as if they were routine. Nothing signals that
installing a package is equivalent to handing over the instance.

**The rule that is missing** (operator, 2026-08-17): some ground is reserved for
core arizuko and core packages. A package's authority must be bounded by what
the operator granted THE PACKAGE, never by what the package asks for.

**SIGNED OFF 2026-08-17, fixed 2026-08-18.** All three bounds land in
`parsePkgManifests` — the one place a package's rows enter the instance — before
the first byte is written, so a refusal leaves no partial state (the `L44`
guarantee). Spec `5/28` §"A package's authority is what the operator handed it".

1. **Reserved route paths.** `compose.ReservedRoutePaths` DERIVES the set from
   `coreProxydRoutes` (no second list to drift) and `ReservedRouteClaim` applies
   proxyd's own matching rule, so `/dash/mine/` is refused as well as `/dash/` —
   longest-prefix wins, so a sub-path front-doors the console. Gates are ignored:
   `/onboard` is core's whether ONBOARDING_ENABLED is on or not.
2. **Bounded grant scope.** `install` takes `--folder <scope>` (repeatable). `**`
   and `*` are refused whatever the operator named; every other scope must be a
   subset of the bound, decided by `auth.MatchGroups` — the one containment
   matcher — applied to witness paths, since both sides are patterns. No
   `--folder` is a bound of NOTHING, so a package declaring a grant is refused
   with the flag it needs.
3. **A third owner value.** `store.RouteOwnerPackage(name)` → `package:<name>`,
   stamped by install and by generate; a manifest declaring `owner` is refused.
   `package:*` is rewritten by `SyncProxydRoutes` like an arizuko row (generate
   re-derives it from the package's fragment) but keeps its attribution — only
   the operator's own row stays hands-off.

Two corrections to this entry, found while implementing:

- `applyPackageGrants` / `applyPackageRoutes` no longer exist — `L44` replaced
  them with `parsePkgManifests` + the apply engine. The substance held.
- `/v1/` is NOT in `coreProxydRoutes`, so the derived reserved set does not cover
  it, nor the prefixes proxyd hand-wires before `MatchRoute` (`/pub/`, `/auth/`,
  `/health`, `/`). A package route there is dead rather than a hijack — proxyd
  dispatches those first — so it is a truthfulness gap, not an authority one.
  Closing it needs those prefixes declared somewhere derivable; a hardcoded
  second list in `compose` is the wrong answer. Not fixed here.


## L45 — proxyd routes shipped in a release never reach an existing instance (2026-08-17, FIXED)

`loadInitialRoutes` (`proxyd/main.go:248`) is seed-once: if `proxyd_routes` has
any row, `PROXYD_ROUTES_JSON` is ignored. There is no reconcile — the env var is
read at that one call site and nowhere else. So every route arizuko has added to
`coreProxydRoutes` since an instance first booted is absent from it forever.

**Live on krons.** 12 rows in `proxyd_routes`; `coreProxydRoutes` ships 18.
Missing: `/dash/onbod/`, `/dash/timed/`, `/invite/`, `/me`, `/me/`, `/pair/`,
`/panel/`.

Confirmed with a control rather than inferred. An unrouted path falls through to
the static site:

    /definitely-not-a-route-xyz/  302 → /pub/krons/definitely-not-a-route-xyz/
    /invite/                      302 → /pub/krons/invite/      (same = unrouted)
    /panel/                       302 → /pub/krons/panel/       (same = unrouted)
    /slink/                       404                            (routed; backend answers)

So the invite surface, device pairing, `/me`, the panel and the federated
onbod/timed dashboards are silently absent on the deploy target. Same class as
`L41`: a config path that stops applying and looks healthy.

**The design intent is legitimate** — the comment says "operator mutations are
durable across restarts", and clobbering an operator's runtime route on every
redeploy would be worse. The fault is the mechanism, not the goal.

**The rule it breaks already exists.** Root `CLAUDE.md`: "Business features
(gates, grants, onboarding) are DB-backed with CLI + chat command for
management. Infra (ports, timeouts, images, paths) stays as env vars in `.env`."
Routes are business config — operators mutate them at runtime — so they were
never env-var material. The operator states the general form: **the DB is
authoritative and the only way to persist; env is for purely static config.**

**SIGNED OFF 2026-08-17.** Arizuko- and package-owned routes are authoritative
and overwrite on regenerate — an operator does NOT get to edit them in place,
and must be told so. A separate override mechanism is required so an operator
can still win deliberately; editing the row is not that mechanism.

Proposal: `arizuko generate` writes core routes as rows,
upserting by path so an operator's own routes and edits survive, and
`PROXYD_ROUTES_JSON` is deleted as a config source. One authority, no seed-once
window. Ties to `L44`, which removes the parallel `*-routes.json` format feeding
the same env var.

**FIXED 2026-08-17.** `arizuko generate` writes the route table into
`proxyd_routes` (`store.SyncProxydRoutes`, called from
`cmd/arizuko/routes.go`); `compose.Generate` returns the routes instead of
emitting them, and `PROXYD_ROUTES_JSON` is gone from both compose and proxyd's
boot path — `loadInitialRoutes` reads rows only. A new `owner` column carries
the authority: `arizuko` rows are rewritten by every generate, `operator` rows
are never touched at any path. The override is ownership, not editing — PATCH on
an arizuko-owned row answers 409 naming the way through (DELETE, then POST,
which stamps `operator`). Migration `routd/0038` (mirrored in `store/0087`)
backfills existing rows to `arizuko`, so the shipped paths get reclaimed while a
hand-added route at a path arizuko does not ship (krons `/arizuko`) is never
read. Residual, recorded deliberately: generate never deletes, so a route that
stops being shipped, or whose `gated_by` is later turned off, stays until the
operator removes it.

**This is also the unlock for `6/16`'s recorded direction** — "shrink toward
off-the-shelf and publish the grant-scoped mount contract", where proxyd is
called "the commodity half — Caddy/Traefik plus Authelia's job, and the weakest
position in the repo". That decomposition needs routes to be rows first. With
them: the routing TABLE is data, the PROXY is off-the-shelf with its config
rendered from those rows (one more reader, per the `one declaration, many
readers` rule), and what stays arizuko's is the forward-auth endpoint the proxy
calls per request — `auth.MatchSlot` folder scoping, which is the one thing
`6/16` names irreplaceable. Three orthogonal pieces instead of one 1,591-line
daemon doing all three.


## L44 — packages declare in two bespoke formats what the manifest already carries (2026-08-17, FIXED)

`5/8`'s manifest carries "ACL, routes, secrets metadata, scheduled tasks, proxyd
routes, web routes, network rules" and `apply` installs it. Packages re-declare
two of those in their own formats, each hand-parsed with its own types:

    <name>-grants.json   → applyPackageGrants  (cmd/arizuko/packages.go:195)
    <name>-routes.json   → applyPackageRoutes  (cmd/arizuko/packages.go:266)
                         → collectProxydRoutes (compose/compose.go:434)

Root `CLAUDE.md`: "If it exists, fix/extend the ORIGINAL; NEVER add a parallel
second path — two paths drift."

**Three costs, all live:**

1. **A package cannot declare what the manifest supports** — no scheduled task,
   network rule, web route or secrets metadata, though `apply` understands all
   four.
2. **Install is not transactional.** `applyPackageGrants` calls `die()` inside
   its loop, so grant 3 of 5 failing leaves 1 and 2 applied and the process
   gone. `apply` already has per-subsystem transactions with **pre-image
   rollback** (`5/8`) — packages hand-roll a worse version beside it.
3. **Routes land in two places** — a generate-time env var via the JSON sidecar,
   and `proxyd_routes` rows via hot-apply.

**Proposal (needs sign-off — redesign, not patch).** A package ships two files,
both in formats that already exist:

    <name>.yml     docker's compose fragment (not our format; unchanged)
    <name>.yaml    the 5/8 manifest — grants, routes, tasks, network rules

Delete both JSON formats and their parsers. `apply` installs the rows and brings
transactional install plus rollback with it. Compose generation reads routes
from the manifest instead of the sidecar — the routes still become one proxyd
env var at generate time (`collectProxydRoutes` documents why they must), so
this is one declaration with two readers, which is the project's own
one-renderer-many-sinks rule rather than a second path.

`installed_packages` is unchanged: it already records owned rows (`Manifest`)
and content hashes (`AssetHashes`), which is the dpkg half arizuko got right.

**SIGNED OFF 2026-08-17.** Hard cutover: existing installs are rewritten and
adapted. No dual-read, no backwards compatibility anywhere.

**FIXED 2026-08-17.** A package ships `<name>.yml` (docker's, unchanged) and
`<name>.yaml` (a `5/8` manifest). Both JSON formats and both parsers are gone,
along with `applyPackageGrants` / `applyPackageRoutes`.

- **Install goes through the `apply` path.** `installPkgRows` merges the
  package's rows onto the subsystem's live projection
  (`resreg.Resource.MergeRows`, new) and hands the whole document to the same
  `applyDocs` `arizuko apply` uses — one transaction per owner DB, pre-image
  rollback, in-tx checksum CAS. The manifest is parsed and validated before the
  first byte is written, and a refusal after that unwinds the fragment files, so
  an install is whole or nothing. `remove` is the mirror: `DropRows` of exactly
  the PKs the record names, so an operator's own row at another key survives.
- **The record is uniform.** `installed_packages.manifest` keys on the resreg
  resource name (`acl`, `proxyd_routes`, `scheduled_tasks`, …) with the PK
  strings of the rows the package owns; `compose_fragment` and `skill` are the
  only non-resource keys. The old `grant` / `proxyd_route` keys are gone — a
  record still carrying them makes `remove` refuse loudly and name the reason
  rather than under-remove.
- **Compose reads the same file.** `collectProxydRoutes` →
  `compose.ManifestRoutes` parses `services/<name>.yaml` with
  `resreg.ParseYAML`, so generate and install share one parser and one
  declaration. `template/services/slakd-routes.json` became `slakd.yaml`.
- **Two knock-on findings.** (1) `resources.ACLRow` omitted `grant_option`, and
  acl has no folder scope, so ANY apply mentioning acl rebuilt the table and
  reset every holder's WITH GRANT OPTION to 0 — a pre-existing `arizuko apply`
  hole this change would have put on the package path. The column is in the row
  type now. (2) The per-grant / per-route audit rows (`Q5`, `F2`) collapse into
  the one `config.apply` row apply emits; `resreg.ApplyOpts.Extra` (new) carries
  the package name and the PK of every row added or withdrawn, so the row still
  names what it handed out. Whole rows do not fit — `params_summary` caps at 512
  bytes.
- **Not done, deliberately:** `L46`. The merge is a single choke point, so
  bounding what a package may ask for is one hook at
  `cmd/arizuko/packages.go:parsePkgManifests`, and `ManifestRoutes` already
  drops the `owner` column on the compose side.

## L43 — 44 stale spec references in the live tree, and they block the 6/7 renumber (2026-08-16, found while measuring the re-cut)

A reference-graph check over tracked `.md`/`.go` (excluding `CHANGELOG.md`, which
is history and correct as written) found **44 distinct spec IDs that no longer
resolve**. They sit in the documents a reader trusts most:

    ROUTING.md          6/F, 6/J, 10/17
    SECURITY.md         6/J, 9/14, 10/17
    ARCHITECTURE.md     5/H, 7/D, 7/F
    README.md           8/Z, 9/3
    BUGS.md             6/2, 8/3, 7/00 (this file)
    core/types.go       5/U, 6/F        store/messages.go   6/F, 6/J
    ipc/README.md       7/7, 7/Y, 8/Y   resreg/README.md    8/F

Not all are errors: `8/Z` in `README.md` is a deliberate forward reference to a
spec phase 8 named and never wrote. The rest are
residue from earlier renumbers — the lettered phase-6 IDs (`6/B`, `6/D`, `6/E`,
`6/F`, `6/J`, `6/R`, `6/Y`) are the largest family.

**Why this blocks the renumber, and why it is the real finding.** A dangling
reference is detectable: follow it, get nothing, notice. But the proposed re-cut
moves the phase-7 cockpit specs INTO phase 6's number space, and stale `6/2`
references already exist in `BUGS.md`, `proxyd/resource_test.go` and migration
`119`. Renumbering `7/2-dashd-hub` to `6/2` makes every one of those resolve —
to a spec about something else entirely. Dangling pointers become confidently
wrong pointers, and the class stops being detectable at all.

So the order is fixed: repair the graph first, renumber second. Repairing is
cheap and independently worth doing; renumbering onto it is then safe.

Recorded, not fixed — found during a general check (Bug Triage Protocol). The
fix is per-reference judgement: each stale ID needs its intended target
identified from context, and a few may be deliberate forward references to keep.


## L42 — krons WhatsApp is down: the paired session is invalidated (2026-08-16, BLOCKED on the operator's phone)

    whapd  error  session invalidated, delete auth dir and re-pair  code=401

whapd restart-loops on it, so krons' primary channel delivers nothing. This is
not a code fault and nothing in the repo fixes it — WhatsApp expired the paired
session and only a re-pair restores it.

It was masked until tonight: whapd was crash-looping earlier on `L41`'s missing
mount and never reached the login, so the 401 is the fault underneath. It is
referenced in passing elsewhere in this file as "the known whapd re-pair loop"
and has never had an entry, which is why it has stayed unaddressed.

Use the pairing-code path, not QR (recipe in the operator's notes): whapd
`--pair <phone>`. The QR path is the one that does not work here.

- **Severity:** high — the primary channel is dead
- **Scope:** operational, krons
- **Status:** blocked — needs the operator's phone, nothing else
- **Fix:**

## L41 — an instance's compose fragments freeze forever once they differ by a byte (2026-08-16, LOUD NOW, auto-repair needs sign-off)

Found when tonight's `L32` whapd fix crash-looped krons' primary channel:

    Error: EACCES: permission denied, mkdir '/srv/data/store/whapd/outbound-dedup'

`template/services/whapd.yml` carries the mount the ledger needs. The instance
copy at `/srv/data/arizuko_krons/services/whapd.yml`, dated 2026-07-28, does
not. `env/` regenerates on every restart; `services/` does not.

`R1` already fixed this class: `compose.RelinkCatalog` replaces an installed
fragment with a symlink at the catalog, so a template edit reaches every
instance. **It only converts a BYTE-IDENTICAL copy.** krons' fragments were
written by an older generator (`# Converted from services/whapd.toml by
'arizuko generate'` — a header today's template does not have), so every one is
classified as an operator hand-edit and skipped, silently, on every generate
since. The R1 fix reaches only instances that did not need it.

Three live faults on krons traced to this one cause:

- **whapd crash-looped** — the `L32` dedup mount never arrived. WhatsApp, the
  primary channel, was down. FIXED live 2026-08-16 by adding the mount and
  `WHAPD_STATE_DIR` to the instance fragment (additively — the instance copy
  carries a `CHANNEL_SECRET` the template does not, so no overwrite).
- **`F53` is this bug** — teled's `${DATA_DIR}/store/teled` mount and `DATA_DIR`
  are in the template and absent live, which is exactly why the getUpdates
  offset resets on every recreate. `T1`'s `RUNED_RUN_TIMEOUT` is missing too.
- **ttsd and kokoro read the whole instance `.env`** — the template scopes them
  to `env/ttsd.env` / `env/kokoro.env` precisely so they do not. kokoro is a
  third-party image, and it currently receives `SECRETS_KEY`, `AUTH_SECRET`,
  `CLAUDE_CODE_OAUTH_TOKEN` and every bot token.

**SHIPPED**: `RelinkCatalog` now returns the diverged set and `generate` prints
a per-fragment warning naming the frozen file. That is the loud half — it would
have surfaced all three above at the first restart after the template changed.

**PROPOSAL, needs sign-off.** The check cannot tell "the operator edited this"
from "we wrote this two versions ago", and it must, or the warning is the only
thing that ever happens. Our own output is positively identifiable by its
generated header, so the byte-compare can consult it: a fragment carrying the
`# Converted from services/<x>.toml by 'arizuko generate'` marker is ours and
may be relinked; anything else stays an operator edit and is only reported.
Shipping that auto-repairs every instance created before the catalog moved —
which is also why it is not shipping without a decision.

- **Severity:** high — it silently withholds every adapter mount and env var
  added since an instance was created, and it caused a live channel outage
- **Scope:** compose fragment installation
- **Affected:** every instance whose `services/*.yml` predate the current
  catalog (krons: whapd, teled, ttsd, kokoro)
- **Source:** `compose/fragments.go` `RelinkCatalog` (byte-identical gate);
  `cmd/arizuko/packages.go` `relinkCatalog`
- **Status:** warning shipped; auto-repair proposed
- **Fix:**

## L40 — live krons cannot tell two anonymous visitors apart (2026-08-16, open — needs an infra decision)

Found by anteval `chat-entrypoint`, which failed `429, want 200` on a freshly
minted chat token. The code half is FIXED in `3e19ea3e` (the shield keyed on
`r.RemoteAddr` instead of the existing `clientIP` helper). The deployment half
is not, and no code change can close it.

`proxyd` on krons logs exactly two source addresses over six hours:
`127.0.0.1` (239) and `10.0.5.1` (18). `10.0.5.1` is the host's own default
gateway (`enp0s2 10.0.5.2/30`), and `krons.fiu.wtf` resolves to `65.21.89.43`,
which is not an address on this host. Every external visitor therefore reaches
proxyd as one address. `TRUSTED_PROXIES` is unset, so `fixForwardedFor`
correctly discards any inbound `X-Forwarded-For` and stamps the peer.

Three mechanisms degrade together, silently until now:

- the anon chat DoS shield (`CHAT_ANON_DOS_RPM`, default 10/min) is one bucket
  for the whole world — one reader locks out the next, which is the 429
  anteval hit;
- the auth-denied log names the hop, not the scanner — the exact regression
  `clientIP`'s comment says was already fixed once;
- `webd.anonSender` hashes that address, so every anonymous chat visitor shares
  one `anon:<hex>` identity.

**ANSWERED 2026-08-16 by the deployed warning.** The edge DOES forward a
client address:

    WARN untrusted edge proxy: X-Forwarded-For discarded, client identity is
    the hop  peer=10.0.5.1  fix="add 10.0.5.1 to TRUSTED_PROXIES"

So the real client IP arrives at proxyd on every request and is thrown away.
One env var recovers it.

**Do not set `TRUSTED_PROXIES` before one test.** proxyd reads the LEFT-most
hop, so trusting an edge that appends to a client-supplied header lets any
caller forge their own source, bypass the shield and poison the audit log.
Decide it empirically: set `TRUSTED_PROXIES=10.0.5.1` on krons, restart, then
compare two requests — one plain, one carrying `X-Forwarded-For: 1.2.3.4`.

- both log the same real client address → the edge overwrites; keep the setting
- the second logs `1.2.3.4` → the edge appends; revert, and fix the edge to
  send `$remote_addr` rather than `$proxy_add_x_forwarded_for`

This is a live-host security posture change on infrastructure we do not own
here, so it is the operator's call, not a code fix.

Secondary, worth measuring once clients are distinguishable: `/chat/` has six
routes and a page load spends root + config + history + POST, with a CORS
preflight per POST for an embedded widget. A 10/min budget is then ~3 messages
a minute for one visitor. Possibly the intended shield strength; currently
untestable, because the budget is not per-visitor.

- **Severity:** high — anonymous chat is the public entry surface
- **Scope:** krons deployment (all instances behind the same edge)
- **Affected:** every anonymous `/chat/` and `/hook/` caller
- **Source:** `proxyd/main.go` `fixForwardedFor`; `TRUSTED_PROXIES` unset in
  `/srv/data/arizuko_krons/.env`
- **Status:** open — needs the edge's `X-Forwarded-For` behaviour confirmed
  before anything is trusted
- **Fix:**

## L39 — whapd acks WhatsApp before routd accepts, the TS half of L25 (2026-08-15, open)

Found while fixing `L25`, which named a sibling entry `L20` for this — `L20`
does not exist in this file, and the in-memory queue it described
(`whapd/src/queue.ts`) was deleted before that pass. The defect itself is real
and still open.

`whapd/src/main.ts` calls `sock.readMessages([msg.key])` — the WhatsApp read
receipt — BEFORE `await rc.sendMessage(payload)`, and the `catch` around the
send only logs `deliver failed`. Baileys will not hand the message over again,
so a routd that is restarting loses it, exactly as slakd did.

`L25` shipped the durable answer as `chanlib.RouterClient.RetainInbound` (a
per-adapter `inbound_pending` table plus a sweep), wired into slakd and discd.
whapd is TypeScript and cannot use it. The fix is the same shape in TS and
should share whatever durable store `L32` picks for the outbound ledger — one
adapter-local database, two tables, not two bespoke stores.

- **Severity:** high — same class as `L25`, on the adapter krons runs as its
  primary channel
- **Scope:** whapd inbound durability
- **Affected:** every WhatsApp message arriving while routd is restarting
- **Source:** `whapd/src/main.ts` (`readMessages` then `rc.sendMessage` in a
  log-only `catch`); contrast `chanlib/inbound.go`
- **Status:** open — do it together with `L32`, which has to choose the same
  durable store (node:22 has no stable `node:sqlite`).
- **Fix:**

## ✅ FIXED 2026-08-15 — L32 — whapd ignores `idempotency_key`: the one adapter still outside the D5 dedup contract (2026-08-15)

Every Go adapter claims/settles routd's `idempotency_key` in a durable
per-adapter ledger (`chanlib/dedup.go`), so a sweep resend of a delivered row
posts nothing. whapd (TS, node:22) drops the field on the floor: a routd
resend after a lost `/send` response double-posts on WhatsApp — exactly the
D5 class, one adapter left. Spec `5/34` now names whapd as outside the
contract. Fix shape: a durable claim/settle ledger in whapd's own state dir
(node:22 has no stable `node:sqlite`, so file-per-key with `O_CREAT|O_EXCL`
as the atomic claim, or a runtime bump), wired into `/send` before
`sock.sendMessage`, plus a Baileys error taxonomy call — which of its errors
prove nothing was sent (release the claim, per `chanlib.ErrNotDelivered`)
vs ambiguous (hold). ~100 lines TS + tests; the taxonomy decision is why
this needs a look rather than a mechanical port.

- **Severity:** medium
- **Scope:** outbound delivery / channel protocol
- **Affected:** whapd (krons runs it as the primary channel)
- **Source:** whapd/src/server.ts `/send` (no idempotency_key read)
- **Status:** FIXED — `whapd/src/dedup.ts` claims the key before the Baileys
  call and settles it after, wired into BOTH `/send` and `/send-file` (the
  entry only named `/send`; routd sends the key on both, `chanreg/httpchan.go`
  writes the `idempotency_key` multipart field). Every adapter is now inside
  the D5 contract and spec `5/34` says so.

  **Store: a directory of files, one per key** — the entry's first option, and
  it needs no runtime bump. node:22 has no stable `node:sqlite`, and the only
  operation that must be atomic here is the CLAIM, which `open(path, 'wx')`
  (`O_CREAT|O_EXCL`) already is at the filesystem level. Keys are
  `encodeURIComponent`-encoded: a routd message id carries `/` and `:`, and a
  raw key as a filename would escape the ledger directory. It lives under
  `WHAPD_STATE_DIR`, and `template/services/whapd.yml` now mounts it — an
  unmounted ledger forgets every claim on recreate, which is the same as
  having none.

  **The Baileys error taxonomy decision — the answer is ORDERING, not error
  parsing.** Baileys exposes no signal that separates "WhatsApp refused it"
  from "the ack was lost", so whapd never guesses: it HOLDS the claim on any
  error after the platform call and answers `409` on the next attempt, which
  routd dead-letters for an operator rather than double-posting.

  What stops that from turning every plain transient into a guaranteed
  dead-letter — the failure `chanlib.ErrNotDelivered` exists to prevent — is
  the CLAIM ORDER, not a classifier: `requireSock` refuses a send when the
  socket is not open, BEFORE any claim exists. So a routd restart or a dropped
  link answers 502 with nothing recorded and the sweep retries freely. That is
  the whole class of proven-non-delivery whapd can actually observe, and it is
  handled where it is observable.
- **Fix:** 44677153

## ✅ FIXED 2026-08-15 — L33 — a one-shot task's retry mints a fresh message id, so a lost response fires it twice (2026-08-15)

`timed.fireSplit` enqueues each due task with `id = core.MsgID("sched-"+t.ID)`
— a `UnixNano` suffix, fresh per attempt. When the `POST /v1/messages`
response is lost but routd recorded the row, timed's error path reschedules a
one-shot (`cron=""`) to fire again in 1 minute with a NEW id, and routd's
id-PK dedup never matches: the task fires twice. Cron tasks are unaffected
(the reschedule targets the next natural slot, which should fire anyway).
The exact fix needs a stable per-fire identity that survives the reschedule —
the claimed `next_run` carried through `/v1/tasks/due` and preserved on the
error-path reschedule, or a fire_id column; a bare `sched-<taskID>` key would
instead silently dedup a legitimately re-armed one-shot.

- **Severity:** low
- **Scope:** scheduled tasks / ingress idempotency
- **Affected:** timed → routd
- **Source:** timed/split.go:184 (`core.MsgID`), core/types.go:275
- **Status:** FIXED — the claimed `next_run` is the fire identity, as this entry
  proposed; no `fire_id` column was needed.

  `GET /v1/tasks/due` now returns `next_run` (RFC3339 UTC, unchanged by the
  claim) and `timed.fireID` mints `sched-<taskID>@<next_run>` instead of
  `core.MsgID`. Every attempt at one fire carries one id, so routd's
  `INSERT OR IGNORE` on `messages.id` collapses them.

  The error path is what makes that hold: a one-shot is re-armed to the
  next_run it was CLAIMED at, not a fresh `now+1m`. That value IS the identity,
  so the next tick re-claims the same fire with the same id. The 60s fire-loop
  cadence preserves the old one-minute retry exactly — the backoff was never
  the `now+1m` value, it was the tick. Cron tasks keep taking their next
  natural slot: the missed fire and the next one are different fires.

  A task with no `next_run` falls back to the wall-clock id. A stable-LOOKING
  id that is not actually per-fire would dedup two legitimate fires into one,
  and a silent drop is worse than a duplicate.
- **Fix:** a56845c7

## ✅ FIXED 2026-08-15 L38 — the product catalog and the product corpus are two different sets (2026-08-15)

`ant/examples/` ships 11 products; `template/web/pub/arizuko/products/` documents
9. They are not the same 9.

- **Shipped, undocumented (6):** `creator`, `personal`, `pm`, `socials`,
  `strategy`, `support`. An operator running `arizuko products <inst> list` sees
  names that appear nowhere on the site.
- **Documented, unshipped (4):** `company-brain`, `discord-team`, `marble`,
  `supermarket`. Pages describe products with no tree behind them. `supermarket`
  at least says so ("No template ships for this one yet"); the other three read
  like something you could install.

Only 5 exist on both sides. `template/web/CLAUDE.md` §"Adding a new product"
already prescribes the two-page + grid-row ritual, so the process exists and was
not followed — this is drift, not a missing convention.

- **Severity:** low — nothing breaks, but the catalog is the first thing a new
  operator reads and roughly half of it is wrong in one direction or the other
- **Scope:** product catalog docs vs `ant/examples/`
- **Affected:** anyone choosing a product from the site
- **Source:** `ls ant/examples` vs `ls template/web/pub/arizuko/products`
- **Status:** FIXED — pages written, not products dropped. Every shipped tree is
  something an operator can seed today; deleting six working templates to make a
  docs assertion pass would be the wrong direction.
- **Fix:** six page pairs written (`support`, `personal`, `pm`, `creator`,
  `socials`, `strategy`) from each product's `PRODUCT.md` + persona + facts, each
  with an intro and a setup page; the six moved from the "Templates without a
  page" list into the grid, and that section is gone. The four documented-only
  products keep their pages and now carry a "This is a shape, not a template"
  note at the top, so the page no longer reads like something you can install.
  The `nav-children` list is identical across all 31 catalog pages. Landing-page
  counts corrected ("all eight" → all fifteen).
  `TestProductCatalogMatchesCorpus` is the gate, directional by design: a shipped
  product MUST have both pages (a name `products list` prints with no page is a
  dead end), a documented product with no tree MUST carry the shape note. Both
  halves checked to fail. `template/web/CLAUDE.md` §"Adding a new product" names
  the test and the nav step. Also dropped the stale `L37` warning from
  `products/helpdesk/setup.html` — the two seeders now agree. SHA 52254c51.

## ✅ FIXED 2026-08-15 L37 — a product's skill is dead when seeded by `--product`, live when blended (2026-08-15)

The two seeders disagree about the one payload kind that relocates, so a product
shipping `skills/<name>/` works one way and silently does not the other.
Reproduced on a throwaway instance:

| Path | Where the skill lands | Agent loads it? |
| ---- | --------------------- | --------------- |
| `arizuko products <inst> apply <folder>` (blend) | `.claude/skills/triage/SKILL.md` | yes |
| `arizuko group add … --product helpdesk` (verbatim `SetupGroup`) | `skills/triage/SKILL.md` | **no** |

`container.blend.go` relocates a product's `skills/<n>/` under `skillsRel`
(`.claude/skills`) because that is where the agent looks; `seedSkills` then
layers the stock tree in beside it. The verbatim path copies the product tree as
it stands — by design, and locked byte-for-byte by `TestProductSeedIsVerbatim` —
so the directory stays at the group root, where nothing reads it.

Consequence: `arizuko create <name> --product <p>` and `group add --product <p>`
— the commands the whole product catalog tells operators to run — seed a skill
into a dead location. No corpus product shipped `skills/` until 2026-08-15, which
is why this never surfaced: the divergence existed the whole time with nothing to
expose it.

Found by running the command I had just written into
`products/helpdesk/setup.html`, which claimed both paths "end with the same files
in the same places". They do not.

- **Severity:** medium — a shipped payload kind silently reaches no consumer on
  the more prominent of the two paths
- **Scope:** `5/28` composition vs `5/21` product seeding
- **Affected:** any product shipping `skills/`; today only the `helpdesk`
  reference product
- **Source:** `container/blend.go` (`skillsRel` relocation) vs
  `container/runner.go` `SetupGroup` → `chanlib.CopyDirNoSymlinks`
- **Status:** FIXED — both seeders now share one placement rule.
- **Fix:** `container.productDest` is the single rule mapping a product-relative
  path to its group destination; `blendSkills` and the new
  `container.SeedProduct` both call it, and `SetupGroup` uses `SeedProduct`
  instead of a plain `chanlib.CopyDirNoSymlinks` (same symlink discipline, same
  bytes, one placement). Verbatim now means byte-identical, not
  layout-identical. `TestProductSeedIsVerbatim` grew the relocation exemption
  and also asserts the file is NOT left at the group root — one placement rule,
  not two — and `TestBothSeedersPlaceAlike` pins the two seeders to the same
  destination set, checked to fail against the old copy. Spec `5/28` blend table
  + §single-product updated. SHA adcbc223.

## ✅ FIXED 2026-08-15 L36 — `packages install` applies grants without saying so (2026-08-15)

Installing a package that ships `<name>-grants.json` prints:

```
installed helpbot@local (2 file(s), 1 route(s)) — run `arizuko generate lab` to apply
```

The grant IS applied and IS attributed (`acl.granted_by = 'package:helpbot'`,
verified in the lab), and `Q5` already made the audit trail per-grant for exactly
this reason: "a bundle row would say 'installed ttsd' and hide that the install
handed somebody admin on a folder". The audit row is right; the operator's
terminal is not. Files and routes are counted, grants are silent — the one asset
kind that changes who can do what is the one the summary omits.

- **Severity:** low — no wrong state, but the operator is not told about the
  change most worth telling them about
- **Scope:** `cmd/arizuko/packages.go` install reporting
- **Affected:** any package shipping `*-grants.json`
- **Source:** the install summary line in `cmd/arizuko/packages.go`; contrast
  `applyPackageGrants`, which returns each applied row for the record
- **Status:** FIXED.
- **Fix:** the summary counts grants beside files and routes, then prints one
  line per grant naming effect, principal, action and scope (plus params and
  `WITH GRANT OPTION` when set) — `describeGrant` in `cmd/arizuko/packages.go`.
  `applyPackageGrants` returns `[]core.ACLRow` instead of pre-marshalled JSON so
  the caller can both record and report from one value; the record's bytes are
  unchanged, so `remove` still reverses exactly. Files and routes are inert
  until `generate`, a grant is live the moment it lands — that is why it is
  named and not counted. `TestPackageInstallSummaryNamesEachGrant` +
  a lab assertion on the printed line. SHA 9bc40ad4.

## ✅ FIXED 2026-08-15 L35 — `tasks.toml` is a product payload nothing reads (2026-08-15)

`specs/5/21:56` says a product ships "`tasks.toml` (seed scheduled tasks)", and
`5/28`'s blend table gives it a union rule. **No code reads it.** The only
mention of the filename in any `.go` or `.ts` file is `container/blend.go:83`,
the classify rule that decides where to COPY it. There is no parser, no schema
type, no `seedTasks`, nothing.

Scheduled tasks are the `scheduled_tasks` resreg resource
(`resreg/resources/scheduled_tasks.go`) — DB rows created through the MCP tool,
the REST face, or the CLI. A file has never been an input to them.

So a product author following `5/21` writes a `tasks.toml`, `arizuko products
apply` reports success, the file lands in the group home, and no task is ever
scheduled. Found while building a reference product for the docs: I wrote one,
then went looking for the schema to document and there was none.

This is the third instance of one class, and the other two were both closed by
refusing the payload: `mcpServers` in `settings.json` (`J15`) and a product's
`migrations/` (`5/28`, refused 2026-08-15). The rule those set is that a payload
which applies cleanly and reaches no consumer is an authoring error, not an
asset.

- **Severity:** medium — silent no-op on an operator-facing path, and a spec
  actively instructs people to use it
- **Scope:** `5/28` composition, `5/21` product manifest
- **Affected:** any product author following `5/21`; no corpus product ships one
  today (asserted by `container/product_seed_test.go`)
- **Source:** `container/blend.go:83`; `specs/5/21-products.md:56`;
  `specs/5/28-packages.md:206`
- **Status:** FIXED — **REFUSED**, the same exit as `mcpServers` and
  `migrations/`. Three reasons, in order of weight. (1) *It is not the product's
  to own*: a scheduled task is a `scheduled_tasks` row with a real owning
  handler on all three faces, so a file input would be a fourth writer of that
  table whose only caller is the seeder — the one-renderer rule forbids it.
  (2) *The apply could not be reversed or re-applied*: `Manifest` maps asset
  kinds to identities and `AssetHashes` to file content, and a seeded ROW is
  neither, so `products remove` could not withdraw it and upgrade could not
  re-apply it — that is `L31`, still unsigned, and a new payload would be born
  into it already broken. (3) *The intent keeps a route that works*: a product's
  daily-digest task stays expressible through its `CLAUDE.md` region telling the
  agent to schedule it, the same escape hatch `migrations/` took. Building it
  would have made `5/21`'s sentence true at the cost of a fourth writer and a
  known-broken lifecycle; deleting the sentence makes it true for free.
- **Fix:** `classify` maps `tasks.toml` to a new `payTasks`, and BOTH refused
  payloads now render through ONE `refusedPayloads` map — the old
  `migrationsRefusal` is gone rather than joined by a twin, so a refusal is a
  row and the two seeders cannot drift on what is refused. Every refusal must
  name the route that works, asserted by `TestRefusedPayloadsNameARoute`.
  `TestBlendRefusesProductTasks` + `TestCheckProductAssetsRefusesWhatTheBlendDoes`
  cover blend and verbatim seeder; the product lab probes it beside the
  `migrations/` and `mcpServers` refusals. Specs `5/21` (sentence replaced with
  the refusal + the working route) and `5/28` (blend-table row + a
  §"Seeding a product's `tasks.toml`" decision record). SHA 84df9e82.

## ✅ FIXED 2026-08-15 L34 — handing an asset between two products in one mix DELETES it (2026-08-15)

Reproduced end to end in the product lab, from a clean group:

1. Mix is `[pa]`; `pa` ships `skills/shared/SKILL.md`. Apply → the file is at
   `.claude/skills/shared/SKILL.md`. Correct.
2. `pa` drops the skill, `pb` ships the **identical** file, mix becomes
   `[pa, pb]`. Apply → **the file is gone from the group.**
3. Apply again → `pb` writes it back.

So the asset vanishes for the whole window between two applies, and nothing
re-applies routinely. Worse, the apply that deleted it reported `pb unchanged`
— the operator gets no signal at all, and `installed_packages` records `pb` as
owning a file that is not on disk.

Cause: `BlendProducts` runs `blendSkills` first (pb's content is byte-identical,
so `writeIfDifferent` correctly writes nothing), then calls `dropStale` **per
product with only that product's own hashes**
(`container/blend.go`, `dropStale(groupDir, prior[mix[i].Name], out[i].AssetHashes)`).
`pa`'s prior hashes contain the path, its current hashes do not, and the on-disk
bytes still match `pa`'s recorded hash — because pb ships the same bytes. So
`dropStale` classifies it as "clean and no longer shipped" and removes it.
`dropStale` cannot see that another product in the SAME mix now owns the path.

The same shape produces a false warning when the contents differ: applying the
two-product mix in the lab printed "`.claude/skills/triage/SKILL.md` is no
longer shipped but was edited locally — left in place" when nobody had edited
anything; `oncall` had simply taken ownership from `helpdesk`.

- **Severity:** medium — silent asset loss on a supported operation (changing
  which product in a mix provides an asset), self-healing only on the next apply.
- **Scope:** `5/28` composition, `container/blend.go` `dropStale`
- **Affected:** any group whose `products.toml` mix moves an asset between
  providers; no live instance runs a multi-product mix yet
- **Source:** `container/blend.go` `BlendProducts` → `dropStale`; reproduced
  2026-08-15 with `arizuko products <inst> apply`
- **Status:** FIXED.
- **Fix:** `BlendProducts` computes the union of every product's `AssetHashes`
  once and passes that one `shipped` set to every product's `dropStale`, so a
  path any current provider ships is still shipped. The false "edited locally"
  warning goes with it — ownership handoff stops looking like a local edit.
  `TestBlendHandoffKeepsTheAsset` covers it (checked to fail against the
  per-product hashes) and asserts no warning; the product lab's L34 probe is now
  a pass/fail assertion instead of a known-bug note. Spec `5/28` states the
  union rule. SHA 4d40f601.

## L31 — a product never really updates: four of six payload kinds are seed-once (2026-08-15, proposal — needs sign-off)

`arizuko packages upgrade` / `sync` re-apply exactly two payload kinds: skills
(and `blendSkills` REFUSES any skill whose hash moved, so a locally edited one
is skipped) and `settings.json` keys not already present. `PERSONA.md`, the
`CLAUDE.md` region, `facts/` and `tasks.toml` go through `blendSeedOnce`, which
writes only when nothing is there yet and never rewrites
(`container/blend.go` `seedOnce`).

Consequence: a product author cannot fix anything they shipped. A typo in
`PERSONA.md`, a wrong fact, a bad default task — every group that already
installed the product keeps it forever, and the author has no channel to
correct it. `5/28` calls itself a package manager; on four of six payload kinds
it is a one-time copy.

- **Severity:** medium — no failure, but the headline capability does not work
- **Scope:** `5/28` composition, the blend table's "On upstream update" column
- **Affected:** every installed product; nothing on the fleet has been upgraded
  in place because there is no meaningful upgrade
- **Source:** `container/blend.go` `blendSeedOnce` vs `blendSkills`;
  `specs/5/28-packages.md` §Composition table
- **Status:** proposal, operator-stated 2026-08-15. **A product is overwritten
  on upgrade, like any software package; customization lives in the local
  layer, not in the package's files.**

  The local layer already exists and is already managed: the agent's memory and
  `facts/`, the operator's `~/CLAUDE.md` overlay, `.disabled` to drop a skill.
  The contract becomes two clean surfaces — product files are the product's,
  local state is the group's — instead of one file with two owners and a
  dirty-hash arbitration between them.

  `CLAUDE.md` is the precedent, not an exception: marked regions already give a
  product ownership of its own region while text outside it is never rewritten
  (`<!-- arizuko:package:<name> BEGIN/END -->`). The proposal extends that
  contract to the other payloads rather than inventing one.

  **What it dissolves.** Per-product `migrations/` (`L?`, deferred then refused
  2026-08-15) exists to transform state you cannot overwrite. If files are
  replaced, the only state left to migrate is the agent's own memory, which the
  agent curates. The deferred runner stops being owed.

  **What it costs, and the one question to settle.** Everything edited in place
  today needs a home in the local layer. `PERSONA.md` is the sharp case:
  operators do tune a persona by editing the file, and under this rule that edit
  is overwritten. Either a persona tweak becomes an overlay, or `PERSONA.md`
  keeps a carve-out — that choice is the whole decision, and it should be made
  before the table is rewritten. `tasks.toml` needs the same answer (an
  operator-added task must not vanish on upgrade).

  On sign-off this is a `5/28` spec edit first — the blend table's two right-hand
  columns — then `container/blend.go` moves payloads from `blendSeedOnce` to the
  managed path, keeping dirty-detection only where a carve-out survives.
- **Fix:**

## ✅ FIXED 2026-08-15 L30 — the SSE hub key is an unwritten string convention both ends re-derive (2026-08-15)

`webd/turn.go` was fixed on 2026-08-15 to subscribe on the real chat JID rather
than the route token's `target_folder` (spec `5/J`). The fix is correct and
tested, but it treats one symptom of a shared cause: **nothing owns the hub
key.** Each end reconstructs it with string surgery, and the two must agree by
convention alone.

Four remaining sites, all verified at `8ef52f47`:

- `routd/turns.go:716` — publishes on `strings.TrimPrefix(tc.ChatJID, "web:")`.
  For a `hook:` chat JID the prefix does not match, so the key stays
  `hook:a/b` while every webd subscriber builds a `web:`-derived key.
- `webd/channel.go:59` — `handleRoundDone` rebuilds `jid := "web:" + req.Folder`
  and looks the topic up by it. A `hook:` chat gets `"web:hook:a/b"`, so
  `TopicByMessageID` returns `""` and the event publishes under an empty topic.
- `webd/route_token.go:195,382` — `handleChatTokenPost` / `injectRouteMessage`
  subscribe and publish on `row.Folder()` (the token's target), while the
  assistant frame arrives through `handleSend` keyed on the chat JID. A
  suffixed token's `wait=` POST never sees its own reply.
- `webd/route_token.go:419,435` — `handleRouteTokenStream` builds
  `"web:" + folder` from the CLIENT-SUPPLIED `group` param and subscribes to
  it, so a suffixed chat is unreachable and the param decides the key.

- **Severity:** medium — each site is a hang, not a leak, but the last one lets
  a request parameter choose a subscription key.
- **Scope:** webd SSE delivery
- **Affected:** every route token minted with a `jid_suffix`, and every `hook:`
  chat
- **Source:** `routd/turns.go:716`; `webd/channel.go:59`;
  `webd/route_token.go:195,382,419,435`; contrast the fixed `webd/turn.go:125-140`
- **Status:** fixed by redesign (signed off): the derivation is ELIMINATED, not
  centralized — the hub key IS the chat JID verbatim (`webd/hub.go`), and the
  routd→webd wire (`/v1/round_done`, `/send`) carries `chat_jid` untouched.
  Every publisher/subscriber passes the JID it already holds (token row, turn
  context, wire field); `/chat/stream` now binds to the token record, so the
  client `group=` param no longer picks a key. A helper both ends must
  remember to call was rejected — a convention with an owner is still a
  convention. Same class as [`L4`](#l4); see the identity-derived-not-read
  rule in `CLAUDE.md`.
- **Fix:** 30a0bf3a

## L27 — five channel platforms ship no output-style file, so their agents format blind (2026-08-15, open)

`specs/5/Y` says WhatsApp, Reddit, Bluesky, Twitter and LinkedIn "keep their
single platform file". None of those files exists. `ant/output-styles/` ships
`slack{,-dm,-channel,-thread,-pane}`, `telegram{,-dm,-group}`,
`discord{,-dm,-channel}`, `email.md`, `web.md` and `README.md` — nothing else.
(Mastodon is correctly absent: `mastd` was dropped 2026-08-15.)

The picker does no host-side existence check by design (`5/Y` §"Name selection
and file loading are separate concerns"), so it names `whatsapp` for a whapd
turn, Claude Code's `readOutputStyle` finds no such file, and the agent answers
with Claude Code's stock formatting — markdown a platform may not render, no
length envelope, no per-surface shape.

- **Severity:** medium — every reply on five live adapters is unstyled; whapd
  and reditd both run today.
- **Scope:** agent output formatting
- **Affected:** whapd, reditd, bskyd, linkd, and the Twitter surface
- **Source:** `ant/output-styles/` (14 files, none of the five);
  `specs/5/Y-output-styles-per-surface.md:33-36`
- **Status:** open. Two halves, and only the first is mechanical: the spec
  sentence is false and must say those platforms ship no file today. Whether to
  AUTHOR the five files is a content decision — each one is a real
  length/markup envelope per platform, not a template — so it wants the user's
  call before anyone writes prose into the agent's mouth.
- **Fix:**

## ✅ FIXED 2026-08-15 — L25 — slakd acks Slack before routd accepts, so a deploy silently drops messages (2026-08-15)

`slakd.handleEvents` answers `200 OK` to Slack BEFORE calling routd
(`slakd/bot.go:405-406`, comment: "ack first; Slack retries on non-2xx, which we
don't want for handled events"). If the routd call then fails, `dispatch` logs
`deliver failed` and returns (`slakd/bot.go:546-548`). Slack has its 2xx, so it
never redelivers. The message is gone, with one ERROR line as the only trace.

`slakd.yml:23` declares `depends_on: [routd]` with no `condition:
service_healthy`, so the documented deploy — `systemctl restart
arizuko_<instance>` — restarts both and opens exactly this window on every
release.

`teled` does the opposite and is correct: `teled/bot.go:212-224` advances its
getUpdates offset ONLY when `delivered` is true, so Telegram redelivers on the
next poll. The two adapters disagree on whether an inbound is durable.

- **Severity:** high — silent user-visible data loss on the documented deploy
  path, on the adapter krons and marinade both run.
- **Scope:** slakd inbound durability
- **Affected:** every Slack message arriving while routd is restarting or down
- **Source:** `slakd/bot.go:405-406,546-548`; `template/services/slakd.yml:23`;
  contrast `teled/bot.go:212-224`
- **Status:** FIXED — the redesign shipped as ONE mechanism in `chanlib`, not a
  slakd-local patch. `RouterClient.RetainInbound(ctx, db)` writes a failed
  inbound to the adapter's own `inbound_pending` table
  (`chanlib/inbound.go`, `chanlib/migrations/0002-inbound-pending.sql`) and a
  15s sweep re-delivers it with a 30s→15m backoff. The ack is untouched: Slack
  still gets its 200 inside 3s.

  Wired into the two adapters that CONSUME their platform event on arrival —
  `slakd` (Slack's webhook ack) and `discd` (the gateway hands each event over
  once). Deliberately NOT wired into `teled`, `bskyd`, `reditd`, `linkd` or
  `emaid`: each already advances its cursor / seen-pointer / `\Seen` flag only
  after routd answers 200, so the platform IS their queue and a second queue
  beside a working one is a second thing to drift.

  Retention covers UNAVAILABILITY, not refusal: a 400/403/413/422 means routd
  read the payload and rejected it, so the row is dropped with an ERROR line
  rather than replayed forever (`permanentIngress`, keyed on the new
  `chanlib.StatusError` rather than on matching error text). The retained row
  carries the adapter's stable message id — routd's `messages` PK — so a
  re-delivery of a row routd already took is an `INSERT OR IGNORE` no-op.
  `GET /health` reports `pending_inbound` while the backlog is non-empty, so
  the failure surfaces where an operator already looks.

  **Correction to this entry's own sibling note.** `L20` does not exist in this
  file — the reference is dangling, and `whapd/src/queue.ts` (the in-memory
  queue it described) was deleted before this pass. whapd's inbound path DOES
  still have the shape (`whapd/src/main.ts` calls `sock.readMessages` to ack,
  then only logs a failed `rc.sendMessage`), but it is TypeScript and outside
  this Go mechanism; filed fresh as `L39` rather than fixed here.
- **Fix:** 054daefd

## L26 — `make test-race`, the documented pre-tag gate, cannot finish (2026-08-15, open)

`routd` and `store` both hit `panic: test timed out after 10m0s` under `-race`,
so the target never completes and never gates anything. CI does not run it
either. 0 races were reported in the packages that did finish, so this is a
timeout, not a detected race — but a gate that cannot run is not a gate, and
the release procedure names it.

- **Severity:** medium — no race coverage on the two most concurrent packages
- **Scope:** release verification
- **Affected:** every tag; `routd` and `store` in particular
- **Source:** `make test-race`; reproduced 2026-08-15 during the CTO audit
- **Status:** open — needs either a longer `-timeout` for those two packages, a
  split target that runs them separately, or the gate removed from the release
  procedure and replaced with something that runs.
- **Fix:**

## L24 — a fresh instance ships with egress isolation OFF, while the docs describe it as the model (2026-08-15, proposed)

`CRACKBOX_ADMIN_API` is the master switch for egress isolation
(`core/config.go:265` → `Config.EgressAPI`; `compose/compose.go:493,621` gate
the crackbox service and the per-folder agent networks on it). Nothing seeds it:
`template/env.example` is 20 lines and does not mention it, and no code under
`cmd/arizuko/` writes it. So `arizuko create` produces an instance whose agent
containers reach the internet unfiltered, silently.

`SECURITY.md:249` and `ARCHITECTURE.md:23,88,761` describe egress isolation as
the deployment model without saying it is opt-in and off by default. A reader
who follows `INSTALL.md` gets the opposite of what the security page describes,
with no warning at any step.

**Correcting the finding that surfaced this.** The 2026-08-15 CEO memo
(`.ship/critique-ceo-20260815.md`) reads `container/runner.go:184-191` — the
`egress` grant appending `"*"`, which `crackbox/pkg/match/match.go:49` honours
as allow-all — as egress default-deny being "negated by its own grant". That
half is NOT a defect: it is the documented behaviour of the `egress` grant per
spec `5/33`, and the comment at the call site says so. The grant is how an
operator deliberately opens a folder. The real gap is the one above — the
control is off unless an operator knows to switch it on.

- **Severity:** high — a security control the docs present as the model does not
  run on a default install, and nothing says so.
- **Scope:** instance seeding + the security docs
- **Affected:** every instance created from `arizuko create`; readers of
  SECURITY.md and ARCHITECTURE.md
- **Source:** `core/config.go:265`; `compose/compose.go:493,621`;
  `template/env.example` (no entry); `SECURITY.md:249`
- **Status:** proposed — needs a decision, two parts:
  1. **Docs (no sign-off needed).** State plainly in `SECURITY.md`,
     `ARCHITECTURE.md` and `INSTALL.md` that egress isolation is opt-in, name
     the variable, and say what an instance without it allows.
  2. **Default (needs sign-off).** Whether `arizuko create` should seed
     `CRACKBOX_ADMIN_API` so a fresh instance is filtered by default. That
     changes what a new install runs and requires the crackbox image to be
     present, so it is a deployment-behaviour change, not an inline fix.
- **Fix:**

## L13 — `secret_use_log` is write-only: no read surface on any face (2026-08-11, proposed 2026-08-15 — needs sign-off)

The table (routd/migrations/0008-secrets.sql) is written on every resolved
secret (`routd/mcp.go:503`, `store/secret_use_log.go:38`) and read by nothing:
no resreg resource, no REST/MCP face, no dashd page, no CLI verb — only the
side `audit_log` row each write also emits is reachable. This repeats the exact
class BUGS `F29`/`F35` closed for the four `audit_log` owners ("audit rows
reachable only with sqlite3"), and it outlived `F68` (2026-08-09), which fixed
this table's writer fields without giving it a reader. Cost: the per-key
forensic detail (key, tool, scope_kind) the writer was just fixed to record
answers "who used which secret" only to an operator with sqlite3 on the host.

- **Severity:** low
- **Scope:** secret-use forensic log read surface
- **Affected:** routd, store
- **Source:** `store/secret_use_log.go:38`, `routd/migrations/0008-secrets.sql`
- **Status:** PROPOSED, not built — the premise is CONFIRMED at 28d20228
  (readers are four test files and nothing else; no `Table:` declaration, no
  REST handler, no dashd page, no CLI verb), but the fix is a new wire contract,
  which this pass does not ship unreviewed.
  Why it is not a mechanical registration: in this codebase a resource's name IS
  its wire identity, and `5/17` derives the MCP tool from the same declaration
  that serves REST. Registering `secret_use_log` therefore publishes a new
  `/v1/secret_use_log` path AND a new agent-visible tool in one move, and the
  question "may an AGENT read which secrets were used, by whom, with which
  tool?" is a security decision the operator owns, not a detail of the wiring.
  Recommended shape, for approval:
  - Owner routd (it owns the table, routd migration `0008`), read-only: `list`
    with folder containment, no create/update/delete — the writer stays
    `store/secret_use_log.go`, the single one.
  - RowType mirroring the columns already written (`key`, `scope`,
    `scope_kind`, `tool`, `caller_sub`, `spawn_id`, `ts`), so the doc and the
    handler read one struct.
  - Gate: operator scope on REST. On the agent MCP face, either omit it
    entirely (operator-only forensics, the safer default and my
    recommendation) or bind it to the caller's own folder — never a plain
    `mcp:` grant with the default target resolver.
  - `F35`'s onbod `audit_log` resource is the precedent to copy for the
    read-only shape.
- **Evidence:** table writer/reader sweep over all migrations
  (`INSERT INTO|UPDATE` vs `FROM <table>` vs `Table:` resreg decls) →
  `secret_use_log writers=1 readers=0 resreg=0`. `grep -rn secret_use_log`
  → writer paths + migrate-split copy + docs only.

## L15 — dead-code sweep 2026-08-11: the hits deliberately REJECTED (record, not a bug)

Not a defect. This is the audit's negative result, kept so the next sweep does not
re-report the same ~135 hits. `deadcode ./...` gave 149 hits, 56 under `-test`;
nine survived verification and are filed above as `L6`-`L14`.

- **Status:** record only — nothing to fix

Production run: 149 hits. With `-test`: 56. Buckets and verdicts:

- **crackbox/** (~67 prod hits; 48 survive `-test`: `pkg/host` + `pkg/host/internal`,
  `pkg/admin.NewAPI`, `pkg/dns Server.LocalAddr`, `pkg/match.LooksLikeDomain`) —
  REJECTED: crackbox is a shipped standalone component with external users
  (`specs/6/8`; brief's false-positive #2). Reachability from arizuko's mains is
  the wrong root set for a public library. Not adjudicated here: whether some
  `pkg/host/internal` helpers are dead even relative to crackbox's public API —
  out of scope for an arizuko audit, flag to a crackbox-scoped pass if wanted.
- **tests/testutils** (28 hits: FakeChannel/FakePlatform/NewInstance/etc.) —
  REJECTED: shared test library; the 8 that survive `-test`
  (Name/Connect/Disconnect/Forward/Quote/Repost/Dislike/Edit) are interface-
  satisfaction stubs, live through the Channel interface contract.
- **resreg/resregtest** (8 hits) + `resreg/engine.go:1311 reset` ("Test-only." by
  its own comment) — REJECTED: test harness by design.
- **routd/db.go:87, runed/db.go:61, store/store.go:178 `OpenMem`** — REJECTED:
  conventional in-memory constructors for tests, colocated with the real Open.
- **runed/runtimes.go FakeRuntime.Run/Kill** — REJECTED: fake in a prod file so
  other packages' tests can import it; standard pattern.
- **queue SetSignalContainerForTest / SetActiveForTest** — REJECTED: named test
  seams (the *unnamed* dead queue surface is filed above).
- **proxyd/resource.go:114 installManualWeb** — REJECTED: self-labeled
  "test-only entry point" in its own doc comment.
- **chanlib isStubVerb + CapImplReport** — REJECTED: consumed by nine adapter
  test suites (`emaid/bskyd/slakd/discd/mastd/reditd/linkd/teled/caps_test.go`,
  `chanlib/extra_test.go`) — the drift-guard is doing exactly its intended job
  at test time.
- **chanlib NoFileSender.SendFile** — REJECTED (with note): unused mixin, but
  its siblings `NoVoiceSender` (5 adapters) and `NoPinSupport` (2) are embedded;
  it is the deliberate mixin family's spare slot for the next file-less adapter.
  4 lines; deleting buys nothing.
- **core/types.go RouteTarget.String** — REJECTED: fmt.Stringer convention;
  absence of any `%v` path over a RouteTarget value is not provable by grep, and
  a wrong deletion breaks log formatting silently.
- **groupfolder/folder.go:34 IsTopLevel** — REJECTED: documented shape predicate
  of the leaf library (deliberately renamed from IsRoot with a semantics note),
  test-covered; 1 line.
- **auth/surrogate/engine.go:83 NewEngineWith** — REJECTED: own comment says
  "tests point a…" — a named test seam.
- **auth/jwks.go:50 NewKeySet** — REJECTED as dead code (test convenience), but
  its stale "used in-process by authd" comment is filed in the wrapper-atrophy
  entry above.
- **chanlib/handler.go:252 NoFileSender / core RouteTarget / audit loadCursor+cursor.get/set/save** —
  cursor family NOT rejected: filed inside the audit-messages entry (same
  deleted chain).
- **auth/jwt.go:29 mintJWT** — NOT rejected: filed as the HS256 entry.
- **routd/tokens.go (5), routd/db.go (4), routd/sibling_db.go (2),
  routd/pending_actions.go (2), routd/loop.go StopQueue, queue
  RegisterProcess/StopProcess/SetHasPendingFn, runed/manager.go intersect,
  obs (3), audit emitMessage** — NOT rejected: filed above.

# Not determined (UNVERIFIED)

- ~~Whether any live instance sets `AUDIT_WEBHOOK_URL_MESSAGES`~~ — MOOT
  2026-08-15: the knob and its producer-less stream are deleted (`L11`). No
  setting of it ever produced an event.
- ~~`authd/http.go:40-43`'s claim that service:runed's grants entry gates a
  per-turn downscope~~ — RESOLVED 2026-08-15 while fixing `L8`: the claim was
  FALSE. runed calls `auth.ServiceToken` nowhere and mints nothing, so the
  grant gated no call. Grant, comment and dead plumbing are deleted.
- Whether the 2s poll fully substitutes for the queue's unwired
  same-group-restart (`hasPending`) in production load — behavior inference
  from code, not observed live.

## L2 — a delegated child is never dispatched: its only message is a bot row (2026-08-08, CAUSE ISOLATED 2026-08-15, fix needs sign-off, REPRODUCED 2026-08-16 on v0.64.0)

Same live run, `child-delegate`. Unlike L1 this one is NOT root-caused, and it
is recorded that way on purpose.

The v0.64.0 anteval run reproduces it unchanged (`no callback for
etjty2l-child-delegate`). The harness prints "case needs `mcp:register_group`,
`mcp:delegate_group`" on any timeout; that hint is generic and wrong here — the
logs below show both calls succeeding.

What is verified from the logs: the parent registered the child
(`"group registered" jid=web:eval/etjgjnv-child-delegate folder=eval/etjgjnv-child-delegate`)
and delegated the task (`"send" folder=eval jid=web:eval/etjgjnv-child-delegate
text="Task: run this exact command and report the result back here"`). So
registration and delegation both work. The child never fired the callback.

**CAUSE ISOLATED 2026-08-15 by the call path.** No probe was needed and no
capability is involved. Agent-to-agent delegation over `send` cannot wake the
target folder, by construction:

1. The parent's `send` reaches `ipc.internalSend` → `gated.SendMessage` →
   `routd.mcpDeliver`, which delivers, and `ipc.putOutboundRow`, which persists
   the row. That writer stamps EVERY agent-authored row `FromMe: true,
   BotMsg: true` (`ipc/ipc.go` putOutboundRow) — there is no separate shape for
   "addressed to another folder".
2. The poll DOES see it: `DB.NewMessages` filters on timestamp only, so the
   child chat is picked up and `resolve` succeeds (the child folder was
   registered), and it is enqueued.
3. `processGroupMessages` then drops it (`routd/dispatch.go:52-62`):

       // Strip bot rows from the trigger batch (don't feed the agent its own
       // output) but keep them in the rendered context.
       for _, m := range msgs { if !m.BotMsg { trigger = append(trigger, m) } }
       if len(trigger) == 0 { l.advance(chatJID, last); return false, nil }

   The child's chat holds nothing BUT bot rows, so `trigger` is empty. The
   cursor advances and the function returns `(false, nil)` — the "silent no-op"
   that is deliberately NOT a failure, so it does not even trip the breaker.
   No container is ever spawned for the child.

Both recorded candidates are answered:
- The `egress` ACL vs `network_rules` question is MOOT. No container starts, so
  no egress decision is ever reached. The two mechanisms never got to disagree.
- "the child may never have been dispatched at all" is CORRECT, and now
  explained: it was enqueued and then dropped at the bot-row strip.

- **Severity:** medium — agent-to-agent delegation silently does nothing.
- **Scope:** message model — what may trigger a turn
- **Affected:** routd (dispatch), ipc (outbound writer)
- **Status:** NOT FIXED — the fix is a redesign and needs sign-off.

**PROPOSAL, for the user, not built.** The bot-row strip is correct and must
stay: it is what stops an agent re-triggering itself on its own output. The
missing distinction is that a row authored by folder A and addressed to folder
B is B's INBOUND, not B's own output. A fix has to say so explicitly, and that
is a new contract on the message model plus a loop-safety rule:

- Make the trigger test `m.BotMsg && m.Sender != <this folder>` rather than
  `m.BotMsg` alone, so a row a DIFFERENT folder wrote can trigger. `Sender` is
  already the author folder at this writer, so no new column is needed.
- Bound it, or two agents ping-pong forever: a hop budget carried on the row, a
  depth cap on the delegation chain, or a rule that only a registered CHILD
  folder may be woken by its parent. This is the part that decides the shape,
  and it is a product call, not a code call.
- Decide whether the woken turn is credentialed as the child (socket folder) —
  it is, since the socket is per-folder — and whether the parent needs an ACL
  action to wake another folder at all.

Until then, delegation should either be refused loudly at the `send` gate when
the target folder is not the caller's own chat, or documented as not supported.
Silently storing a row nobody will ever act on is the worst of the three.

## J16 — `ant/CLAUDE.md` tells the agent connector tools are deferred; they are eager (2026-08-09, proposed)

"Third-party connector tools (Slack, GitHub, …) do NOT [load eagerly] — they're
deferred behind the **Tool Search Tool**." Connectors are `ExtTools` on the
arizuko server, and that server is `alwaysLoad: true`, so they ride the eager
prefix. `ant/src/mcp-servers.ts` said as much in its own comment before `J1`
touched it. The agent is told to search for tools already in its list, which
costs turns and invites "capability missing" conclusions when a search misses.

Pre-existing, not caused by `J1` — but `J1` removed the last server that COULD
be deferred, so no deferred MCP server exists at all now.

- **Severity:** low
- **Scope:** agent instructions vs MCP assembly
- **Affected:** ant
- **Source:** ant/CLAUDE.md:104
- **Status:** proposed
- **Fix:** either drop the paragraph, or split the routd-side MCP server so
  connector tools really are deferred (`5/17` names the split as the cost).
  Editing `ant/CLAUDE.md` fires the release path — migration file,
  `MIGRATION_VERSION` bump, agent image rebuild — so it rides a release, not a
  doc commit.

## F75 — three skill trees are triplicated and one pair has already drifted (2026-08-08, open — verified 2026-08-15, still a redesign)

`ant/skills/p5js/` also exists under `ant/skills/create/art/.../p5js/` (same for
excalidraw and manim-video). A `diff` shows the top-level
`p5js/scripts/export-frames.js` carries a chromium `executablePath` fix the
`create/`-tree copy lacks — so the duplicate is not merely redundant, it is
already stale. Deciding which is canonical for skill dispatch is the fix.

**Verified 2026-08-15, premise still holds.** All three pairs are real and
each nested copy carries a `README.md` where the top-level carries `SKILL.md`
(dispatchable skill vs. reference resource read by the `create` router):
`ant/skills/p5js` ↔ `ant/skills/create/art/p5js`,
`ant/skills/excalidraw` ↔ `ant/skills/create/diagram/excalidraw`,
`ant/skills/manim-video` ↔ `ant/skills/create/video/manim`. The `p5js` pair's
`scripts/export-frames.js` still differs — the top-level tree has the
chromium `executablePath` fix, the `create/`-tree copy does not.
NOT fixed here: syncing the stale script would patch the symptom (this
instance of drift) while leaving the duplication mechanism that produced it
in place — the same class root `CLAUDE.md` calls out under "No duplication —
amend the original." The actual fix is the canonicalization decision the
entry already names (which tree is the single source, and whether the other
becomes a symlink/reference rather than a second copy), which is a
cross-cutting structural change to skill dispatch and wants sign-off.

## ✅ FIXED 2026-08-16 — F53 — live krons teled has no state mount, so its offset resets every recreate (2026-08-07)

Closed by `L41`, which this entry had already diagnosed a week earlier ("worth
a fleet-wide fragment diff … relink only replaces fragments that are still
identical catalog copies") and which then went unacted-on until tonight's whapd
outage forced it. The generalisation, the shipped warning and the auto-repair
proposal are all in `L41`.

Live on krons 2026-08-16: the current template is installed, so the mount and
`DATA_DIR` are present, and the fragment is now a symlink to the catalog rather
than a copy that can fall behind again.

    /srv/data/arizuko_krons/store/teled -> /srv/app/home/store/teled
    DATA_DIR=/srv/app/home/store/teled
    RUNED_RUN_TIMEOUT=20m

The same install applied `T1`'s typing timeout, which had shipped to the
template and never reached the instance, and moved ttsd/kokoro onto their
scoped env files — kokoro, a third-party image, went from five instance
secrets in its environment to none.

**Note:** `teled-rhias.yml` is an operator variant and is deliberately never
relinked, so it needed the same mount by hand. Added live 2026-08-16 —
`store/teled-rhias` plus `DATA_DIR` and `RUNED_RUN_TIMEOUT`. A variant carries
this cost permanently: no catalog change will ever reach it.

Deployed `/srv/data/arizuko_krons/services/teled.yml` carries neither a state
mount nor `DATA_DIR`; the repo's `template/services/teled.yml` has both. teled's
default dataDir is `/srv/app/home` (`teled/main.go:69`), unmounted for teled, so
`teled-offset-telegram` lives in the container's ephemeral layer and is lost on
every recreate.

The deployed fragment is stale against the repo. Worth a fleet-wide fragment
diff, not just a teled fix — `arizuko generate`'s relink only replaces fragments
that are still identical catalog copies.

- **Source:** deployed `services/teled.yml` vs `template/services/teled.yml`; `teled/main.go:69,78`
- **Status:** open

## F41 — the re-greet cooldown is a rate, not a cap (2026-08-06, open question — NOT a spec defect)

Shipped with the `5/31` fold and named in the spec rather than hidden. A chat
that (a) keeps messaging, (b) still routes nowhere, and (c) never redeems its
pairing link is offered a fresh one every `store.PairingTTL` (10 min) — 144/day
in the worst case. Before the fold it was greeted exactly once, ever, which was
the permanent lockout `P1b` blocker 2 existed to remove; the trade is a live
link the user can always get for a message the user cannot switch off.

Two bounds already hold and are what make this a rate rather than a flood: the
re-arm sits in `store.InsertOnboarding`, which only routd's route-MISS branch
calls, so a since-routed chat and a silent chat are BOTH never greeted again
(this is what stopped the timer-only design from re-greeting three live krons
group chats every ten minutes — see `5/31`).

Open question, not a defect with an obvious fix: should the Nth greeting back
off? A `prompt_count` column and a widening interval is the obvious shape, and
it is a new column plus a policy constant for a case nobody has hit yet. Left
as a rate until an instance reports it.

**Reclassified 2026-08-11 — this never made `5/31` defected.** A spec is
`defected` when an open bug CONTRADICTS its guarantee. `5/31` names the rate in
its own text and shipped it deliberately, so the spec is accurate and the code
matches it; what is open is a future policy choice, which is a backlog item.
`5/31` returns to `shipped` and keeps this entry as the recorded question.

## ✅ FIXED 2026-08-15 — F46 — `runed`, `routd` and `authd` all serve `GET /v1/sessions`, for different tables (2026-08-06 — CORRECTED 2026-08-07: three daemons, not two)

**Correction (2026-08-07, during `F33`).** This is THREE daemons, not two.
`routd/server.go:286` also mounts `GET /v1/sessions` (hand-rolled,
`s.handleSessionGet`, reads `core.SessionRecord`). The original entry missed it.
The count matters: it was the reason `F33`'s signed-off "derive the advertised
set by probing paths" approach was rejected and rebuilt as derivation by mount
IDENTITY. Probing would have made BOTH routd's and runed's `/openapi.json`
publish authd's `SessionsRow` schema.

Found while picking a foreign-resource anchor for `runed`'s new doc-vs-mux
guard: `sessions` was unusable as one, because `runed` really does serve that
path. `runed/server.go:44` mounts `GET /v1/sessions` (hand-rolled, reads
`session_log` — agent spawn sessions); `routd/server.go:286` mounts it over
`core.SessionRecord`; `authd/http.go` mounts the resreg `sessions` resource at
the same path (refresh-token families, `SessionsRow`). Three daemons, one wire
path, three unrelated tables.

Since `F33` the doc no longer lies about this — only authd's RegisterREST mount
is documented, and `resreg/openapi_mount_test.go` pins that a hand-rolled mount
at a resource's path documents nothing. The wire-identity collision itself is
what the rename below closed.

It was never reachable as a live fault — the daemons listen on separate
containers, and both squatters were hand-rolled rather than resreg
registrations, so `TestResourceEndpoints_SingleSource` saw nothing. It was the
exact shape the `Name` rule exists to prevent — "a resource's name IS its wire
identity, globally unique" (root `CLAUDE.md`), the rule `proxyd_routes` violated
in 2026-07 — and it would have bitten the moment either surface was federated
behind one host, or `runed`'s spawn sessions became a resreg resource (which
`5/16`'s adoption program would do).

- **Severity:** low today, medium on federation (an aggregator or generated
  client keying on `/v1/sessions` gets whichever daemon it was pointed at)
- **Scope:** runed/server.go, routd/server.go, resreg/resregtest, authd sessions resource
- **Source:** runed/server.go `mux.HandleFunc("GET /v1/sessions", …)`;
  routd/server.go `mux.HandleFunc("GET /v1/sessions", s.handleSessionGet)`;
  authd/http.go `s.mountSessions(m)` → `/v1/sessions`
- **Fix:** ✅ 2026-08-15. `authd` keeps `sessions` — it is the registered resreg
  resource and the one with a REST twin, so it holds the wire identity. `runed`'s
  two reads became `/v1/spawn_sessions` and `/v1/spawn_sessions/recent` (matching
  its `spawns`/`session_log` vocabulary); `routd`'s became `/v1/topic_sessions`
  (its `sessions` table is the `(group_folder, topic)` → Claude-session-id map).
  Not `runs` for `runed`, though `5/5` names that for the tier: `/v1/runs` and
  `/v1/runs/{run_id}` already serve the `spawns` table, and one name over two
  tables is the defect being closed.
- **Cross-daemon path change, and why it needed no sign-off:** the ONE federated
  caller is routd's `SessionResolver` → `runed/api/v1.Client.RecentSessions`
  (`routd/session.go`), both sides in the same binary set and the same
  `docker compose up`. Its contract is already advisory — any transport error or
  non-2xx returns nil and the prompt renders "no prior session" — so a
  mixed-version window degrades exactly like an unreachable runed, never a
  failed turn. `runed`'s plain `/v1/spawn_sessions` had NO caller in the tree at
  all. `authd`'s `/v1/sessions` did not move, so dashd's authd control plane is
  untouched.
- **Cause fix, not just this instance:** `resregtest.AssertServesNoneOf(t,
  daemon, foreign, mux)` took a hand-written foreign list, and none of the six
  daemons' lists named `sessions` — which is how this survived three audits. It
  is now `AssertServesNoForeignResource(t, daemon, mux)` and DERIVES the foreign
  set as the complement of what the mux owns (every registered resource minus
  the ones `RegisterREST` mounted there), so a list can no longer omit the name
  that matters. Falsified against the pre-fix tree: restoring routd's
  `GET /v1/sessions` fails `TestRoutdMux_ServesNoForeignResource` with the
  collision message.

## F37 — three dispatch tests fail under whole-suite load, so the suite cannot gate a release (2026-08-06, open)

Found while verifying `F36`. Two packages failed on two separate whole-suite
runs and passed every isolated re-run:

- `tests/TestFeature_MessageRouting/inbound-to-dispatch` — `bot rows = 0, want
1`, with `ERROR silent turn: clean run delivered nothing` in the log. It
  polls `dispatchedFolder` on a 5s deadline and then asserts the bot row
  immediately; the dispatch was seen, the reply row was not yet written.
  Passed 6 of 6 isolated, and `go test ./tests/ -count=3` clean.
- `slakd` — one whole-suite failure, no `--- FAIL` line captured at all (only
  the package-level `FAIL`), then 20 of 20 subtests PASS isolated and
  `-count=3` clean.

None touches `authd`, refresh rotation, signing keys or sessions; the same
tree ran `53 ok / 0 fail` on three other whole-suite runs, including one
immediately before and one immediately after these.

**Third instance, same package, new test name (2026-08-06, base `9c944875`).**
`tests/TestSplitFederation_InboundToTurnRoundTrip` failed one whole-suite run
(`52 ok / 1 fail`), then passed isolated (0.098s), at `-count=3` on the package,
and on a whole-suite re-run that came back `53 ok / 0 fail`. So the flake is not
specific to `inbound-to-dispatch`: it is the `tests/` package's poll-then-assert
shape, and the fix below should be applied to every such site in the package,
not just the one named test. Disk was 89% throughout, not 100%, which weakens
the disk-pressure confound for THIS instance without eliminating it.

**A second, unrelated failure mode seen in the same session — do not confuse it
with this bug.** One whole-suite run returned `0 ok / 61 FAIL`, every package
`[setup failed]` with `package net is not in std
(/home/onvos/sdk/go1.27rc2/src/net)`. The directories all exist; the next run
was clean. That is a transient toolchain/cache race under concurrent agents, not
a dispatch flake and not `no space left on device` — it presents as total,
uniform failure across every package, whereas this bug is one or two packages.

**A confound that must not be dropped from the record.** The host was at 100%
disk during the first failure — a later run in the same session aborted
outright with `no space left on device` across ~40 packages — and three agents
were running suites concurrently. So "flaky under load" is supported, but
"flaky under *load alone*" is not: disk pressure is an equally good explanation
and was demonstrably present. Do not close this by re-running on an idle box;
that reproduces the pass, not the failure.

This is the same shape as `F36`'s own filing — a test whose outcome depends on
scheduling is reporting something, and the honest first step is to make the
failure reproducible rather than to re-run until green. The difference is that
`F36`'s invariant was over committed rows and could be forced with a barrier;
these two are waiting on asynchronous work with no barrier to hold.

- **Severity:** medium (a suite that flakes under load cannot gate a release,
  and the two flakes were on different packages on different runs)
- **Scope:** the `tests/` package's poll-then-assert sites (at least
  `feat_routing_test.go` and `TestSplitFederation_InboundToTurnRoundTrip`),
  `slakd` package tests
- **Affected:** CI and any developer running the full suite on a busy machine
- **Source:** tests/feat_routing_test.go:66-75 (poll-then-assert); TestSplitFederation_InboundToTurnRoundTrip; slakd whole-package FAIL with no subtest line
- **Status:** open
- **Fix:** give `inbound-to-dispatch` the same deadline treatment for the bot
  row that it already gives the dispatch — poll `countBotRows` rather than
  reading it once the instant dispatch is observed. `slakd` needs a captured
  failure first: run it under `-count=N` with the log kept, because a
  package-level `FAIL` with no `--- FAIL` line is usually a panic or timeout in
  a goroutine, not an assertion.

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

## F38 — `task_run_logs` holds orphan rows on all three live instances (2026-08-06, open)

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

## ✅ FIXED 2026-08-15 C9 — queue replay opens circuit breakers before runed is ready (2026-07-30)

On every full-stack restart, routd begins replaying pending group work before
runed listens on `:8080`. The three immediate `connection refused` attempts
consume each group's breaker budget, so healthy queued work remains blocked
after runed becomes ready. Live krons reproduced this fleet-wide at
07:49:05–07:49:08; restarting only routd after runed was healthy cleared it.

- **Severity:** high
- **Scope:** routd queue replay readiness / runed client retry
- **Affected:** every instance with pending work during restart
- **Source:** krons journal 2026-07-30 07:49:05–07:49:08; reproduced live on a
  krons deploy 2026-08-15 19:54:48 (`connect: connection refused` to
  `runed:8080`, four groups' breakers opened, then self-recovered)
- **Fix:** `49e967e5` — the breaker no longer counts a dependency outage.
  `queue.ErrDependencyDown` marks a failure the group did not cause;
  `queue.runForGroup` leaves the streak untouched for it (neither incremented
  nor reset — nothing ran, so there is nothing to blame or credit) and logs it
  loud as an operator condition. `routd.runedUnreachable` decides, tagging only
  a failed CONNECT: `syscall.ECONNREFUSED` (nothing listening on :8080 yet) and
  `*net.DNSError` (compose has not published the name yet). A
  `*runedv1.APIError` is excluded explicitly — runed took the request and
  refused it, which IS the folder's business — and a mid-run deadline still
  counts, because that run started.

Chose the breaker-accounting fix over startup ordering: runed can restart at any
time, not only at boot, so an ordering gate would fix the boot case and leave
the general one. No new mechanism — this joins the exemption `dispatch.go`
already applies for `busy` ("do NOT return an err, which would trip the queue
breaker"), and expresses the one rule the breaker has: only a failure
attributable to processing THIS group counts.

**Correction to the original entry.** "Healthy queued work remains blocked
after runed becomes ready" is WRONG, and the fix should not be read as
unblocking anything. `consecutiveFailures` is never a gate — the only non-test
read is in `EnqueueMessageCheck`, which RESETS it — so routd's breaker was
log-only and the next poll always re-fed the batch. runed's own
`circuit_breaker` table could not have been touched either: runed was not
running to increment it. The real defect was a false alarm on a signal
`CLAUDE.md` lists as a post-deploy RED FLAG ("circuit breaker open"), pointing
an operator at four healthy groups instead of at the one daemon still starting.
The severity stands for that reason; the blocked-work claim does not.

- **Verified:** `queue/dependency_down_test.go` — a dependency-down return
  leaves the streak at 0 across threshold+2 runs, does not reset a streak real
  failures built, and real failures still count once the plane recovers.
  `routd/runed_unreachable_test.go` — table over the classifier, using a real
  dial to a closed port rather than a hand-built error, plus the API-error,
  deadline and decode-failure negatives.

## ✅ FIXED 2026-08-15 — T1 — stalled typing indicator on timed-out/errored turns (2026-07-26)

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

Re-verified 2026-08-15, unchanged: `DefaultTypingMaxTTL` (`chanlib/typing.go`)
is still `20*time.Minute`, `RUNED_RUN_TIMEOUT` (`runed/cmd/runed/main.go`)
still defaults to the same `20*time.Minute`, and `dispatch.go`'s typing-clear
still gates on `!out.Steered` with no run-end reconcile for an orphaned
steered turn (`turns.go`'s `appendAndDeliver` is still the only other clear
site). Not contained — routd turn-lifecycle change across dispatch.go +
turns.go + the chanlib backstop, not one call path — so left unbuilt per the
redesign sign-off gate.

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

**FIXED 2026-08-15 — points 1 and 2 shipped; point 3 was CONFIRMED and split
out.**

Point 1, and it is the cause rather than the symptom: `turn_context.run_id`
ALREADY records which run each turn was steered into (`SetTurnRunID`, set from
the steer ack's parent `RunID`), so the run-end clear reads that instead of
keeping a second ledger of indicator owners. `DB.TypingJIDsForRun` resolves each
turn's indicator chat the same way dispatch picked it — `return_to` when set,
else `chat_jid` — and dispatch's single `Typing(typingJID, false)` became a loop
over `typingOwnedBy(typingJID, out.RunID)`. This is why the reported case ran
the full 20 minutes: the parent's own clear fires on the PARENT's chat, and
routd steers by FOLDER, so a follow-up from a second chat in the same folder was
never covered by it.

Point 2: `DefaultTypingMaxTTL` is now `90%` of `RUNED_RUN_TIMEOUT`
(`chanlib.typingMaxTTL`, floored at 1m) instead of a flat `20m` that equalled
the wall. It reads the env var runed and routd already read, and
`template/services/{teled,discd}.yml` now pass it, so an instance with a wider
wall widens the backstop instead of losing the indicator early. A test asserts
the derivation stays strictly below the wall for every plausible value.

Point 3 CONFIRMED OPEN and filed as `T2` rather than folded in: the steered
branch returns before the `Outcome == OutcomeError` block, so a steered turn in
another chat gets no failure notice at all. That is a delivery concern, not a
typing concern, and merging it here would make one fix span two.

**Fix:** a4f2152d

## T2 — a steered turn gets no failure notice when its run dies (2026-08-15, open)

Split out of `T1` point 3, confirmed at this tree while fixing `T1` points 1
and 2.

`dispatch.go`'s steered branch marks the turn done and returns
(`return true, true, false, nil`) BEFORE the `out.Outcome == OutcomeError`
block that sends `runFailureNotice`. The parent run's own error branch does
send a notice — to the PARENT's chat. routd steers by FOLDER, so a follow-up
from a second chat in the same folder had its message consumed into the
container and gets nothing at all when that run dies at the timeout wall.

`T1` closed the half that lies (the indicator now stops). This is the half that
is silent.

- **Severity:** medium — the user's message was accepted and produced neither a
  reply nor an error
- **Scope:** turn failure delivery
- **Affected:** routd dispatch, any folder with two chats routed to it
- **Source:** `routd/dispatch.go` (steered early return vs the
  `OutcomeError` notice block)
- **Status:** open — needs a decision before code. The steered turn is already
  marked `done`, so there is no retry to wait for, but notifying every steered
  chat of a failed run risks a notice storm on a folder that steers heavily.
  A per-run cap, or notifying only steered turns with no delivered bot row
  (`TurnHasBotReply`), are the two shapes worth costing.
- **Fix:**

## X2 — the aws-devops product's credential model died with X1 (2026-08-09, DECIDED 2026-08-20: retire)

`specs/17/product-aws-devops.md` sells one headline: each engineer's `AWS_*`
keys reach `aws`/`boto3` **inside the container**, so CloudTrail attributes the
call to that person. `X1` removed the mechanism — `AWS_ACCESS_KEY_ID` is a
capability credential, so it is now broker-only and container env never carries
it. Six claims in that spec and the whole flow diagram assert the removed
behavior.

This is not dead paper. `arizuko create --product aws-devops` is a shipped flag
(`cmd/arizuko/main.go:221`) that seeds `ant/examples/aws-devops/`, whose
`CLAUDE.md:9-12` tells the agent its env holds the triggering user's AWS keys.
After `X1` those vars are always empty, so the product fails silently rather
than loudly — worse than a crash.

- **Severity:** medium (no live instance runs it; blast radius is the next
  operator who passes `--product aws-devops`)
- **Source:** specs/17/product-aws-devops.md:30,34,55,110,144,175;
  ant/examples/aws-devops/CLAUDE.md:9-12; ant/examples/aws-devops/PRODUCT.md:8
- **Status:** proposed — spec marked `defected`, code untouched, awaiting the
  design decision. NOT worked around.
- **Options (sign-off):** (a) host-side SigV4 connector — the AWS call becomes a
  `5/13` shape 1/2 tool, the credential stays on the host, per-operator
  attribution survives, and `registerWithSecrets` finally gets its first
  consumer; (b) an explicit, narrow, signed-off exception letting one declared
  key prefix into spawn env, which reopens exactly the hole `X1` closed and
  should be refused unless (a) is proven impossible; (c) retire the product.
  Until one is chosen, `ant/examples/aws-devops/` must not ship.
- **DECISION 2026-08-20 — option (c), retire the product.** The egress-credential
  spec is deleted and every tier of it declined: arizuko serves credentials through the MCP
  broker or not at all, and an opaque in-container client is out of scope by
  decision. That removes (a) as an option and (b) was always refusable. Verified
  the same day: `--product aws-devops` is still a shipped flag
  (`cmd/arizuko/main.go:231`) and `ant/examples/aws-devops/` still exists, but
  **zero groups on krons, sloth or marinade use it** — so retiring costs nothing
  live. REMAINING WORK: delete the flag value + the example dir, and the six
  claims in `specs/17/product-aws-devops.md` that assert the removed behavior.
- **Superseded proposal note (2026-08-11):** the egress-credential spec (deleted 2026-08-20)
  (rewritten) is the concrete mechanism for (a): iron-proxy's `aws_auth`
  transform re-signs SigV4 host-side from the same credential store —
  configuration, not new crypto code, and `boto3` works unmodified in the
  container. Decision between (a)/(b)/(c) remains the user's.

## ✅ FIXED 2026-08-15 X3 — runed gets `SECRETS_KEY` and never reads it (2026-08-09)

`compose/compose.go:178` ships `SECRETS_KEY` to runed. Nothing in `runed/` or
`container/` reads it: routd decrypts and sends plaintext in
`RunRequest.Secrets`, and runed has no store. So the decryption key for every
secret in the fleet sits in the env of a daemon that has no use for it, next to
the daemon that spawns agent containers. Found while closing `X1`.

- **Severity:** low (runed is a trusted daemon on the compose network; this
  widens the blast radius of a runed compromise, it does not create one)
- **Source:** compose/compose.go:178; no reader in runed/ or container/
- **Fix:** `c7722184` — `SECRETS_KEY` removed from `daemonKeys["runed"]` in
  `compose/compose.go`. routd and dashd keep it (they seal/unseal rows); runed
  gets already-decrypted credentials in `RunRequest.Secrets` and opens no
  secrets table.

**Why this regeneration is NOT the v0.62 `/login` outage (`M1`).** That outage
happened because the model credentials (`ANTHROPIC_API_KEY`,
`CLAUDE_CODE_OAUTH_TOKEN`, …) vanished from a regenerated `env/runed.env`. Those
keys do not come from `daemonKeys["runed"]` at all — `writeEnvFiles` appends
`store.EnvProfileKeys` for runed separately (`compose/compose.go`), so editing
the runed key list cannot drop them. `TestModelCredentialsScopedToRuned` pins
that, and it still passes.

**OPERATOR ACTION ON DEPLOY.** This changes generated output, so it only takes
effect where the compose env is regenerated:

1. Regenerate, then diff `env/runed.env` before restarting. The ONLY expected
   change is the `SECRETS_KEY=` line disappearing. If any `ANTHROPIC_API_KEY` /
   `CLAUDE_CODE_OAUTH_TOKEN` / `OPENAI_API_KEY` / `CODEX_API_KEY` line also
   disappeared, STOP — that is `M1` recurring, not this change.
2. Leave `SECRETS_KEY` in the instance's top-level `.env`. It is still required:
   routd refuses to start without it, and `env/routd.env` + `env/dashd.env`
   still carry it. Removing it from `.env` crash-loops the instance
   (`deploy_secrets_key_landmine`).
3. `sudo systemctl restart arizuko_<instance>` and confirm a real turn replies
   end-to-end — runed injecting model credentials is the path this touches.

- **Verified:** `compose.TestSplitScopesSecretsKey` now asserts routd + dashd
  carry the key and runed does not; `TestModelCredentialsScopedToRuned` (the M1
  guard) unchanged and green.

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

**Premise re-verified 2026-08-15 at `bcb5bb83`: still live, still deferred, and
the deferral now has a second reason.** All three documented sites still carry
the failing form verbatim (`ant/skills/self/mcp.md:134`,
`ant/skills/mcp/SKILL.md:24`, `ant/skills/migrate/SKILL.md:277`), plus a fourth
in the frozen migration record `ant/skills/self/migrations/052-mcpc-cli.md:7`,
which is history and must not be rewritten. `ant/Dockerfile:171` installs
`@apify/mcpc` UNPINNED, so the version an image gets is whatever npm serves at
build time — the regression can move under the fleet without a code change, and
pinning is itself the first half of the fix.

Not fixed here, for the reason the entry already gives and one more: the fix is
unverifiable without a live socket (this worktree has none, and a form that
merely LOOKS right is what put the failing one in three skills), and
`ant/skills/` is release machinery — a change there needs a migration file, a
`MIGRATION_VERSION` bump and an ant image rebuild to reach any agent. Shipping
an unverified form into all three skills would replace a known-broken
instruction with an unknown one.

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

## ✅ FIXED 2026-08-15 — L23 — a dead-lettered message is countable but not readable (2026-08-11)

Premise re-verified at this tree: `dashd/routd_page.go` still only shows a
`dead-lettered` COUNT; grepped `resreg/` for any dead-letter resource —
none exists. Still true: an operator can tell messages were lost but not
which ones, to whom, or with what content, and nothing can replay them.

Not contained — this is a new resreg `Resource` (REST + MCP, per the
CLAUDE.md "every cold-tier management entity is a resreg resource" rule),
not a one-file fix, so it needs the redesign sign-off this pass doesn't
have authority to give. Recommended shape, unchanged from the original
report: `messages WHERE status='failed' AND is_bot_message=1` is already the
durable, identifiable row set — read-only surface, no new state.

L22 (this pass) added the one piece this needs that didn't exist yet:
`delivery_attempts` is now readable (`core.Message.DeliveryAttempts` via
`msgReadCols`/`scanMessages`), where before it was write-only. The
resource itself — registration, REST handler, MCP annotation, a replay
action — is still undone.

- **Severity:** medium — the recovery path the dead-letter state exists for does not exist.
- **Scope:** dead-letter visibility
- **Affected:** dashd, routd, resreg
- **Source:** `dashd/routd_page.go`
- **Status:** FIXED — shipped as the resreg `dead_letters` resource, in the
  shape this entry recommended: `messages WHERE status='failed' AND
  is_bot_message=1` is the row set, and NO new state was added.

  `GET /v1/dead_letters` + `POST /v1/dead_letters/{id}/replay`
  (`resreg/resources/dead_letters.go` for the catalog, `routd/
  dead_letters_resource.go` for the mounted handler), with the agent twins
  `list_dead_letters` / `replay_dead_letter` on the MCP socket. One handler,
  two injected gates, containment on `routed_to` by subtree — and the replay
  re-reads the row to bind the id to the caller's folder before mutating, so
  an id alone cannot reach another tenant's message.

  **Replay adds no second delivery path.** It flips the row to `pending` with
  a zeroed attempt count, which is exactly what `PendingOutbound` selects, so
  routd's existing outbound sweep sends it. It reports whether a row actually
  moved: a row already replayed answers 409 rather than claiming a queueing
  it did not do.

  Three decisions worth keeping:

  - **`DB: ""` in the registration**, like `audit`. A dead letter is EVIDENCE
    that a message was lost, so `Export`/`Plan`/`Apply` must never round-trip
    it — `arizuko apply` would be able to rewrite the record. It is also a
    VIEW of `messages`, which no manifest owns.
  - **Its own `dead_letters:read`/`dead_letters:write` scope pair**, not
    `messages:*`. `messages:write` is "speak as a channel" (it authorizes
    POST /v1/messages), and authd's `service:dashd` ceiling explicitly denies
    that; requeueing one already-composed row is a strictly smaller authority.
    dashd's ceiling went 9 → 11 with the case stated at the grant.
  - **The handler projects to `DeadLetterRow`.** `core.Message` carries no
    json tags, so returning it raw shipped Go field names in a body whose
    `/openapi.json` schema is reflected off `DeadLetterRow` — the two
    declarations disagreed on every field name. Caught by the REST test.

  `/dash/routd/` now renders the rows (chat, the lost text, refused-vs-retries
  -exhausted, when) with a replay button, where it rendered a bare count. It
  reads over HTTP and writes over HTTP even though the count beside it reads
  routd.db directly: a page whose read and write disagree about the source of
  truth is how the two drift.
- **Fix:** 752185f0

## ✅ FIXED 2026-08-15 L16 — legacy plaintext secret rows are never sealed; the migrator has no caller (2026-08-11)

`store.EncryptPlaintextSecrets` exists to seal pre-encryption rows in place, and
it has **zero non-test callers** — it is wired to no startup path, so it has
never run on any instance.

This matters more since `S2`: requiring the key stops NEW plaintext writes, but
every row dashd already wrote in plaintext stays that way on disk. `S2` closed
the leak; it did not clean up what leaked. A reader still decrypts those rows
correctly (`open` falls back for un-prefixed values), so nothing is broken —
they are simply unencrypted at rest, which is exactly what `5/14` promises they
are not.

Wiring a write-migration into boot is a behavior change on a live table, so it
is recorded rather than done.

- **Severity:** medium — a real at-rest exposure, bounded to rows written
  before the fix
- **Scope:** secrets at-rest migration
- **Affected:** store, routd
- **Source:** `store/secrets.go` `EncryptPlaintextSecrets`
- **Status:** open — needs sign-off on where the migration runs.

**Premise re-verified 2026-08-15 at `bcb5bb83`, and it holds exactly.**
`EncryptPlaintextSecrets` has two callers and both are in
`store/secrets_test.go`; no daemon boot path, no CLI verb, no migration
references it. It is correct code (idempotent, `WHERE value NOT LIKE 'v2:%'`,
re-seals in one transaction, no-ops without a key) with nowhere to run.

**PROPOSED SHAPE — an explicit operator verb, NOT a boot hook.** Recommending
`arizuko secret seal-legacy <instance>` (a `--dry-run` counting rows first)
over wiring the call into routd's boot after `SetSecretKeys`, for two reasons
this file already paid for:

- The function it REPLACED, `PurgeUnencryptedSecrets`, was a boot-time mutation
  of this same table, and it wiped every secret on each startup because writes
  never wrote the prefix it keyed on (recorded in its own doc comment). A
  second boot-time in-place rewrite of `secrets.value` is the same shape as the
  outage.
- `derived_vs_stored_migration` is the standing lesson: a data migration whose
  rehearsal matches 0 rows is a red flag, not an all-clear. An operator verb
  reports the count before and after; a boot hook runs silently on three
  instances at once with the daemon's exit code as its only signal.

Blast radius if wrong is total — this rewrites the column every credential
lives in — so it does not ship inline, and it wants a rehearsal against a
read-only `.backup` copy of each instance before the real run.

- **Fix:** `33d4d277` — the proposed shape, built as proposed. `arizuko secret <inst>
  seal-legacy [--dry-run]` (`cmd/arizuko/secret.go` `runSealLegacy`) is the
  caller — an explicit operator verb, NOT a boot hook, for the two reasons
  recorded above. `store.CountPlaintextSecrets` is new and backs the dry run;
  `EncryptPlaintextSecrets` now returns the count it sealed, and the verb prints
  before/after and FAILS if any row is still plaintext after the run. Reachable
  from `user-secret` too — it shares `cmdScopedSecret`, and the table is one
  table, so the verb seals every scope either way.

**Reachability checked before writing the caller, per the brief.** The migrator
was live code with no caller, not dead code: `EncryptPlaintextSecrets` compiled,
had two passing tests, and the only thing missing was an entry point. That is
the opposite of `L17`'s `LookupSecret`, which was a dead READER (deleted rather
than wired). A sealer with no caller leaves data exposed; a reader with no
caller leaves nothing.

**Also changed: no key is now an error, not a silent success.** It returned
`nil` on an empty keyring, so a caller could not tell "sealed nothing, there was
nothing to seal" from "sealed nothing, there was no key" — the exact ambiguity
that let this sit unrun. It returns `store.ErrNoSecretsKey` (the existing error,
not a second one). The CLI cannot reach that branch: `openStoreWithKey` already
calls `core.RequireSecretKeyring`.

**Doc drift this exposed.** Three docs asserted the mechanism was already wired,
which is part of why nobody noticed it was not: `SECURITY.md` claimed "legacy
plaintext rows migrated in place on boot"; `store/README.md` still documented
`PurgeUnencryptedSecrets` — the function DELETED in this area's last fix — as
"called on startup ... removes rows without `v1:` prefix". Both corrected;
`cmd/arizuko/README.md` gained the verb.

- **Operator note:** rehearse `--dry-run` against a read-only `.backup` copy per
  instance before the real run, and read the count. A rehearsal matching 0 rows
  is a RED FLAG, not an all-clear (`derived_vs_stored_migration`).
- **Verified:** `cmd/arizuko/seal_legacy_test.go` — seals plaintext rows without
  changing what any reader sees (including the already-sealed row, which must
  not be re-encrypted), `--dry-run` reports the count and changes nothing, a
  clean table is a no-op, and a keyless store refuses. `store/secrets_test.go`
  updated for the count return, still asserting idempotence (second run seals
  0).

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

## OPERATIONAL 2026-08-15 — Admin cannot log in to the krons dashboard (2026-07-12, HIGH, config-not-code)

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

  **2026-08-15 re-check:** OPERATIONAL — a live-instance login attempt, not
  a repo-verifiable claim. Operator: run `sudo sqlite3
  /srv/data/arizuko_krons/store/authd/auth.db "select sub, created_at from
  auth_users;"` to confirm whether a login has since completed; if still 0
  rows, follow the three numbered actions above. No further code change.

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

## dashd reads SQLite directly — spec 7/1 (formerly 6/1) says it shouldn't; code and CLAUDE.md say it should (2026-06-16, PROPOSED — spec/code conflict, needs sign-off, verified 2026-08-15)

`dashd/main.go` opens `routd.db` (`dbRoutd`), `onbod.db` (`dbOnbod`) and `runed.db`
(`dbRuned`, via `openSiblingOwner`) directly and queries them from most page handlers
(groups, tasks, routes, activity, secrets, usage). The renamed spec (`6/1` →
`specs/7/1-cockpit-index.md`, still `status: draft`) still says, verbatim: "Every
datum a dashboard shows or mutates flows through the owning daemon's `/v1` surface.
No dashboard — hub or per-daemon — reads a SQLite DB directly. This retires dashd's
current direct reads of routd.db/onbod.db/messages.db."

That is the OPPOSITE of root `CLAUDE.md`'s current "### Split write-discipline":
"FS-mounted daemons (dashd, onbod, CLI) write owned tables directly to the owner's
DB" — and the code comments agree with CLAUDE.md, not the spec: `dashd/main.go:385`
"dbRoutd *sql.DB // routd.db handle — dashd's ONLY message/config store", `:417`
"dashd IS FS-mounted on onbod.db (dbOnbod, for the invites page)". `openSiblingOwner`
(`dashd/main.go:451`) is a THIRD documented pattern: dashd also opens `runed.db`
read-only as a "sibling owner" for dashboard views it doesn't own.

The one page that WAS moved off direct DB reads is `/dash/audit/`
(`dashd/audit_page.go`), whose header comment explicitly frames the old direct-read
pattern as "the recorded defect class that dashd/route_tokens.go already is" and
fans out to each owner's `/v1/audit` instead. So the codebase is not uniform: one
page adopted the spec's contract, most did not, and CLAUDE.md documents the
NON-adoption as the shipped decision.

- **Severity:** high
- **Scope:** dashd, spec 7/1 (draft, never shipped) vs. root CLAUDE.md (shipped)
- **Affected:** all dashd pages (groups, status, invites, memory, tokens, activity, tasks, usage)
- **Source:** dashd/main.go:385-451; dashd/audit_page.go:3-16; specs/7/1-cockpit-index.md:33-61;
  root CLAUDE.md "### Split write-discipline"
- **Status:** PROPOSED, not fixed — this is a spec-vs-implementation conflict, not a
  contained bug: resolving it either means (a) implementing the full `/v1`-only
  read-path for every dashd page the way `/dash/audit/` already does (cross-daemon
  control-flow change, new `/v1` read endpoints per `specs/7/3`/`7/12`), or (b)
  marking spec 7/1's "no direct DB" section declined/superseded and updating it to
  document the FS-mounted direct-read pattern CLAUDE.md and the code already ship.
  Either direction needs a decision, not a docs-only close. NOT fixed this pass.
- **Fix:** needs sign-off — see Status.

## Codex audit 2026-06-18 — dashd full sweep

---

## chat: loadChatSessions limits before filtering; nondeterministic continue links (2026-06-18, PARTIAL — folderSessionTokens half PROPOSED, needs sign-off, verified still open 2026-08-15)

`chat.go:90`: newest 200 rows fetched then filtered in Go — older sessions disappear once table
grows. `chat.go:361`: `folderSessionTokens` infers topic→token by time proximity; two sessions
without an early message produce nondeterministic continue links.

- **Severity:** medium
- **Scope:** dashd/chat.go:90,:361
- **Source:** codex audit 2026-06-18
- **Status:** partial — LIMIT-before-filter half resolved a51989cc (2026-07-05,
  verified 2026-07-13: `folder IN (...)` pushed into SQL before the LIMIT 200).
  `folderSessionTokens` half re-verified 2026-08-15, still open and NOT a contained
  fix: for each `chat_sessions` row (newest created_at first) it takes the first
  `messages` row with `timestamp >= created_at AND topic != ''` as that session's
  topic, first claim wins (`dashd/chat.go:359-391`). Two sessions from the same
  folder created close together can each have a topic-bearing message fall inside
  the other's `>= created_at` window, so the session→topic mapping can attach a
  continue link to the wrong session — a heuristic on message timing, not an
  explicit link. A real fix needs an explicit session↔topic (or session↔turn)
  column persisted at session-creation/first-message time — a schema change to an
  existing table, not a new call path — so per CLAUDE.md redesign discipline this
  is reported, not fixed, pending sign-off on where that column lives
  (`chat_sessions.topic` set once at first message, vs. `messages.session_id`).
- **Fix:** push filter into SQL (done); persist topic-session linkage explicitly (proposed, open)

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
- **2026-08-15 re-check:** two separate facets, still both true. (a) OPERATIONAL,
  live-krons-only — verify with `sudo curl -s -o /dev/null -w '%{http_code}\n'
  https://krons.fiu.wtf/hook/<a-real-token>` (should NOT 302 to `/pub/...`) and
  `sudo sqlite3 /srv/data/arizuko_krons/store/proxyd/proxyd_routes.db "select
  path from proxyd_routes where path like '/hook%';"`. (b) code-level design
  gap CONFIRMED still present: `proxyd/main.go:loadInitialRoutes` (`:238-273`)
  still seeds `PROXYD_ROUTES_JSON` into `proxyd_routes` ONLY when the table has
  zero rows ("Persistence wins when proxyd_routes has rows" — spec 6/2
  Phase-3), so any pre-existing instance never gains a newly-added
  `coreProxydRoutes` entry without a manual row insert. A durable fix is an
  upsert-missing-core-routes-on-boot path — new boot behavior, cross-cutting
  (every instance's route table), so this stays a PROPOSAL needing sign-off,
  not a same-session fix.

## ✅ FIXED 2026-08-15 P3 — Proxyd dispatch combines routes from two database snapshots (2026-07-30)

Route matching reads one snapshot, then proxy selection rereads the table and
rebuilds proxies. A concurrent route mutation can pair old authentication policy
with a new backend, return a false 404, and doubles database/proxy construction
work on every routed request.

- **Severity:** high
- **Scope:** proxyd request dispatch
- **Affected:** requests concurrent with `proxyd_routes` mutation
- **Source:** proxyd/main.go:124-136,625-626,654-660; proxyd/resource.go:60-91
- **Fix:** `0dc7dce8` — one snapshot per dispatch. `s.routes()`/`s.proxies()` each called
  `rr.snapshot()` — two reads of `proxyd_routes` and two full proxy-map builds
  per routed request. Replaced by `s.routeSnapshot()`, taken once in the
  catch-all handler and passed into `dispatchRoute(rt, proxies, …)`; `s.routes()`
  survives only for the callers that need the table and no backend (startup log,
  `/health` count). Halves the per-request DB reads and proxy construction as a
  side effect.

The false 404 is now impossible-by-construction rather than merely unlikely:
`snapshot()` builds a proxy for every non-redirect route in the same read, and
`dispatchRoute` returns early on redirects, so `proxies[rt.Path]` cannot be nil
for a route matched from that snapshot. That branch answered a bare 404 —
blaming the caller for a proxyd bug — and now logs loud and answers 500.

- **Verified:** `proxyd/dispatch_snapshot_test.go`.
  `TestSnapshotCarriesAProxyForEveryRoute` is the deterministic structural
  guard. `TestConcurrentRouteMutationNeverSplitsAcrossSnapshots` deletes and
  re-adds THE MATCHED route under 8 concurrent readers and requires both a
  match and a miss to occur (else the fixture is vacuous), then asserts no
  matched request ever lost its backend. Mutation-checked: restoring the second
  `routeSnapshot()` call inside the loop reproduces it — "4 of 58 matched
  requests found no backend". Full `go test ./proxyd/ -race` green (424s).

## ✅ CLOSED-STALE 2026-08-15 K1 — Security-sensitive identifier generators discard entropy failures (2026-07-30)

JWT JTIs, OAuth-state nonces, signing-key IDs, run IDs, and session UUIDs are
returned after unchecked `crypto/rand.Read`. Entropy failure therefore emits
predictable or duplicate identifiers instead of aborting issuance or execution.
PKCE and refresh-token generators already return these errors correctly.

- **Severity:** medium
- **Scope:** authentication and execution identifier generation
- **Affected:** JWT lineage, OAuth state uniqueness, key rotation, run/session identity
- **Source:** auth/es256.go:140-145; auth/oauth.go:160-164;
  authd/server.go:95-101; runed/manager.go:364-376
- **Fix:** NONE — no code changed. The premise does not hold on this toolchain.

**`crypto/rand.Read` cannot return a non-nil error.** Since Go 1.24 it is
documented "It never returns an error, and always fills b entirely. Read calls
io.ReadFull on Reader and CRASHES THE PROGRAM IRRECOVERABLY if an error is
returned." `go.mod` declares `go 1.27`, so no supported build of this module
gets the old behaviour.

Verified empirically rather than from the doc comment, on `go1.27rc2`: a probe
swapping in a always-failing `rand.Reader` does not return an error to the
caller — it dies with `fatal error: crypto/rand: failed to read random data (see
https://go.dev/issue/66821)`, unrecoverable, from `crypto/rand.fatal`.

So the failure mode the entry describes — "entropy failure therefore emits
predictable or duplicate identifiers instead of aborting" — has no reachable
path. Every listed site is ALREADY maximally fail-loud: the process dies before
a weak identifier can exist. All eight discarding sites (`auth/es256.go:152`,
`auth/oauth.go:162`, `authd/server.go:101`, `routd/server.go:772`,
`runed/manager.go:505,512`, `crackbox/pkg/run/run.go:168`) import
`crypto/rand`, not `math/rand` — checked, not assumed.

The entry's closing line has it backwards: the PKCE and refresh-token generators
it praises for "returning these errors correctly" are the ones carrying
UNREACHABLE branches. That is harmless and conventional, so it is left alone —
deleting dead `if err != nil` across seven files would be churn with no
behaviour change.

**Checked for a real instance of the class while here, and found none.** The two
`math/rand` users are not identifier generators in the security sense:
`chanlib/retry.go:121` is backoff jitter, and `runed/docker.go:244` is a
uniqueness suffix on a temp filename inside one folder's own private IPC dir. If
either had minted a token or nonce it WOULD have been this bug for real, since
`math/rand` genuinely is predictable.

**Re-file only if** `go.mod`'s floor ever drops below `go 1.24`, or a
security-sensitive identifier starts coming from `math/rand`.

## ✅ FIXED 2026-08-15 A4 — ACL read errors are indistinguishable from valid empty policy (2026-07-30)

Query failures intentionally return no rows and therefore deny authorization,
but the API cannot distinguish that failure from an empty policy. User-scope and
list endpoints consequently return successful empty results. Worse, individual
scan failures are skipped, producing a partial policy that can omit a deny row
while retaining allows.

- **Severity:** high
- **Scope:** ACL storage and authorization evaluation
- **Affected:** agent authorization, login scope snapshots, ACL management surfaces
- **Source:** store/acl.go:176-310; auth/authorize.go:59-99; routd/server.go:506-514
- **Fix:** `9076253a` — the four `store` ACL readers (`ACLRowsFor`, `ACLWildcardRows`,
  `ListACL`, `ListACLByScope`) return `([]core.ACLRow, error)`. Authorization
  still fails CLOSED everywhere — the error return changes who can SEE the
  failure, never the direction it fails in.

Two distinct defects, both closed:

1. **A read error read as an empty policy.** A query error returned a bare `nil`
   with no log at all. Now an error, so `GET /v1/users/{sub}/scopes` answers 500
   instead of `404 no_grants` (authd snapshots that at login — it would have
   minted a scopeless session indistinguishable from a legitimately
   unprivileged one), `list_acl` answers 500 instead of `{"acl":[]}`, dashd's
   grants page shows an error banner instead of an empty table, and `arizuko
   grants` errors instead of printing "no grants".
2. **A partial policy could ESCALATE.** Per-row scan failures were SKIPPED
   (`if err == nil { append }`). Dropping a DENY row leaves the allow rows
   above it intact, so the caller is granted something an operator explicitly
   forbade — the more dangerous half, and the reason a partial read is now
   never returned at all: one bad row fails the whole read.

Consolidated rather than repeated: `store.scanACLRows` is the single place a
policy read materialises, and `auth.policyRows` is the single exact+wildcard read
all four evaluators (`Authorize`, `EffectiveActions`, `UserScopes`, `CheckHold`)
go through — previously four hand-copied pairs of calls that could each drift on
what a failed read means.

Fail-closed directions, all logged loud: `Authorize` DENIES; `EffectiveActions`
advertises NO tools; `CheckHold` holds nothing; `auth.Delegate` REFUSES (it is
the precondition on a write handing out authority — if what the granter holds
cannot be read, nothing is provably a subset of it); proxyd stamps no
`X-User-Groups`; dashd treats the caller as having no access.

- **Verified:** `store/acl_partial_test.go` corrupts ONE row's `grant_option` so
  only that row fails to scan, and requires an error with no rows — the
  dropped-deny escalation. `store/failclosed_test.go` rewritten: it used to
  ASSERT the old contract (empty on a broken table) and now requires an error
  from all four readers. `auth/userscopes_test.go` gained
  broken-table cases for `UserScopes` (error, not empty), `Authorize` +
  `EffectiveActions` (still deny/hide) and `Delegate` (refuses), each with a
  vacuity guard proving the fixture allowed before the table was dropped.
  `routd/acl_read_error_test.go` pins the endpoint 500; mutation-checked by
  disabling the error branch, which reproduces the exact `404 no_grants`.

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

## ✅ FIXED 2026-08-15 (CLOSED-STALE — fixed in 32b180da, entry never closed) — E1 — five adapters keep state in unmounted paths; it dies on every recreate (2026-08-03)

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
- **Fix:** 32b180da — give each fragment the narrow rw mount it needs
  (`whapd.yml:9` and `twitd.yml:9` already show the pattern — a single subpath,
  not the data dir), set `DATA_DIR` where absent, and make `loadOffset` fail
  loud instead of swallowing the read error.

**CLOSED-STALE 2026-08-15. Every claim in this entry is false at HEAD: the fix
landed in `32b180da` and the entry was simply never closed.** Re-verified point
by point:

- `grep -c 'volumes:'` on
  `template/services/{teled,bskyd,reditd,linkd,emaid}.yml` returns **1 for
  each**, where the entry says "declares `volumes:` zero times". Each is the
  narrow subpath the fix line prescribed, e.g.
  `${DATA_DIR}/store/teled:/srv/app/home/store/teled`.
- All five fragments now set `DATA_DIR` explicitly, including the two the entry
  singled out as not even having it (`reditd`, `emaid`), and all five daemons
  carry a matching code default (`chanlib.EnvOr("DATA_DIR", …)`).
- `loadOffset` no longer swallows the read error. It separates the three cases
  (absent file → genuine first run, offset 0; unreadable → error; unparseable →
  error), and `teled/main.go` returns that error out of `Start` with the
  comment saying why, so `chanlib.Run` exits rather than polling from offset 0
  and re-answering Telegram's ~24h backlog.

The LIVE half is a different entry and stays open: deployed instances include
their OWN `/srv/data/arizuko_<inst>/services/*.yml`, not `template/`, so krons
still runs the pre-fix fragment. That is `F53` (and `R1` finding 1) — fragment
relink on deployed instances, not this repo's templates.

## R1 — codex review of the 2026-08-03 fix batch: six gaps (2026-08-03, 5 of 6 closed 2026-08-15; 1 open on live-state only)

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
- **Status:** 2026-08-15 — each of the six re-verified SEPARATELY at `bcb5bb83`.
  Five are closed, one (1) is closed in code and unverifiable here. Verdicts:

  1. **Closed in code; live state unverified.** The mechanism claim still holds —
     `compose/compose.go:764` writes `./services/<name>.yml`, i.e. the
     instance's own dir, never `template/services/`. But the update path R1
     demanded now exists as a real command: `arizuko packages upgrade` (one
     package) and `arizuko packages sync` (all) both re-fetch the recorded
     source and rewrite the installed fragment through ONE updater,
     `reapplyPackage` (`cmd/arizuko/packages.go:371`), which refuses to
     overwrite a locally edited asset instead of clobbering it. `template/
     services/teled.yml` carries the `${DATA_DIR}/store/teled` state mount and
     no `CHANNEL_SECRET`. What cannot be checked from this worktree: whether
     krons/sloth/marinade have actually RUN it, and whether hand-made variants
     like `teled-rhias.yml` carry an `installed_packages` record for `sync` to
     reach. Both are live-host questions.
  2. **Closed.** teled no longer swallows the save. `maxSaveFailStreak = 3`
     consecutive `saveOffset` failures now call `fatalExit` (`teled/bot.go`),
     so a full or read-only mount crashes the process instead of looking
     healthy until restart. A single failure is still logged and tolerated,
     which is correct — a transient write hiccup must not crash-loop the poll
     goroutine.
  3. **Closed.** Both halves. `resreg/resources/*.go` contains no tier language
     at all (the `route_tokens.go:86` sentence is gone), and `ant/CLAUDE.md`
     now states the opposite of what R1 found: lines 4, 35 and 173 each say
     "5/33: no tiers" explicitly. Repo-wide, nothing outside this queue's own
     history says "Tier 0-2 only" or "Tier 0-1 only". See `M1` for the last
     residue of this class, fixed in the same pass.
  4. **Closed by a schema change, not by a serializer tag.** `json:"token"` is
     gone because the FIELD is gone: migration
     `store/migrations/0077-invites-hash-at-rest` made `ref` (=
     `hex(sha256(token))`) the primary key, so the raw bearer is never
     persisted and `InvitesRow` has no token field to leak by omission. dashd
     renders `ref`. The only surviving raw token is
     `toInviteCreatedJSON`'s mint-time response (`onbod/admin.go`), which is the
     one moment the caller must receive it.
  5. **FIXED 2026-08-15** — see the commit closing it. It was real: FIVE
     resources carry `SkipApplyRebuild` (secrets, route_tokens, invites,
     onboarding, installed_packages), and `printPlan` printed the secrets
     command for all five.
  6. **Closed.** `webd/Makefile` `test-e2e` captures the output and `exit 1`s on
     "no tests to run", with a comment naming the root Makefile's guard as its
     mirror. A selector matching zero tests can no longer pass as green.

## V2 — dashd duplicates control planes phase 7 moves away — CHAT PORTAL + MINT FORM DELETED 2026-08-15; the rest BLOCKED on `7/3` (2026-08-04, partial)

Codex review of the dashd surface against the post-phase-5 end state. Almost
nothing is *dead* — the identity deletions left only textual rot. The problem is
**duplication**: dashd hosts control planes that `specs/7/3` moves to each
daemon's own `/dash/<daemon>/`.

| target | LOC | why |
| ------ | --- | ---- |
| ~~`chat.go` + test~~ **DONE** | ~1,019 (really 477 + 504 test) | duplicates webd's `/me/chats/new`; **records the RAW reusable route bearer** (`chat.go:25,38,450`) |
| routes / route_tokens / errored-retry — **mint form DONE, rest blocked** | ~867 | duplicates routd's planned `/dash/routd/`. The mint form (`route_tokens.go:103`) should not survive anywhere — cockpit may list/revoke, minting belongs to webd and agent tools |
| `runed_page.go` | 526 → **really 289** | reads `runed.db.spawns` directly and proxies kill; both are runed's |
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
- **Fix:** `f98f2f33` — chat portal + `chat_sessions` drop (routd `0036`) +
  route-token mint form. The rest stays — see below.

### Done 2026-08-15 — the two that were not sequenced

**`chat.go` deleted** (477 lines + 504 of test, not the ~1,019 counted here —
the count was of both files). Replacement verified live, not assumed: webd
registers `GET`+`POST /me/chats/new` (`webd/server.go:130-131`) and proxyd
routes `/me/` to it with `Auth: "user"` (`compose/compose.go`). `chat_sessions`
— the RAW reusable bearer store at `chat.go:428` — is dropped by
`routd/migrations/0036-drop-chat-sessions.sql`; 0029's own header records zero
rows fleet-wide, so there was nothing to preserve. dashd's `chat` tile and
both folder-link sites (`runed_page.go`, `routd_page.go`) now point at
`/me/chats`, and the dead `.conv-*` CSS is out of `theme/theme.go`.

**Route-token mint form deleted** (`route_tokens.go`, the `POST
/dash/tokens/{folder}/` route with it). This one needed no replacement: routd's
`route_tokens` resource already serves both faces —
`POST /v1/route_tokens/{chat,hook}` for operators and `issue_chat_link` /
`issue_webhook` / `issue_pairing_link` for agents
(`resreg/resources/route_tokens.go:38-54`) — plus `arizuko token` for the CLI.
List + revoke stay. `7/3` §webd already said it: "There is no token minting
anywhere in the cockpit."

### BLOCKED on `7/3` — do not delete these yet

Verified against each daemon's route registration, not the spec's intention.
No daemon but onbod and timed has a `/dash/` route at all
(`compose/compose.go:367-370`); dashd answers `/dash/routd/` and `/dash/runed/`
from the catch-all.

| dashd surface                                        | LOC (verified)              | what `7/3` must ship first                                                                                                       |
| ----------------------------------------------------- | --------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| routes + tokens list/revoke + errored-retry           | 304 + 145 + 173 (`routd_page.go`) | routd's `/dash/routd/` — routd serves no `/dash/` namespace and compose has no route to it                                    |
| `runed_page.go` runs + kill                           | **289**, not the 526 claimed | runed's `/dash/runed/` AND `GET /v1/runs?state=`. runed's mux is 8 routes and none lists runs (`runed/server.go:39-46`); there is no `ListSpawns` in the tree, so dashd's direct `spawns` read is the only run list |
| tasks list/detail/create/pause/resume/cancel          | 323 (`tasks_admin.go`) + `main.go` list | timed's task + task-detail pages. `/dash/timed/` is ONE handler, the overview (`timed/split.go:42`), with no controls        |

Deleting any of these today removes the capability outright. Order stands:
build the per-daemon page, then delete dashd's copy.

### The cron duplication is real and ALREADY DRIFTED

Canonical is **timed's** (`timed/main.go:33-57`) — `7/3` §timed: "timed owns
the fire runtime … and all cron/interval computation". There are three copies,
not two, and the drift is live: routd's `parseTaskSchedule`
(`scheduled_tasks_resource.go:285`) accepts an RFC3339 one-shot and computes in
the process's local time; dashd's `taskNextRun` (`tasks_admin.go:20,36`)
rejects that same expression with 400 and computes in `TZ`. So a schedule the
agent accepts, the dashboard refuses. dashd's copy dies with its tasks page
(blocked above); routd's needs to converge on timed's semantics when timed's
pages land — filed as the follow-up, not fixed here, because it is a second
concern.

## ✅ FIXED 2026-08-15 N2 — an operator can grant to a non-canonical alias, silently (2026-08-04)

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
- **Status:** open — PROPOSAL, needs sign-off. Premise re-verified 2026-08-15
  at `bcb5bb83` and it holds.

**"The check is a lookup, not new state" understates it: the lookup crosses a
daemon.** `oauth_identities` lives in **auth.db** and `oauthIdentityForSub` is
unexported in package `main` of **authd** (`authd/store.go:116`). The grant and
pairing writes are **routd**, against **routd.db** (`routd/acl_resource.go`
`add_acl`). routd holds no handle on auth.db — `routd/sibling_db.go:13` states
identity is federated over HTTP, never opened — so the check is a cross-daemon
call, not a local lookup.

The transport exists: `GET /v1/identities/{sub}` returns the canonical identity
plus every linked sub, and routd already calls it via `httpIdentity.Resolve`
(`routd/identity.go:47`). Canonicality is then `identity.id != principal`.

**Why it still needs sign-off rather than being contained.** `Resolve` is
deliberately advisory: EVERY failure — transport, auth, non-200, decode —
returns the unclaimed shape, never an error, because its only caller today is
an inspect tool. Making `add_acl` REFUSE on its answer puts a hard dependency
on authd into a write path that has none, and the fail-open/fail-closed choice
is the whole decision:

- fail-CLOSED (authd down → no grants) turns an authd blip into a control-plane
  outage, over a risk with zero live instances.
- fail-OPEN (authd down → grant proceeds) is a guard that stops working exactly
  when identity data is unavailable, which is a guarantee worth less than it
  reads.

Recommending fail-OPEN with a LOUD warn on both the unreachable case and the
non-canonical case, on the grounds that N2 is a data-hygiene guard and not a
containment boundary — but that is the user's call, and either way `Resolve`
must grow a distinguishable "could not answer" result instead of folding it
into "unclaimed". Do not ship this inline.

- **Fix:** `22e16615` — built as recommended: fail-OPEN, both cases warned.
  `Server.nonCanonicalWarning` (`routd/acl_resource.go`) runs on the `add` case
  of the ONE shared `aclHandler`, so the agent's `add_acl` and the operator's
  `POST /v1/acl` get it from the same line. `IdentityResolver.Resolve` grew the
  distinguishable fourth result the entry required: `(identity, subs, claimed,
  error)`, where a 404 or a null identity is a real "unclaimed" (`false, nil`)
  and a token/transport/other-non-200/decode failure is an error. It never
  rewrites the principal — moving a grant the operator did not ask to move is
  the failure mode N1 chose an immutable canonical to avoid.

**The warning rides on the RESPONSE, not only the log.** A fail-open guard whose
only output is a log line is a guard nobody sees, and `add_acl` is a user-facing
path. `add` now returns `aclAddResult{ok, warning omitempty}` — the wire shape is
byte-identical to the old `{"ok":true}` when there is nothing to say, and when
there is, the caller is told in the same breath the grant lands. Both cases
warn: a known alias names the canonical sub to re-grant to, and an unreachable
authd says plainly that it could NOT verify, rather than implying the principal
checked out.

`inspect_identity` keeps its advisory behaviour (an error still renders as
unclaimed) but now logs instead of swallowing — that tool cannot distinguish
"authd is down" from "nobody claims this sub" in its output either way.

**Not covered, and deliberately:** the pairing half. `acl_membership.parent`
naming an alias has the same shape, but it is a different write path and a
different concern; folding it in here would be two fixes pretending to be one.
Still open as recorded above.

- **Verified:** `routd/acl_canonical_test.go` — an alias warns and names the
  canonical; canonical, unclaimed, `role:` principals and a nil resolver are all
  silent; an unreachable authd warns that it could not verify; and the grant
  still lands on the principal AS ASKED, with nothing written to the canonical.
  `routd/identity_test.go` gained the 404-is-unclaimed, 5xx-is-an-error and
  dead-authd-is-an-error cases that the old advisory `Resolve` could not express.

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

## ✅ FIXED 2026-08-15 Z5 — `network_rules`' seed migration uses `CURRENT_TIMESTAMP`, making two fresh instances' checksums permanently unequal (2026-08-04)

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
- **Fix:** `493b280e` — `routd/migrations/0037-network-rules-seed-timestamp.sql` normalises
  the two system-seeded rows to the fixed literal `2026-08-04T00:00:00Z`,
  matching 0022's pattern. Scoped by exact PK plus `created_by='system'`, so an
  operator who re-added either target keeps their own stamp. 0005 is left
  untouched (already applied everywhere); a fresh install runs 0005 then 0037
  and lands on the same bytes as an existing instance.

**The `Checksum`/`StampedFields` question is DECIDED: no.** `Checksum` keeps
hashing the exact `EmitYAML(Export(...))` bytes. Excluding `StampedFields` there
would break the property the CAS rests on — that the hash is the hash of the
document the operator holds — and would leave two renderers where 5/8 requires
one. `Diff` ignores stamped fields because it answers a different question (did
INTENT change); the hash answers "is the content still what I read". A
non-deterministic seed is cured by making the seed deterministic, not by making
the hash blind. Z2's gap stands on its own and is NOT closed by this.

Also surfaced by the fix: `CURRENT_TIMESTAMP` was writing `2026-08-15 20:16:08`,
SQLite's space-separated form — every other stamp in the projection is RFC3339,
so the seed was off-format as well as non-deterministic. The literal is RFC3339.

- **Verified:** `cmd/arizuko/seed_determinism_test.go` — two instances
  bootstrapped >1s apart must agree on `resreg.Checksum`, plus a direct assert
  on the literal so a future `CURRENT_TIMESTAMP` re-seed cannot pass by landing
  inside one second. Mutation-checked: both tests FAIL with 0037 moved aside
  (`a = sha256:e2c741d7…` vs `b = sha256:de29e07a…`) and pass with it restored.
