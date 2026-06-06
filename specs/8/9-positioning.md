# Spec 8/9 — arizuko Positioning in the Agent Orchestration Landscape

Status: research / informational

## The edge: focused products, not general agents

The strongest differentiator first. **General agents fail most of the
time.** A blob that's meant to do everything has no persona to hold it,
no curated skill set to bound it, no routes to aim it — it drifts,
hallucinates scope, and gives generic answers to specific jobs. arizuko's
wedge is the opposite move: **ship a FOCUSED product** — a pre-seeded
agent (persona + a tight, curated skill set + routes) aimed at exactly
one job: support, company-brain, content authoring, PM, trip planning.

A focused agent assembled from arizuko's primitives beats renting a
general blob, on two axes:

- **You own and shape it, you don't rent it.** The persona, the skills,
  the routes, the knowledge are files in a folder you control — not a
  prompt buried in someone's SaaS. Own the stack: the agent's identity
  and capability surface are yours to read, diff, fork, and harden, on
  your host. A rented general agent is a black box you tune through a
  text box; a focused arizuko product is an asset you author.
- **A tight skill set is a feature, not a limitation.** Constraining the
  agent to the skills its one job needs (and gating the rest via ACL)
  removes the failure modes general agents have — wrong tool, wrong
  scope, confident nonsense outside its lane. The product catalog
  ([P-product-templates](P-product-templates.md), [R-products](R-products.md))
  is exactly this: curated `ant/examples/<name>/` folders — persona +
  skill whitelist + seed `facts/` — installed with one command
  (`arizuko create <inst> --product <name>`). Company-brain
  ([8-company-brain](8-company-brain.md)) is the same move pointed at
  knowledge work: arizuko is the _action_ layer on top of retrieval,
  not another general chatbot over your docs.

Everything below — folder=agent, the small primitive set, MCP+REST,
self-hosting — is what makes focused products cheap to assemble and
yours to keep. That is the positioning lead.

## The Market

Five camps:

| Platform                             | Model                           | Gap                                                        |
| ------------------------------------ | ------------------------------- | ---------------------------------------------------------- |
| LangGraph / AutoGen                  | Code-first, single-context DAGs | No persistent identity per agent; one flat namespace       |
| CrewAI / Swarm                       | Ephemeral role-based task crews | No cross-session memory; cloud-centric                     |
| Dify / Flowise                       | Low-code visual workflows       | GUI-drag paradigm; agents are nodes, not folders           |
| n8n / Zapier AI                      | Automation + AI bolt-ons        | Agents are steps, not autonomous entities                  |
| Vertex AI / Bedrock / Copilot Studio | Managed enterprise cloud        | Data leaves infrastructure; per-seat pricing; no fork path |

None of them ship a **focused, ownable agent** — a persona + curated
skills + routes you author and host — assembled from **first-class
persistent folder-agents** that coordinate over a uniform MCP+REST
surface. They sell either a general blob (managed cloud) or low-level
wiring you assemble yourself with no identity, persona, or ownership
model (code/workflow frameworks).

## What arizuko Is

The positioning that resonates:

> **Self-hosted, focused agents built as code.** A folder is an agent —
> persona, skills, memory, ACL. Something happens, an agent reacts.
> Folders compose into hierarchies. Every action is one MCP tool call,
> reachable over REST too. No GUI, no managed control plane, no rented
> blob.

Four properties no competitor has together:

1. **Folder = agent.** Every team, customer, or job gets its own context
   boundary, channel presence, persona, memory, and curated skill set.
   Git-manageable. Diff-reviewable. The org chart is the folder tree
   (`corp/eng/sre`, arbitrary depth).

2. **Event → reaction over a small orthogonal primitive set.** The whole
   system reduces to a handful of primitives — Event, Agent, Routing,
   Authorization, Turn, State, with identity as the cross-cutting
   namespace — each owning one concern, composed in a fixed pipeline,
   no special cases. The apparent feature sprawl (channels, topics,
   tasks, webhooks, secrets, egress, delegation, observe) is all
   recomposition of those primitives, never new machinery
   ([specs/5/A](../5/A-primitives-framing.md)). A focused product is one
   such recomposition: a folder with the right persona, skills, and
   routes.

3. **One uniform MCP+REST surface.** Every resource is reachable through
   one hand-rolled handler with two faces — MCP for in-container agents,
   REST for humans and external tools — over one auth gate and one
   audited mutation path ([specs/5/5](../5/5-uniform-mcp-rest.md)).
   Agents that manage agents (register a child, set routes, schedule a
   task) call the same handlers an operator would. Code manages code.

