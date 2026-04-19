# Sidecars

Self-contained service images built alongside arizuko.

- `whisper/` — `arizuko-whisper` image (faster-whisper over HTTP).
  Build: `make -C sidecar/whisper image`. Deployed separately;
  gated reaches it via `WHISPER_BASE_URL`.
