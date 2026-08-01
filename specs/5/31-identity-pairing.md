---
status: draft
depends:
  [../4/9-acl-unified, 18-onboarding-model, 1-auth-standalone, W-webhook-routes]
---

# specs/5/31 — identity pairing (channel identity → verified account)

## Problem

A channel user is anonymous. `telegram:user/123` is a principal that holds
no grants and has no way to acquire any — grants attach to verified
accounts, and nothing binds the two.

The bridge already exists: `4/9`'s JID claim, an
`acl_membership(child=<channel JID>, parent=<canonical sub>)` edge that
`expandPrincipals` (`auth/authorize.go:120`) follows transitively, tested at
`auth/authorize_test.go:88`. And the write already exists: `onbod.linkJID`
(`onbod/main.go:781`), fed by a token minted into the chat
(`promptUnprompted`, `onbod/main.go:349`) and redeemed after OAuth
(`claimByToken`, `onbod/main.go:480`).

But that write is reachable from exactly one place: a route **miss**, on an
`ONBOARDING_PLATFORMS` platform, with `ONBOARDING_ENABLED`, whose success
path creates a world (`createWorldTx`). A user in an already-routed chat, or
one who wants to act as themselves rather than provision a tenant, can never
get an edge.

Pairing is not missing — it is **trapped inside onboarding**. This spec
extracts it, so that onboarding becomes `pairing + admission` rather than
the sole owner of the bridge.

## The one rule

A channel identity holds **no grants, ever**. The only bridge to authority
is the membership edge, and the only way to write one is for the human who
owns the account to authenticate and consent.

Pairing itself grants nothing. It makes the channel identity resolve to the
human, who holds whatever the human already held. Grant-issuance stays where
it is (`acl` rows; invites, `5/5`) — a pairing token that could also grant
would be an invite, and that mechanism already exists.

## Flow

1. A channel user asks to link, or the agent offers it. The agent calls
   `issue_pairing_link(jid)`; an operator `POST`s `/v1/pairings` — one
   handler, two faces (`5/17`).
2. The injected gate binds `jid` to the caller: the JID must route to the
   caller's folder. This is the containment check outbound `send` already
   applies (`4/11` §"Outbound JID authorization"), not a second check. No
   caller can mint for a JID it cannot already address.
3. onbod mints an opaque 256-bit token, persists `sha256(token)` with the
   JID and a 10-minute expiry, and returns `<WEB_HOST>/pair/<token>`. The
   link is delivered into the chat.
4. `GET /pair/<token>` is **side-effect-free** — chat platforms unfurl
   links, and an unfurl bot must not consume or complete a pairing.
   Unauthenticated → OAuth login (`5/1`), with the pairing URL as
   `StateIntent.Return` (`auth/oauth.go:410`). That brings the token back
   through the round-trip inside the signed, 10-minute, double-submit state
   that `?intent=link` already uses. Authenticated → a confirm page naming
   the channel identity and its consequence.
5. `POST /pair/<token>` consumes the token — success or refusal — then in
   one tx: reject if the JID already has a different parent; else write the
   edge with `CanonicalSub(sub)` as parent (`store/auth.go:60`, so an
   aliased sub never becomes a second parent), plus an audit row in onbod's
   own `audit_log`.
6. The outcome is delivered to the originating chat and rendered on the
   page. Both are loud on failure; neither is silent.
7. The paired identity's new authority is live on its **next tool call**,
   not its next spawn.
8. Unpair = delete the edge (`store/membership.go:85`). Same next-call
   effect.

The edge write is `PutMembership` (`store/membership.go:107`), the
audit-free twin, because `acl_membership` lives in `routd.db` in the split
and that DB has no `audit_log`; the audit row belongs to onbod's own, per
the per-daemon audit rule. Both twins carry the same self- and cycle-checks.

## Consent is the security boundary

The edge is directional and the direction decides who bears the risk.
`acl_membership(child=telegram:user/999, parent=google:alice)` means whoever
controls that Telegram account can act as alice. Alice bears the whole risk;
the Telegram side bears none.

So the attack is consent phishing: Mallory mints a link for **her own**
Telegram account (step 2 permits nothing else) and sends it to Alice. If
Alice authenticates and the edge is written, Mallory's Telegram account
holds Alice's authority.

