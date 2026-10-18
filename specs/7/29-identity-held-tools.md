---
status: queued
depends:
  [
    28-identity-held-credentials,
    ../5/15-surrogate-oauth,
    ../5/14-credentials,
    ../5/13-ext-mcp,
    ../5/32-acl-unified,
    ../5/33-paths-roles,
  ]
---

# specs/7/29 — tools bound to an identity, and whose account they act as

`status: queued`: FILED, NOT STARTED. Held for the owner's word, after phase
6 finishes. Separate from `28-identity-held-credentials.md` on the owner's
instruction, not for tidiness.

## The default is the agent's own login, and it stays the default

The owner's words:

> normally the agent should use its own logins for tools, and acting on
> behalf of users should be explicitly granted anyway.

So the ordinary case is already right and must not change: a tool call uses
the INSTANCE's credential for that tool, resolved by the broker and scrubbed
from the result (`../5/13-ext-mcp.md`). The agent never holds the value, and
nothing about a user's identity enters that path.

Acting as a human is the EXCEPTION, it is a grant, and it already exists.
`../5/15-surrogate-oauth.md` is `shipped`: a human consents at
`/dash/me/connections`, the provider's token is written under their identity,
and the agent can then act as them rather than as a shared PAT.
`auth/surrogate/providers/github.toml` writes the same `GITHUB_TOKEN` key the
connector reads, so either source fills one slot.

**What is missing is therefore narrow.** Not a mechanism for acting as a user —
that ships. The gap is that an identity cannot CARRY a tool: there is no
answer to "which tools has this person connected, and which may the agent
reach through them".

## The question this spec exists to answer

When a folder holds `mcp:github_create_issue` and the initiator has connected
their own GitHub account, three outcomes are possible and the code picks none
of them deliberately today:

1. the instance's token acts, and the user's connection is ignored;
2. the user's token acts, silently, because it is present;
3. the call is refused unless a grant names which of the two applies.

Outcome 2 is the dangerous one, and it is what a naive join of
`28-identity-held-credentials.md` to the connector would produce. A user who
connected GitHub so that THEY could be acted for in one folder would find
their token spending authority in every folder that reaches the same tool.

So the containment is the point of this spec, and `../5/32-acl-unified.md`
already owns the primitive: one `acl` row, three principals, and the row's
scope glob IS the containment. `../5/33-paths-roles.md` adds the two axes —
the turn's folder confers in full, the attested sender adds only inside that
folder's subtree.

## What must be settled before any code

1. **Whether "this tool belongs to this identity" is an `acl` row or a new
   table.** `5/32` says one primitive, three principals. A fourth store has to
   justify itself against that sentence, and `../5/13-ext-mcp.md` already
   gates each remote tool as `mcp:<name>_<tool>`.
2. **Which principal a surrogate call is authorized as.** The agent, the
   human, or a composite. `5/33`'s two axes constrain it; nothing names it.
3. **What the audit row says.** A call made as a human under a grant and a
   call made as the instance must not be indistinguishable after the fact.
4. **Revocation.** A user disconnecting a tool mid-turn, and whether a running
   call keeps the authority it started with.
5. **Whether a tool the agent has no instance login for may exist at all as a
   user-only tool.** That inverts the default and needs saying out loud.

## Unverified

| #   | Claim                                                                               | How to settle it                                                                                          |
| --- | ----------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| 1   | An `acl` row can express "this tool belongs to this identity" with no schema change | Read `5/32`'s evaluator against a `github:` principal.                                                    |
| 2   | `grantableAction` accepts an action naming a surrogate tool                         | A new `acl` action is unreachable until `grantableAction` accepts it; grep it before designing around it. |
| 3   | The broker can resolve a per-identity secret, not only a per-folder one             | Read `store/secrets.go` `FolderSecretsResolved` and the broker's call site.                               |
| 4   | An audit row today records which credential a connector call used                   | Read the connector's audit write.                                                                         |

## Order

After `28-identity-held-credentials.md`, and after phase 6. The two share a
join — platform principal to identity — and nothing else. Build that join once,
in `7/28`, and read it here.
