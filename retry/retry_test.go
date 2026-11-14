package retry

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestJavaScriptPolicy(t *testing.T) {
	script := `
import assert from 'node:assert/strict';
import fixtures from './fixtures.json';
import { httpFailure, next, notice } from './retry.mjs';
for (const c of fixtures)
  assert.deepEqual(next(c.failures, { kind: c.kind, retryAt: c.retryAt }, c.now, c.deadline),
    { failures: c.wantFailures, nextAt: c.wantNextAt, stop: c.stop });
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
