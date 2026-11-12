# ttsd

ttsd proxies the OpenAI-compatible speech API to a configured backend. routd calls ttsd to synthesize audio before channel delivery.
ttsd owns no store tables, persistent files, outbound queue, or model process.

```text
routd ──► ttsd:8880 ──► kokoro:8880 or TTS_BACKEND_URL
proxyd ──► /dash/ttsd/ operator page
```

## HTTP interfaces

| Method and path         | Behavior and access                                                      |
| ----------------------- | ------------------------------------------------------------------------ |
| `POST /v1/audio/speech` | Proxies JSON speech request and audio response verbatim. No bearer gate. |
| `GET /v1/voices`        | Proxies the backend voice list. No bearer gate.                          |
| `GET /health`           | Public backend reachability probe; 200 `ok` or 503 `disconnected`.       |
| `GET /dash/ttsd/`       | Operator-only backend URL, reachability, and probe duration.             |

There are no resreg resources, MCP tools, IPC sockets, `/openapi.json`, or `/metrics` route.
The dashboard verifies proxyd's operator identity through `chanlib.AuthdKeys` when `AUTHD_URL` is set.
Speech and voice routes have no application authentication. Keep the service on the internal network or front it with an auth gate.
`StoreReach["ttsd"]` is empty. ttsd makes no store or router request.

## Configuration

| Variable                      | Default              | Meaning                                                                              |
| ----------------------------- | -------------------- | ------------------------------------------------------------------------------------ |
| `TTSD_ADDR`                   | `:8880`              | Listener. The bundled compose fragment also sets `:8880`; `LISTEN_ADDR` is not read. |
| `TTS_BACKEND_URL`             | `http://kokoro:8880` | Backend base URL.                                                                    |
| `AUTHD_URL`                   | unset                | Dashboard JWKS verification; unset opens the shared development gate.                |
| `ARIZUKO_INSTANCE`            | unset                | Observability instance label.                                                        |
| `LOG_LEVEL`                   | `info`               | JSON stderr level: debug, info, warn, error.                                         |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset                | Optional OTLP logs through [obs](../obs/README.md).                                  |

routd's `TTS_ENABLED`, `TTS_BASE_URL`, `TTS_VOICE`, and `TTS_MODEL` select speech generation at the caller.
They are not ttsd environment reads. [routd](../routd/README.md) documents them.

## Probes, failures, and limits

Probes run per health or dashboard request; there is no periodic backend loop.
Each backend probe call has a 3 s timeout. A `GET /health` response below 500 counts as reachable, including 404.
After a failed or 5xx health probe, ttsd tries `HEAD /` and accepts any response below 500.
A reachable server with a broken speech model can therefore report healthy.

The proxy returns 502 and logs `backend proxy error` on a transport failure.
ttsd does not retry or retain failed synthesis requests. routd sees the synthesis error.
The HTTP server sets a 30 s read timeout and 60 s write timeout. Signal shutdown allows 5 s.
Grep `ttsd listening`, `invalid TTS_BACKEND_URL`, `backend proxy error`, and `graceful shutdown failed`.
The adapter health loop does not discover or withdraw TTS capabilities; routd's TTS setting is static.

Tests use an HTTP backend stub. Local checks do not verify Kokoro voice availability or cloud authentication.

Sources: [entry point, proxy, and probes](main.go), [dashboard](dash.go),
[bundled fragment](../template/services/ttsd/ttsd.yml), [routd synthesis](../routd/tts.go).
