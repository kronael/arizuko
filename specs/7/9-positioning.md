# Spec 7/9 — arizuko Positioning in the Agent Orchestration Landscape

Status: research / informational

## The Market

Five camps:

| Platform                             | Model                           | Gap                                                        |
| ------------------------------------ | ------------------------------- | ---------------------------------------------------------- |
| LangGraph / AutoGen                  | Code-first, single-context DAGs | No persistent identity per agent; one flat namespace       |
| CrewAI / Swarm                       | Ephemeral role-based task crews | No cross-session memory; cloud-centric                     |
| Dify / Flowise                       | Low-code visual workflows       | GUI-drag paradigm; agents are nodes, not folders           |
| n8n / Zapier AI                      | Automation + AI bolt-ons        | Agents are steps, not autonomous entities                  |
| Vertex AI / Bedrock / Copilot Studio | Managed enterprise cloud        | Data leaves infrastructure; per-seat pricing; no fork path |

None of them treat agents as **first-class persistent identities organized hierarchically** that coordinate through a standard protocol and can manage other agents.

## What arizuko Is

The positioning that resonates:

> **Self-hosted agent organization through code.** A folder is an agent. Folders compose into hierarchies. Agents coordinate via MCP. No GUI, no managed control plane.

Three properties no competitor has together:

1. **Folder = agent.** Every team gets its own context boundary, channel presence, memory, and skill set. Git-manageable. Diff-reviewable. The org chart is the folder tree.

2. **MCP as the coordination bus.** Every tool call — send message, delegate to sibling, schedule task — goes through one auditable socket. Not just tool-calling: inter-agent routing, ACL enforcement, and audit trail all go through the same primitive.

3. **Agents that manage agents.** Root agent can run `register_group`, set routes, and schedule tasks for child agents — via the same MCP tools a user would invoke. Code manages code. Agents manage agents. This is the self-reflecting property.

## The Positioning Gap to Own

**"Infrastructure-as-Code for agents."** The DevOps parallel is exact:

- Kubernetes put infrastructure in YAML; teams gained versioning, review, GitOps.
- arizuko puts agents in folders; teams gain versioning, review, hierarchy.

The market is splitting into "GUI for business users" (Dify, n8n, Copilot Studio) and "code primitives for engineers" (LangGraph, AutoGen). arizuko occupies a third position: **code-first + organization-aware** — targeting teams that want agents to be as manageable as their codebase.

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

- "Agents organized like code — folder hierarchy, version control, hierarchy."
- "MCP-first: every tool call, delegation, and task goes through one auditable socket."
- "Agents that manage agents. Code that manages code. The self-reflecting stack."
- "$5 VPS. One tar of the data dir is the full backup. No managed control plane — that's the feature."
- "11 channel adapters. Each speaks the platform's native API, not a webhook relay."

## What to Avoid in Positioning

- "AI assistant platform" — too generic; sounds like ChatGPT wrappers.
- Comparison tables with cloud vendors (we lose on managed ops, win on everything else; neutral framing is better).
- "RAG platform" — not the primary story; Dify owns that frame.
- Benchmarks — too early; correctness over performance.

## References

- LangGraph: [python.langchain.com/docs/langgraph](https://python.langchain.com/docs/langgraph)
- CrewAI vs LangGraph: datacamp.com/tutorial/crewai-vs-langgraph-vs-autogen
- MCP anniversary post: blog.modelcontextprotocol.io/posts/2025-11-25-first-mcp-anniversary
- Workflow-first vs code-first: techcommunity.microsoft.com/blog/azurearchitectureblog/building-ai-agents-workflow-first-vs-code-first-vs-hybrid/4466788