The only defence that sits where the risk sits is an explicit confirm at the
browser step, because that is the only step the account owner performs. Step
4's page therefore names the channel identity as the platform renders it
(handle, not just numeric ID) and states the consequence in one sentence:
_anyone who controls that account will be able to act as you._ Step 5 is a
POST carrying onbod's existing double-submit CSRF cookie
(`onbod/main.go:562`).

Pasting a pairing URL into a different channel is **not** a separate threat:
the token names the JID it binds, so relocating the URL changes nothing
about the outcome. It only discloses the secret, which is the phishing case
above.

Short TTL narrows the window but is not the defence — Mallory mints on
demand. Ten minutes is chosen to match the two short-lived
browser-round-trip TTLs already in the system (surrogate OAuth state,
`5/15`; the collision-form token, `5/1`), not because it stops anything.

## One parent per channel identity

`expandPrincipals` unions. A channel JID with two parent subs would silently
hold the union of two humans' authority, and no one would have agreed to it.
So a channel-identity child has **at most one parent**.

Enforced by the check-then-insert in step 5's tx, **not** by a table
constraint: `acl_membership` legitimately holds multi-parent rows for role
membership (`google:alice → role:operator` and `→ role:oncall`), so the
constraint is about what pairing may write, not about the table.

This restores an invariant that was lost. The legacy `user_jids` table
carried `UNIQUE(jid)`; migration `0053-acl-cutover.sql` folded it into
`acl_membership`, whose only key is `(child, parent)`. Since then the sole
guard has been `linkJID`'s read-then-write check (`onbod/main.go:782`),
which is not in the same tx as its insert.

The remaining cases fall out:

- **Same identity, same account, twice** — the edge exists; idempotent
  no-op, and the chat says so.
- **Same identity, second account** — rejected, loud, at both surfaces:
  unpair first. Today this case logs `slog.Warn("jid already claimed")` and
  returns with no return value for the caller to check (`onbod/main.go:786`)
  — a silent failure on a user-facing path.
- **One account, many channel identities** — expected and unconstrained.
  Alice pairs Telegram, Discord and Slack; many children, one parent.

## Revocation

There is no unpair verb. Unpairing is deleting one `acl_membership` row —
`store.RemoveMembership` (`store/membership.go:85`). Adding a
`pairings`-owned unpair would be a second path to the same delete.

The `acl_membership` resreg resource already exists
(`resreg/resources/membership.go`) but declares no `Endpoints` and no
`MCPNames`, so today it is reachable only through `arizuko apply`
manifests — there is no operator REST and no agent tool for removing an
edge. Pairing needs that surface; it is added to **that** resource rather
than mirrored onto `pairings`.

- **Who** — either endpoint of the edge, plus an operator. The account owner
  is withdrawing authority they lent; the channel side is dropping authority
  it holds. Both directions are de-escalation, so neither needs ceremony.
- **In flight** — an edge deleted mid-turn takes effect on the turn's next
  tool call. A turn already running loses the authority partway through; it
  does not run to completion on stale grants. This falls out of `Authorize`
  reading the DB live: `auth/authorize.go` has no cache, and `4/9`
  §Caching's `acl_version` watermark was never built. If that cache is ever
  added, next-call revocation is the contract it must preserve — for
  pairing it is the difference between unpairing and waiting out a turn.
- **What survives** — messages already sent, audit rows, and every grant the
  human holds. Unpairing removes the bridge, not the grants; there is
  nothing to cascade.
- **Unredeemed tokens** are revoked by deleting the `pairings` row —
  identical to `5/W` route-token revocation, and the reason the table stores
  a hash rather than the token.

## Schema

```sql
CREATE TABLE pairings (
  token_hash   BLOB PRIMARY KEY,   -- sha256(raw); the raw token never rests
  jid          TEXT NOT NULL,      -- the channel identity this token binds
  owner_folder TEXT NOT NULL,      -- minting folder, snapshot for the gate + audit
  expires_at   TEXT NOT NULL,
  created_at   TEXT NOT NULL
);
```

onbod owns it (`onbod.db`), beside `invites` and `onboarding`; it already
mints these links, already lands the OAuth return, and already writes
`acl_membership` cross-DB under the FS-mounted write discipline. No new
daemon, no new cross-DB edge.

Token generation and hashing reuse `store.HashRouteToken`
(`store/route_tokens.go:52`), which exists precisely so a second writer does
not duplicate the scheme. Both shipped token tables store their token in
**plaintext** — `onboarding.token` and `invites.token` — so a DB read
discloses every live link. Pairing does not inherit that.

