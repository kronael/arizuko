# Webdata

Webdata is a standalone Python service for callers that need rendered page text.
It keeps extraction tasks and results in memory, without a database.

```text
caller ──POST──► handle ──► page extraction ──► Chromium
caller ──GET───► result or error
caller ──DELETE or 1200-second expiry──► close and cancel
```

## Run one worker

- Use the service only with trusted callers. It can browse locations reachable from the host.
- The service has no authentication. The command below binds to loopback.
- Use one worker because each process owns separate handles.

From the repository root:

```bash
cd webdata
uv sync --locked --python 3.13
uv run playwright install chromium
uv run uvicorn app:app --host 127.0.0.1 --port 8080 --workers 1 \
  --limit-concurrency 64 --timeout-graceful-shutdown 5
```

Chromium needs its system libraries. Playwright reports missing libraries at startup.
`GET /health` reports process health. `GET /ready` checks the browser connection.

## Create, wait, close

Create accepts an HTTP or HTTPS URL. It returns `202`, `{"handle":"..."}`,
and a `Location: /v1/extractions/{handle}/result` header.

```bash
base=http://127.0.0.1:8080
handle=$(curl -fsS "$base/v1/extractions" \
  -H 'Content-Type: application/json' -d '{"url":"https://example.com"}' \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["handle"])')
curl -i "$base/v1/extractions/$handle/result?timeout=30"
curl -i -X DELETE "$base/v1/extractions/$handle"
```

GET waits for the shared extraction. `timeout` defaults to 30 seconds and accepts 0 through 1200.
A wait timeout returns `202` with an empty body. Call GET again to continue waiting.
Multiple callers can wait on one handle. A disconnected caller removes only its waiter.
Successful GET returns `200` with this shape:

```json
{
  "url": "https://example.com/",
  "title": "Example Domain",
  "text": "Example Domain",
  "truncated": false,
  "observed_at": "2026-11-07T12:00:00Z"
}
```

The generic adapter reads the final URL, page title and rendered body text after `DOMContentLoaded`.
`observed_at` is the UTC observation time. Site-specific adapters and solvers are not integrated.

| Response | Meaning                                                    |
| -------- | ---------------------------------------------------------- |
| `404`    | GET used an unknown, closed or expired handle.             |
| `409`    | An `Idempotency-Key` belongs to a different URL.           |
| `422`    | The request URL, header or timeout is invalid.             |
| `502`    | Extraction failed. The body contains `code` and `message`. |
| `503`    | Handle capacity or the Uvicorn concurrency limit is full.  |

Optional `Idempotency-Key` reuses a retained handle for the same URL without extending its lifetime.
DELETE returns `204`, including for an unknown handle. It cancels extraction and awaits cleanup.

## Handles expire and browser contexts close

The service removes each handle 1200 seconds after creation and cancels unfinished work.
It retains results and errors until close or expiry. Restart loses all handles and work.
It retains at most 128 handles and runs at most four browser contexts at once.
Each extraction closes its context on completion, failure or cancellation. Shutdown drains extraction cleanup.
An adapter implements async `Extractor.extract` in [page.py](page.py) and must release resources on cancellation.

The adapter limits URL, title and text to 8192, 4096 and 100000 characters respectively.
`truncated` reports whether any field exceeds its limit. Navigation and body text each have a 60-second timeout.

## Run the component checks

From `webdata/`, run `make check right test smoke`.
`check` runs Ruff, `right` runs basedpyright, `test` runs lifecycle and HTTP tests, and `smoke` runs the browser smoke test.
Both test targets require Chromium. Tests use local page fixtures.
