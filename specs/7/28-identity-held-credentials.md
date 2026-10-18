---
status: queued
depends:
  [
    ../5/15-surrogate-oauth,
    ../5/14-credentials,
    ../5/31-identity-pairing,
    ../5/32-acl-unified,
    ../5/13-ext-mcp,
    23-agent-habitat,
  ]
---

# specs/7/28 — a user subscription pays for the turn that user initiates

`status: queued`: FILED, NOT STARTED. The owner asked for it to be written
down and held until he says to begin, and he asked that phase 6 finish first.
Nothing below is approved, and no step of it runs before that word.

## What the owner asked for

Users already authenticate, and authentication already collects their chat
JIDs into one OAuth identity. From there, a user connects a MODEL
SUBSCRIPTION — Claude, codex, OpenRouter — through OAuth, and the token that
yields POWERS THE TURNS THAT USER INITIATES, instead of the instance's own
model credential.

A turn a user starts should spend that user's plan, not the operator's. That
is the whole of this spec.

## This spec is NOT about tool credentials

The owner drew the line and it is load-bearing:

> normally the agent should use its own logins for tools, and acting on
> behalf of users should be explicitly granted anyway, so that is a
> different spec than the oauth for users' subscription to tokens.

Two different questions, two different answers:

- **Who pays for the model** — this spec. The subscription is the user's, the
  turn is the user's, and the bill follows the initiator.
- **Whose account a tool acts as** — `29-identity-held-tools.md`. The agent
  uses ITS OWN login by default, and acting as a human is a grant that
  `../5/15-surrogate-oauth.md` already defines and ships.

A design that routes both through one mechanism would make "the user pays for
the tokens" silently imply "the tool acts as the user", which is a privilege
escalation nobody asked for.

## The notion of an initiating turn exists, and it is not yet an identity

The owner asked whether arizuko has a notion of an initiating turn. It does,
and naming its exact shape is what this spec must build on.

`store.TurnContext` (`store/turn_context.go:65`) carries `Trigger`. Measured
live on sloth 2026-10-18, a GitHub turn recorded
`"trigger":"github:kronael"` beside `turn_id`, `folder`, `topic` and
`chat_jid`. So every turn already records WHO started it.

What `Trigger` holds is a PLATFORM-SCOPED PRINCIPAL — `github:kronael`,
`telegram:user/<id>` — not an OAuth identity. The identity exists separately:
`5/31-identity-pairing` writes the `acl_membership` edge that binds a chat JID
to a confirmed human, in routd's transaction, stamped `added_by='pairing'`.

**The gap this spec closes is one join, not one feature.** Platform principal
→ identity is already written down by pairing; identity → credential is
already written down by surrogate OAuth. Nothing reads the two together at the
moment a turn is dispatched. A design that adds a third store of "who is this"
would be the `identity_derived_not_read` failure at a new site.

## The old specs this assembles, and what each already settled

| Spec                          | Status  | What it already decides                                                                                                                                                            |
| ----------------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `../5/15-surrogate-oauth.md`  | shipped | A human consents at `/dash/me/connections` and the provider's token is written under their identity, so an agent can act AS the human rather than as a shared PAT.                 |
| `../5/14-credentials.md`      | shipped | Three tiers, and the one that matters here: env-profile keys are the ONLY table-backed credential that reaches a container's environment. Capability credentials stay broker-only. |
| `../5/31-identity-pairing.md` | shipped | The chat JID → identity edge, written once, reachable by `unpair`.                                                                                                                 |
| `../5/32-acl-unified.md`      | shipped | One `acl` primitive over three principals; the row's scope glob IS the containment.                                                                                                |
| `../5/13-ext-mcp.md`          | shipped | Connectors: a tool reached under `mcp:<name>_<tool>`, its secret resolved on the host and scrubbed from the result.                                                                |

Reviving them means reading what they already decided and writing only the
join. Four of the five are `shipped`, so a contradiction between this spec and
one of them is a defect in this spec until proven otherwise.

## The measurement that makes the subscription half possible

Measured 2026-09-04 against live vendors, recorded in `[[model_gateway]]`:

- **claude**: a gateway holds the real credential and the container carries
  only `ANTHROPIC_BASE_URL` + a dummy key. Anthropic answers 200 with
  `anthropic-ratelimit-unified-5h/7d` headers — PLAN billing, not per-token
  credits, and `overage-disabled-reason: org_level_disabled` means it cannot
  silently fall through.
- **codex**: frontable too. `requires_openai_auth=true` makes codex send its
  ChatGPT subscription bearer to an arbitrary `base_url`; `wire_api` MUST be
  `responses`.
- **pi**: blocked BY THE VENDOR, not by the proxy — 400, "Third-party apps now
  draw from your extra usage, not your plan limits." Enforcement is on client
  identity. Making pi draw plan quota would mean forging Claude Code's
  identity, which arizuko does not ship and must not.

So two of the three harnesses can spend a user's plan. pi cannot, and a design
that pretends otherwise is wrong on day one.

## What is NOT settled, and must be before any code

1. **Whose plan pays when the initiator is not the folder's owner.** A turn in
   a shared folder started by a guest: the guest's plan, the owner's, or a
   refusal? `5/33`'s two axes constrain the answer but do not give it.
2. **What happens when the initiator holds no subscription.** Falling back to
   the instance credential silently is the failure this project calls a
   best-effort swallow. The alternative is a visible refusal.
3. **OpenRouter's OAuth shape** is unmeasured. No vendor page has been read.
4. **Revocation.** A user who disconnects mid-turn: does the running turn keep
   spending? Nothing today answers this.
5. **The container never holds the real credential** under the gateway model —
   but `runed.env` carries `store.EnvProfileKeys` today. Which of the two is
   the reader after this change is undecided.

## Unverified

| #   | Claim                                                                               | How to settle it                                             |
| --- | ----------------------------------------------------------------------------------- | ------------------------------------------------------------ |
| 1   | OpenRouter supports an OAuth flow that yields a reusable token                      | Read OpenRouter's own docs; do not infer from other vendors. |
| 2   | A per-identity gateway key can be minted and revoked without a restart              | Measure against the gateway, as `[[model_gateway]]` did.     |
| 3   | `Trigger` is populated for every ingress path, not only GitHub and web              | Count distinct `trigger` prefixes in live turn contexts.     |
| 4   | An `acl` row can express "this tool belongs to this identity" with no schema change | Read `5/32`'s evaluator against a `github:` principal.       |

## Order

Phase 6 finishes first — the owner said so. `6/47` is `planned` and unbuilt,
and `6/45` and `6/46` are `partial`. This spec begins only on the owner's word.
