---
status: draft
depends:
  [32-acl-unified, 18-onboarding-model, 1-auth-standalone, W-webhook-routes]
---

# specs/5/31 — identity pairing (channel identity → verified account)

## Problem

A channel user is anonymous. `telegram:user/123` holds no grants and has no way
to acquire any, because grants attach to verified accounts and nothing binds the
two.

Both halves already ship. The bridge is `5/32`'s JID claim — an
`acl_membership(child=<channel JID>, parent=<canonical sub>)` edge that
`expandPrincipals` (`auth/authorize.go:120`) follows transitively. The write is
`onbod.linkJID`, fed by a token minted into the chat and redeemed after OAuth.

But that write is reachable from exactly one place: a route **miss**, on an
`ONBOARDING_PLATFORMS` platform, with `ONBOARDING_ENABLED`, whose success path
creates a world. A user in an already-routed chat, or one who wants to act as
themselves rather than provision a tenant, can never get an edge.

Pairing is not missing — it is **trapped inside onboarding**. This spec extracts
it, so onboarding becomes `pairing + admission` rather than the sole owner of
the bridge.

**Nothing here is built**: there is no `pairings` table and no `/pair/` route.

## The one rule

A channel identity holds **no grants, ever**. The only bridge to authority is
the membership edge, and the only way to write one is for the human who owns the
account to authenticate and consent.

Pairing itself grants nothing — it makes the channel identity resolve to the
human, who holds whatever they already held. A pairing token that could also
grant would be an invite, and that mechanism exists (`5/5`).

## Flow

1. A channel user asks to link, or the agent offers it: `issue_pairing_link(jid)`
   (agent) / `POST /v1/pairings` (operator) — one handler, two faces (`5/17`).
2. The injected gate binds `jid` to the caller: the JID must route to the
   caller's folder. This is the containment outbound `send` already applies, not
   a second check. No caller can mint for a JID it cannot already address.
3. onbod mints an opaque 256-bit token, persists `sha256(token)` with the JID
   and a 10-minute expiry, and returns `<WEB_HOST>/pair/<token>` into the chat.
4. **`GET /pair/<token>` is side-effect-free** — chat platforms unfurl links,
   and an unfurl bot must not consume a pairing. Unauthenticated → OAuth login
   (`5/1`) with the pairing URL as `StateIntent.Return` (`auth/oauth.go:410`),
   which carries the token back inside the signed, short-lived, double-submit
   state that `?intent=link` already uses. Authenticated → a confirm page.
5. **`POST /pair/<token>` writes the edge FIRST, then consumes the token.** The
   token lives in onbod's DB and the edge in routd's with no cross-DB
   transaction, so the ordering decides what a crash between them leaves behind.
   Edge-first is safe: `PutMembership` (`store/membership.go:107`) is
   `INSERT OR IGNORE`, so a replay writes the identical row and the still-live
   token is simply clicked again. Consume-first strands the user — token spent,
   no edge, no retry. The consume is one `UPDATE … RETURNING` keyed on the
   token, so concurrent landings race there and exactly one wins; the loser has
   already written the identical edge.

   The claim marker is the **token**, not `user_sub`: the token is NULLed only
   by the consume, whereas `user_sub` is set earlier, so keying on it makes a
   crash-replay look already-claimed.

   Reject if the JID already has a different **non-role** parent (role
   membership is not a pairing claim — a JID may hold both).

6. The outcome is delivered to the originating chat **and** rendered on the
   page. Both loud on failure; neither silent.
7. New authority is live on the paired identity's **next tool call**, not its
   next spawn. Unpair (`store/membership.go:85` `RemoveMembership`) has the same
   next-call effect.

The edge write uses `PutMembership` — the audit-free twin — because
`acl_membership` lives in `routd.db`, which has no `audit_log`; the audit row
belongs to onbod's own, per the per-daemon audit rule. Both twins carry the same
self- and cycle-checks.

## Consent is the security boundary

The edge is directional and the direction decides who bears the risk.
`acl_membership(child=telegram:user/999, parent=google:alice)` means whoever
controls that Telegram account can act as alice. Alice bears the whole risk; the
Telegram side bears none.

So the attack is **consent phishing**: Mallory mints a link for _her own_
Telegram account (step 2 permits nothing else) and sends it to Alice. If Alice
authenticates and the edge is written, Mallory's account holds Alice's authority.

The only defence that sits where the risk sits is an explicit confirm at the
browser step, because that is the only step the account owner performs. Step 4's
page therefore names the channel identity as the platform renders it (handle,
not just numeric ID) and states the consequence in one sentence: _anyone who
controls that account will be able to act as you._ Step 5 is a POST carrying
onbod's existing double-submit CSRF cookie.

Pasting the URL into a different channel is **not** a separate threat: the token
names the JID it binds, so relocating it changes nothing about the outcome — it
only discloses the secret, which is the phishing case above. Short TTL narrows
the window but is not the defence; Mallory mints on demand. Ten minutes matches
the two short-lived browser-round-trip TTLs already in the system.

## One parent per channel identity

`expandPrincipals` unions. A channel JID with two parent subs would silently
hold the union of two humans' authority, and nobody would have agreed to it. So
a channel-identity child has **at most one parent**.

Enforced by the check-then-insert in step 5's tx, **not** by a table constraint:
`acl_membership` legitimately holds multi-parent rows for role membership, so
the constraint is about what pairing may write, not about the table.

This restores an invariant that was lost: the legacy `user_jids` table carried
`UNIQUE(jid)`, and `0053-acl-cutover.sql` folded it into `acl_membership`, whose
only key is `(child, parent)`. Since then the sole guard has been `linkJID`'s
read-then-write check, which is not in the same tx as its insert.

