# Agent Orchestration & Workflows — speculative

## Two distinct concepts

### 1. Agent orchestration (inter-agent messaging)

Independent specialized agents (groups) that can message each other via
slink. An orchestrator group routes tasks to workers; workers reply back.
Useful for: async background jobs, agents with persistent memory and
specialized tools that live across many requests, notification/delegation
patterns.

The key property: **agents are long-lived**. Each group has its own session,
memory, persona. Messages are the coordination primitive.

**Depends on**: `specs/v3/agent-messaging.md` (JWT auth + `/pub/s/<token>/send`)
— not yet implemented.

### 2. Workflows (sequential subagent pipeline)

A single group orchestrates a multi-step job by spawning subagents (Agent
tool) within one container. Each subagent plays a role (research, verify,
rewrite) but shares the same context window, same mounts, same session.
Useful for: rich context-dependent pipelines where each step builds on the
last, within a single response.

The key property: **context is naturally shared**. No serialization, no
payload size limits. But no role isolation — workers can't have independent
personas or persistent memory.

---

## Which to use

|                 | Orchestration                         | Workflow                       |
| --------------- | ------------------------------------- | ------------------------------ |
| Context sharing | explicit (payload / shared mount)     | implicit (same context window) |
| Role isolation  | strong (own session, memory, tools)   | none                           |
| Latency         | compounds per hop (container startup) | single container               |
| Persistence     | workers remember across runs          | ephemeral                      |
| Complexity      | high (slink, JWT, session handoff)    | low (Agent tool today)         |

**Today**: workflows (subagents) are viable now. Orchestration requires
agent-messaging to be implemented first.

---

## Status

This spec is intentionally incomplete. The right design will only become
clear through implementing real use cases (Atlas and similar). The concepts
are named here to avoid conflation; the implementations are open.

---

## Roles

**Orchestrator group** — receives the user message. Decides which workers to
invoke, in what order, with what inputs. Assembles the final reply or delegates
final delivery to the last worker in the chain.

**Worker group** — specialized agent with a focused skill (web research, fact
check, persona rewriter, etc.). Receives a task via slink, produces a result,
POSTs the continuation forward.

**Terminal hop** — the last step in the chain. Instead of forwarding via slink,
it calls `send_message` IPC to deliver the reply back to the original
`chat_jid`. It knows where to reply because the continuation payload carries
`reply_to`.

---

## Continuation payload

The slink POST body for inter-agent hops:

```json
{
  "task": "rewrite in REDACTED persona",
  "context": "...",
  "pipeline": {
    "reply_to": {
      "chat_jid": "tg:123456789",
      "sender": "user:112233"
    },
    "remaining_hops": ["web:persona"]
  }
}
```

Fields:

| Field                        | Description                                                                               |
| ---------------------------- | ----------------------------------------------------------------------------------------- |
| `task`                       | What this worker should do                                                                |
| `context`                    | Input data (prior worker output, original query, etc.)                                    |
| `pipeline.reply_to.chat_jid` | Originating chat — where the final reply goes                                             |
| `pipeline.reply_to.sender`   | Original sender identity (for attribution)                                                |
| `pipeline.remaining_hops`    | Ordered list of slink tokens or group identifiers still to execute; pop front on each hop |

When `remaining_hops` is empty after a worker completes, that worker is the
terminal hop and sends the reply directly via `send_message` IPC.

---

## Workflow skill

The orchestrator group carries a `SKILL.md` (or a dedicated skill file) that
describes the pipeline topology. The agent reads this skill at invocation time
to decide routing. The gateway is unaware of the topology.

Example `workflow-research.md` on the orchestrator:

```markdown
# Research Pipeline

When the user asks a question requiring research:

1. POST to researcher group (web:research) with the raw question
2. Researcher returns findings in context
3. POST to verifier group (web:verify) with findings
4. Verifier returns confidence-annotated result
5. POST to persona group (web:persona) with verified result
   — this is the terminal hop; persona group replies to reply_to.chat_jid

Slink tokens (operator-provisioned, stored in secrets skill):

- researcher: <token-r>
- verifier: <token-v>
- persona: <token-p>
```

The orchestrator is responsible for constructing the initial continuation
payload with the full `remaining_hops` list before POSTing to the first worker.

---

## Example flow: research → verify → persona reply

```
User (Telegram)
  │  "What caused the 2024 YEN crash?"
  ▼
orchestrator group (web:main)
  │  Reads workflow skill, constructs continuation:
  │  { task: "research YEN crash 2024",
  │    pipeline: { reply_to: { chat_jid: "tg:123", sender: "user:99" },
  │                remaining_hops: ["<verify-token>", "<persona-token>"] } }
  │  POST /pub/s/<research-token>/send  (JWT auth)
  ▼
researcher group (web:research)
  │  Runs web search, assembles findings
  │  Pops front hop → next = "<verify-token>"
  │  { task: "verify these claims", context: "...findings...",
  │    pipeline: { reply_to: ..., remaining_hops: ["<persona-token>"] } }
  │  POST /pub/s/<verify-token>/send
  ▼
verifier group (web:verify)
  │  Fact-checks, annotates confidence
  │  Pops front hop → next = "<persona-token>"
  │  { task: "rewrite as REDACTED", context: "...verified findings...",
  │    pipeline: { reply_to: ..., remaining_hops: [] } }
  │  POST /pub/s/<persona-token>/send
  ▼
persona group (web:persona)
  │  remaining_hops is empty → terminal hop
  │  Rewrites in persona voice
  │  IPC send_message → chat_jid: "tg:123"
  ▼
User (Telegram)
  "The YEN crash in 2024 was driven by..."
```

