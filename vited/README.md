# vited

Vite dev server / static file origin behind proxyd.

## Purpose

Serves the web UI (`/pub/*`, `/priv/*`) from `<data>/web/`. proxyd
reverse-proxies to vited for all requests not matched by a `web_routes`
rule. vited is not a Go daemon — the container runs the Vite dev server
(`arizuko-vite:latest` image, `template/web/`).

## Responsibilities

- Serve `/pub/*` — public operator docs and landing pages (unauthenticated).
- Serve `/priv/*` — JWT-gated operator UI (proxyd enforces auth before
  forwarding).
- Push a reload to an open tab when a page under `pub/` or `priv/` changes.
  The watcher is inotify over those two trees only — a write from an agent's
  container reaches it through the shared bind mount — and the tree's size is
  the server's idle memory: ~110 MB at 2k files on krons, ~55 MB at 500 on
  sloth, ~0% CPU. Nothing is bundled; the dev server runs in production.

## Entry points

- Image: `arizuko-vite:latest` (`ant/Dockerfile.vite`, config `ant/vite.config.js`).
- Listen: `:8080` inside the container.
- Volume: `<data>/web` mounted at `/web` (read-only source for pages).
- Health probe: `GET /@vite/client` (always 200 in dev mode).

## Configuration

- `VITE_ADDR` — proxyd reads this to locate vited (`http://vited:8080`
  default); vited itself has no arizuko env vars.

## Related docs

- `ARCHITECTURE.md` (Web Channel section, proxyd routing table)
- `proxyd/README.md` — proxyd is the auth gate; vited trusts all proxied
  requests.
- `template/web/` — the operator doc site's source, hub.css