The remaining cases fall out: same identity/same account twice is an idempotent
no-op; same identity/second account is **rejected loud at both surfaces**
(`errLinkRefused` → 403 with the reason — it previously logged a warning and
returned nothing for the caller to check, a silent failure on a user-facing
path); one account with many channel identities is expected and unconstrained.

## Revocation

There is no unpair verb — unpairing is deleting one `acl_membership` row.
Adding a `pairings`-owned unpair would be a second path to the same delete.

The `acl_membership` resreg resource exists (`resreg/resources/membership.go`)
but declares no `Endpoints` and no `MCPNames`, so today an edge is removable
only through `arizuko apply` manifests. Pairing needs that surface; it is added
to **that** resource rather than mirrored onto `pairings`.

- **Who** — either endpoint of the edge, plus an operator. Both directions are
  de-escalation, so neither needs ceremony.
- **In flight** — an edge deleted mid-turn takes effect on the turn's next tool
  call; a running turn does not complete on stale grants. This falls out of
  `Authorize` reading the DB live (`5/32` §"No caching"). For pairing it is the
  difference between unpairing and waiting out a turn.
- **What survives** — messages already sent, audit rows, and every grant the
  human holds. Unpairing removes the bridge, not the grants; nothing cascades.
- **Unredeemed tokens** are revoked by deleting the `pairings` row — identical
  to `5/W` route-token revocation, and the reason the table stores a hash.

## Schema

`pairings(token_hash BLOB PK, jid, owner_folder, expires_at, created_at)`, owned
by onbod (`onbod.db`) beside `invites` and `onboarding` — it already mints these
links, already lands the OAuth return, and already writes `acl_membership`
cross-DB under the FS-mounted write discipline. No new daemon, no new cross-DB
edge.

Token generation and hashing reuse `store.HashRouteToken`
(`store/route_tokens.go:52`), which exists precisely so a second writer does not
duplicate the scheme. Both shipped token tables store their token in
**plaintext** (`onboarding.token`, `invites.token`), so a DB read discloses
every live link. Pairing does not inherit that.

`owner_folder` is snapshotted at mint rather than looked up from `routes` at
redeem, for the reason `5/W` gives: routes change, and a token's contract must
not be re-interpreted after it was issued.

`pairings` is a resreg resource (`5/17`): `POST /v1/pairings` (mint, JID bound
by step 2's gate), `GET` (list outstanding — hash, JID, expiry only),
`DELETE /v1/pairings/{token_hash}`.

## Onboarding becomes a consumer

`onboarding` keeps its admission state machine — gates, daily caps, the queue,
`createWorldTx`. It stops owning the bridge: to greet an unprompted JID it mints
a **pairing** and posts that URL, and its gate logic runs off the resulting
edge. That is what makes pairing available with `ONBOARDING_ENABLED=false`, on a
platform outside `ONBOARDING_PLATFORMS`, and in an already-routed chat — the
three conditions that make the shipped path unreachable.

It also removes the second write path into `linkJID`: the `onboard_jid` cookie
branch carries the JID across the OAuth round-trip in an **unsigned** cookie
rather than reading it from the token row — a parallel binding path that can
drift from the primary one. Step 4's signed `StateIntent.Return` replaces it, so
there is one carrier and one binder.

## Not in scope

- **`anon:<ip-hash>` web-chat senders** (`webd/route_token.go:50`). An IP hash
  is not a durable identity; binding it would hand the next holder of that IP
  the account. A web visitor who wants authority logs in.
- **Merging two accounts** — `5/1` is explicit that merging populated canonical
  users is an operator action. Pairing adds an edge and never merges.
- **`identity_codes`** — a table with zero code, designed for a `/auth/link-code`
  endpoint never built. It belongs to the advisory `identities`/`identity_claims`
  sub↔sub axis, not to this authorization-bearing bridge, and is now dropped in
  `auth.db` (`authd/migrations/0005-drop-identity-codes.sql`).

## Rejected

- **Fold onto `route_tokens` (`5/W`)** — right token hygiene (copied here),
  wrong lifecycle: route tokens are long-lived multi-use folder addresses, and a
  row whose redemption mutates the ACL would overload a primitive whose whole
  contract is "drop a message into a folder".
- **Fold onto `invites` (`5/5`)** — the closest near-miss, but an invite is
  issued _by_ a granting principal, targets a folder glob, is multi-use, and
  **grants**; a pairing is requested by its own subject, targets a principal, is
  single-use, and grants nothing. Folding them yields a table whose redemption
  semantics depend on which columns are NULL, with a bug class where a pairing
  token grants.
- **A per-platform link (Telegram Login Widget, Discord OAuth)** — already
  shipped as a _login_ provider minting `sub=telegram:<id>`, a sub that reads
  like the channel principal without being it. One generic mechanism instead.
- **A short numeric code alongside the URL** — the URL is the code; a second
  redemption path for one binding is a second mechanism.
- **Rewriting `acl` rows to the canonical sub at pair time** — rows untouched
  means no migration, no merge, and unpair is a delete rather than a reversal.
- **`used_count` on a pairing token** — a reusable pairing token binds one
  identity pair, so the second use has nothing to do. Single-use makes
  revocation and consumption the same operation.

## Ties

`5/32` — the membership edge, the principal namespace, the no-cache revocation
contract; its Open-Q4 closed here. `5/18` — onboarding, which this extracts
from. `5/29` — a guest is a paired channel identity; distinct from that spec's
"account linking", which is **surrogate** OAuth (`5/15`, arizuko acting _as_ the
user outbound), not identity pairing (the user proving who they are inbound).
`5/1` — the OAuth login this redeems behind. `5/W` — token hygiene and the
snapshot-at-mint pattern. `5/17` — the resource's two faces.
