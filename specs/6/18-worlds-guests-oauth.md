---
status: blank
---

# worlds, guests, and delegated OAuth credentials

> **Blank spec — skeleton only.** Essence captured; to be filled in.

## Essence

A **world** is owned by a user (the owner). The owner can **invite** other
users into the world; invited users are **guests**. Guests **join groups**
inside the world, where the owner **grants** them scoped access. A guest can
**link their own accounts** on OAuth-connected platforms, and the agent may
**act with a guest's linked credentials** — governed by explicit **rules**.

## Concepts (to define)

- **World** — ownership boundary; relation to instance / group hierarchy.
- **Owner** vs **guest** — role emergence (grants-based, per `4/9`?) vs a new
  membership primitive. Guests are not operators.
- **Invite** — issue / accept / revoke; relation to existing `onbod` invites
  (`5/5`) and route-token invites (`5/W`).
- **Group membership** — how a guest is admitted to a group and what the owner
  can allow them to do there (per-guest grant scope).

## Delegated credentials (to define)

- **Account linking** — a guest links a platform account via that platform's
  OAuth (surrogate-OAuth surface, `5/43`).
- **Credential storage** — per-guest, folder-scoped secrets (secrets model);
  never shared to other guests.
- **Use rules** — when/whether the agent may act _as_ a guest with their creds:
  which actions, which groups, consent + revocation, audit.

## Open questions

- World as a first-class table vs a top-level folder convention?
- Guest identity across platforms (one guest, many linked accounts).
- Rule language for credential use — reuse the grant DSL (`[!]action(param=glob)`)
  or a new predicate layer?

## Ties

`4/9` grants · `5/5` onboarding/invites · `5/43` surrogate OAuth ·
`5/W` route-token invites · secrets model. Detailed design to be written here
in `6/`.
