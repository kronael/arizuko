---
status: draft
---

# pay.sh — agent-native paid API access

Agents call paid APIs (email, SMS, image generation, search, crypto data,
domains, RPC) over HTTP 402 with a Solana wallet as identity — no API keys
per provider. `pay mcp` exposes them as native MCP tools the agent discovers
at session start, so no skill file is needed: MCP is self-describing.

Reference: pay.sh, `github.com/solana-foundation/pay`.

## Blocked on HITL

Human-in-the-loop approval ([5/19](../5/19-hitl-firewall.md)) must ship
first. Until an agent's spend can be held for approval, it cannot
autonomously spend money. This is the whole gate; everything below is
cheap once it lifts.

## Shape

No new daemon, no skill file. `PAY_ENABLED=1` in a group's `.env` opts it
in; the `pay` binary lands in the agent image, its wallet lives in the group
home so it survives restarts, and the spawn injects `pay mcp` into the
agent's MCP config. Per-group spend is capped by a daily-limit env var.

## Open

- Headless signing: confirm the CLI supports non-interactive approval. The
  signing key is a capability credential, so it never reaches container env
  (`5/14` §2) — a wrapper would have to run host-side as a connector and take
  the key from the broker.
- Whether the daily cap belongs in env or, like other business state, in the
  DB with a dashboard surface. Env is the v1 answer; a real deployment with
  more than one paying group probably wants the DB.
