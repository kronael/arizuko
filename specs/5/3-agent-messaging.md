---
status: draft
---

# Agent-to-Agent Messaging (v3)

## status: planned

## Overview

REDACTED links as universal addressable inboxes — not just for browser users
but for other agents and external services. An agent that holds a link to
another group can POST to it directly, enabling inter-agent communication
through the same endpoint.

## Concept

A REDACTED link (`/pub/s/<token>`) is a group's public inbox. Any sender —
browser, agent, external service — uses the same POST endpoint. The
receiving group's agent handles it as a normal inbound message regardless
of sender type.

## Auth

Sending agent identifies itself via JWT in `Authorization: Bearer <jwt>`.
The JWT `sub` encodes the sender identity (e.g. `agent:<group_folder>`).
The gateway verifies the JWT and sets `sender`/`sender_name` accordingly.

Agent JWTs are minted by the gateway on request (new IPC task
`mint_agent_jwt` — not yet implemented) and stored by the agent for reuse.

## Flow

1. Agent A holds a link to agent B's group (shared out-of-band or via main).
2. Agent A POSTs to `/pub/s/<token>/send` with its JWT.
3. Gateway verifies JWT, delivers message to group B's `chat_jid`.
4. Agent B handles it as a normal inbound message with `sender = agent:a`.

## Routing

Main group can create links targeting any group — natural hub for
orchestrating inter-agent communication.

## Decided (previously open)

- **Agent discovery**: via MCP `tools/list` on shared unix
  sockets. Agents connected to the same `ipc` instance
  discover available groups through the `refresh_groups` tool.
  Main group acts as natural registry — it sees all groups
  and can share link tokens via delegation.

- **JWT scoping**: per-link-token. Each agent JWT encodes
  the specific link token it was minted for in the `aud`
  claim. An agent with a JWT for link A cannot use it to
  POST to link B. Minted via `mint_agent_jwt` MCP tool
  (not yet implemented).

- **Rate limiting**: standard per-sender limits. Same rate
  limits that apply to human senders apply to agent senders.
  No special agent-to-agent rate policy. If an agent floods,
  it gets throttled like any other sender.
