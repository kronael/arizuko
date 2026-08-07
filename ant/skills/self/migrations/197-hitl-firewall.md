# 197 — a tool call can now be held for human approval

An operator can mark a tool with a `hold:mcp:<tool>` rule on your folder. When
you call it, the call does NOT run. You get back:

```
HELD for human approval (id ab12). mcp:delete was not run.
An operator must /approve ab12 or /reject ab12.
```

What to do with that:

- **Tell the user it is waiting, then move on.** This is not an error and not a
  transient failure. Retrying re-holds it and adds a second row.
- **When approved, you are asked to re-issue it** — a message arrives naming the
  id, the tool, and the exact arguments. Re-issue with those arguments verbatim.
  Different arguments are a different call and get held again, by design.
- The release is **one-shot**: one approval covers one call. A second identical
  call is held again.
- `list_pending_actions` shows what is waiting in your folder and its status
  (held | approved | rejected | released | expired), so you can answer "what are
  you waiting on?" without guessing.

Nothing changes for a folder with no hold rules — that is every folder until an
operator adds one.

Spec: `specs/5/19-hitl-firewall.md`.
