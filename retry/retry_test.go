package retry

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

type statusError int

func (e statusError) Error() string       { return "http failure" }
func (e statusError) HTTPStatusCode() int { return int(e) }

type retryAfterError struct{ error }

func (e retryAfterError) HTTPRetryAfter() string { return "7200" }
func (e retryAfterError) Unwrap() error          { return e.error }

func TestFromError(t *testing.T) {
	now := time.Unix(1000000, 0)
	status := statusError(503)
	fact := &Error{Failure: Failure{Kind: "permanent"}, Err: status}
	wrapped := fmt.Errorf("outer: %w", fact)
	if got := FromError(wrapped, now); got.Kind != "permanent" {
		t.Fatalf("explicit fact: %+v", got)
	}
	var found statusError
	if !errors.As(wrapped, &found) || found != status {
		t.Fatalf("cause not preserved: %v", wrapped)
	}
	if !errors.Is(fact, status) || fact.Error() != status.Error() {
		t.Fatal("error did not preserve cause")
	}
	for _, c := range []struct {
		name string
		err  error
		kind string
	}{
		{"status", fmt.Errorf("wrapped: %w", status), "transient"},
		{"unknown", errors.New("unknown"), "permanent"},
		{"canceled fact", &Error{Failure: Failure{Kind: "transient"}, Err: context.Canceled}, "canceled"},
		{"timeout", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), "timeout"},
		{"transport", fmt.Errorf("wrapped: %w", syscall.ECONNRESET), "transient"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := FromError(c.err, now); got.Kind != c.kind {
				t.Fatalf("got %+v, want %s", got, c.kind)
			}
		})
	}
	rateLimit := retryAfterError{statusError(429)}
	got := FromError(fmt.Errorf("wrapped: %w", rateLimit), now)
	if got.Kind != "transient" || !got.RetryAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("rate limit: %+v", got)
	}
	if decision := Next(0, got, now, time.Time{}); !decision.NextAt.Equal(got.RetryAt) {
		t.Fatalf("retry lower bound: %+v", decision)
	}
}

type falseTimeout struct{ error }

func (e falseTimeout) Timeout() bool   { return false }
func (e falseTimeout) Temporary() bool { return false }
func (e falseTimeout) Unwrap() error   { return e.error }

func TestHTTPFailureErrorTree(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		kind string
	}{
		{"joined deadline", fmt.Errorf("%w: %w", errors.New("dependency down"), context.DeadlineExceeded), "timeout"},
		{"joined DNS", errors.Join(errors.New("dependency down"), &net.DNSError{Err: "lookup failed"}), "transient"},
		{"joined DNS timeout", errors.Join(errors.New("dependency down"), &net.DNSError{Err: "lookup failed", IsTimeout: true}), "timeout"},
		{"wrapped timeout", &url.Error{Op: "GET", Err: fmt.Errorf("wrapped: %w", os.ErrDeadlineExceeded)}, "timeout"},
		{"nested false timeout", falseTimeout{context.DeadlineExceeded}, "timeout"},
		{"joined transport", errors.Join(errors.New("dependency down"), syscall.ECONNRESET), "transient"},
		{"canceled before timeout", errors.Join(context.DeadlineExceeded, context.Canceled), "canceled"},
		{"explicit permanent before timeout", errors.Join(context.DeadlineExceeded, &Error{Failure: Failure{Kind: "permanent"}, Err: errors.New("refused")}), "permanent"},
		{"joined TLS", errors.Join(errors.New("dependency down"), x509.UnknownAuthorityError{}), "permanent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FromError(tc.err, time.Now()); got.Kind != tc.kind {
				t.Fatalf("failure = %+v, want %s", got, tc.kind)
			}
		})
	}
}

