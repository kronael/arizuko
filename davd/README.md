# davd

WebDAV daemon: per-group workspace browser/editor.

## Purpose

Exposes `<DATA_DIR>/groups/` over WebDAV using `sigoden/dufs`
(single Rust binary, ships UI). Wrapped in alpine to add the
arizuko-standard `wget` healthcheck (`dufs` is distroless).

## Responsibilities

- Serve `/data` (mounted from `<DATA_DIR>/groups/`) over HTTP +
  WebDAV on `:8080`. Write enforcement lives upstream in proxyd's
  `davAllow` guard, not at the volume mount.
- Nothing else. Auth and per-group scoping happen upstream in
  `proxyd`; davd has no notion of identity.

## Entry points

- Image: `arizuko-davd:latest` (built from `davd/Dockerfile`)
- Listen: `:8080`

## Dependencies

- Binary: `sigoden/dufs` (upstream)
- Wrapper base: `alpine:3.20` (for `wget` + `ca-certificates`)

## Configuration

None — the binary is invoked with `--port 8080 /data` from compose.
The volume mount and the `WEBDAV_ENABLED` toggle live in the `compose/`
generator, which also hardcodes the `/dav/` route's backend as
`http://davd:8080`. There is no backend-URL env var.

## Health signal

`GET /` returns 200 (dufs index). Healthcheck probes that with a
3-second `wget` timeout. dashd's hub tile probes `GET /` for the same
reason.

## No dashboard

davd serves no `/dash/davd/` and has no route for one. davd is dufs with no
arizuko code: it holds no session list, no disconnect verb and no
`/health`, so a dashboard would have nothing of its own to show. Its hub
tile shows the probe and does not link.

## Related docs

- `proxyd/README.md` — `/dav/*` routing, the auth model and the `davAllow`
  write-block guard
