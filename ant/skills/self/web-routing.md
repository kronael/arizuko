# Web routing

Proxyd routes all web traffic. URL structure:

| Path        | Auth     | Backend | Purpose                                  |
| ----------- | -------- | ------- | ---------------------------------------- |
| `/pub/*`    | none     | vite    | Public static files                      |
| `/chat/*`   | token    | webd    | Route-token chat widget (public)         |
| `/hook/*`   | token    | webd    | Route-token webhook ingest (public)      |
| `/panel/*`  | JWT      | webd    | Authenticated operator chat panel        |
| `/dash/*`   | JWT      | dashd   | Operator dashboard                       |
| `/api/*`    | JWT      | webd    | API endpoints                            |
| `/auth/*`   | none     | proxyd  | OAuth login/callback/logout              |
| `/x/*`      | JWT      | webd    | Extensions (served by webd, not static)  |
| other       | JWT      | vite    | Auth-gated; rewrites to `/pub/<path>`    |

Legacy `/slink/*` 301-redirects to `/chat/*`.

## Route tokens

Agents mint chat links and webhook URLs on demand:

```
issue_chat_link()     → {jid, token}   # token returned once, store in workspace
issue_webhook(label)  → {jid, token}
```

Full reference: `chat-link.md`
