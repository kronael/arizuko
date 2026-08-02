---
status: partial
depends:
  [
    B-route-mode-ingestion,
    L-mention-promotion,
    G-engagement,
    E-routd,
    S-jid-format,
    5-tenant-self-service,
    29-worlds-guests-oauth,
    4/9-acl-unified,
    4/R-paths-roles,
  ]
supersedes: []
---

# Onboarding: routing an unrouted JID into a world

## Problem

A chat enters the system by acquiring a **route row**. Until it has one,
the default posture is **silence**: a new Telegram group the bot joins
hits a route miss and vanishes — stored in `routd.db`, visible to
nobody. Witnessed on krons 2026-07-09: `telegram:group/5567410596`
posted for 40 minutes, every message stored, zero trace surfaced; the
operator had to grep the DB for the JID.

Onboarding is the act that ends that silence. This spec owns **where** a
chat lands and **who may put it there**.

## Two axes, both fail-closed

An inbound message resolves against two independent unknowns. Each is
resolved by exactly one explicit act, and neither implies the other:

| axis                 | unknown           | resolving act                                                 | result                   | owner  |
| -------------------- | ----------------- | ------------------------------------------------------------- | ------------------------ | ------ |
| **identity** (who)   | sender is a guest | pairs via an OAuth URL in the chat                            | an `acl_membership` edge | `5/29` |
| **location** (where) | JID has no route  | a user with routing authority over the target world routes it | a `routes` row → folder  | here   |

An anonymous sender in an unrouted JID yields **nothing** — no turn, no
reply, no grant. That property is structural: routing never consults the
ACL (`5/B`), and the ACL never creates routes.

**This spec owns the location axis only.** It CONSUMES an authenticated
session; it never mints one. Where the flow says "authenticates", that is
`5/29`'s pairing act — see the seam at step 5.

## The target flow

Eight steps. Each transition has exactly one trigger.

1. **Listen.** A channel adapter is configured; the bot's presence in a
   chat is platform-side, granted by whoever added it. Presence grants
   nothing (`§Actors`).
2. **Miss.** A message at a JID with no route resolves to `routeMiss`
   (`routd/loop.go:529`). An `#observe` catch-all consumes the miss
   (`§Staging`); otherwise it is genuine.
3. **Record.** routd federates `POST /v1/onboarding {jid}` to onbod,
   which owns the table. Idempotent on `jid` (PK) — re-posts are no-ops.
4. **Greet.** onbod replies **in-channel** with a welcome and a link
   (`<AUTH_BASE_URL>/onboard?token=…`, single-use, 24h). The token proves
   the bearer can read that chat. It carries no authority and names no
   folder.
5. **Authenticate.** The link lands on proxyd's OAuth.
   **← seam: identity axis.** The flow resumes with a
   transit-proven `X-User-Sub`; binding that sub to the JID
   (`acl_membership`) is `5/29`'s act, not this spec's.
6. **Choose.** onbod shows the caller the JID they just proved and the
   worlds they hold **routing authority** over. Empty set → a terminal
   page: no world, no route (an invite or a world of their own is the
   only way forward — `§Invites`). **Never auto-pick.**
7. **Route.** The caller picks world W or a subgroup of W. onbod writes
   `routes(seq 0, 'room=<room>', <target>)` through the one handler that
   binds `target` to the caller's authority and `match` to a JID they
   have paired.
8. **Confirm.** The next message at the JID routes to the target. History
   does not move.

### The authority rule

> A caller may write a route whose `target` is T **iff** they hold
> routing authority over T.

Approval is **world-scoped, not global**: the question is never "is this
an operator", it is "does this user hold authority over _this_ target".
A user with no worlds approves nothing.

How that authority is represented — the grant shape, delegation,
`WITH GRANT OPTION` — is [`4/R`](../4/R-paths-roles.md)'s output. This
spec consumes the predicate and does not define it. Two shipped facts
that constrain it:

- The check already exists in the right shape:
  `auth.MatchGroups(userFolders(sub), target)` (`onbod/main.go:1355`).
- **World creation now grants the subtree** (`createWorldTx` writes
  `acl(sub, admin, <folder>/**)`), so step 7's "or a subgroup of W" is
  reachable. It could not be widened until its reader stopped comparing
  strings: the folder lookup was `JOIN acl a ON a.scope = g.folder`, and
  scopes are PATTERNS, so equality matched neither `acme` against
  `acme/eng` nor `acme/**` against anything. The reader now asks
  `auth.Authorize` per group — the single evaluator answers for ONE
  group, so asking for all of them is a loop — rather than a second
  matcher that could drift from it.
