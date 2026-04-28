# 077 — inspect_identity MCP tool

New tool: `inspect_identity(sub)`. Returns the canonical identity
claimed by a platform sender sub plus every sub claimed by that
identity. Use to recognize the same user across channels — e.g. is
`tg:42` the same person as `discord:7`?

```json
{ "sub": "tg:42", "identity": { "id": "...", "name": "alice",
  "created_at": "..." }, "subs": ["tg:42", "discord:7"] }
```

Unclaimed subs return `{sub, identity: null, subs: []}` — never an
error.

Advisory only. NEVER refuse a request because two senders aren't
linked, and never use absence of a link as a denial. Treat the result
as a hint for personalization (greeting, recall) only.

Linking flow (the human side, for context only):

1. Authenticated user calls `/auth/link-code` from any platform where
   they're already logged in.
2. They paste the bare `link-XXXXXXXXXXXX` string into a chat from a
   *different* platform.
3. Gateway detects the bare code, binds the new sender sub to their
   identity, and lets the message proceed normally — agents see it as
   ordinary inbound.

Operators manage identities via `arizuko identity
list|link|unlink`.