func TestHTTPFailureCombinedFacts(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	timeout := fmt.Errorf("read: %w", context.DeadlineExceeded)
	for _, status := range []int{429, 503} {
		for _, hint := range []string{"7200", now.Add(2 * time.Hour).Format(http.TimeFormat)} {
			got := HTTPFailure(status, hint, timeout, now)
			if got.Kind != "transient" || !got.RetryAt.Equal(now.Add(2*time.Hour)) {
				t.Errorf("status %d hint %q: %+v", status, hint, got)
			}
		}
	}
	for _, err := range []error{io.EOF, syscall.ECONNRESET, timeout} {
		got := HTTPFailure(400, "7200", err, now)
		if got.Kind != "permanent" || !got.RetryAt.IsZero() {
			t.Errorf("400 %v: %+v", err, got)
		}
	}
	for _, tc := range []struct {
		status int
		err    error
		kind   string
	}{
		{503, errors.Join(timeout, context.Canceled), "canceled"},
		{0, errors.Join(errors.New("outer"), timeout), "timeout"},
		{0, syscall.ECONNRESET, "transient"},
	} {
		got := HTTPFailure(tc.status, "7200", tc.err, now)
		if got.Kind != tc.kind || (tc.kind == "canceled" && !got.RetryAt.IsZero()) {
			t.Errorf("%+v: %+v", tc, got)
		}
	}
	for _, tc := range []struct {
		err  error
		kind string
	}{
		{errors.Join(statusError(503), timeout, &Error{Failure: Failure{Kind: "permanent"}, Err: errors.New("explicit")}), "permanent"},
		{&Error{Failure: Failure{Kind: "transient"}, Err: context.Canceled}, "canceled"},
	} {
		if got := FromError(tc.err, now); got.Kind != tc.kind || !got.RetryAt.IsZero() {
			t.Errorf("explicit %v: %+v", tc.err, got)
		}
	}
	script := `
import assert from 'node:assert/strict';
import { httpFailure } from './retry.mjs';
const now = Date.parse('2026-11-14T12:00:00Z');
const timeout = { cause: { cause: { code: 'ETIMEDOUT' } } };
for (const status of [429, 503])
 for (const hint of ['7200', new Date(now + 7200000).toUTCString()])
  assert.deepEqual(httpFailure(status, hint, timeout, now), {kind:'transient', retryAt:now + 7200000});
for (const error of [{code:'ECONNRESET'}, timeout])
 assert.deepEqual(httpFailure(400, '7200', error, now), {kind:'permanent', retryAt:0});
assert.deepEqual(httpFailure(503, '7200', {cause:{cause:{name:'AbortError'}}}, now), {kind:'canceled', retryAt:0});
assert.equal(httpFailure(0, '', timeout, now).kind, 'timeout');
assert.equal(httpFailure(0, '', {cause:{cause:{code:'ECONNRESET'}}}, now).kind, 'transient');
`
	if output, err := exec.Command("bun", "-e", script).CombinedOutput(); err != nil {
		t.Errorf("JavaScript facts: %v\n%s", err, output)
	}
}

func TestRetryAfterParity(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		Hint      string
		Now, Want int64
	}{
		{"", now.UnixMilli(), 0}, {"invalid", now.UnixMilli(), 0},
		{"+30", now.UnixMilli(), 0}, {"-1", now.UnixMilli(), 0}, {"1.5", now.UnixMilli(), 0},
		{"2026-11-14", now.UnixMilli(), 0}, {"2026-11-14T12:00:00Z", now.UnixMilli(), 0},
		{"Sat, 31 Feb 2026 12:00:00 GMT", now.UnixMilli(), 0},
		{"Sat, 14 Nov 2026 24:00:00 GMT", now.UnixMilli(), 0}, {"Sat, 14 Nov 2026 12:60:00 GMT", now.UnixMilli(), 0},
		{"0", now.UnixMilli(), now.UnixMilli()}, {"7200", now.UnixMilli(), now.Add(2 * time.Hour).UnixMilli()},
		{"999999999999999999999999999999999", now.UnixMilli(), 253402300799000},
		{"  Sat, 14 Nov 2026 14:00:00 GMT  ", now.UnixMilli(), now.Add(2 * time.Hour).UnixMilli()},
		{"Saturday, 14-Nov-26 14:00:00 GMT", now.UnixMilli(), now.Add(2 * time.Hour).UnixMilli()},
		{"Sat Nov 14 14:00:00 2026", now.UnixMilli(), now.Add(2 * time.Hour).UnixMilli()},
		{"Sun Nov  6 08:49:37 1994", now.UnixMilli(), time.Date(1994, 11, 6, 8, 49, 37, 0, time.UTC).UnixMilli()},
		{"Saturday, 14-Nov-26 14:00:00 UTC", now.UnixMilli(), 0},
		{"sat, 14 nov 2026 14:00:00 GMT", now.UnixMilli(), now.Add(2 * time.Hour).UnixMilli()},
		{"Sun Nov 01 14:00:00 2026", now.UnixMilli(), time.Date(2026, 11, 1, 14, 0, 0, 0, time.UTC).UnixMilli()},
		{"Thursday, 06-Nov-70 08:49:37 GMT", now.UnixMilli(), time.Date(2070, 11, 6, 8, 49, 37, 0, time.UTC).UnixMilli()},
		{"Saturday, 14-Nov-76 11:59:59 GMT", now.UnixMilli(), time.Date(2076, 11, 14, 11, 59, 59, 0, time.UTC).UnixMilli()},
		{"Saturday, 14-Nov-76 12:00:00 GMT", now.UnixMilli(), time.Date(2076, 11, 14, 12, 0, 0, 0, time.UTC).UnixMilli()},
		{"Saturday, 14-Nov-76 12:00:01 GMT", now.UnixMilli(), time.Date(1976, 11, 14, 12, 0, 1, 0, time.UTC).UnixMilli()},
		{"Sunday, 06-Nov-40 08:49:37 GMT", time.Date(1994, 11, 14, 12, 0, 0, 0, time.UTC).UnixMilli(), time.Date(2040, 11, 6, 8, 49, 37, 0, time.UTC).UnixMilli()},
		{"Sunday, 29-Feb-00 08:49:37 GMT", time.Date(2050, 11, 14, 12, 0, 0, 0, time.UTC).UnixMilli(), 0},
		{"Sunday, 29-Feb-00 08:49:37 GMT", time.Date(2050, 1, 1, 12, 0, 0, 0, time.UTC).UnixMilli(), time.Date(2000, 2, 29, 8, 49, 37, 0, time.UTC).UnixMilli()},
	}
	for _, c := range cases {
		got := HTTPFailure(429, c.Hint, nil, time.UnixMilli(c.Now)).RetryAt
		var ms int64
		if !got.IsZero() {
			ms = got.UnixMilli()
		}
		if ms != c.Want {
			t.Errorf("Go %q: got %d, want %d", c.Hint, ms, c.Want)
		}
	}
	data, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	script := `import assert from 'node:assert/strict'; import {httpFailure} from './retry.mjs';
 for (const c of JSON.parse(process.argv[1])) assert.equal(httpFailure(429, c.Hint, null, c.Now).retryAt, c.Want, c.Hint);`
	cmd := exec.Command("bun", "-e", script, string(data))
	cmd.Env = append(os.Environ(), "TZ=America/New_York")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("JavaScript: %v\n%s", err, output)
	}
}

