---
status: shipped
relates-to: [57-group-handler, ../8/40-groups-without-agents]
---

# specs/7/58 — a group post is a message

Agents message each other with `send`. ACL rows decide which group may post
to which, and routing delivers the message.

## A group folder is a send destination

`send(chatJid="<folder>", text="...")` checks `mcp:send` on the target
folder. A platform-qualified `chatJid` follows the platform delivery path.
The group post returns `{"ok": true, "id": "<message id>"}`.

Both `send` and routd's routing hand-off use `store.PostGroup` to write an
inbound message on the target folder JID. `ForwardedFrom` records the calling
folder for `send` and the originating chat for a routing hand-off.

```text
agent send → target grant check ─┐
                               ├→ store.PostGroup → target folder → turn
routd routing hand-off ─────────┘
```

Code: `ipc/ipc.go`, `routd/steer.go`, `store/messages.go`.

## A post carries its distance from a person

Every message row carries `hops`: the number of agent turns between it and
the last person or external input. A chat message, a webhook, a channel
adapter row, a scheduled task prompt, an operator `arizuko send` and a
routing hand-off from a person's command are 0. An absent attribute reads
as 0.

A turn's distance is the largest `hops` among its saved inputs. A message
that an agent turn writes to start another turn — `send` to a group and
`inject_message` — gets the turn's distance + 1. A message that reaches
`store.MaxHops` (2) is refused with a tool error and is not written. So a
person can ask A, A can send to B, and B can answer on a platform chat, but
B cannot start a turn in A, in C or in itself.

```text
person ─0→ A ─1→ B ─2→ (refused: A, C or B)
```

routd enforces it in one place, `putAgentMessage` in `routd/mcp.go`, the
write behind both tools. It reads the distance from the turn's saved input
rows, so a retried or restarted turn counts the same.

## Acceptance

1. `send` to a granted group starts that group's turn.
2. `send` to an ungranted group fails with the grant error.
3. The MCP tool list exposes `send` for both group and platform destinations.
4. A person → A → B post is written at hop 1; B → A and B → C are refused;
   a scheduled task's turn may post once.

Evidence: `ipc/send_platformless_test.go` `TestSendGroupAndPlatform`,
`ipc/ipc_test.go` `TestServeMCP_SubmitTurnHiddenFromToolsList` and
`routd/agent_hops_test.go` `TestAgentHopsBound`.