- Invite join-mode (`onbod/main.go:1013`) still writes a bare folder
  scope, and `role:operator` is still unscoped and global
  (`cmd/arizuko/main.go:535`, `routd/acl_resource.go:227`).
  Delegation (`grant_option=1`) is unset at creation: the column is
  `4/R`'s and has not reached every instance, so writing it would fail
  world creation with "no such column".

## Staging: the other posture

Where the operator controls bot presence, a catch-all captures instead of
greeting. This is route-table data, not code:

```
seq   match                     target
0     room=group/42             corp/board        ← promoted chat (specific)
9999  platform=telegram         staging#observe   ← catch-all staging
```

- **Capture**: every un-promoted chat lands in the staging folder in
  observe mode — `is_observed=1`, no turn, no reply (`5/B`). The bot
  stays silent in chats nobody set up.
- **Discovery**: the operator reads staged traffic where anything routed
  is read (dashd, `inspect_messages`, the next turn's `<observed>`
  window). The JID is in the row; no DB grep.
- **Precedence** is plain `seq` ordering, first match wins. Promotion
  inserts a `seq 0` `room=` row that outranks the catch-all. No flag.
- **Promotion** is the existing group-add — `arizuko group add`
  (`cmd/arizuko/main.go:410`), the `groups`+`routes` resreg resources, or
  an agent's `register_group`. History does not move.

The two postures are **mutually exclusive per platform by construction**:
the observe branch consumes the miss before the greeting branch runs
(`routd/loop.go:531`). A platform with a catch-all never greets. That is
why `arizuko create` does not seed one — the trade-off is the operator's.

`web:` JIDs are out of scope: they address folders directly and never
consult the route table (`ROUTING.md` "Web JID model"); web admission is
route tokens (`5/W`).

### Routing is not firing

Routing a JID does not decide who inside it drives the agent — that is
sender-predicate stacking on the same table (`sender=` beside `room=`,
with a lower-precedence `#observe` row catching everyone else). An
unauthorized sender matches only the observe row: context, no turn, no
reply, no fallback. Engagement (`5/G`) then overrides per `(jid, topic)`,
so sender rows gate who _opens_ a conversation, not who speaks inside
one. Firing never consults the ACL; do not add a second check in the
loop.

## As-built — what ships today

Steps 1–5 ship. Steps 6–8 do not.

- **Steps 1–4 as specified.** onbod's poll tick (10s, `promptUnprompted`,
  `onbod/main.go:349`) claims each unprompted row with an atomic
  `UPDATE … WHERE prompted_at IS NULL`, mints the token, and sends the
  link via routd's `/v1/outbound` under a `service:onbod` bearer.
- **Step 5 as specified.** `handleTokenLanding` sets an `onboard_jid`
  cookie and bounces to `/auth/login`; `stripUnsignedGuard` keeps
  `X-User-Sub` only when `auth.ProxydTransit` holds. `claimOnboarding` +
  `linkJID` write the `acl_membership` edge and stamp the row.
- **Step 6 — no picker.** `handleDashboard` auto-picks with
  `SELECT g.folder FROM groups g JOIN acl a ON a.scope = g.folder WHERE
a.principal = ? AND a.effect='allow' LIMIT 1` (`onbod/main.go:523`) —
  no `ORDER BY`, no membership walk, no action filter, no user
  confirmation. Whatever row SQLite returns first is where the JID goes.
- **Step 7 — the route write is a side effect, not an act.** It happens
  inside that claim, inside `createWorldTx` (`onbod/main.go:744`), and
  inside invite redemption (`onbod/main.go:1057`) — the last two loop
  over _every_ JID the sub has paired and route them all at the target.
  Nothing records who routed what.
- **Step 8 — dead end.** A caller with no world reaches "You need an
  invite link to join" (`onbod/main.go:554`). The username picker renders
  only behind a `pending_target` cookie, which **only invite redemption
  sets** (`onbod/main.go:1038`). Chat-initiated onboarding cannot produce
  a world on its own: the gate queue admits you, then tells you to go
  find an invite.

The correctly-shaped handler already exists and is tested —
`handleAddRoute` (`onbod/main.go:1339`) checks
`MatchGroups(folders, target)` **and** `userOwnsMatch(sub, match)`,
exactly step 7's binding. **No HTML form anywhere renders it**:
`renderDashboard` emits a read-only routing table (`onbod/main.go:1114`).
Steps 6–7 are built server-side and unreachable from a browser.

### Invites (the out-of-band entry)

Operator or agent mints (`arizuko invite`, routd `/invite`,
`invite_create`, dashd) → recipient opens `/invite/{token}` → OAuth →
`ConsumeInviteNoGrant`. Trailing-slash target → `pending_target` cookie →
username picker → `SetupGroup` then `createWorldTx`. No trailing slash →
`PutACLRow(sub, admin, target)`; a failure there rolls the consume back
(`RestoreInvite`) so an invite is never burned without its grant. Detail:
`5/5` Phase B.