Each worker runs as a normal container invocation. No shared state beyond what
is passed in the continuation payload.

---

## Slink token distribution

Worker slink tokens must be known to the orchestrator before it can route.
Two options:

1. **Secrets skill** — operator adds tokens to orchestrator's skill files at
   setup time. Simple, static.
2. **Registry in main** — groups publish their tokens to a shared registry
   (future work). Dynamic, self-describing.

Option 1 is the correct starting point. Static configuration in the skill
files is readable, auditable, and requires no infrastructure.

---

## Gateway role

The gateway does nothing special for pipeline messages. A slink POST with a
`pipeline` field in the body is delivered to the receiving group as a normal
inbound message. The agent interprets the payload. The gateway's existing
`send_message` IPC handles terminal-hop delivery.

No new gateway code is required beyond what `specs/v3/agent-messaging.md`
defines (JWT mint + `/pub/s/<token>/send` route).

---

## Constraints

- Each hop is a full container invocation — startup latency compounds across
  the chain. Keep pipelines short (2–4 hops).
- Workers must not trust `pipeline.reply_to` as a capability — they can only
  deliver to `chat_jid` values the gateway recognises. The gateway's existing
  `send_message` IPC handler validates the target.
- No built-in retry. If a worker fails mid-chain the user gets no reply. Error
  handling is the orchestrator's responsibility (timeout + fallback in skill).
- Slink tokens in skill files are secrets. Operators must not expose them in
  public-facing content.

---

## Fundamental design fork (open)

Two approaches exist and the right answer is not yet clear:

### A) Inter-agent message links (current spec)

Separate groups, each with own session/memory/persona. Messages pass between
them via slink. Each hop is an independent container with its own context
window — workers have no shared state beyond what's in the payload.

**Problem**: context blows up fast. Researcher returns 5000 tokens of
findings. Verifier returns annotated version. Persona rewrites. Each hop
must carry the full accumulated context in the POST body or it operates
blind. A 4-hop pipeline with rich context can easily exceed what fits
comfortably in a slink payload — and agents at each hop have no memory of
prior hops beyond what was explicitly passed.

### B) Sequential subagents within one group

Orchestrator spawns subagents (Agent tool) inside its container — each
subagent plays a specialized role but runs in the same session, shares the
same context window, and has access to the same mounted files. No slink
needed. No inter-container latency. Context is naturally shared.

**Problem**: subagents share the same container, same mounts, same persona.
No true isolation between roles. Can't have a "researcher" with its own
session memory and tools and a separate "verifier" with different skills.
Also limited by the single context window.

### What would make inter-agent links viable

- **Session continuity via slink**: the continuation payload carries not
  just `context` (text) but a `session_id` the worker can resume from, or
  a pointer to a shared file in a common mount. Without this, each worker
  starts cold and must re-establish context from the payload alone.
- **Shared file access**: workers need a common scratch space (e.g.
  `/workspace/pipeline/<run-id>/`) mounted read-write into each container
  in the chain. Workers write artifacts; successors read them. This is the
  equivalent of a shared working directory.
- **Session handoff**: if researcher writes a claude session, verifier could
  resume it (same `sessionId`) — inheriting the full conversation history.
  This would make context continuity near-free. Requires the gateway to
  mount the session dir into each worker container.

### Recommendation (tentative)

Start with **option B** (subagents within one group) for pipelines where
context must flow richly between steps. Use **option A** (inter-agent
links) only for pipelines where workers are genuinely independent — e.g.
fire-and-forget enrichment, async background research that delivers back
later, or worker agents with persistent session memory that must survive
across many pipeline runs.

The two models are not mutually exclusive. A single pipeline could use
subagents internally for the research step, then hand off the result to a
separate persona group via slink for final delivery.

---

## Open questions

- **Session handoff**: can a slink POST carry a `session_id` the receiving
  worker resumes? Gateway would need to mount the correct session dir.
- **Shared scratch mount**: how is a per-pipeline-run directory provisioned
  and cleaned up? Who owns it?
- **Context size budget**: what is the practical payload limit for slink
  POST bodies? Does compression help or is a file reference better?
- **Worker discovery**: static tokens in skill files work for fixed
  pipelines; dynamic pipelines need a registry.
- **Error propagation**: if a mid-chain worker fails, how does the
  orchestrator learn about it? (Currently: it doesn't.)

---

## Related

- `specs/v1/slink.md` — slink endpoint and token model
- `specs/v3/agent-messaging.md` — JWT auth for agent-to-agent POSTs (required)
- `specs/v2/agent-teams-ipc.md` — why Claude agent teams don't work in kanipi
