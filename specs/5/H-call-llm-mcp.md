---
status: draft
---

# H — `call_llm` MCP tool for calling a different model

An MCP tool exposed to the in-container agent that performs a one-shot
completion against a **non-Claude** model (GPT-4o, Gemini 2.5, DeepSeek,
Llama 3.3, Grok, local llama.cpp, …) and returns the text. Not another
Claude subagent; a genuinely different backend.

## Motivation

Three use cases, all currently unserved:

1. **Cross-model verification.** The agent has a claim, a design, a
   code patch. It wants a model trained on different data to check it.
   Asking the same Claude via a subagent gives correlated errors. The
   `llm-council-mcp` project demonstrates why: anonymized peer review
   across GPT / Gemini / Claude / Grok reduces hallucinations on
   complex questions (three-stage: first opinions → peer review →
   chairman synthesis). We don't need the whole council; we need the
   primitive under it.

2. **Cheap bulk work.** Classifying 500 messages, summarizing long
   logs, rewriting boilerplate. Sonnet 4.6 at current prices is
   wasteful for single-shot labeling. DeepInfra's $0.10/M-token tier,
   Groq's LPU sub-100ms TTFT, or DeepSeek V3.2 on OpenRouter
   all serve this better — 10-100× cheaper per token than frontier
   Claude, and the quality floor is fine for well-scoped tasks.