func TestJavaScriptPolicy(t *testing.T) {
	script := `
import assert from 'node:assert/strict';
import fixtures from './fixtures.json';
import { httpFailure, providerFailure, next, notice } from './retry.mjs';
for (const c of fixtures)
  assert.deepEqual(next(c.failures, { kind: c.kind, retryAt: c.retryAt }, c.now, c.deadline),
    { failures: c.wantFailures, nextAt: c.wantNextAt, stop: c.stop });
for (const c of fixtures) if (c.reason !== undefined) assert.deepEqual(providerFailure(c.reason, c.resetAt, c.now), { kind: c.kind, retryAt: c.retryAt });
for (const status of [408, 425, 429, 500, 599]) assert.equal(httpFailure(status, '', null, 1000000).kind, 'transient');
for (const status of [400, 600]) assert.equal(httpFailure(status, '', null, 1000000).kind, 'permanent');
assert.equal(httpFailure(0, '', new Error('validation'), 1000000).kind, 'permanent');
assert.equal(httpFailure(0, '', new TypeError('validation'), 1000000).kind, 'permanent');
assert.equal(httpFailure(0, '', new TypeError('Failed to fetch'), 1000000).kind, 'transient');
assert.equal(httpFailure(0, '', new TypeError('NetworkError when attempting to fetch resource.'), 1000000).kind, 'transient');
assert.equal(httpFailure(0, '', { code: 'ECONNRESET' }, 1000000).kind, 'transient');
assert.equal(httpFailure(0, '', { code: 'ETIMEDOUT' }, 1000000).kind, 'timeout');
assert.equal(httpFailure(500, '', { name: 'AbortError' }, 1000000).kind, 'canceled');
for (const value of ['7200', new Date(8200000).toUTCString()]) assert.equal(httpFailure(429, value, null, 1000000).retryAt, 8200000);
assert.equal(httpFailure(429, '999999999999999999999999999999999', null, 1000000).retryAt, 253402300799000);
assert.equal(notice('Delivery failed', { failures: 1, nextAt: 0, stop: true }), 'Delivery failed. Automatic retries stopped.');
assert.equal(notice('Delivery failed', { failures: 1, nextAt: 1030000, stop: false }), 'Delivery failed. Retry scheduled.');
`
	output, err := exec.Command("bun", "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("JavaScript policy: %v\n%s", err, output)
	}
}

