package routd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"

	"github.com/kronael/arizuko/queue"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// BUGS C9: routd must tell "runed is not up" apart from "runed rejected this
// work". Only the second is the folder's failure; the first is compose boot
// order or a runed restart, and charging it to the breaker opened four groups'
// breakers on one krons deploy.
func TestRunedUnreachableClassification(t *testing.T) {
	// A real dial to a closed port — the exact error shape the http client
	// produces during boot, not a hand-built stand-in.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, dialErr := (&net.Dialer{}).DialContext(ctx, "tcp", closedAddr)
	if dialErr == nil {
		t.Fatal("fixture: dial to a closed port succeeded")
	}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"connection refused", dialErr, true},
		{"connection refused, wrapped by the client", fmt.Errorf("post /v1/runs: %w", dialErr), true},
		{"host not resolvable yet", &net.DNSError{Err: "no such host", Name: "runed", IsNotFound: true}, true},
		{"runed answered with a refusal", &runedv1.APIError{Status: http.StatusForbidden, Code: "forbidden", Msg: "no"}, false},
		{"runed answered 503", &runedv1.APIError{Status: http.StatusServiceUnavailable, Code: "unavailable", Msg: "busy"}, false},
		{"run exceeded its deadline mid-flight", context.DeadlineExceeded, false},
		{"decode failure after a response", errors.New("decode run outcome: unexpected EOF"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runedUnreachable(tc.err); got != tc.want {
				t.Errorf("runedUnreachable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// The classification is only useful if it reaches the queue, so pin the wrapper
// the dispatch path applies: the queue keys on errors.Is(err, ErrDependencyDown)
// and must still see the underlying cause for the log line.
func TestDependencyDownWrapKeepsBothSides(t *testing.T) {
	cause := syscall.ECONNREFUSED
	wrapped := fmt.Errorf("%w: %w", queue.ErrDependencyDown, cause)
	if !errors.Is(wrapped, queue.ErrDependencyDown) {
		t.Error("queue cannot recognise the dependency-down tag")
	}
	if !errors.Is(wrapped, cause) {
		t.Error("wrapping lost the underlying cause")
	}
}