3. **Capability gaps.** Gemini's 1M+ context when Claude hits limits.
   GPT-image-1 generation. A local uncensored model for adversarial
   prompt testing. Grok for live-web grounding (if we don't want to
   pay for Claude's WebSearch tool call). Whisper-variant audio paths
   already live in `WHISPER_URL`; this is the text analog.

`specs/5/3-agent-teams.md` disabled Claude agent teams for good reasons
(stdio, orphans, scoping). `call_llm` is the explicit replacement for
the case the user actually wanted: "ask someone else". Not another
Claude sub-session, but a genuinely different model — which the Claude
Agent SDK explicitly does **not** offer for subagents. `sdk.d.ts`
subagent definition restricts `model?: 'sonnet' | 'opus' | 'haiku' |
'inherit'`. There is no hook for an external provider inside the SDK
surface.

## Non-goals

- **Not a general LLM router for the gateway.** The router picks
  agents per-group, not models per-call. Gateway behavior does not
  change — only a new MCP tool is added.
- **Not replacing Claude as the primary agent.** Claude keeps the
  turn, the tools, the session, the steering loop. `call_llm` is a
  leaf tool call, like `WebFetch`.
- **Not streaming in v1.** MCP stdio via `mark3labs/mcp-go` supports
  streamable HTTP and SSE transports, but our unix-socket-stdio
  bridge is plain JSON-RPC request/response. Tool result is one
  blob. Partial tokens are discarded.
- **No chains / agents / tool-use recursion on the other model.**
  One round-trip. The other model produces text. If it tries to
  tool-call, we ignore the tool calls and return the text.
- **No fine-tuning, no embeddings, no image gen in v1.** Text-in
  text-out chat completions only. Other modalities are follow-ups.
- **No MCP pass-through.** The other model does not see arizuko's MCP
  server. It cannot `send_message`, cannot read history, cannot
  delegate. It is a pure function.

## Proposed interface

### MCP tool shape

<tool name="call_llm">
Tool name is `call_llm`. Alternatives considered: `ask_model`,
`second_opinion`, `external_llm`. `call_llm` is the most neutral —
the tool is used for many things besides second opinions.

```json
{
  "name": "call_llm",
  "description": "Call an external LLM (non-Claude) for a one-shot completion. Use for cross-model verification, cheap bulk tasks, or capability gaps. Returns text + usage + cost.",
  "inputSchema": {
    "type": "object",
    "required": ["model", "prompt"],
    "properties": {
      "model": {
        "type": "string",
        "description": "Model identifier. Provider-qualified, e.g. 'openai/gpt-4o', 'google/gemini-2.5-pro', 'deepseek/deepseek-v3.2', 'meta-llama/llama-3.3-70b'. See allowlist."
      },
      "prompt": { "type": "string", "description": "User message text." },
      "system": {
        "type": "string",
        "description": "Optional system prompt. If omitted, model runs with its default."
      },
      "max_tokens": { "type": "integer", "default": 1024 },
      "temperature": { "type": "number", "default": 0.7 },
      "top_p": { "type": "number", "default": 1.0 },
      "timeout_seconds": { "type": "integer", "default": 30, "maximum": 120 },
      "reason": {
        "type": "string",
        "description": "Human-readable reason. Logged for audit, not sent to the model."
      }
    }
  }
}
```

Single-turn only in v1. No `messages[]` array. The agent builds the
prompt string itself. If a multi-turn pattern is needed later, add
`messages` in a follow-up; do not try to design it up front.

Return shape (`mcp.NewToolResultText` with a JSON blob):

```json
{
  "text": "…",
  "model": "openai/gpt-4o",
  "provider": "openrouter",
  "usage": {
    "prompt_tokens": 432,
    "completion_tokens": 118,
    "total_tokens": 550
  },
  "cost_usd": 0.00287,
  "latency_ms": 1843,
  "finish_reason": "stop"
}
```

Errors return `mcp.NewToolResultError` with message like
`call_llm: model not allowed` or
`call_llm: provider timeout after 30s` or
`call_llm: daily spend cap reached ($5.00)`.

</tool>

### Grants

Grants use the existing rule engine (`grants/grants.go`,
`MatchingRules`, `CheckAction`, param globs). Shape:

- `call_llm` — blanket, any model, no limits (dangerous default;
  never in the seed).
- `call_llm(model=openai/*)` — any OpenAI model.
- `call_llm(model=openai/gpt-4o)` — one specific model.
- `call_llm(model=google/gemini-*,max_tokens=2048)` — clamp output.
- `!call_llm(model=*/*-uncensored*)` — deny uncensored variants.

The existing `grants.CheckAction(rules, "call_llm", params)` wiring in
`ipc/ipc.go` works with no engine changes — the handler validates the
resolved `model` and `max_tokens` (after defaults) against the rules
before firing the HTTP call.

**Spend caps** are the open question grants currently can't express.
Options:

1. New grant sugar: `call_llm(daily_usd<=5.00)` — requires a numeric
   comparator, currently the engine only does globs.
2. Separate config key `LLM_DAILY_CAP_USD=5.00` per-group in
   `core.LoadConfig`. Simpler, no grants-engine change. Likely right.
3. Token caps only (no dollar caps), leaving cost control to the
   provider dashboard. Cheapest to build, weakest guardrail.

### Auditing

Every call logs at Info level (same level as `send_message` today):

```
mcp.call_llm folder=root/support model=openai/gpt-4o
  prompt_tokens=432 completion_tokens=118 cost=0.00287
  latency_ms=1843 reason="verify sql migration"
```

Persistence: **v1 = log only**. v2 = new SQLite table `llm_calls`:

```sql
CREATE TABLE llm_calls (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  folder        TEXT NOT NULL,
  ts            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  model         TEXT NOT NULL,
  provider      TEXT NOT NULL,
  prompt_bytes  INTEGER NOT NULL,
  output_bytes  INTEGER NOT NULL,
  prompt_tok    INTEGER,
  output_tok    INTEGER,
  cost_usd      REAL,
  latency_ms    INTEGER,
  reason        TEXT,
  error         TEXT
);
CREATE INDEX idx_llm_calls_folder_ts ON llm_calls(folder, ts);
```

Daily-cap enforcement (option 2 above) queries this table with a
`SUM(cost_usd) WHERE folder=? AND ts > now()-24h` before dispatch.
Same pattern the scheduler uses for task quotas.

## Implementation sketch

### Wire-up (no new package)

Register in `ipc/ipc.go` `buildMCPServer`, next to `send_message`.
GatedFns gains one dependency:

```go
type GatedFns struct {
  // ...existing fields...
  CallLLM func(ctx context.Context, req LLMRequest) (LLMResponse, error)
}
```

`LLMRequest`/`LLMResponse` live in a new subpackage `llm/` (parallel
to `router/`, `queue/`). `gated` wires a concrete client at startup
based on `.env`:

```env
LLM_PROVIDER=openrouter        # openrouter | openai | direct | sidecar
LLM_BASE_URL=                  # override, e.g. http://litellm:4000
LLM_API_KEY=sk-or-v1-…         # passed via Authorization: Bearer
LLM_ALLOWED_MODELS=openai/gpt-4o,google/gemini-2.5-pro,deepseek/deepseek-v3.2
LLM_DAILY_CAP_USD=5.00
LLM_DEFAULT_TIMEOUT=30
```

API keys flow the same way `CHANNEL_SECRET` and provider tokens do
today: env → `core.LoadConfig` → passed by reference into `gated`.
They never enter the container. The agent only sees the MCP tool;
it cannot read `LLM_API_KEY`.

### Code path

```
agent (ant/index.ts)
  → MCP stdio call: mcp__arizuko__call_llm(model, prompt, …)
    via socat -> /workspace/ipc/gated.sock
  → ipc.go handler (CheckAction grants, validate model allowlist,
                    check daily cap from llm_calls table)
  → llm.Client.Complete(ctx, req)  (new package)
  → HTTP POST to base_url/chat/completions (OpenAI-compatible)
  → parse response, estimate cost from pricing table
  → INSERT llm_calls row
  → return mcp.NewToolResultText(JSON)
```

### Rate-limit, retry, cache

- **Rate limit on the tool**: token bucket per folder,
  e.g. 30 calls/minute. Enforced in `ipc.go` before dispatch.
- **Retry**: bounded backoff on HTTP 429 and 5xx, 2 retries, total
  wall-clock ≤ `timeout_seconds`. No retry on 4xx except 429.
- **Cache**: CLAUDE.md says "NEVER hit external APIs per request,
  cache everything." For an inherently non-idempotent LLM call, the
  cache key is `hash(model, system, prompt, temperature, max_tokens,
top_p)`. LRU, disk-backed under
  `/srv/data/arizuko_<name>/store/llm_cache/`, TTL 24h. Bypass with
  `temperature > 0.1` (non-deterministic), or an explicit `no_cache`
  param. **Open question**: is caching even desired? For
  verification calls it's wrong — you want a fresh second opinion.
  For bulk labeling it's right. Probably default-off, agent opts in.

### MCP stdio timeout interaction

`mark3labs/mcp-go` stdio default request timeout is 60s (same as the
spec's `DEFAULT_REQUEST_TIMEOUT_MSEC`). Our `timeout_seconds` cap of
120s is above that, which would produce an MCP -32001 error before
the HTTP call returns. Two fixes:

1. Clamp `timeout_seconds` to 55 in v1.
2. Increase MCP stdio timeout on the client side (`ant/src/index.ts`)
   via the SDK's MCP config — check whether Claude Agent SDK exposes
   this; if not, clamp.

The second option surfaces as an open question.

## Approach evaluation

Six buckets, each scored against arizuko's "boring tech + small deps"
rule. LOC estimates assume Go, single file in `llm/`, plus ~80 LOC in
`ipc/ipc.go` to register and validate.

### 1. Thin HTTP client (OpenAI-compatible, one endpoint)

We write ~150 LOC against the chat-completions schema, POST to any
OpenAI-compatible endpoint. Works with OpenRouter, Groq, Together,
DeepInfra, Fireworks, and self-hosted llama.cpp / ollama / vllm out
of the box.

- Pros: zero dependencies. `net/http` + `encoding/json`. One moving
  part. Reads exactly like the existing whisper/proxyd code. Full
  control over timeouts, retries, headers, User-Agent,
  error mapping. Cache is a 40-line `map + sync.Mutex + file`.
- Cons: we hand-code the tokenizer-free cost estimate (use the
  provider's `usage` field and a hardcoded price table). If a
  provider returns a non-standard response shape, we break. No
  built-in streaming. No vision / tool-use support (non-goal).
- LOC: ~150 + 80 = **~230**
- Deps: **zero** net new (uses stdlib and existing `slog`).
- Fit: **excellent**. Matches the whisper pattern, the chanlib
  adapters, and the minimum-viable philosophy.

### 2. OpenAI-compatible aggregator (OpenRouter as the single provider)

One config `LLM_BASE_URL=https://openrouter.ai/api/v1`. Same thin
HTTP client. OpenRouter exposes 300+ models through one endpoint,
one key, unified billing. Zero-data-retention by default (must
explicitly opt in to prompt logging for a 1% discount — so the
**default is private**). Pricing is pass-through (no markup).
Sub-option: BYOK mode lets us use our own provider keys and still
route through OpenRouter for analytics.

- Pros: one API key. Instant access to GPT-5.4, Gemini 3.1,
  Claude Sonnet 4.6, DeepSeek V3.2, Grok, Llama 3.3, Mistral, etc.
  No per-provider auth dance. Good free tier for smoke tests (free
  models with 20 req/min, 200/day). We trust one vendor for data
  handling instead of N. Easy to swap if it breaks — just change
  `LLM_BASE_URL`.
- Cons: one vendor. OpenRouter outage kills all LLM calls. Latency
  is slightly worse than direct (one extra hop). If OpenRouter
  enshittifies pricing, we eat it.
- LOC: same as #1 (~230). The difference is the `.env` default.
- Deps: same as #1.
- Fit: **excellent**. Bucket 1 + bucket 2 are the same code; this is
  just the recommended first config.

### 3. Go libraries

Candidates evaluated:

- **`github.com/sashabaranov/go-openai`** v1.41.2 — de facto standard
  Go OpenAI client. Works with any OpenAI-compatible endpoint via
  `config.BaseURL`. ~0 deps beyond stdlib. Saves us writing the JSON
  structs, but those structs are trivial. A single dep to own the
  full chat-completions surface is fine but arguably unnecessary for
  200 LOC of request+response.
- **`github.com/openai/openai-go`** — OpenAI's official Go SDK (July
  2024). Focused on the Responses API (new surface). Heavier, more
  opinionated, tightly coupled to OpenAI endpoints. Less suited to
  OpenRouter-style passthrough.
- **`github.com/anthropics/anthropic-sdk-go`** — official, active
  (March 2026). We already have Claude via the agent loop; calling
  Claude from `call_llm` is mostly a convenience. Could use this for
  an `anthropic` provider branch, but would blur the "call a
  different model" framing.
- **`github.com/google/generative-ai-go`** — Gemini official SDK.
  Separate auth/schema. Only needed if direct Gemini is the answer.
- **`github.com/tmc/langchaingo`** — LangChain port, multi-provider,
  vector stores, chains. Way too much for a one-shot completion.
  Straight "no" per the `3 innovation tokens` rule.
- **`github.com/henomis/lingoose`** — same class as langchaingo. Same
  "no".

- Pros: `go-openai` would trim ~80 LOC of struct definitions.
  Saves 30 minutes of typing and nothing else of value.
- Cons: adds a dep to the `go.mod` for what is 80 lines of typed
  JSON. langchaingo/lingoose are framework libraries, antithetical
  to arizuko's style.
- LOC: ~150 + 80 = **~230** (with `go-openai`) vs **~230** without.
- Deps: **1** (`go-openai`), or 0 for #1.
- Fit: **okay but not advantaged**. We'd pick #1 over this unless
  the code gets meaningfully shorter.

### 4. Existing MCP server as a sidecar

Several MCP servers already ship this feature:

- **`sammcj/mcp-llm`** — `ask_question`, `generate_code`,
  `generate_documentation`. Uses LlamaIndexTS. Node runtime. Focus
  is code generation, not general LLM access.
- **`dshills/second-opinion`** — Go. Pluggable providers (OpenAI,
  Google, Ollama, Mistral). Config via
  `~/.second-opinion.json` or env. Focus is code review of git
  diffs, not general text completion. Written in Go, uses official
  SDKs. Repo is MIT.
- **`NabilAttia123/llm-council-mcp`** — Python, OpenRouter-backed.
  Three-stage council (first opinions → peer review → chairman).
  Strong feature but **too opinionated**; we only want the
  primitive, not the whole council.

Deployment model: a new sidecar container in
`compose/docker-compose.yml`, mounted into the agent container via a
second socat bridge (`mcpServers.llm = { command: 'socat', ... }`)
mirroring the existing arizuko MCP path.

- Pros: zero code written on our side. Battle-tested. Upstream
  fixes get picked up for free. New providers land upstream, not
  in our backlog.
- Cons: extra container, extra config, extra process. Wrong
  abstraction — all three options are opinionated (council, code
  review, code generation) rather than a raw `call_llm` primitive.
  Data flow becomes agent → MCP → sidecar → provider, extra hop
  extra latency. Multi-language stack (Python / Node). Maintenance
  risk — solo developers on two of three.
- LOC: **~30** (compose entry + socat mount + settings.json merge)
  but **+1 container** and a dep on a third-party MCP server.
- Deps: full external service.
- Fit: **poor**. Violates "boring, small, ours".

### 5. LiteLLM / Portkey / OpenLit as a proxy sidecar

LiteLLM is a Python proxy server, 140+ providers, 2,500+ models,
unified OpenAI-compatible API. Portkey-AI gateway is a TypeScript
alternative (250+ LLMs, built-in guardrails, cache, fallbacks,
retries, load balancing). Open-source, MIT.

Deployment: run LiteLLM in compose (`ghcr.io/berriai/litellm`), point
`LLM_BASE_URL=http://litellm:4000`. The thin HTTP client (bucket 1)
now talks to the proxy; LiteLLM handles auth for every provider,
cost tracking, retries, fallback chains, rate limiting. Our code
stays the same ~230 LOC.

- Pros: off-the-shelf 140+ providers. Cost tracking with per-key
  budgets (real-time enforcement, solves the daily-cap problem).
  Automatic fallback chains (GPT-4o down → Llama 3.3). Per-request
  retries with exponential backoff. Guardrails (input/output
  filtering). We gain massive provider coverage for the cost of one
  container.
- Cons: +1 container, +Python runtime (LiteLLM) or +TS (Portkey).
  New failure domain. Config sprawl — LiteLLM config YAML is its
  own learning curve. Cold start adds startup time. Potentially
  overkill — arizuko groups are small-scale relative to what these
  gateways are built for (Portkey processes 2T tokens/day).
- LOC: **~230** on our side (same client) + a compose entry + a
  LiteLLM config YAML.
- Deps: **+1 container**.
- Fit: **decent but expensive**. Best choice if we want native
  multi-provider fallback and real-time budget enforcement from day
  one. Worst choice if we want "one more thing in the box is too
  much".

### 6. What the Claude Agent SDK allows

The SDK's subagent definition
(`ant/node_modules/@anthropic-ai/claude-agent-sdk/sdk.d.ts`) accepts
`model?: 'sonnet' | 'opus' | 'haiku' | 'inherit'`. There is **no
external-provider hook** in any SDK surface. `SubagentStart`,
`TaskCompleted`, `TeammateIdle` all assume a Claude backend. The SDK
also exposes `setModel(model?: string)` on the main session, but the
string must be a Claude model ID. There is no path for GPT or Gemini.

- Pros: nothing to build on our side.
- Cons: doesn't exist. Closed.
- LOC: N/A.
- Deps: N/A.
- Fit: **not a real option**. Listed only to rule it out.

### Recommendation

**Ship bucket 1 + bucket 2 together, defer 5, ignore 3/4/6.**

Concretely:

- Write the thin HTTP client in `llm/` (~150 LOC, zero deps).
- Register `call_llm` in `ipc/ipc.go` (~80 LOC, grants-aware).
- Default `LLM_PROVIDER=openrouter`, `LLM_BASE_URL=https://openrouter.ai/api/v1`.
- Allowlist in `.env`, starts conservative: three models.
- Log-only audit first. `llm_calls` table in v2 when we have a
  reason (cap enforcement, dashboard).
- Revisit LiteLLM sidecar (bucket 5) **only** if we need native
  fallback chains or multi-key budget enforcement — and only when
  the log data says it's worth the extra container.

Rationale:

- Matches the whisper, socat, chanlib, proxyd, and vited patterns:
  thin, opinionated, minimal deps.
- Uses 0 innovation tokens (HTTP + JSON + stdlib is boring).
- OpenRouter's ZDR-by-default (no prompt logging unless opted in)
  is the minimum bar for running against private group content.
- Agent can still call OpenAI / Gemini directly by changing
  `LLM_BASE_URL` — no code change.
- The thin client is ~230 LOC. If we need LiteLLM later, we keep
  the same client and swap only `LLM_BASE_URL=http://litellm:4000`.
  Nothing is thrown away.

## Open questions

1. **OpenRouter vs LiteLLM sidecar for v1** — ship the zero-container
   HTTP client against OpenRouter, or pay the +1-container cost up
   front for LiteLLM's fallback chains and real-time budgets? Default
   recommendation is OpenRouter; user's call on the tradeoff.

2. **Allowlist in `.env` vs allowlist in grants** — both can enforce
   `model=`. `.env` is instance-wide (operator policy), grants are
   per-group (tenant policy). Probably both: `.env` defines the
   universe, grants pick from it. But does "tier 2 sub-group inherits
   call_llm from tier 1" map cleanly to the existing grants
   inheritance? Needs a walkthrough.

3. **Streaming** — `mark3labs/mcp-go` supports streamable-HTTP and
   SSE, but our stdio-over-socat transport does not. First-token
   latency matters for long completions. Does Claude Agent SDK expose
   any streaming tool-result path at all? Probably no (tool results
   are atomic blobs in the MCP spec for stdio). Defer is the obvious
   answer; confirm.

4. **MCP request timeout clamp** — default stdio timeout is 60s. Our
   `timeout_seconds` cap of 120 would blow the MCP request window
   and return -32001 before the HTTP call completes. Clamp to 55s in
   v1, or check whether `@anthropic-ai/claude-agent-sdk` exposes
   per-server request timeout config so we can widen it?

5. **Cost tracking: real-time or post-hoc** — real-time means we hold
   a running `folder → $spent-today` map in memory and persist it on
   every call. Post-hoc means we query the provider's billing API
   once per hour and sync. Real-time is simpler and more accurate
   (OpenRouter returns `usage` and we compute from a static price
   table). Post-hoc is more correct for providers with hidden fees
   but requires per-provider integration. Real-time is probably
   right; confirm.

6. **Latency impact on the steering loop** — we just shipped the
   `PostToolUse` mid-loop hook (v0.25.1). A 30s `call_llm` holds the
   turn for 30s. The hook drains IPC between tool calls, so steering
   still happens — but the user may perceive a 30s stall after the
   agent announces "asking GPT-4o for a second opinion". Is that
   acceptable, or do we want a pattern where `call_llm` returns
   immediately with a handle and the result arrives via a follow-up
   message? (Async/promise pattern. Adds complexity. Probably not
   v1.)

7. **Fallback chains** — if GPT-4o is down, do we auto-retry with
   Claude (via the thin HTTP client hitting Anthropic's API) or
   Llama 3.3, or return the error and let the agent decide? Bucket 5
   (LiteLLM) handles this natively. Bucket 1 would need us to code
   it. Default: **return the error, agent retries with a different
   `model` argument**. Explicit > implicit.

8. **Retry policy** — 429 and 5xx retries. Where do they live? Inside
   the thin HTTP client (with a bounded backoff that respects
   `timeout_seconds`)? Or bounced up to the agent to redecide?
   Default: **inside the client, bounded**. Standard Go HTTP client
   pattern.

9. **First allowlist** — concrete shortlist for the `.env` default:
   - `openai/gpt-4o` — second-opinion default
   - `openai/gpt-4o-mini` — cheap bulk
   - `google/gemini-2.5-pro` — long context
   - `deepseek/deepseek-v3.2` — cheap, strong at code
   - `meta-llama/llama-3.3-70b-instruct` — OSS, Groq-fast
   - `x-ai/grok-4` — alternative POV (optional)

   Accept this list or prune?

10. **Local / self-hosted models** — should `LLM_BASE_URL` accept
    `http://ollama:11434/v1` or `http://llama-cpp:8080/v1` for air-gapped
    or uncensored deployments? Trivial to support (same
    OpenAI-compatible contract). Only question is whether the allowlist
    semantics handle `local/*` prefixes. Probably yes.

11. **Privacy: prompts leave the cluster** — every provider sees the
    prompt text. OpenRouter is ZDR by default; most others are not.
    Sending private group content (WhatsApp, Telegram DMs) to GPT-4o
    may violate user expectations, even if the operator allowed it.
    Should `call_llm` require an explicit per-group opt-in (new
    `.env` key `LLM_GROUPS_ALLOWED=` or a column on `groups`)?
    Should the grant name itself be the opt-in (only folders with
    the `call_llm` grant in the seed can use it)? Probably the
    latter — grants are already the opt-in mechanism for every
    other tool.

12. **Reproducibility / seed** — some providers (OpenAI, Groq) support
    a `seed` parameter for deterministic sampling. Expose as an
    optional param? Useful for cache hit-rate and regression tests,
    but most providers don't support it and silently ignore. Skip
    in v1, add on request.

13. **Tool-use recursion on the other model** — if GPT-4o decides to
    emit a function-call / tool_use, we drop it and return the raw
    text response (or an error). Confirm — in v1 the other model is
    a pure text function.

14. **Daily cap enforcement point** — pre-flight (query the cap,
    reject the call) or post-flight (let the call land, blacklist
    for the rest of the day)? Pre-flight is racy without a
    transaction, post-flight can overrun the cap by one call. Given
    rates are low (~dozens/day per group), post-flight with a
    `SUM(cost_usd)` check before each call is fine and simpler.

15. **What does `call_llm` look like in the help text / `/hello`
    skill output** — should the agent know about it the way it knows
    about `send_file`, or should it be a "rarely surfaced" tool
    documented only in a `call_llm` skill? Default: documented in a
    new `ant/skills/call_llm/SKILL.md` with examples, not in the
    main `/hello` or `/dispatch` output.

## Related specs

- [3-agent-teams.md](3-agent-teams.md) — why Claude Agent Teams were
  disabled; `call_llm` is the replacement for the "ask someone else"
  use case.
- [A-ipc-mcp-proxy.md](A-ipc-mcp-proxy.md) — the MCP stdio-over-unix-
  socket transport this tool plugs into.
- [G-agent-backends.md](G-agent-backends.md) — why we rejected
  swapping the whole agent backend; `call_llm` gives us multi-model
  capability without changing the harness.
- [C-message-mcp.md](C-message-mcp.md) — similar shape (a new
  gateway-side MCP tool gated by grants).
- [E-plugins.md](E-plugins.md) — long-term alternative path where
  the agent could propose a new MCP server for a specific model
  instead of going through `call_llm`. Out of scope for v1.

## References

- [OpenRouter API reference](https://openrouter.ai/docs/api/reference/overview)
- [OpenRouter ZDR policy](https://openrouter.ai/docs/guides/features/zdr)
- [OpenRouter models](https://openrouter.ai/models)
- [LiteLLM (BerriAI/litellm)](https://github.com/BerriAI/litellm) — 140+ providers
- [Portkey-AI gateway](https://github.com/Portkey-AI/gateway) — 250+ LLMs, TS
- [sashabaranov/go-openai](https://github.com/sashabaranov/go-openai) v1.41.2
- [openai/openai-go](https://github.com/openai/openai-go) — official, July 2024
- [anthropics/anthropic-sdk-go](https://github.com/anthropics/anthropic-sdk-go)
- [mark3labs/mcp-go](https://github.com/mark3labs/mcp-go) — stdio/SSE/streamable
- [sammcj/mcp-llm](https://github.com/sammcj/mcp-llm) — MCP tool for LLM access
- [dshills/second-opinion](https://github.com/dshills/second-opinion) — Go MCP, multi-provider code review
- [NabilAttia123/llm-council-mcp](https://github.com/NabilAttia123/llm-council-mcp) — three-stage council
- [Groq, DeepInfra, Together, Fireworks comparison](https://infrabase.ai/blog/ai-inference-api-providers-compared)
- [MCP -32001 timeout reference](https://mcpcat.io/guides/fixing-mcp-error-32001-request-timeout/)
