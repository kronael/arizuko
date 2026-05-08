package chanlib

import (
	"bytes"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// retryBackoffs lists base sleeps between attempts for 5xx responses and
// network errors. Length determines max total attempts = len+1. Exposed
// as a var so tests can shrink it.
var retryBackoffs = []time.Duration{300 * time.Millisecond, 800 * time.Millisecond}

// retryMaxRetryAfter caps Retry-After wait so a misbehaving upstream
// can't stall a caller for long. Values larger than this cause the
// retry loop to give up and return the response as-is.
var retryMaxRetryAfter = 30 * time.Second

// DoWithRetry retries on 5xx/429 with jittered backoff, max 3 total attempts.
// Accepts double-post risk on non-idempotent requests — caller's choice.
// On 429, respects Retry-After header (seconds or HTTP-date) if present and <= 30s.
// On 5xx, uses jittered exponential backoff: ~300ms, ~800ms.
// Returns the final response or final error. Caller still closes Body.
func DoWithRetry(client *http.Client, req *http.Request) (*http.Response, error) {
	if req.Body != nil && req.GetBody == nil {
		b, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(b))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(b)), nil
		}
	}

	attempts := len(retryBackoffs) + 1
	for i := 0; i < attempts; i++ {
		if i > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}
		resp, err := client.Do(req)
		if err != nil {
			if i == attempts-1 {
				return nil, err
			}
			sleepJittered(retryBackoffs[i])
			continue
		}
		if resp.StatusCode == 429 {
			wait, ok := parseRetryAfter(resp.Header.Get("Retry-After"))
			if !ok || wait > retryMaxRetryAfter {
				wait = retryBackoffs[min(i, len(retryBackoffs)-1)]
			}
			resp.Body.Close()
			if i == attempts-1 {
				// Re-issue to return a fresh response to caller.
				if req.GetBody != nil {
					if body, berr := req.GetBody(); berr == nil {
						req.Body = body
					}
				}
				return client.Do(req)
			}
			sleepJittered(wait)
			continue
		}
		if resp.StatusCode >= 500 {
			if i == attempts-1 {
				return resp, nil
			}
			resp.Body.Close()
			sleepJittered(retryBackoffs[i])
			continue
		}
		return resp, nil
	}
	return nil, nil // unreachable
}

func parseRetryAfter(h string) (time.Duration, bool) {
	if h == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(h); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(h); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

func sleepJittered(base time.Duration) {
	if base <= 0 {
		return
	}
	// ±20% jitter.
	jitter := time.Duration(rand.Int63n(int64(base)/5*2+1)) - base/5
	d := base + jitter
	if d < 0 {
		d = base
	}
	time.Sleep(d)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
