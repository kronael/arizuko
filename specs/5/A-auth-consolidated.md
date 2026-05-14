---
status: spec
supersedes: [5/28-mass-onboarding.md, 5/29-acl.md]
---

# Authorization, consolidated

Four primitives compose into "may this user act on this folder via this
JID":

- **folders** — `groups` table, path is identity, tier from depth.
- **routes** — `routes` table, `match` (key=glob) → `target` folder.
- **user_groups** — glob ACL keyed by `user_sub` against folder path.
- **identities** — `user_jids` (platform JID → sub), OAuth-issued subs
  (`github:<id>`, `google:<id>`, `discord:<id>`, …).

How they wire together today is described in `GRANTS.md`. This spec
explains the **shapes** these primitives can express, where today's
implementation falls short, and the proposed end-state. Onboarding flow
and ACL semantics that previously lived in `28` and `29` are folded in;
the org-chart layer (invites, secrets, hierarchy) stays in `32`.

## The three auth shapes

### Shape A — user-bot (each user owns a folder)

One human → one folder (`alice/`). All inbound from any of that user's
JIDs route to their folder. Per-user ACL is the gate: `user_groups`
binds `sub` → `alice`.

- Works today. This is the path `28-mass-onboarding` ships.
- The folder IS the user. Identity binding is mandatory: no folder
  without OAuth.

### Shape B — channel-bot (channel owns a folder)

A platform-side container (Discord guild, Slack workspace, WhatsApp
group, Telegram supergroup) → one folder (`acme-eng/support`). Any
member of the channel reaches the same folder.

- **Routing** works today (`routes` row binds channel JID → folder).
- **Per-user gating is broken**: `user_groups` glob-matches on folder
  side but exact-matches on user_sub side, so "anyone in this guild"
  cannot be expressed without enumerating subs. The agent processes
  every inbound, regardless of who sent it.
- "Lurker vs. granted" is invisible to the agent today: the gateway
  surfaces the sender, but there's no per-message authorization check
  that distinguishes a channel member from a stranger DMing the bot.

### Shape C — hybrid (channel folder + per-user overlays)

Channel-bot for everyone, but specific users get extra capabilities:
write to a private subfolder, see per-user secrets, run privileged
tools. The channel grant is the floor; per-user grants stack on top.

- Storage works (`user_groups` rows compose with channel rules).
- Composition story is undocumented and the gateway permission check
  does not distinguish "channel admitted you" from "you have a personal
  grant." See open questions.

## Today's reality

Folder-side globs work. User-sub side is exact-only. This asymmetry
is undocumented and surprising:

```sql
-- in store/auth.go:153
SELECT folder FROM user_groups WHERE user_sub = ?
```

`auth.MatchGroups` then globs each `folder` pattern against the target.
There is no analogous globbing on `user_sub`. Consequences:

- `grant main "discord:user/*"` is silently meaningless — no row would
  ever match a real user sub via that grant.
- Anyone-in-this-channel access requires either enumerating subs at
  invite time (does not scale) or weakening folder-side checks
  (defeats the ACL).

The gateway today also couples two questions in one check: "what folder
owns this JID?" (route resolution) and "is this sender allowed?"
(`user_groups`). Splitting them is the precondition for the proposed
end-state.

## Proposed end-state — route-as-auth

When a `routes` row binds channel JID → folder, the route IS the auth
scope. Reaching the channel (joining the Discord guild, being in the
Slack workspace, being in the WhatsApp group) is the auth fence. The
platform already gatekeeps membership; arizuko trusts the platform's
membership test for inbound on routed JIDs.

Mechanism:

- Gateway splits the permission check into route-resolution (today's
  router) and authorization. For channel-routed inbound,
  authorization succeeds when the route exists. For DM inbound (no
  route, or route to a user-bot folder), authorization falls back to
  `user_groups`.
- Per-user `user_groups` rows become **additive overlays**, not the
  baseline: they grant access to subfolders the channel route does
  not cover, or unlock per-user secrets / tools.
- "Lurker vs. granted" is decided per message: the agent receives the
  sender's grant set in `<user>` context, distinguishing channel
  members (no personal grant) from named principals (have a row).

This is an **architectural decision** because it changes what
`user_groups` is for: today it answers "may sub touch folder F"; after,
it answers "what extra does sub get beyond the channel default".

## Tactical step — user-sub patterns

Independent of the route-as-auth decision, extend `auth.MatchGroups`
(`auth/acl.go`) to glob on user_sub as well. Today `store/auth.go:153`
exact-matches; change to walk rows and glob-match both sides.

Scope:

- `auth.MatchGroups(subPatterns, sub)` helper (mirror of folder-side).
- `store.UserGroups` becomes `store.MatchingGroups(sub)` that walks
  rows and returns those where the row's `user_sub` (treated as glob)
  matches the caller's sub.
- Write-side validation: reject patterns that would match
  unintentionally (e.g. bare `*` rows without claim conditions; see
  open questions on syntax).
- ~30 LoC + tests.

Composable with route-as-auth: `grant <folder> "discord:user/*"`
combined with the channel route `room=guild/*` expresses "any discord
user reaching this guild's channels has folder access."