func TestPolicy(t *testing.T) {
	data, err := os.ReadFile("fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Failures               int
		Kind                   string
		Reason                 *string
		ResetAt                int64
		Now, RetryAt, Deadline int64
		WantFailures           int
		WantNextAt             int64
		Stop                   bool
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	toTime := func(ms int64) time.Time {
		if ms == 0 {
			return time.Time{}
		}
		return time.UnixMilli(ms)
	}
	for _, c := range cases {
		if c.Reason != nil {
			got := ProviderFailure(*c.Reason, toTime(c.ResetAt), toTime(c.Now))
			if got.Kind != c.Kind || !got.RetryAt.Equal(toTime(c.RetryAt)) {
				t.Errorf("provider %+v: %+v", c, got)
			}
		}
		got := Next(c.Failures, Failure{Kind: c.Kind, RetryAt: toTime(c.RetryAt)}, toTime(c.Now), toTime(c.Deadline))
		if got.Failures != c.WantFailures || !got.NextAt.Equal(toTime(c.WantNextAt)) || got.Stop != c.Stop {
			t.Errorf("%+v: got %+v", c, got)
		}
	}
	now := time.Unix(1000000, 0)
	for _, c := range []struct {
		status int
		err    error
		kind   string
	}{
		{408, nil, "transient"}, {425, nil, "transient"}, {429, nil, "transient"}, {500, nil, "transient"}, {599, nil, "transient"},
		{400, nil, "permanent"}, {600, nil, "permanent"}, {0, errors.New("validation"), "permanent"},
		{0, syscall.ECONNRESET, "transient"}, {0, context.DeadlineExceeded, "timeout"}, {500, context.Canceled, "canceled"},
		{0, &url.Error{Op: "Get", Err: errors.New("validation")}, "permanent"},
		{0, &url.Error{Op: "Get", Err: x509.UnknownAuthorityError{}}, "permanent"},
		{0, &url.Error{Op: "Get", Err: syscall.ECONNRESET}, "transient"},
		{0, &url.Error{Op: "Get", Err: syscall.EINVAL}, "permanent"},
		{0, &net.OpError{Op: "dial", Err: syscall.ENETUNREACH}, "transient"},
	} {
		if got := HTTPFailure(c.status, "", c.err, now); got.Kind != c.kind {
			t.Errorf("%+v: %+v", c, got)
		}
	}
	if got := HTTPFailure(429, "999999999999999999999999999999999", nil, now); got.RetryAt.UnixMilli() != 253402300799000 {
		t.Errorf("huge retry-after: %+v", got)
	}
	for _, value := range []string{"7200", now.Add(2 * time.Hour).UTC().Format(http.TimeFormat)} {
		if got := HTTPFailure(429, value, nil, now); !got.RetryAt.Equal(now.Add(2 * time.Hour)) {
			t.Errorf("retry-after %q: %+v", value, got)
		}
	}
	if got := Notice("Delivery failed", Decision{Stop: true}); got != "Delivery failed. Automatic retries stopped." {
		t.Error(got)
	}
	if got := Notice("Delivery failed", Decision{}); got != "Delivery failed. Retry scheduled." {
		t.Error(got)
	}
}

func TestWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Wait(ctx, time.Now().Add(time.Hour)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for range 100 {
		if err := Wait(ctx, time.Now().Add(-time.Hour)); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
}

func TestDecisionEpisode(t *testing.T) {
	now := time.Unix(1000000, 0)
	var decision Decision
	if !decision.IsDue(now) {
		t.Fatal("zero decision must be due")
	}
	decision.Record(statusError(503), now, time.Time{})
	if decision.Failures != 1 || decision.Stop || !decision.NextAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("first failure: %+v", decision)
	}
	if decision.IsDue(decision.NextAt.Add(-time.Nanosecond)) || !decision.IsDue(decision.NextAt) {
		t.Fatalf("due boundary: %+v", decision)
	}
	decision.Record(statusError(503), decision.NextAt, time.Time{})
	if decision.Failures != 2 || decision.Stop || !decision.NextAt.Equal(now.Add(90*time.Second)) {
		t.Fatalf("second failure: %+v", decision)
	}
	decision.Record(nil, now, time.Time{})
	if decision != (Decision{}) || !decision.IsDue(now) {
		t.Fatalf("success reset: %+v", decision)
	}
	decision.Record(errors.New("permanent"), now, time.Time{})
	if !decision.Stop || decision.IsDue(now.Add(time.Hour)) {
		t.Fatalf("terminal: %+v", decision)
	}
	decision.Record(nil, now, time.Time{})
	decision.Record(statusError(503), now, now.Add(30*time.Second))
	if !decision.Stop || decision.Failures != 1 || !decision.NextAt.IsZero() || decision.IsDue(now.Add(time.Hour)) {
		t.Fatalf("deadline: %+v", decision)
	}
}
