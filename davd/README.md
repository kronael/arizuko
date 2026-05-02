# davd

WebDAV daemon: per-group workspace browser/editor.

## Purpose

Exposes `<DATA_DIR>/groups/` over WebDAV using `sigoden/dufs`
(single Rust binary, ships UI). Wrapped in alpine to add the
arizuko-standard `wget` healthcheck (`dufs` is distroless).

## Responsibilities

- Serve `/data` (mounted from `<DATA_DIR>/groups/:ro`) over HTTP +
  WebDAV on `:8080`.
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
The volume mount and the `WEBDAV_ENABLED`/`DAV_ADDR` toggle live in
the `compose/` generator.

## Health signal

`GET /` returns 200 (dufs index). Healthcheck probes that with a
3-second `wget` timeout.

## Related docs

- `specs/5/M-webdav.md` — auth model, write-block guard
- `proxyd/README.md` — `/dav/*` routing and `davAllow`