`owner_folder` is snapshotted at mint rather than looked up from `routes` at
redeem, for the reason `5/W` §"Link context" gives: routes change, and a
token's contract must not be re-interpreted after it was issued.

`pairings` is a resreg resource (`5/17`): `POST /v1/pairings` (mint, JID
bound by step 2's gate), `GET` (list outstanding — hash, JID and expiry
only), `DELETE /v1/pairings/{token_hash}` (revoke unredeemed).

## Onboarding becomes a consumer

`onboarding` keeps its admission state machine — gates, daily caps, the
queue, `createWorldTx`. It stops owning the bridge: to greet an unprompted
JID it mints a **pairing** and posts that URL, and its gate logic runs off
the resulting edge.

This is what makes pairing available when `ONBOARDING_ENABLED=false`, on a
platform outside `ONBOARDING_PLATFORMS`, and in an already-routed chat —
the three conditions that make the shipped path unreachable.

It also removes the second write path into `linkJID`: the `onboard_jid`
cookie branch at `onbod/main.go:513` carries the JID across the OAuth
round-trip in an unsigned cookie rather than reading it from the token row,
a parallel binding path that can drift from the primary one. Step 4's signed
`StateIntent.Return` replaces it, so there is one carrier and one binder.

## Not in scope

- **`anon:<ip-hash>` web-chat senders** (`webd/route_token.go:41`). An IP
  hash is not a durable identity — there is nothing stable to bind, and
  binding it would hand the next holder of that IP the account. A web
  visitor who wants authority logs in; `5/W` axis 2 already stamps the real
  sub when they do.
- **Merging two accounts.** `5/1` §"Account linking" is explicit that
  merging populated canonical users is an operator action. Pairing adds an
  edge and never merges.
- **`identity_codes`** (`store/migrations/0035-identities.sql`) — a shipped
  table with zero code, designed for a `/auth/link-code` endpoint that was
  never built. It belongs to the advisory `identities`/`identity_claims`
  sub↔sub merge axis (`inspect_identity`, "agents query, never enforce"),
  not to this authorization-bearing bridge. It should be deleted rather than
  repurposed: a dead table adjacent to a live one invites exactly the
  confusion this spec exists to remove.

## Rejected

- **Fold onto `route_tokens` (`5/W`).** Right token hygiene — opaque 256-bit,
  sha256 at rest, revoke-by-delete — and this spec copies it. Wrong
  lifecycle: route tokens are long-lived, multi-use folder addresses. A row
  in that table whose redemption mutates the ACL would overload a primitive
  whose entire contract is "this token drops a message into a folder".
- **Fold onto `invites` (`5/5`).** Nearly the same shape — mint a URL, OAuth,
  redeem — and the closest near-miss. But an invite is issued **by** a
  granting principal, targets a folder glob, is multi-use, and its
  redemption **grants**. Pairing is requested by its own subject, targets a
  principal, is strictly single-use, and grants nothing. Folding them means
  a token table whose redemption semantics depend on which columns are NULL,
  with a bug class where a pairing token grants. They stay orthogonal: an
  invite grants, a pairing binds, a user needing both does both.
- **A per-platform link (Telegram Login Widget, Discord OAuth).** `4/11`
  already ships the Telegram widget as a **login** provider, minting
  `sub=telegram:<id>` — one mechanism per platform, and a sub that reads
  like the channel principal `telegram:user/<id>` without being it. One
  generic mechanism instead.
- **A short numeric code alongside the URL.** The URL is the code; a second
  redemption path for one binding is a second mechanism.
- **Rewriting `acl` rows to the canonical sub at pair time** (`4/9` §4's
  other option). Rows untouched means no migration, no merge, and unpair is
  a row delete rather than a reversal.
- **`used_count` on a pairing token.** A reusable pairing token binds one
  identity pair; the second use has nothing left to do. Single-use is a row
  that exists or doesn't, which makes revocation and consumption the same
  operation.

## Ties

`4/9` — the membership edge, the principal namespace, the `acl_version`
revocation contract; §Open-Q4 closed here. `5/18` — onboarding, which this
extracts from. `5/29` — a guest is a paired channel identity; distinct from
that spec's "account linking", which is **surrogate** OAuth (`5/15`,
arizuko acting _as_ the user outbound), not identity pairing (the user
proving who they are inbound). `5/1` — the OAuth login this redeems behind.
`5/W` — token hygiene and the snapshot-at-mint pattern. `5/17` — the
resource's two faces.