Standalone value (without route-as-auth): operators can express
"anyone with a github sub gets this folder" without enumeration.

## OAuth membership claims

Today `auth/oauth.go` issues `discord:<userid>` / `github:<userid>` /
`google:<userid>` subs but does not embed membership data. Extend:

- **Discord** — `/users/@me/guilds` → embed guild IDs in JWT claims.
- **GitHub** — `read:org` scope → embed org memberships; `repo` scope
  → embed repo refs.
- **Google** — domain is already derivable from email; lift it into
  claims explicitly.

`user_groups` patterns (or a sibling `admission_predicates` table —
see open questions) then express conditions over claims, not just over
the bare sub. Composition with `28-mass-onboarding`'s gates:
the membership-predicate primitive is the same shape as
`github:org=mycompany:10/day`; today's gates are admission-throttle
predicates, tomorrow's grants are admission-grant predicates. One
matcher serves both — the renderer is the claim-shape; the sinks are
the gate-counter and the grant-resolver.

## Open questions

These are not decided. Resolving any of them is its own follow-up.

1. **DECIDED — Identification is separate from access.** Three
   independent concepts: (a) user account = canonical identity
   created via OAuth, no folder implied; (b) JID claim = optional
   link from channel-side identity to a user account
   (`user_jids` row); (c) channel access = orthogonal — anonymous
   interaction is first-class. The bot sees `<user id="..."
name="..."/>` always; if a `user_jids` claim exists, it
   additionally sees `canonical_sub="..."`. World creation is a
   fourth, also-separate action. Spec 28's "username = world"
   coupling is dissolved: OAuth → account only, no implicit world.
   Invites split into JID-claim invites and folder-grant invites
   (overlay path). User-actions / world-creation flow is its own
   spec follow-up.
2. **DECIDED — Agent finds out at the failure site.** The `<user>`
   element stays minimal (id + name + optional canonical_sub).
   Grants are NOT pre-announced. When the agent calls a tool or
   accesses a resource the user isn't authorized for, the gateway /
   tool returns a denied error; the agent surfaces it. No
   pre-state, no differential UX based on cached perm lists. Mid-
   conversation revoke takes effect on next message (grants evaluated
   per inbound). Implication: also resolves Q4 (lurker visibility)
   and Q5 (auto-create on first contact — no, stateless).
3. **Predicate syntax in user_groups.** A pattern like
   `+claim:discord:guild=G` as a prefix? A new `predicate` column? A
   separate `admission_predicates` table? Each has trade-offs around
   write-side validation and read-side joins.
4. **Lurker visibility.** In a channel-bot, how does the agent
   distinguish a channel member with no personal grant from one with
   a grant? Today `<user>` carries id + name; it does not carry the
   matched-grant list. Should it?
5. **Auto-create on first contact.** When a new joiner posts in a
   channel-bot, do we lazily insert a `user_groups` row to record
   their identity for audit, or stay stateless (only the route
   authorizes)? Audit vs. minimality tension.
6. **Membership loss.** If a Discord user leaves the guild, does the
   route still authorize their next message (with a stale token)?
   Membership claims have a freshness problem; how often do we
   re-verify?
7. **DM vs. channel from the same sub.** If alice DMs the bot AND
   posts in the channel, does she get one folder (her user-bot) or
   two (user-bot + channel-bot)? Today: two, by route. Should the
   agent see them as one principal?
8. **Operator scope on shape-B.** Today `**` matches any folder. In a
   route-as-auth world, does `**` still trump routes? Most likely
   yes, but the layering rule needs to be stated.
9. **JID identity vs. claim identity.** Today user_jids binds JID
   → canonical sub via OAuth. In a route-as-auth world, the JID's
   platform-side identity (the Discord user id) is what we trust;
   the canonical sub becomes optional. Two parallel identity models
   shouldn't drift — one renderer.
10. **Onboarding mode selection.** spec 28's flow assumes user-bot
    (one folder per user). Shape B doesn't need it. Shape C needs
    it for the overlay path only. The current `ONBOARDING_ENABLED`
    switch is too coarse; per-route or per-folder onboarding policy
    is undecided.

## Migration

- `5/28-mass-onboarding.md` and `5/29-acl.md` are replaced by this
  spec. The originals were deleted rather than kept as historical
  — their content is folded above.
- `5/32-tenant-self-service.md` stays as-is — different concern
  (org-chart vocabulary, invites, secrets hierarchy). This spec
  references 32 but does not subsume it.
- `GRANTS.md` continues to document today's wiring. This spec is the
  forward-looking lens; GRANTS is the present-tense reference.
- No code change is mandated by this spec. The end-state and tactical
  step each need their own follow-up spec and PR.
- **Subsumed by `6/9-acl-unified.md`**: the user-side and agent-side
  grant systems collapse into a single `acl` table with a unified
  `Authorize` function. Role indirection (Postgres / IAM-shaped) and
  audience predicates live there. The "user-sub patterns" tactical
  step from this spec lands as part of that unification, not as a
  separate `user_groups` column extension. The decided open questions
  here (identification vs access, agent finds out at failure site)
  carry into 6/9 as the auth contract.
