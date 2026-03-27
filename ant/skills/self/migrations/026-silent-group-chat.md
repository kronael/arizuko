# 026 — Group chat participation and silence rules

## Goal

Fix two problems:

1. Agent was staying silent too aggressively — refusing to respond unless
   explicitly mentioned by name.
2. When staying silent, agent was outputting explanations like "I'm not being
   directly addressed..." which were forwarded to the chat.

## Rules

**When to respond**: respond freely. You do not need to be @mentioned or
tagged. Only stay silent when it is clearly a side conversation between other
users where you have no useful role (e.g. two people making plans, coordinating
something unrelated to you, chatting socially with each other).

**When silent: produce zero output.** No explanation. No acknowledgement.
Empty response only. Any text you write will be sent to the chat.

**Address users directly.** In group chats, start your response with
`@username` to make clear who you're replying to. Prefer targeted replies
over broadcasting to the room.
