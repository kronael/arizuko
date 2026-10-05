---
name: web
description: >
  Deploy web apps, pages, sites, dashboards to your group's web slot —
  `~/public_html/` (public) or `~/private_html/` (OAuth/JWT). USE for
  "build a page", "deploy this site", "publish a dashboard", "show my
  web", static sites, index.html, single-page apps. NOT for
  howto/getting-started (use howto), knowledge hubs (use hub), or REST
  APIs (use service).
user-invocable: true
---

# Web

Deploy by writing files to `~/public_html/<app>/`. Any directory with
`index.html` is served by vite MPA. The bind mount projects each path
into the unified `<data>/web/pub/<your-folder>/` tree.

## Two slots, two URL spaces

| Slot              | URL                                          | Auth      |
| ----------------- | -------------------------------------------- | --------- |
| `~/public_html/`  | `https://$WEB_HOST/pub/<your-folder>/...`    | none      |
| `~/private_html/` | `https://$WEB_HOST/priv/<your-folder>/...`   | OAuth/JWT |

`https://$WEB_HOST/pub/<X>` (public) and `https://$WEB_HOST/<X>`
(JWT-rewrite) serve the SAME file. `https://$WEB_HOST/priv/<X>`
serves a DIFFERENT file from a separate filesystem tree.

## Other web URL prefixes

- `/pub/*` — public, no login (your `~/public_html/`)
- `/priv/*` — JWT, OAuth-gated (your `~/private_html/`)
- `/dash/`, `/chat/`, `/api/`, `/x/` — JWT-gated (not static)
- `/auth/*` — OAuth flow

ALWAYS place public apps under `~/public_html/<app>/`. ALWAYS place
OAuth-gated apps under `~/private_html/<app>/`. NEVER write to
`/workspace/web/...` — that path is gone (v0.45.11).

## Pages only

Every file under `~/public_html/` is served to anyone, and the dev server
indexes and watches all of it. Put there ONLY what a page serves. A source
clone you read for research, a reference checkout, notes, `.claude/`, caches
— all of that is `~/workspace/` (say `~/workspace/refs/<name>`), and a page
links to the upstream URL. Cost: krons kept two 21k-file reference trees
under `pub/*/agents/refs`, 7.6 G each with their `.git` public, and the
server paid 145 MB of memory idle to watch them (BUGS L360, L393).

## Create a public app

1. `mkdir -p ~/public_html/myapp && cat > ~/public_html/myapp/index.html`
2. Live at `https://$WEB_HOST/pub/$ARIZUKO_GROUP_FOLDER/myapp/`
3. Vite handles TypeScript, CSS, hot reload natively

## Create an OAuth-gated app

1. `mkdir -p ~/private_html/admin && cat > ~/private_html/admin/index.html`
2. Live at `https://$WEB_HOST/priv/$ARIZUKO_GROUP_FOLDER/admin/`
3. Requires a logged-in user with JWT (operator + invited users)

## Stack

- Vite MPA (no build step)
- Vanilla HTML + CSS + JS/TS
- Shared assets: link to `/pub/arizuko/assets/hub.css` (operator-curated docs)
- Rich apps: Tailwind CDN, Alpine CDN

## Browse what other groups publish

`/var/lib/www/` mounts the whole public web tree read-only. To see
what's already published or link to operator-curated docs:

```bash
ls /var/lib/www/
# link in your HTML:
# <link rel="stylesheet" href="/pub/arizuko/assets/hub.css">
```

You cannot write to `/var/lib/www/` directly — it is mounted read-only.
Your writable view is `~/public_html/`. Both appear only if your folder
holds the `web:publish` grant. Without it the one subtree you still see
there is `arizuko/`, the doc site — so `arizuko/` alone under
`/var/lib/www/` means you do NOT hold the grant.

## Nested subgroups

A subgroup `atlas/support` has `~/public_html/` projecting to
`<data>/web/pub/atlas/support/` — URL: `/pub/atlas/support/...`.
The parent group `atlas` SEES this subdir as RO via
`/var/lib/www/atlas/support/`. Treat subgroup directory names as
reserved.

## Hub page

Root `~/public_html/index.html` lists deployed apps for your folder.
Update when adding/removing apps.

## Web chat

Each group has a route token for public chat at
`https://$WEB_HOST/chat/<token>/`. Mint via `issue_chat_link`. For a
custom chat page inside your web slot, use the round-handle JSON API.

## Verify before you announce (mandatory)

Public `/pub/*` MUST return 200. JWT-gated `/priv/*` MUST return 401 from
your unauthenticated container (or 200 with a session cookie) — 401
confirms the file is there AND the gate is engaged. 404 means the file
isn't where you think — do NOT announce.

```bash
url="https://$WEB_HOST/pub/$ARIZUKO_GROUP_FOLDER/myapp/"
curl -sI "$url" | head -1   # MUST be 200
curl -sI "https://$WEB_HOST/priv/$ARIZUKO_GROUP_FOLDER/admin/" | head -1   # MUST be 401, NOT 404
```

**A 302 is neither 200 nor 404: the file is fine, the ROUTE is wrong.** A
`web_routes` row on a path that covers yours redirects your page somewhere
that does not exist. Read the target and stop: `curl -sI "$url" | grep -i
location`. Report the redirect target and say an operator must narrow or drop
that row. NEVER move the file, NEVER probe host and path variants — sloth
2026-09-19 had one `/pub/` → `/pub/atlas/` row that broke publishing for every
folder except `atlas`, and the hunt for a working URL cost a whole turn.

**Elevated `/root`:** `~/public_html/` projects to `<data>/web/pub/` at
the top level (no folder prefix); it can also write `/var/lib/www/`
directly (RW, root-only) to stage content for any group.

## `~/public_html` not writable, or missing

That is a MISSING GRANT, never a broken mount. The platform bind-mounts
`~/public_html` and `~/private_html` only when your folder holds
`web:publish`. With no grant there is no mount, so you see a root-owned
directory you cannot write, or none at all. Do NOT ask for a `chown`, do
NOT hunt for a static-file source repo, do NOT report a broken container
— none of those is the cause and the chown would write to a directory
nothing serves. Say exactly this instead: "I need `web:publish` on
`<your folder>`. Ask an operator: `/root arizuko group <inst> grant
'folder:<your folder>' '<your folder>' web:publish`."

A grant scoped to a PARENT folder does not reach you — a path carries
zero authorization (spec `5/33`), so `main` covers `main` and never
`main/sub`. Cost: every subgroup on one instance silently lost web
publishing after the tier removal, and the agent that hit it filed a
chown request four separate times. Egress has the same shape:
`self/egress.md`.

## Anti-patterns (each shipped to a real user)

- Announcing a URL from env vars without curl-verifying.
- Writing to `/workspace/web/...` (gone — v0.45.11 renamed the mounts to
  FHS).
- Treating `curl -sI` 4xx as transient — almost always the file isn't
  where you think, not slow DNS/cache.
- Spending the turn hunting for a URL that serves. Report the first answer
  you have, then diagnose; a report that arrives after the caller gave up is
  the same as no report.
