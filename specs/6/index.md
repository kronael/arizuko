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

| Spec                                                   | Status     | Hook                                                                                                                                                                                                                                                                                                                          |
| ------------------------------------------------------ | ---------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [R-genericization.md](R-genericization.md)             | draft      | Generic primitives in shared types; per-daemon DB ownership; gated split.                                                                                                                                                                                                                                                     |
| [R-platform-api.md](R-platform-api.md)                 | spec       | Federated `/v1/*` per daemon, capability-token auth, shared `auth/` lib.                                                                                                                                                                                                                                                      |
| [1-auth-standalone.md](1-auth-standalone.md)           | spec       | `auth/` capability library — verify/mint/OAuth-flow/MCP-tools, no daemon.                                                                                                                                                                                                                                                     |
| [2-proxyd-standalone.md](2-proxyd-standalone.md)       | shipped    | proxyd as TOML-driven authenticating gateway. v0.35.0: per-daemon `[[proxyd_route]]` → `PROXYD_ROUTES_JSON`. v0.36.0: runtime `/v1/routes` REST + `routes.*` MCP via the `resreg` registry (first instance of spec 6/5).                                                                                                      |
| [3-user-spawned-agents.md](3-user-spawned-agents.md)   | spec       | End-user `POST /v1/agents` flow: submit definition, get sandboxed tenant.                                                                                                                                                                                                                                                     |
| [4-openapi-discoverable.md](4-openapi-discoverable.md) | spec       | Every daemon exposes generated `/openapi.json` + `/docs/`; aggregator landing.                                                                                                                                                                                                                                                |
| [5-uniform-mcp-rest.md](5-uniform-mcp-rest.md)         | spec       | Every operator action wired via REST (OAuth-gated) AND MCP (tier-gated) over a single handler; resource registry, scope vocabulary, per-resource access matrix.                                                                                                                                                               |
| [6-middleware-pipeline.md](6-middleware-pipeline.md)   | draft      | Finish existing factoring in three pipelines (MCP `grantedJID`, inbound `enrichBatch`/`buildPromptContext`, HTTP `pathDeny` split). Kills three duplications, no new abstraction. ~−10 LOC; structural win is drift prevention.                                                                                               |
| [9-acl-unified.md](9-acl-unified.md)                   | spec       | Collapse `user_groups` + `grant_rules` + dead `grants` into one `acl` table with three principal namespaces (OAuth sub, `folder:<path>`, channel JID), action implication (`*`⊃`admin`⊃`interact`/`mcp:*`), role indirection (`role_members`), and one `Authorize` function. Subsumes spec 5/A's tactical user-sub extension. |
| [A-mcp-everywhere.md](A-mcp-everywhere.md)             | draft      | Cleanup catalog after `9-acl-unified` ships: every state-changing operation reachable via MCP + REST through one `resreg` handler. CLI/dashd become thin clients. Anti-patterns (inbound ingestion, cost-log writes, streaming) stay outside.                                                                                 |
| [B-route-mode-ingestion.md](B-route-mode-ingestion.md) | draft      | Collapse `impulse_config` weights into a URI fragment on `target` (`folder#observe`). Verb filtering via match-key + seq priority. Observed messages surface as bounded context on next trigger turn — same primitive serves feed/news ingestion.                                                                             |
| [E-rest-mcp-bridge.md](E-rest-mcp-bridge.md)           | brainstorm | Wrap platform REST APIs (Slack, X, GitHub, …) as authorized MCP tools the agent calls directly. Catalog-driven REST→MCP mapper + `auth.Authorize` gate. Adapter outbound code (`slakd.Send`, etc.) deprecated. Three framings still on the table; framing comparison + open questions captured before drafting the real spec. |

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
