# sidecar

Self-contained service images built and shipped alongside arizuko.

## Purpose

Out-of-process capabilities that aren't core daemons but which gated (or
other Go daemons) call over HTTP. Each sidecar is its own docker image,
its own Makefile, and its own runtime — arizuko reaches it via a URL
env var, never via a Go import. Add a new sidecar by adding a
subdirectory with a `Dockerfile`, `Makefile` (`image` target), and
whatever language runtime it needs.

## What's here

- `whisper/` — `arizuko-whisper` image. faster-whisper STT exposed
  over a tiny FastAPI app. Used for inbound voice transcription.

## whisper

FastAPI app (`whisper/main.py`) wrapping `faster-whisper.WhisperModel`.
One endpoint:

- `POST /inference` — multipart upload (`file`, optional `language`),
  returns `{text, language}`. Optional `Authorization: Bearer <token>`
  enforced when `WHISPER_AUTH_TOKEN` is set.

The image binds to `127.0.0.1:8178` by default. Flip the Dockerfile
`CMD` host to `0.0.0.0` only when deploying behind a compose-internal
network AND setting `WHISPER_AUTH_TOKEN` — the dual rule (loopback OR
token) is what keeps the model server from being open on the box.

### Env vars (read by the container)

- `WHISPER_MODEL` — model size (default `base`)
- `WHISPER_DEVICE` — `cpu` or `cuda` (default `cpu`)
- `WHISPER_COMPUTE` — compute type (default `int8`)
- `WHISPER_AUTH_TOKEN` — bearer for `/inference`; unset disables the check

### How arizuko calls it

gated picks up `WHISPER_BASE_URL` from `.env` and propagates it into
agent containers (see `compose/compose.go` env passthrough list). The
agent or gated POSTs `/inference` against that URL when transcribing
inbound voice attachments. No registration handshake — sidecars are
discovered via env, not via the channel registry.

### Build

```bash
make -C sidecar/whisper image      # builds arizuko-whisper:latest
```

Deployed separately from `make images`; sidecars are opt-in per
instance and may live on a different host than the router.

## Files

- `whisper/Dockerfile`, `whisper/Makefile`, `whisper/main.py`,
  `whisper/requirements.txt`

## Dependencies

None (each sidecar is standalone; no Go imports).

## Related docs

- `EXTENDING.md` — adding new sidecars
- `CLAUDE.md` ("Integrations") — sidecars are integration-tier, not core
