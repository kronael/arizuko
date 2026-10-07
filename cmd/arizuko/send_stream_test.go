package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// `arizuko send --wait` blocks on the round. Both wait paths -- SSE and the
// polling fallback -- decide when to stop from frames the server sends, and
// neither had a test. A frame the parser does not recognise is not a wrong
// answer: it is a CLI that never returns.

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

// pollRound — the fallback for a client with no SSE.

// The poller advances its cursor with last_frame_id. Without that the same
// frames reprint every second forever.
func TestPollRoundAdvancesItsCursorAndStopsOnSuccess(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		after := r.URL.Query().Get("after")
		seen = append(seen, after)
		switch after {
		case "":
			_, _ = fmt.Fprint(w, `{"status":"running","last_frame_id":"f1","frames":[{"content":"one"}]}`)
		case "f1":
			_, _ = fmt.Fprint(w, `{"status":"success","last_frame_id":"f2","frames":[{"content":"two"}]}`)
		default:
			t.Errorf("unexpected cursor %q", after)
			_, _ = fmt.Fprint(w, `{"status":"failed"}`)
		}
	}))
	t.Cleanup(srv.Close)

	done := make(chan int, 1)
	go func() { done <- pollRound(srv.Client(), srv.URL) }()
	select {
	case got := <-done:
		if got != 0 {
			t.Errorf("pollRound = %d, want 0", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pollRound never returned; the CLI would hang")
	}

	if len(seen) != 2 || seen[0] != "" || seen[1] != "f1" {
		t.Errorf("cursors = %v, want [\"\" \"f1\"] — the poller must advance", seen)
	}
}

func TestPollRoundReturnsOneOnFailedAndTwoOnTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"status":"failed"}`)
	}))
	t.Cleanup(srv.Close)
	if got := pollRound(srv.Client(), srv.URL); got != 1 {
		t.Errorf("pollRound on a failed round = %d, want 1", got)
	}

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url, client := dead.URL, dead.Client()
	dead.Close()
	if got := pollRound(client, url); got != 2 {
		t.Errorf("pollRound against a closed server = %d, want 2", got)
	}
}

// A snapshot body the poller cannot read carries no status, and the loop used
// to poll on for one forever — the same "CLI that never returns" this file was
// written against, reached through a bad body instead of a bad frame.
func TestPollRoundStopsOnAnUnreadableSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{not json`)
	}))
	t.Cleanup(srv.Close)

	done := make(chan int, 1)
	go func() { done <- pollRound(srv.Client(), srv.URL) }()
	select {
	case got := <-done:
		if got != 2 {
			t.Errorf("pollRound on an unreadable snapshot = %d, want 2", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pollRound never returned; the CLI would hang")
	}
}
