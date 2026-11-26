---
status: draft
---

# Select a tool account only under explicit consent

An identity-held tool lets an agent use a connected human account under a
separate grant, while the agent's own account remains the ordinary choice.

## Deferred until the account-selection contract is approved

The owner's instruction is binding:

> normally the agent should use its own logins for tools, and acting on
> behalf of users should be explicitly granted anyway.

This phase-9 draft is unapproved authorization work, not a shipped selection
rule. Model funding and tool authority are separate concerns.
[Identity-held model credentials](55-identity-held-credentials.md)
proposes the principal-to-account join; this feature must read that join once
it exists, not create a second identity map.

At `880119f1b75548b3d3fdbab40d4eda526f3fd060`,
`routd/sibling_db.go:185` resolves folder secrets and overlays the raw caller's
user secrets through `FolderSecretsResolvedForUser`. `routd/mcp.go:636`
uses that reader for granted connector calls. Presence of a personal secret can
therefore select an account. The code does not prove the proposed rule that
all personal tool use requires a distinct account-selection grant.
`dashd/me_connections.go` supplies OAuth consent and local disconnect.
Consent to connect an account is not the proposed folder-specific authority
to spend that account.

## One existing grant primitive must carry the selection

`auth/README.md` owns ACL evaluation and scope containment.
[Paths and roles](../5/33-paths-roles.md) owns the two authorization axes.
`EXTENDING.md` owns connector declarations and broker delivery.
A grant to call a tool and a grant to act as a human must remain distinguishable.
A canonical model payer must never silently enter the connector caller path.

No new table, credential format or action name is chosen. The existing broker
and its secret store remain the only credential path. The draft must resolve
these questions before implementation:

1. Which existing ACL relation selects the human account for a named tool and
   folder? Recommend an explicit grant; a connected token alone confers nothing.
2. Which principal authorizes the surrogate operation? Recommend checking both
   the turn's tool authority and the human's consent within the target folder.
3. Which existing audit fields identify the chosen account without exposing its
   token? Recommend recording the canonical subject and credential key name.
4. What happens on disconnect during a call? Recommend fresh acquisition for
   every call, with a defined boundary for a call already admitted.
5. May a tool operate with only a human login? Recommend refusal until the owner
   explicitly approves that exception to the ordinary agent-account path.

These are recommendations, not approved grants or implemented guarantees.

## Account selection replaces an implicit overlay

Estimated cost: **5–8 engineering days** after the identity join and owner
selection of the auth contract. Work includes the existing broker, per-call
refresh, audit attribution, disconnect races, operator controls and cross-folder
refusal tests. Provider consent and live account acceptance add operator time.
The feature replaces presence-based user-secret selection; it must not add a
second broker or bypass `AuthorizeTurn` and hold checks.

Build when a real connected human account needs delegated use in a named
folder, after the owner approves consent and revocation semantics. A future
acceptance must capture the outgoing account, refuse a foreign folder, and
prove disconnect blocks the next acquisition. Current connector tests do not
establish those future guarantees.

No cache or message/log expiry is permitted. The existing 90-day `audit_log`
policy is the only log-expiry exception.
