---
status: research
---

# Positioning — the competitive landscape

Competitive research. The _message_ is not decided here — the canonical
framing (primitives → components → daemons → products, identity as the
coordinate system) is signed off in
[5/A-primitives-framing](../5/A-primitives-framing.md). This file holds
only what 5/A does not: who else is in the market, and what not to say.

## The market

| Platform                             | Model                           | Gap                                                        |
| ------------------------------------ | ------------------------------- | ---------------------------------------------------------- |
| LangGraph / AutoGen                  | Code-first, single-context DAGs | No persistent identity per agent; one flat namespace       |
| CrewAI / Swarm                       | Ephemeral role-based task crews | No cross-session memory; cloud-centric                     |
| Dify / Flowise                       | Low-code visual workflows       | GUI-drag paradigm; agents are nodes, not folders           |
| n8n / Zapier AI                      | Automation + AI bolt-ons        | Agents are steps, not autonomous entities                  |
| Vertex AI / Bedrock / Copilot Studio | Managed enterprise cloud        | Data leaves infrastructure; per-seat pricing; no fork path |

The market is splitting two ways — "GUI for business users" (Dify, n8n,
Copilot Studio) and "code primitives for engineers" (LangGraph, AutoGen).
Neither ships a **focused, ownable agent**: a persona + curated skills +
routes the buyer authors and hosts. That third position — code-first _and_
organization-aware — is the gap to occupy.

## Why focused beats general

The strongest differentiator, and the one that survives contact with a
buyer: **general agents fail most of the time.** A blob meant to do
everything has no persona to hold it, no curated skill set to bound it, and
no routes to aim it — it drifts and gives generic answers to specific jobs.
Constraining an agent to the skills its one job needs (gating the rest via
ACL) removes exactly the failure modes general agents have.

The product catalog ([5/21-products](../5/21-products.md)) is what makes
this concrete: IaC without modules is a blank file; arizuko ships modules.
The pitch is not "build your agent from primitives" — that is the engine —
it is "take a focused agent that already does the job, and make it yours."

## What to avoid saying

- **"AI assistant platform"** — too generic, reads as a ChatGPT wrapper.
- **"General agent" / "do-anything agent"** — the anti-pitch.
- **Comparison tables with cloud vendors** — we lose on managed ops and win
  on everything else; neutral framing lands better.
- **"RAG platform"** — Dify owns that frame. arizuko is the action layer
  over retrieval ([8-company-brain](8-company-brain.md)).
- **Benchmarks** — too early; correctness over performance.

## Honest cost to name when asked

N daemons means N services, N upgrade paths, N log streams. A systemd unit
template plus `docker compose` mitigates it; nothing eliminates it. (The
older version of this spec claimed a shared-schema migration chokepoint
under `gated` — that is stale: `gated` was deleted at v0.50.0 and each
split daemon owns and migrates its own DB.)

## Feature gaps that would strengthen the pitch

Not a plan — a list of what a skeptical buyer asks for and we cannot yet
show. Each needs its own spec before it is work.

1. `arizuko diff <instance>` — what changed in agent config since last
   deploy. Makes "agents as code" tangible to an SRE reviewing a change.
2. Grant visualization in dashd — an ACL explorer; today it is grep-only.
   The compliance/audit story needs it.
3. `inspect_agents` — let an agent discover siblings and children so
   agent-to-agent coordination stops hardcoding paths.
4. Per-folder git integration — auto-commit agent-written files at session
   end, answering "what did the agent change".
5. MCP federation — expose the MCP surface beyond the unix socket so
   external IDE/desktop clients route tools through arizuko.

## References

- CrewAI vs LangGraph: datacamp.com/tutorial/crewai-vs-langgraph-vs-autogen
- Workflow-first vs code-first: techcommunity.microsoft.com/blog/azurearchitectureblog/building-ai-agents-workflow-first-vs-code-first-vs-hybrid/4466788
