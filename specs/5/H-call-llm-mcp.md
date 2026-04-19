---
status: unshipped
---

# `call_llm` MCP tool

Agent-side tool for one-shot completions against non-Claude models
(cross-model verification, cheap bulk classification, capability gaps).

Shape: `call_llm(model, prompt, system?, max_tokens?, temperature?,
timeout_seconds?, reason?)` → `{text, model, provider, usage,
cost_usd, latency_ms, finish_reason}`.

Approach: thin HTTP client against OpenAI-compatible API; OpenRouter
as default (300+ models, one key). Config: `LLM_PROVIDER`,
`LLM_BASE_URL`, `LLM_API_KEY`, `LLM_ALLOWED_MODELS`,
`LLM_DAILY_CAP_USD`. Access via grants (`call_llm(model=openai/*)`).

Rationale: same-model subagents give correlated errors; cheap
classification is 10-100x cheaper on smaller models.

Unblockers: implement in `ipc/`, pick initial allowlist, cost tracking
via provider `usage` field + static price table.
