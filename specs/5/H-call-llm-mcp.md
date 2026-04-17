---
status: draft
---

# H — `call_llm` MCP tool

An MCP tool exposed to the in-container agent for one-shot completions
against non-Claude models (GPT-4o, Gemini, DeepSeek, Llama, Grok,
local llama.cpp, etc.). Not another Claude subagent — a genuinely
different backend.

## Motivation

1. **Cross-model verification** — check claims against differently-trained
   models. Same-model subagents give correlated errors.
2. **Cheap bulk work** — classify, summarize, label at 10-100x less cost.
3. **Capability gaps** — Gemini 1M+ context, GPT image gen, local
   uncensored models for adversarial testing.

## Non-goals

- Not a general LLM router for the gateway
- Not replacing Claude as primary agent
- Not streaming in v1 (MCP stdio is request/response)
- No tool-use recursion on the other model (text-in, text-out)
- No fine-tuning, embeddings, or image gen in v1

## MCP tool shape

```json
{
  "name": "call_llm",
  "inputSchema": {
    "required": ["model", "prompt"],
    "properties": {
      "model": { "type": "string", "description": "e.g. 'openai/gpt-4o'" },
      "prompt": { "type": "string" },
      "system": { "type": "string" },
      "max_tokens": { "type": "integer", "default": 1024 },
      "temperature": { "type": "number", "default": 0.7 },
      "timeout_seconds": { "type": "integer", "default": 30, "maximum": 55 },
      "reason": {
        "type": "string",
        "description": "Logged for audit, not sent to model"
      }
    }
  }
}
```

Return: `{ text, model, provider, usage, cost_usd, latency_ms, finish_reason }`

## Chosen approach

**Thin HTTP client (OpenAI-compatible) + OpenRouter as default provider.**

| Approach                   | LOC  | Deps          | Fit        |
| -------------------------- | ---- | ------------- | ---------- |
| Thin HTTP client (chosen)  | ~230 | zero (stdlib) | excellent  |
| OpenRouter as provider     | same | same          | excellent  |
| Go libraries (go-openai)   | ~230 | +1            | okay       |
| MCP sidecar (mcp-llm etc.) | ~30  | +1 container  | poor       |
| LiteLLM proxy sidecar      | ~230 | +1 container  | decent     |
| Claude Agent SDK hook      | N/A  | N/A           | impossible |

Rationale: matches whisper/chanlib/proxyd patterns (thin, opinionated,
zero deps). Uses `net/http` + `encoding/json`. OpenRouter gives 300+
models through one key, ZDR by default. If we need LiteLLM later, swap
`LLM_BASE_URL` — nothing thrown away.

## Grants

Uses existing rule engine (`grants/grants.go`):

- `call_llm(model=openai/*)` — any OpenAI model
- `call_llm(model=openai/gpt-4o)` — one specific model
- `!call_llm(model=*/*-uncensored*)` — deny uncensored variants

Spend caps: separate `LLM_DAILY_CAP_USD` config key per instance.

## Config

```env
LLM_PROVIDER=openrouter
LLM_BASE_URL=https://openrouter.ai/api/v1
LLM_API_KEY=sk-or-v1-...
LLM_ALLOWED_MODELS=openai/gpt-4o,google/gemini-2.5-pro,deepseek/deepseek-v3.2
LLM_DAILY_CAP_USD=5.00
```

Keys flow like `CHANNEL_SECRET`: env -> `core.LoadConfig` -> `gated`.
Never enter the container.

## Auditing

v1 = log only (Info level, same as `send_message`).
v2 = `llm_calls` SQLite table for cap enforcement and dashboards.

## Open questions

1. **Streaming** — MCP stdio is request/response. Clamp timeout to 55s
   (under MCP's 60s default). Revisit if SDK exposes per-server timeout.

2. **Allowlist in .env vs grants** — probably both: `.env` defines the
   universe (operator policy), grants pick from it (tenant policy). Need
   to verify grants inheritance works cleanly.

3. **Cost tracking** — real-time (running map + persist per call) vs
   post-hoc (provider billing API). Real-time is simpler and more
   accurate; use provider's `usage` field + static price table.

4. **Fallback chains** — return the error, let agent retry with a
   different `model` argument. Explicit > implicit. Revisit LiteLLM
   if this becomes painful.

5. **Privacy** — prompts leave the cluster. OpenRouter is ZDR by
   default. Grants are the opt-in mechanism (only folders with
   `call_llm` grant can use it).

6. **First allowlist** — openai/gpt-4o, openai/gpt-4o-mini,
   google/gemini-2.5-pro, deepseek/deepseek-v3.2,
   meta-llama/llama-3.3-70b-instruct. Accept or prune?

7. **Local models** — `LLM_BASE_URL=http://ollama:11434/v1` works
   trivially (same OpenAI-compatible contract).
