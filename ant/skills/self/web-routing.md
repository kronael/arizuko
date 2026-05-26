# Web routing

Proxyd routes all web traffic. URL structure:

| Path        | Auth     | Backend | Purpose                                                          |
| ----------- | -------- | ------- | ---------------------------------------------------------------- |
| `/pub/*`    | none     | vite    | Public static files (from `<data>/web/pub/`)                     |
| `/priv/*`   | JWT      | vite    | OAuth-gated static files (from `<data>/web/priv/` — separate tree) |
| `/chat/*`   | token    | webd    | Route-token chat widget (public)                                 |
| `/hook/*`   | token    | webd    | Route-token webhook ingest (public)                              |
| `/panel/*`  | JWT      | webd    | Authenticated operator chat panel                                |
| `/dash/*`   | JWT      | dashd   | Operator dashboard                                               |
| `/me/*`     | JWT      | webd    | User portal (folder tree, chats, threads)                        |
| `/api/*`    | JWT      | webd    | API endpoints                                                    |
| `/auth/*`   | none     | proxyd  | OAuth login/callback/logout                                      |
| `/x/*`      | JWT      | webd    | Extensions (served by webd, not static)                          |
| other       | JWT      | vite    | Auth-gated; rewrites to `/pub/<path>`                            |

Legacy `/slink/*` 301-redirects to `/chat/*`.

## Two URLs, one file (the `/pub` ↔ `/` rule)

`https://$WEB_HOST/pub/<X>` (no auth) and `https://$WEB_HOST/<X>`
(JWT-rewrite) BOTH serve `<data>/web/pub/<X>` — the SAME file via
two different doors. `https://$WEB_HOST/priv/<X>` serves
`<data>/web/priv/<X>` — a DIFFERENT filesystem tree that is NEVER
served via `/pub/` URLs.

## Where the files live (agent view)

Every group container has two writable web slots in its home, plus a
read-only view of the unified public web tree:

| Container path    | URL                              | Auth      | Bind-mount source            |
| ----------------- | -------------------------------- | --------- | ---------------------------- |
| `~/public_html/`  | `/pub/<your-folder>/...`         | none      | `<data>/web/pub/<folder>/`   |
| `~/private_html/` | `/priv/<your-folder>/...`        | JWT       | `<data>/web/priv/<folder>/`  |
| `/var/lib/www/`   | (no URL — RO browse mount)       | n/a       | `<data>/web/pub/` whole tree |

## Route tokens

Agents mint chat links and webhook URLs on demand:

```
issue_chat_link()     → {jid, token}   # token returned once, store in workspace
issue_webhook(label)  → {jid, token}
```

Full reference: `chat-link.md`

## Web JID model — 1:1 with groups (no route table)

Web chats use a **direct addressing** model: `web:<folder>` always
addresses group `<folder>`. The gateway resolves the JID via
`GroupByFolder` and **never** consults the `routes` table for web
JIDs. This is enforced at insertion time: `add_route` /
`store.AddRoute` rejects any rule whose match contains
`chat_jid=web:*` with `ErrWebJIDRouted`.

### Implications

- **Sub-chats need sub-groups.** Want a separate intake thread for
  forms (`web:<folder>/intake`)? Create the sub-group via
  `setup_group <folder>/intake` first. Don't add a route — it won't
  fire and you'll silently lose messages.
- **No route table workarounds.** If you've inherited a setup where
  the route table tries to redirect a `web:<X>` chat to a different
  folder, that route is dead. The chat is unrouted unless `<X>` is
  itself a real group folder.
- **`add_route` error means "create the group instead".** When you
  see `web: JIDs are 1:1 with groups; route table does not apply.
  Create the group via setup_group instead.`, the answer is literally
  what the error says — call `setup_group` for the missing folder.
- **Non-web JIDs are unaffected.** `chat_jid=telegram:*`,
  `chat_jid=slack:*`, `chat_jid=hook:*`, `chat_jid=slink:*` etc. all
  still route normally. Only `web:*` is special.

### Why it works this way

`web:<folder>` is a **synthesised** JID — there's no real platform
behind it, just our own webd. The 1:1 mapping keeps the surface
mechanically symmetric: each folder owns its public
`/chat/<token>/` URL, its `slink` widget, its `~/public_html/`
slot, and its agent. One arrow per surface; no intermediate
routing rule. Sub-flows (form intakes, report channels) become
sub-groups, which compose naturally with the rest of arizuko
(persona, memory, grants — all per group).

Future work: a thread specifier (e.g. `web:<folder>#<thread>`) may
let one group own multiple chat threads without spawning sub-groups;
not shipped yet.

Enforcement lives at `store/routes.go: matchesWebJID`; resolution at
`gateway/gateway.go: resolveGroup`.
