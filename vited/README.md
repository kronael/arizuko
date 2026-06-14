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
- Serve `/@vite/client` and hot-module-reload in dev; in production a built
  dist is served from the same path.

## Entry points

- Image: `arizuko-vite:latest` (built from `template/web/`, `vite.config.ts`).
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
- `template/web/` — page source, `vite.config.ts`, hub.css
