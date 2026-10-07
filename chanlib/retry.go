package chanlib

import (
	"bytes"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// retryBackoffs lists base sleeps between attempts; max total attempts is
// len+1. A var so tests can shrink it.
var retryBackoffs = []time.Duration{300 * time.Millisecond, 800 * time.Millisecond}

// retryMaxRetryAfter caps the Retry-After wait so a misbehaving upstream
// cannot stall a caller; a larger value falls back to the backoff table.
var retryMaxRetryAfter = 30 * time.Second

// RetryPolicy says which failures a request may be retried on.
type RetryPolicy int

const (
	// RetryTransient retries 429, 5xx and transport errors. Correct for a read
	// or for a mutation addressed by target id.
	RetryTransient RetryPolicy = iota
	// RetryRateLimitOnly retries ONLY a 429. Use it for a create call the
	// platform gives no idempotency key for: a 429 is a documented refusal —
	// nothing was created and Retry-After says when to try again — while a 5xx
	// or a dropped connection may have created the thing already, so resending
	// posts it twice (BUGS D5).
	RetryRateLimitOnly
)

// DoWithRetry retries transient failures. See DoWithPolicy.
func DoWithRetry(client *http.Client, req *http.Request) (*http.Response, error) {
	return DoWithPolicy(client, req, RetryTransient)
}

// DoWithPolicy retries per policy with jittered backoff. On 429 it honours
// Retry-After (seconds or HTTP-date) when <= retryMaxRetryAfter. Returns the
// final response or error; the caller still closes Body.
func DoWithPolicy(client *http.Client, req *http.Request, policy RetryPolicy) (*http.Response, error) {
	if req.Body != nil && req.GetBody == nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(b))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(b)), nil
		}
	}

	attempts := len(retryBackoffs) + 1
	for i := range attempts {
		if i > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}
		resp, err := client.Do(req)
		if err != nil {
			if i == attempts-1 || policy == RetryRateLimitOnly {
				return nil, err
			}
			sleepJittered(retryBackoffs[i])
			continue
		}
		if resp.StatusCode == 429 {
			if i == attempts-1 {
				return resp, nil
			}
			wait, ok := parseRetryAfter(resp.Header.Get("Retry-After"))
			if !ok || wait > retryMaxRetryAfter {
				wait = retryBackoffs[min(i, len(retryBackoffs)-1)]
			}
			_ = resp.Body.Close()
			sleepJittered(wait)
			continue
		}
		if resp.StatusCode >= 500 {
			if i == attempts-1 || policy == RetryRateLimitOnly {
				return resp, nil
			}
			_ = resp.Body.Close()
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
		d := max(time.Until(t), 0)
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
