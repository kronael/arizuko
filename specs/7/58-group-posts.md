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

## Acceptance

1. `send` to a granted group starts that group's turn.
2. `send` to an ungranted group fails with the grant error.
3. The MCP tool list exposes `send` for both group and platform destinations.

Evidence: `ipc/send_platformless_test.go` `TestSendGroupAndPlatform` and
`ipc/ipc_test.go` `TestServeMCP_SubmitTurnHiddenFromToolsList`.