### Failure branches

| condition                         | today                                                                                  |
| --------------------------------- | -------------------------------------------------------------------------------------- |
| `InsertOnboarding` fails          | `slog.Error` + `onboardingFailedNotice` into the chat (loud, correct)                  |
| route-table read fails            | logged, treated as a miss, cursor advances — message dropped                           |
| JID already paired to another sub | `errLinkRefused` → 403 with the reason (was `slog.Warn` only)                          |
| gates configured, none match      | `errLinkRefused` → 403 with the reason; row still stalls at `token_used` (below)       |
| row in an unknown status          | `slog.Error` per poll naming the jid and status — stranded, never advances (BUGS O1)   |
| token expired or reused           | "invalid, already used, or has expired" — no way to request another from the chat      |
| queue full                        | **does not exist** — `limit_per_day` throttles rate; the queue is unbounded            |
| operator deny                     | deletes the row; the JID re-onboards on its next message. Deny is a reset, not a block |

## What the shipped schema cannot express

`onboarding(jid PK, status, prompted_at, created, token, token_expires,
user_sub, gate, queued_at, admitted_at)` is **a single global admission
queue keyed on JID, throttled per identity provider.** It cannot
represent "routed into world W by user U":

- **No target.** There is no folder or world column anywhere in the
  table. `gate` is `github:org=x` / `google:domain=y` / `*` — `matchGate`
  switches on the `user_sub` prefix alone (`onbod/main.go:298`). It is an
  identity-provider bucket, on the _identity_ axis; it names no location.
- **No approver.** `user_sub` is whoever proved control of the chat, not
  whoever exercised routing authority. Those coincide only in the
  self-serve case.
- **`approved` is instance-global.** `admitFromQueue` counts
  `status='approved' AND admitted_at` within today, per `gate`, across
  the whole instance (`onbod/main.go:887`). Two worlds cannot hold
  independent admission policy; `limit_per_day` is one number per
  provider for everyone.
- **The routing act is not in the table.** It is a side effect of the
  claim / world-create / invite redeem, leaving an unattributed `routes`
  row and no record of who caused it.

`approved` therefore grants nothing. Nothing reads it as a precondition;
the only gate is the negative `status='queued'` short-circuit on
`/onboard` (`onbod/main.go:541`), and even that runs **after** the
auto-route write above — a queued caller's JID is already routed.

**Consequence for the target flow:** the row must shrink to its pairing
half and stop pretending to be an approval record. The approval that
matters is the authority check at step 7, evaluated against the target —
not a status column.

## States to delete

Every remaining state must be reachable and load-bearing. These are not:

- **`resetRow` + `promptCoolDown` are dead code.** The predicate is
  `status='token_used' AND user_sub IS NULL` (`onbod/main.go:401`), but
  both writers of `token_used` set `user_sub` in the _same statement_
  (`claimByToken` :484, `claimOnboarding` :501). The conjunction is
  unreachable. The 30-minute re-prompt documented in CHANGELOG has not
  fired since the claim was made atomic.
- **`token_used` is not a state.** It means `user_sub IS NOT NULL`, which
  the column already says. Its one distinct role — the terminal
  no-gate-matched dead-end — is now an explicit, user-visible failure
  (`linkJID` returns `errLinkRefused`, the landing answers 403); the row
  still stalls there, so the state itself is what remains to remove. The
  dead `resetRow` that never rescued it has been deleted.

  Note `user_sub` is no longer a claim marker either: since the pairing
  edge is written BEFORE the token is consumed (`5/31` step 5), a row with
  `user_sub` set and a live token is the legitimate mid-flight state a
  crash-replay must be able to finish.

- **`status` is fully derivable** from `prompted_at` / `user_sub` /
  `queued_at` / `admitted_at` — one stamp per transition, which is the
  "exactly one trigger" property stated as schema:

  | condition                              | meaning                 |
  | -------------------------------------- | ----------------------- |
  | `prompted_at IS NULL`                  | to prompt               |
  | `prompted_at` set, `user_sub IS NULL`  | greeted, awaiting click |
  | `user_sub` set, `queued_at IS NULL`    | paired, not queued      |
  | `queued_at` set, `admitted_at IS NULL` | queued                  |
  | `admitted_at` set                      | admitted                |

- **`gate` belongs to the identity axis.** It throttles per identity
  provider and is orthogonal to where a JID lands. It stays only as long
  as rate-limiting strangers is wanted; it must not be mistaken for
  world-scoped policy. Flagged to `5/29`.

## The three token mechanisms

Three mint-and-redeem shapes exist. They are not one mechanism:

