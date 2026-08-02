---
status: superseded-in-part
superseded-in-part: [5/32-acl-unified]
---

# dashd ACL UI — designed four pages, shipped one

> **Status corrected 2026-08-02.** This spec was marked `shipped`, but
> none of the four top-level pages it specifies exist. Grepping the tree
> for `/dash/acl`, `/dash/membership`, `/dash/roles`, or
> `/dash/principals` returns nothing. What shipped instead is a single
> folder-scoped page, and the gap between the two is the record worth
> keeping.

## What shipped

`GET /dash/groups/{folder}/grants`, plus `POST .../grants` and
`POST .../grants/revoke` (`dashd/grants_admin.go`). It renders
`store.ListACLByScope(folder)` — the ACL rows whose scope covers that
one folder — behind `d.requireAdmin(w, r, folder)`.

Authorization is per-folder, not the global `Authorize(caller, "admin",
"**")` this spec proposed. That is the more conservative shape: an
operator who administers one tenant does not get a console listing every
principal in the instance.

## What did not ship, and why it still might

Four global pages: `/dash/acl` (filterable row table with an insert
form), `/dash/membership` (edge table + transitive closure expander),
`/dash/roles` (role index + member/permission detail), and
`/dash/principals/{id}` (effective-grants view — direct rows,
role-derived rows, and the tier-default fallback).

The one genuinely missing capability is **effective-grants**: "what can
this principal actually do", resolved across `acl_membership`. The
folder-scoped page answers "who can act here", which is the inverse
question, and the inverse question is the one an operator asks when
debugging a _deny_. Anyone reviving this should build that page first
and skip the rest.

The tier-default fallback section of that design is already dead —
`grants.DeriveRules` no longer exists
([`19-action-grants.md`](19-action-grants.md)), so effective grants are
now just direct rows plus role-derived rows, which makes the page
simpler than originally specced.

## Constraints that carry over

- **Composite-key deletes.** `acl` and `acl_membership` have no
  surrogate id, so a delete form must post every PK column
  (`principal, action, scope, params, predicate, effect`;
  `child, parent`). Schema:
  [`../5/32-acl-unified.md`](../5/32-acl-unified.md).
- **Cycle check before an edge insert** — `acl_membership` is walked
  transitively by `Authorize`; a cycle hangs the resolver.
- **Reuse the resolver, never reimplement it.** A transitive expander
  must call the same lookup `Authorize` uses. A second implementation of
  the closure walk is a second answer to the same question.
- **Wildcard delete safety.** Deleting a `principal='**'` or
  `scope='**'` row can lock everyone out; the bootstrap
  `role:operator, *, **` row seeded at `arizuko create` is the one whose
  removal needs DB-shell recovery. Gate it behind a type-the-name
  confirm.
- No SPA, no JSON endpoints from dashd — HTMX partials on `x/` siblings,
  matching the rest of the daemon.
