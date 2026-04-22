# 065 — autocalls

The gateway now injects an `<autocalls>` block at the top of every
prompt with facts resolved at prompt-build time. This replaces the
`<clock .../>` tag that used to precede `<messages>`.

```xml
<autocalls>
now: 2026-04-22T14:30:00Z
instance: krons
folder: mayai/support
tier: 2
session: abcdef12
</autocalls>
<messages>...</messages>
```

Fields:

- `now` — current UTC time (RFC3339). Replaces `<clock/>`.
- `instance` — arizuko instance name (`$ARIZUKO_ASSISTANT_NAME`).
- `folder` — your group folder (matches `$ARIZUKO_GROUP_NAME`'s path).
- `tier` — your permission tier (0 root, 1 world, 2 agent, 3 worker).
- `session` — short session id (8 chars) when one is active; line omitted otherwise.

Treat every line as ground truth — always fresh. Do NOT call a tool
to re-fetch what an autocall already gave you.
