# Issues

## BUG: proxyd catch-all silently 302'd unknown paths to /pub/<path>

**File**: `proxyd/main.go:425`
**Status**: FIXED — changed to `http.NotFound(w, r)`

Unknown paths (e.g. `/priv/test.html`) were 302-redirected to `/pub/priv/test.html`
with no auth and no existence check. Appeared to "work" but landed on Vite 404.
Now returns 404 immediately.

---

## BUG: /priv/ is not a real route (no auth protection)

**Status**: KNOWN — no `/priv/` handler exists in proxyd or webd

Agents/users expecting `/priv/` to be auth-protected are wrong — it was hitting
the catch-all redirect (now fixed to 404). If auth-protected static files are
needed, a proper `/priv/` route must be added to proxyd routing to viteProxy
behind `requireAuth`.

---

## BUG: /x/ routes to webd (webdProxy), not viteProxy — static files don't work

**File**: `proxyd/main.go`, `webd/server.go`
**Status**: KNOWN

`/x/` is in the auth-gated list but the upstream is `webdProxy` (webd daemon),
not viteProxy. Dropping static files under `/workspace/web/x/` does nothing —
webd has no handler for them. Static auth-protected files need a new route
(e.g. `/priv/`) wired to viteProxy behind requireAuth.

---

## UX: No welcome/howto content shown after invite acceptance

**Status**: OPEN

After a user accepts an invite (direct or subworld), they land on the ant link
but receive no instructions on how to connect a Telegram/Discord/WhatsApp channel.
Need a welcome message or onboarding UI step.

---
