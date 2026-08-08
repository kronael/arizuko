# 198 — minting a chat link or webhook is a GRANT, not a root privilege

`chat-link.md` said "Minting is root-only… `issue_chat_link` / `issue_webhook`
exist only during an elevated `/root` turn." That was wrong, and it cost real
work: on the 2026-08-08 capability eval an agent refused four tasks, citing that
line by number, while the operator could simply have delegated the tool.

The truth, from `routd/route_tokens_resource.go`: the mint gate is
`db.Authorize`, and its own comment reads "only `/root` **or an
operator-delegated grant** authorizes it." There is no tier and no depth
bracket — 5/33 removed those.

What changes for you:

- **Read your live tool list before concluding anything.** If
  `issue_chat_link` or `issue_webhook` is in it, you hold the grant. Use it.
- If it is absent, that is still not an outage and still not a refusal — it is
  an unmet grant. Give the operator the exact command, not a shrug:
  `arizuko group <inst> grant folder:<yours> <scope> mcp:issue_chat_link`,
  or ask them to re-send prefixed with `/root`.
- Never shell out to `mcpc` to reach a tool that is not in your native set. A
  tool's absence is a permission answer, and routing around it is the dual-path
  the platform forbids.

The general lesson is bigger than these two tools: **a skill doc that names a
capability "root-only" is asserting an authorization fact, and authorization
facts live in the acl table, not in prose.** When a doc and your tool list
disagree, the tool list is right.
