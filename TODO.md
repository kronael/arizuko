# TODO

## Channel adapters

Port from kanipi TS, strip to minimal standalone adapters.
Each is a separate process speaking the channel protocol
(`specs/7/1-channel-protocol.md`). No router imports, no
shared state.

| Adapter   | Source                      | Language | Priority |
| --------- | --------------------------- | -------- | -------- |
| whatsapp  | kanipi channels/whatsapp.ts | TS       | high     |
| discord   | kanipi channels/discord.ts  | TS       | high     |
| email     | kanipi channels/email.ts    | Go       | medium   |
| web/slink | kanipi slink.ts + web.ts    | Go       | medium   |
| reddit    | kanipi channels/reddit/     | TS       | low      |
| twitter   | kanipi channels/twitter/    | TS       | low      |
| facebook  | kanipi channels/facebook/   | TS       | low      |
| mastodon  | kanipi channels/mastodon/   | TS       | low      |
| bluesky   | kanipi channels/bluesky/    | TS       | low      |

Rules:

- Strip all kanipi integration (deps callbacks, shared state)
- Replace with HTTP calls to router API
- Each adapter is self-contained (own package.json or go.mod)
- Testable in isolation without the router running
- Start minimal (inbound only), add outbound incrementally

## Remaining kanipi feature ports

| Feature            | Where     | Notes                    |
| ------------------ | --------- | ------------------------ |
| prototype spawning | container | clone group on missing   |
| reply-to outbound  | chanreg   | Channel interface change |
| whisper pipeline   | mime      | voice/video transcribe   |
