package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleSSEFrameStopsOnlyOnRoundDone(t *testing.T) {
	cases := []struct {
		name, event, data string
		want              int
	}{
		{"a successful round ends the wait", "round_done", `{"status":"success"}`, 0},
		{"a failed round exits non-zero", "round_done", `{"status":"failed","error":"boom"}`, 1},
		{"an unknown round status is a failure", "round_done", `{"status":"weird"}`, 1},
		{"an empty round_done payload is a failure", "round_done", ``, 1},

		{"a message frame keeps waiting", "message", `{"content":"hi","kind":"text"}`, -1},
		{"a status frame keeps waiting", "status", `{"content":"thinking"}`, -1},
		{"an unparseable message keeps waiting", "message", `{not json`, -1},
		{"an event we do not know keeps waiting", "heartbeat", ``, -1},
		{"an empty event keeps waiting", "", ``, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := handleSSEFrame(c.event, c.data); got != c.want {
				t.Errorf("handleSSEFrame(%q, %q) = %d, want %d", c.event, c.data, got, c.want)
			}
		})
	}
}

// sseServer replays a canned event stream.
func sseServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept = %q, want text/event-stream", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestStreamRoundReadsFramesUntilRoundDone(t *testing.T) {
	srv := sseServer(t, 200, strings.Join([]string{
		"event: message",
		`data: {"content":"working"}`,
		"",
		"event: message",
		`data: {"content":"still working"}`,
		"",
		"event: round_done",
		`data: {"status":"success"}`,
		"",
		"",
	}, "\n"))

	if got := streamRound(srv.Client(), srv.URL); got != 0 {
		t.Errorf("streamRound = %d, want 0 for a successful round", got)
	}
}

func TestStreamRoundReturnsOneOnAFailedRound(t *testing.T) {
	srv := sseServer(t, 200, "event: round_done\ndata: {\"status\":\"failed\",\"error\":\"container timeout\"}\n\n")
	if got := streamRound(srv.Client(), srv.URL); got != 1 {
		t.Errorf("streamRound = %d, want 1 for a failed round", got)
	}
}

// A stream that ends without a round_done -- the server restarted, the proxy
// cut it -- must exit 2 rather than 0. Reporting success for a round nobody
// saw finish is the one wrong answer here.
func TestStreamRoundReturnsTwoWhenTheStreamEndsEarly(t *testing.T) {
	srv := sseServer(t, 200, "event: message\ndata: {\"content\":\"hi\"}\n\n")
	if got := streamRound(srv.Client(), srv.URL); got != 2 {
		t.Errorf("streamRound = %d, want 2 for a truncated stream", got)
	}
}

func TestStreamRoundReportsANon200AndAnUnreachableServer(t *testing.T) {
	srv := sseServer(t, http.StatusForbidden, "denied")
	if got := streamRound(srv.Client(), srv.URL); got != 2 {
		t.Errorf("streamRound on a 403 = %d, want 2", got)
	}

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	client := dead.Client()
	dead.Close()
	if got := streamRound(client, url); got != 2 {
		t.Errorf("streamRound against a closed server = %d, want 2", got)
	}
}

func TestStreamRoundOutlivesRequestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(80 * time.Millisecond)
		_, _ = fmt.Fprint(w, "event: round_done\ndata: {\"status\":\"success\"}\n\n")
	}))
	t.Cleanup(srv.Close)
	client := srv.Client()
	client.Timeout = 20 * time.Millisecond
	if got := streamRound(client, srv.URL); got != 0 {
		t.Fatalf("stream ended at request timeout: exit %d", got)
	}
	if client.Timeout != 20*time.Millisecond {
		t.Fatal("stream changed caller's request timeout")
	}
}
