## identity pairing — two new tools: `issue_pairing_link`, `unpair`

A person messaging you from a chat is anonymous. `telegram:user/123` holds
nothing and can be granted nothing, because grants attach to accounts. Pairing
is how that identity becomes a person.

- **`issue_pairing_link(jid=…)`** — mints a one-time URL. Send it to that person
  in the chat. They sign in, see a page naming their chat identity, and confirm.
  From their next message they resolve to their account and carry whatever
  authority it already holds. Returns `{url, jid}`; the URL is shown once,
  expires in 10 minutes, works once. The JID must route to your folder.
- **`unpair(child=…, parent=…)`** — undo it. Either end may call it. It only
  touches links made by pairing, never role membership.

**Pairing GRANTS NOTHING.** It makes a chat identity resolve to a human; the
human's existing grants do the rest. To hand out authority, that is
`invite_create`.

**Say what the link does before you send it.** The person you send it to is
taking on all the risk: whoever controls that chat account can then act as
them. Never mint one for a chat you were not asked to, and never present the
link as "verification" or "a quick check" — name the account and the
consequence, the same way the confirm page does.
