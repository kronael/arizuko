---
status: planned
depends: [hitl]
---

# pay.sh Integration

Agent-first micropayment layer. Agents call paid APIs (email, SMS, image
gen, search, crypto data, domains, blockchain RPC) via HTTP 402 — no API
keys, Solana wallet is identity. 1,094+ endpoints across 75 providers.

Reference: https://pay.sh, github.com/solana-foundation/pay (Rust + TS)

## What

`pay mcp` exposes paid API access as native MCP tools the agent discovers
at session start. No skill file needed — MCP is self-describing.

## Phase A — HITL first (prerequisite)

HITL (human-in-the-loop approval) must ship before agents can autonomously
spend money. Until then, pay.sh is blocked.

## Phase B — pay.sh integration

1. Add `pay` binary to `ant/Dockerfile` (`npm install -g @solana/pay`)
2. Wallet lives at `~/.pay/wallet` in group home (persists across restarts)
3. gated injects `pay mcp` into agent `settings.json` at spawn when
   `PAY_ENABLED=1` in group `.env`
4. Headless signing: `PAY_AUTO_APPROVE=1` env var (verify CLI support;
   if not supported, 30-line wrapper that reads key from env)
5. `PAY_DAILY_LIMIT_USD` env var — per-group daily spend cap

No new daemon. No skill file. `PAY_ENABLED=1` in `.env` opts a group in.

## Providers (examples)

| Category       | Provider      | Price range  |
| -------------- | ------------- | ------------ |
| Email          | StableEmail   | $0.001–$8    |
| SMS/voice      | StablePhone   | $0.05–$20    |
| Image gen      | StableStudio  | $0.01–$20    |
| Web search     | Perplexity    | $0.01        |
| Domain reg     | StableDomains | $0.10–$1,500 |
| Crypto data    | StableCrypto  | $0.01        |
| Blockchain RPC | QuickNode     | $0.001–$1    |