4. **Own the stack.** One Linux host, Docker, SQLite WAL. A tar of the
   data dir is the full backup. No per-seat pricing, no data leaving
   your infrastructure, no control plane to depend on — and the focused
   agent itself (persona, skills, routes, knowledge) is yours to read,
   fork, and harden, not a black box you rent.

## The Positioning Gap to Own

**"Infrastructure-as-Code for agents."** The DevOps parallel is exact:

- Kubernetes put infrastructure in YAML; teams gained versioning, review, GitOps.
- arizuko puts agents in folders; teams gain versioning, review, hierarchy.

The market is splitting into "GUI for business users" (Dify, n8n, Copilot Studio) and "code primitives for engineers" (LangGraph, AutoGen). arizuko occupies a third position: **code-first + organization-aware** — targeting teams that want agents to be as manageable as their codebase.

The product catalog is what makes this concrete for a buyer. IaC without
modules is a blank file; arizuko ships modules — focused products
([P-product-templates](P-product-templates.md), [R-products](R-products.md))
the operator installs and then owns and edits as code. The pitch is not
"build your agent from primitives" (that's the engine); it's "take a
focused agent that already does the job, and make it yours."

## Features to Add / Strengthen

### Near-term (fills obvious gaps vs competition)

1. **`arizuko diff <instance>` / `arizuko status <instance>`**
   Show what changed in agent config vs last deploy. Makes the "agents as code" story tangible. Audience: SREs doing code review of agent changes.

2. **Grant visualization in dashd**
   Interactive ACL explorer: which agents can do what, where. Currently grep-only. Needed for the compliance/audit story. Slogan hook: "audit-native agent orchestration."

3. **`inspect_agents` MCP tool**
   Let an agent discover its siblings and children (folder names, tier, last-active). Enables true agent-to-agent coordination without hardcoding paths. Required for the "agents manage agents" story.

4. **Webhook-to-agent routing UI in dashd**
   Visual routing table: "POST /hook/<token> → which folder." Currently config-only. Needed for operators who aren't reading ROUTING.md.

### Medium-term (differentiators)

5. **Per-folder git integration**
   `groups/<folder>/workspace/` is already a docker volume mount. Add `git init` + auto-commit of agent-written files at session end. Makes every agent's knowledge base version-controlled. Addresses the "what did the agent change" audit question.

6. **Agent-authored skills**
   An agent writes `~/.claude/skills/custom-name/SKILL.md` → it shows up in the next session. Currently possible but undocumented. Formalizing this closes the "agents that can grow their own capabilities" loop.

7. **Spawn-on-demand child groups**
   An agent calls `register_group` to create a per-customer or per-issue child group. Root agent commissions it; customer talks to their own agent. Demo: `corp/support` auto-creates `corp/support/customer-<id>` on first contact.

8. **MCP federation**
   Expose arizuko's MCP socket as a reachable endpoint (not just unix, optionally TCP+TLS). Lets external Claude Desktop / IDE instances connect their tools through a controlled arizuko gateway. Positions arizuko as the MCP hub for a team.

## Messaging Anchors (for landing, docs, pitch)

- "Focused agents beat general ones. Ship an agent built for one job — own it, don't rent it."
- "A folder is an agent: persona, curated skills, memory, ACL. Built as code, hosted on your box."
- "Event → reaction over a handful of primitives. No special cases, no daemon zoo to learn."
- "One surface: every action is an MCP tool call, reachable over REST too. Agents and operators drive the same handlers."
- "$5 VPS. One tar of the data dir is the full backup. No managed control plane — that's the feature."
- "10 channel adapters. Each speaks the platform's native API, not a webhook relay."

## What to Avoid in Positioning

- "AI assistant platform" — too generic; sounds like ChatGPT wrappers. The story is focused agents, not a general assistant.
- "General agent" / "do-anything agent" — the anti-pitch. General agents fail most of the time; lead with the focused, ownable agent instead.
- Comparison tables with cloud vendors (we lose on managed ops, win on everything else; neutral framing is better).
- "RAG platform" — not the primary story; Dify owns that frame. arizuko is the action layer over retrieval ([8-company-brain](8-company-brain.md)).
- Benchmarks — too early; correctness over performance.

## References

- LangGraph: [python.langchain.com/docs/langgraph](https://python.langchain.com/docs/langgraph)
- CrewAI vs LangGraph: datacamp.com/tutorial/crewai-vs-langgraph-vs-autogen
- MCP anniversary post: blog.modelcontextprotocol.io/posts/2025-11-25-first-mcp-anniversary
- Workflow-first vs code-first: techcommunity.microsoft.com/blog/azurearchitectureblog/building-ai-agents-workflow-first-vs-code-first-vs-hybrid/4466788
