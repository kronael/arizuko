# ttsd

Thin OpenAI-compatible TTS proxy. Default backend is Kokoro-FastAPI
(`remsky/kokoro-fastapi-cpu` or `:gpu`); operators who run Piper, Coqui,
or OpenAI cloud override `TTS_BACKEND_URL` and skip the bundled service.

## Why a wrapper

Pins arizuko's TTS contract to the OpenAI `/v1/audio/speech` shape so
the gateway, ttsd, and any future drop-in (third-party API, local model
swap) speak the same protocol. Adds a `/health` endpoint matching the
rest of arizuko's adapter healthcheck convention — the gateway can flip
`TTS_ENABLED=false` automatically if the backend's down without poking
implementation details.

## Endpoints

- `GET /health` → 200 `{status:"ok"}` when the backend is reachable;
  503 `{status:"disconnected"}` otherwise.
- `POST /v1/audio/speech` → forwards verbatim to the backend.
  Request body: `{model, voice, input, response_format}` per OpenAI.
  Response: audio bytes (`audio/ogg`, `audio/mpeg`, etc).
- `GET /v1/voices` → forwards verbatim. Convenience for listing voices
  on Kokoro-FastAPI.

## Config

| env               | default              | meaning                              |
| ----------------- | -------------------- | ------------------------------------ |
| `TTSD_ADDR`       | `:8880`              | listen address                       |
| `TTS_BACKEND_URL` | `http://kokoro:8880` | backend OpenAI-compatible TTS server |
| `LOG_LEVEL`       | `info`               | `debug` / `info` / `warn` / `error`  |

## Running

```bash
# Use bundled Kokoro:
docker run -d --name kokoro -p 8881:8880 ghcr.io/remsky/kokoro-fastapi-cpu:latest
docker run -d --name ttsd -p 8880:8880 -e TTS_BACKEND_URL=http://localhost:8881 arizuko-ttsd:latest

# Use external (e.g. OpenAI cloud):
docker run -d --name ttsd -p 8880:8880 \
  -e TTS_BACKEND_URL=https://api.openai.com -e TTS_AUTH_HEADER='Authorization: Bearer sk-...' \
  arizuko-ttsd:latest
```

(`TTS_AUTH_HEADER` injection is a future extension; today, front the
backend with whatever auth your infrastructure provides.)

## Testing

```bash
make test
```

Runs unit tests against an in-memory backend stub (no Kokoro required).
