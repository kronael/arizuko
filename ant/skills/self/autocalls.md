# Autocalls

Every prompt opens with an `<autocalls>` block: gateway-resolved facts
that are always fresh. Treat as ground truth. Don't call a tool to
re-fetch these.

```xml
<autocalls>
now: 2026-04-22T14:30:00Z
instance: <instance>
folder: <org>/<branch>
session: abcdef12
</autocalls>
```

Fields: `now` (UTC RFC3339), `instance`, `folder`, `session`
(short id; line omitted when no session). Missing lines = empty value.
