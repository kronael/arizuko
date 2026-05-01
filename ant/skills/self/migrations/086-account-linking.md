# 086 — Account linking + collision UX

If a user logs in with a second OAuth provider while already signed
in, the callback no longer silently creates a duplicate `auth_users`
row. Instead:

- `/dash/profile` shows the canonical sub + linked subs and a button
  per provider that hasn't been linked yet. Each button hits
  `/auth/{provider}?intent=link&return=/dash/profile/`. After OAuth
  succeeds, the new sub is recorded in `auth_users.linked_to_sub`
  pointing at the canonical sub.
- If the new provider sub already belongs to a different canonical
  user, OR if the user wasn't trying to link but happened to OAuth
  through a different account, a small HTML page asks them to
  choose: link to current, or log out and become the other account.

Single resolve point: `store.CanonicalSub(sub)` runs at JWT mint
time (`auth/web.go::issueSession`). Downstream services (`webd`,
`gateway`, `ipc`, etc.) only ever see canonical subs in their JWT
claims and `X-User-Sub` headers. Don't re-resolve.
