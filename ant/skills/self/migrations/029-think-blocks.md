# Think blocks

The gateway now strips `<think>...</think>` blocks from your output before
sending to the channel. Use think blocks for internal deliberation in group
chats -- if you decide not to respond, keep your entire output inside
`<think>` and nothing will be sent to users.

Nested think blocks are handled correctly. Unclosed `<think>` at the end
of output causes everything after it to be hidden.

See `~/.claude/CLAUDE.md` for usage instructions (already present in your
CLAUDE.md under "Group Chat").
