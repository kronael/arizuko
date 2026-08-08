package v1

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCall_PreservesPerMethodDifferences locks the three ways the five methods
// legitimately differ, which the shared `call` now carries as parameters: only
// Run and Hold inject trace context, only body-carrying requests set
// Content-Type, and every method keeps its own decode-error label.
func TestCall_PreservesPerMethodDifferences(t *testing.T) {
	var gotCT, gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), Token: "t"}
	ctx := context.Background()

	if _, err := c.Run(ctx, RunRequest{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotCT != "application/json" || gotMethod != http.MethodPost || gotPath != "/v1/runs" {
		t.Fatalf("Run sent %s %s ct=%q", gotMethod, gotPath, gotCT)
	}

	// DELETE carries no body, so it must not claim one.
	if err := c.ReleaseHold(ctx, "r1"); err != nil {
		t.Fatalf("ReleaseHold: %v", err)
	}
	if gotCT != "" || gotMethod != http.MethodDelete {
		t.Fatalf("ReleaseHold sent %s ct=%q, want DELETE with no Content-Type", gotMethod, gotCT)
	}

	if _, err := c.RecentSessions(ctx, "atlas", 3); err != nil {
		t.Fatalf("RecentSessions: %v", err)
	}
	if gotCT != "" || gotMethod != http.MethodGet {
		t.Fatalf("RecentSessions sent %s ct=%q, want GET with no Content-Type", gotMethod, gotCT)
	}
}

// TestCall_DecodeErrorNamesTheMethod: a garbled body must say WHICH call failed
// to decode, or an operator reading the log cannot tell runed's routes apart.
func TestCall_DecodeErrorNamesTheMethod(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), Token: "t"}

	if _, err := c.Run(context.Background(), RunRequest{}); err == nil ||
		!strings.Contains(err.Error(), "decode run outcome") {
		t.Fatalf("Run decode error = %v, want it to name 'run outcome'", err)
	}
	if _, err := c.RecentSessions(context.Background(), "f", 0); err == nil ||
		!strings.Contains(err.Error(), "decode recent sessions") {
		t.Fatalf("RecentSessions decode error = %v, want it to name 'recent sessions'", err)
	}
}

// TestCall_NonOKBecomesAPIError: the Err envelope is what routd keys on to tell
// a transport failure from a clean outcome:error, so a non-200 must not arrive
// as a bare error.
func TestCall_NonOKBecomesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"denied","message":"nope"}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), Token: "t"}

	_, err := c.Run(context.Background(), RunRequest{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %T %v, want *APIError", err, err)
	}
	if apiErr.Status != http.StatusForbidden || apiErr.Code != "denied" || apiErr.Msg != "nope" {
		t.Fatalf("APIError = %+v", apiErr)
	}
	if err := c.ReleaseHold(context.Background(), "r1"); !errors.As(err, &apiErr) {
		t.Fatalf("ReleaseHold err = %T, want *APIError even with no body to decode", err)
	}
}
