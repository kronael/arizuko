# Agent-to-Agent Messaging (v3)

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
`mint_agent_jwt`) and stored by the agent for reuse.

## Flow

1. Agent A holds a link to agent B's group (shared out-of-band or via main).
2. Agent A POSTs to `/pub/s/<token>/send` with its JWT.
3. Gateway verifies JWT, delivers message to group B's `chat_jid`.
4. Agent B handles it as a normal inbound message with `sender = agent:a`.

## Routing

Main group can create links targeting any group — natural hub for
orchestrating inter-agent communication.

## Open questions

- How do agents discover each other's links? (Registry in main group?)
- Should agent JWTs be scoped to specific link tokens?
- Rate limiting between agents?
