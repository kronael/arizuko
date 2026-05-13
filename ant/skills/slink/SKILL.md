---
name: slink
description: >
  Build a branded chat page on top of a slink token using the
  arizuko-client SDK at `/assets/arizuko-client.js`. USE for "embed
  slink in my site", "branded chat widget", "host a chat page",
  "drop-in chat for my product". NOT for one-off external calls
  (use raw HTTP) or MCP-over-HTTP (use `slink-mcp` skill).
user-invocable: true
---

# Slink SDK — building chat pages

The slink REST surface (POST → SSE round handles) is consumable from
any page. arizuko ships a vanilla-JS SDK at `/assets/arizuko-client.js`
so you don't reinvent the `fetch` + `EventSource` dance per page.

## Drop-in template

```html
<!DOCTYPE html>
<meta charset="utf-8" />
<title>my chat</title>
<script src="/assets/arizuko-client.js"></script>
<div id="thread"></div>
<form id="f"><input id="m" autofocus /><button>send</button></form>
<script>
  const TOKEN = '<paste-token-here>';
  const thread = document.getElementById('thread');
  const add = (role, text) => {
    const d = document.createElement('div');
    d.className = role;
    d.textContent = text;
    thread.appendChild(d);
  };
  (async () => {
    const slink = await Arizuko.connect(TOKEN);
    document.title = slink.name;
    document.getElementById('f').onsubmit = async (e) => {
      e.preventDefault();
      const m = document.getElementById('m');
      const content = m.value.trim();
      if (!content) return;
      m.value = '';
      add('user', content);
      const turn = await slink.send(content);
      slink.stream(turn.turn_id, {
        onMessage: (f) => add('assistant', f.content),
        onDone: (_) => {},
      });
    };
  })();
</script>
```

Working sample: `/pub/examples/slink-sdk.html` (operator-facing).

## API

| Method                           | Returns                   | Purpose                               |
| -------------------------------- | ------------------------- | ------------------------------------- |
| `Arizuko.connect(token, opts?)`  | `Promise<Slink>`          | Fetches `/config`, returns Slink      |
| `slink.send(content, opts?)`     | `Promise<TurnHandle>`     | New round — `{turn_id, user, status}` |
| `slink.steer(turnId, content)`   | `Promise<TurnHandle>`     | Follow-up to an in-flight round       |
| `slink.status(turnId)`           | `Promise<StatusEnvelope>` | Cheap poll — `{status, frames_count}` |
| `slink.snapshot(turnId, opts?)`  | `Promise<Snapshot>`       | All frames (or `?after=<id>` page)    |
| `slink.stream(turnId, handlers)` | `()=>void` (close fn)     | SSE — `onMessage`/`onStatus`/`onDone` |

`opts.baseURL` on `connect()` overrides same-origin (default empty).

## Where to host the page

- **In-group**: drop your `index.html` into `/workspace/web/pub/<app>/`
  and it serves at `/pub/<app>/index.html` on the arizuko origin.
- **Third-party origin**: the SDK is CORS-permissive
  (`Access-Control-Allow-Origin: *`), so any external page can
  `<script src="https://<your-arizuko>/assets/arizuko-client.js">`.

## Discovery

`GET /slink/<token>/config` returns `{token, folder, name, endpoints, sdk}`.
The `sdk` field gives the canonical SDK URL — useful when the SDK
origin is unknown at page-build time:

```js
const cfg = await (await fetch(`${origin}/slink/${token}/config`)).json();
await import(`${origin}${cfg.sdk}`);
```

## Spec

- Protocol: `specs/1/W-slink.md` (round handle, POST/SSE/snapshot)
- REST surface: `specs/1/Z-slink-widget.md` (`/config`, CORS, JSON default)
- SDK + hosting: `specs/1/Z2-slink-sdk.md`
