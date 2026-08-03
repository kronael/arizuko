---
status: draft
depends:
  [32-acl-unified, 18-onboarding-model, 1-auth-standalone, W-webhook-routes]
---

# specs/5/31 — identity pairing (channel identity → verified account)

## Problem

A channel user is anonymous. `telegram:user/123` holds no grants and no way to
acquire any, because grants attach to verified accounts and nothing binds the
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
it.

**Nothing here is built.**

## The one rule

A channel identity holds **no grants, ever**. The only bridge to authority is
the membership edge, and the only way to write one is for the human who owns the
account to authenticate and consent.

Pairing grants nothing — it makes the channel identity resolve to the human, who
holds whatever they already held. A pairing token that could also grant would be
an invite, and that mechanism exists (`5/5`).

## Design

Pairing is a **kind of route token**, not a table of its own.

`route_tokens` gains `kind TEXT NOT NULL DEFAULT 'route'`. A pairing row is an
ordinary row with `kind='pair'`: `token_hash`, `jid`, `owner_folder`,
`created_at`. Resolution is kind-scoped in both directions — route delivery
accepts only `'route'`, pairing redemption only `'pair'` — so neither can be
redeemed as the other.

This is the whole schema change. It also removes the failure mode the earlier
draft spent a section handling: the token lived in `onbod.db` and the edge in
`routd.db` with no cross-DB transaction, so a crash between them stranded the
user and the ordering had to be reasoned about. In `routd.db` both are one
transaction and the question does not arise.

`owner_folder` is already `NOT NULL` with an FK to `groups`; the mint gate has
the value in hand (below), so it is populated, not worked around.

## Flow

1. `issue_pairing_link(jid)` — agent MCP only, a custom action on the existing
   route-token resource. The agent in the chat is the practical minter; an
   operator REST twin would be a second face for a caller who does not exist.
2. The gate binds `jid` to the caller: the JID must route to the caller's
   folder — the containment outbound `send` already applies, not a second check.
   That folder is `owner_folder`.
3. onbod's role disappears. webd serves the browser endpoints and already opens
   `routd.db` (`webd/main.go:64`), so redemption is local.
4. **`GET /pair/<token>` is side-effect-free.** Chat platforms unfurl links, and
   an unfurl bot must not consume a pairing. Unauthenticated → OAuth login
   (`5/1`) with the pairing URL as `StateIntent.Return` (`auth/oauth.go:410`).
   Authenticated → a confirm page.
5. **`POST /pair/<token>`** — one routd transaction: check the token's age
   (10 minutes from `created_at`), reject a different **non-role** parent, insert
   the edge with `added_by='pairing'`, delete the token.
6. New authority is live on the paired identity's **next tool call**, not its
   next spawn — `Authorize` reads the DB live (`5/32` §"No caching"). Unpair has
   the same next-call effect.

Missing, expired, consumed and malformed tokens share one "link unavailable"
response; a conflicting existing parent is the only distinct error, because it
is the only one the user can act on.

## Consent is the security boundary

The edge is directional and the direction decides who bears the risk.
`acl_membership(child=telegram:user/999, parent=google:alice)` means whoever
controls that Telegram account can act as alice. Alice bears the whole risk.

So the attack is **consent phishing**: Mallory mints a link for _her own_
Telegram account (step 2 permits nothing else) and sends it to Alice. If Alice
authenticates and the edge is written, Mallory's account holds Alice's authority.

The only defence that sits where the risk sits is an explicit confirm at the
browser step, because that is the only step the account owner performs. The page
names the channel identity and states the consequence in one sentence: _anyone
who controls that account will be able to act as you._ Step 5 is a POST carrying
webd's double-submit CSRF cookie.

Pasting the URL into a different channel is **not** a separate threat: the token
names the JID it binds, so relocating it changes nothing — it only discloses the
secret, which is the phishing case above. Short TTL narrows the window but is
not the defence; Mallory mints on demand.

## One parent per channel identity

`expandPrincipals` unions. A channel JID with two parent subs would silently
hold the union of two humans' authority, and nobody would have agreed to it. So
a channel-identity child has **at most one parent**.

Enforced by the check inside step 5's transaction, **not** by a table
constraint: `acl_membership` legitimately holds multi-parent rows for role
membership, so the constraint is about what pairing may write. Role parents are
excluded from the check — a JID may hold a pairing edge and a role membership at
once (`4e831f10`).

## Unpair

Unpairing is deleting one `acl_membership` row. The resource already exists
(`resreg/resources/membership.go`) but declares no `Endpoints` and no
`MCPNames`, so today an edge is removable only through `arizuko apply`. It gains
exactly one action — delete, scoped to `added_by='pairing'` so it cannot reach
role membership. `added_by` already exists on the table; no migration.

Either endpoint of the edge may call it: MCP for the routed channel side, REST
for the authenticated parent. Both directions are de-escalation. Operators
already reach it through manifests and need no third path.

Unredeemed tokens are not separately revocable — they expire in ten minutes, and
minting again is the same operation. Listing outstanding pairings is not an
ability anyone needs; enumerating ten-minute bearer attempts is surface.

## Onboarding

Onboarding keeps its admission state machine — gates, daily caps, the queue,
`createWorldTx` — and stops owning the bridge. To greet an unprompted JID it
mints a pairing and posts that URL. Pairing itself has no dependency on onbod,
`ONBOARDING_ENABLED`, `ONBOARDING_PLATFORMS`, or route-miss handling; that
independence is the point of the extraction.

This also removes the second write path into `linkJID`: the `onboard_jid` cookie
carried the JID across the OAuth round-trip **unsigned**, a parallel binding
that can drift from the primary one. The signed `StateIntent.Return` replaces
it — one carrier, one binder.

## Not in scope

- **`anon:<ip-hash>` web-chat senders** (`webd/route_token.go:50`). An IP hash
  is not a durable identity; binding it hands the next holder of that IP the
  account. A web visitor who wants authority logs in.
- **Merging two accounts** — `5/1` makes merging populated canonical users an
  operator action. Pairing adds an edge and never merges.

## Rejected

- **A `pairings` table** — a third token table with the same hygiene, lifecycle
  and revocation story as `route_tokens`, plus a cross-DB write ordering problem
  that only exists because it is a different table in a different daemon's DB.
- **Fold onto `invites` (`5/5`)** — hash-at-rest (`96a1293e`) removed the
  storage distinction, but an invite is issued _by_ a granting principal, targets
  a folder glob, is multi-use, **grants**, and lives in `onbod.db`, which is
  exactly the dependency this spec removes.
- **An operator REST mint face** — no operator mints a pairing for a chat they
  are not in; the agent is the minter.
- **A per-platform link (Telegram Login Widget, Discord OAuth)** — already
  shipped as a _login_ provider minting `sub=telegram:<id>`, a sub that reads
  like the channel principal without being it. One generic mechanism instead.
- **A short numeric code alongside the URL** — the URL is the code; a second
  redemption path for one binding is a second mechanism.
- **`used_count`** — a pairing binds one identity pair, so a second use has
  nothing to do. Single-use makes revocation and consumption the same operation.

## Ties

`5/32` — the membership edge, the principal namespace, the no-cache revocation
contract; its Open-Q4 closed here. `5/18` — onboarding, which this extracts
from. `5/29` — a guest is a paired channel identity; distinct from that spec's
"account linking", which is **surrogate** OAuth (`5/15`), not identity pairing.
`5/1` — the OAuth login this redeems behind. `5/W` — the token table it joins.
