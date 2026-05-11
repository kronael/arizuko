---
name: auth
description: >
  Reference for arizuko OAuth identity — canonical sub, account linking,
  seven-branch collision dispatcher. USE for "account collision",
  "link my Google account", "why did login fail", "how does OAuth work
  here", OAuth provider/callback questions. NOT for slink tokens
  (use slink-mcp).
user-invocable: true
---

# Auth

arizuko stores one row per `(provider:sub)` in `auth_users`. One row
per identity is **canonical**; others link to it via `linked_to_sub`.
The canonical sub is the JWT subject — every backend (proxyd, dashd,
webd) sees one stable identity per user, no matter how they logged in.

## Canonical sub

- First provider you log in with becomes canonical.
- Linked rows have `linked_to_sub = <canonical>`; their session resolves
  to the canonical sub when issued (see `auth/oauth.go:dispatchOAuth`).
- A canonical row can have any number of linked siblings — log in via
  any of them, you land in the same account.

## Linking via `/dash/profile`

`https://<host>/dash/profile/` lists your canonical sub plus all linked
provider rows, with an **Add account** button per provider. The button
redirects to `/auth/<provider>?intent=link`; the OAuth state cookie
carries `LinkFrom = <your canonical sub>` so the callback knows to
write a link, not a fresh user.

## Collision dispatcher

`dispatchOAuth` fans the OAuth callback into seven cases. In plain
language:

| # | Situation                                              | What happens                                |
| - | ------------------------------------------------------ | ------------------------------------------- |
| 1 | Linking, sub is already linked to me                  | No-op refresh                               |
| 2 | Linking, sub canonical for **another** user           | Render collision page                       |
| 3 | Linking, sub is brand new                             | Write link, refresh session                 |
| 4 | No link intent, session active, sub is brand new      | Render collision page                       |
| 5 | No link intent, session active, sub belongs to other  | Render collision page                       |
| 6 | No link intent, no session, sub brand new             | Create canonical user, log in               |
| 7 | Default                                                | Log in via canonical sub                    |

The collision page asks "Link to current account?" or "Log out and
become that user?" — token signed with `collideTTL = 10m`. Cannot
merge two existing canonical users via this UI; that's by design (it
would silently destroy account state).

## Recovery

- "Account collision" page → user picks: link the new sub to current
  account, or log out and continue as the other identity.
- Lost access to canonical provider → log in with any linked sibling;
  same canonical sub resolves.
- Wrong account merged → no auto-undo. Operator removes the
  `linked_to_sub` value directly in `auth_users` if needed.

Spec: `specs/1/f-auth-oauth.md`. Code: `auth/oauth.go`, `auth/collide.go`.