| token                     | carries           | lifetime        | at rest   | verdict                                            |
| ------------------------- | ----------------- | --------------- | --------- | -------------------------------------------------- |
| `onboarding.token`        | a **JID proof**   | single-use, 24h | plaintext | belongs to the identity axis — fold into pairing   |
| `invites.token`           | a **grant**       | `max_uses`, TTL | plaintext | survives; it is authorization delivery             |
| `route_tokens.token_hash` | a **destination** | permanent       | `sha256`  | survives; distinct — no identity, no authz (`5/W`) |

Route tokens are unambiguously separate: they name where inbound data
lands and confer nothing.

`onboarding.token` does **not** earn separate existence on this axis. It
is a pairing nonce — the carrier that proves "the bearer reads this chat"
so an OAuth session can be bound to a JID. That is the identity axis's
job. Strip it and the remaining onboarding row is `(jid, prompted_at,
user_sub)` plus the throttle columns, and this spec's flow is unchanged.

**Coordination note, not a unilateral deletion.** `5/29` is separately
evaluating invites and route tokens for pairing. If `onboarding.token` is
folded into the pairing carrier there, this spec follows; if pairing
instead reuses `invites`, note that an invite carries a _scope_ and an
onboarding token carries a _JID_ — they must not be merged by making
`target_glob` nullable. The overlap is real; whoever ships first names
the survivor.

## Operator surface

The operator does not approve locations today — there is no location to
approve. What exists is the _identity_-side admission queue, and it is
inconsistent across the three entry points:

| surface              | onboarding queue                                      | gates                     | invites                   |
| -------------------- | ----------------------------------------------------- | ------------------------- | ------------------------- |
| chat                 | —                                                     | `/gate` (operator-only)   | `/invite` (operator-only) |
| CLI                  | —                                                     | `arizuko gate`            | `arizuko invite`          |
| dashd                | — (tile is `Built=false`, `dashd/services.go:35`)     | —                         | `/dash/invites/`          |
| onbod `/dash/onbod/` | approve / deny / reprompt, gated on `**`              | —                         | —                         |
| REST/MCP             | `/v1/onboarding` (bearer `invites:write`) — no resreg | `onboarding_gates` resreg | `invites` resreg          |

Three findings:

1. **The queue page is unreachable.** `/dash/onbod/` renders every row
   with approve/deny/reprompt (`onbod/dash.go:53`), but dashd's services
   hub marks onbod `Built=false`, so no navigation reaches it. An
   operator must know the URL.
2. **`onboarding` is not a resreg resource.** onbod's `openapi.json`
   declares only `onboarding_gates` and `invites` (`onbod/main.go:149`);
   the five `/v1/onboarding*` endpoints are hand-mounted with no MCP
   twin. Root `CLAUDE.md` makes that a review-blocker: every cold-tier
   management entity registers a `Resource`. Agents can mint invites but
   cannot see or act on the admission queue.
3. **The approval privilege is global.** Both onbod's dash gate
   (`requireOperator`, `onbod/dash.go:20`) and routd's `/invite` + `/gate`
   (`db.IsOperator`) demand the unscoped operator role. Under the target
   flow the privilege is world-scoped authority over the target — a
   different check, and one that must not be satisfied by the global role
   alone once `4/R` lands.

## Open, blocking

Ordered by who must move first.

1. **`4/R`** — the representation of world-scoped routing authority, and
   whether a self-service grant can ever be a subtree (`acme/**`). Step
   7 cannot be built until the predicate exists.
2. **`5/29`** — whether `onboarding.token` folds into the pairing
   carrier; whether `gate` survives as a stranger throttle; whether a
   stranger who chose no world may create one (today: only via an
   invite), which is `5/29`'s "admit INTO a world" vs this path's
   "create a world per stranger".
3. **Here, once both land** — replace the auto-pick at
   `onbod/main.go:523` with the picker, render a form for the existing
   `handleAddRoute`, register `onboarding` as a resreg resource, drop the
   derivable `status` column and the dead `resetRow`.

## Consolidates

Canonical umbrella for the onboarding narrative; the referenced specs
stay canonical for their mechanisms. Nothing is superseded:

- `5/B` — `#observe` semantics (firing/visibility split quoted here).
- `5/L`, `5/G` — mention promotion, engagement continuation.
- `5/E` — route-miss hook in the loop.
- `4/9`, `4/R` — grant vocabulary; the authority predicate step 7 needs.
- `5/5` — invites, gates, tenant self-service phases.
- `5/29` — World → Agent → Session; the identity axis and the world
  roster this flow routes into.
- `5/31` — identity pairing. Onboarding is `pairing + admission`: it no
  longer owns the channel-identity→account bridge, it redeems one. That
  extraction is what makes pairing reachable outside a route miss.
- `7/7` — the operator queue page.
- `ROUTING.md` — route-table syntax the staging and sender examples
  generalize.
