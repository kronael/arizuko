---
status: active
---

# specs/6 — platform: genericization + control API

The infrastructure phase that has to ship before products. Two strands:

1. **Genericization** — make each daemon truly standalone and reusable.
   Today the daemons share `messages.db`, share a `go.mod`, and hardcode
   arizuko concepts (`folder`, `tier`, `group`). This phase decouples
   them: each daemon owns its tables, exposes its own contract, and
   accepts capability tokens for auth. Reusable parts (router core,
   scheduler, OAuth proxy, auth lib) can be deployed without the AI
   stack.

2. **Federated control API** — every daemon serves `/v1/*` for the
   resources it owns; the dashboard consumes those APIs; agents reach
   the same surface via MCP forwarding. One contract, two fronts. Adds
   write parity to the dashboard (today the dashboard is read-only;
   mutations live in scattered MCP tools). Token model and verification
   centralize in the shared `auth/` library.

| Spec                                       | Status | Hook                                                                      |
| ------------------------------------------ | ------ | ------------------------------------------------------------------------- |
| [R-genericization.md](R-genericization.md) | draft  | Generic primitives in shared types; per-daemon DB ownership; gated split. |
| [R-platform-api.md](R-platform-api.md)     | spec   | Federated `/v1/*` per daemon, capability-token auth, shared `auth/` lib.  |

Genericization spec is sketched but not written. It precedes the API
work in implementation order: a generic daemon with hardcoded arizuko
concepts isn't reusable, and the API contract is more honest once the
concepts are factored.

## Why this is its own phase

Earlier the platform-API spec lived in [specs/7/](../7/) (products), as
a sub-bullet of "what products need to ship." That framing was wrong:
the API + genericization is the bigger piece of work and the
prerequisite for products being properly manageable. Promoted to its
own phase so that ordering is explicit — platform first, products
consume it.

## Open questions tracked here

- TTL and revocation policy for capability tokens.
- Whether/when to split storage per daemon (own DB vs shared schema).
- Whether to add a dedicated `authd` for centralized minting + audit.
- Whether `gated` should split into `routerd` (generic) +
  `agent-runnerd` (AI-specific spawner).
