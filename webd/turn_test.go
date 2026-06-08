package main

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// authorizeTurn is the data-isolation boundary between chat widgets: a turn is
// reachable only through the route-token whose JID owns it. (a) an unknown token
// 404s; (b) a turn owned by a DIFFERENT token's JID 404s even with a valid token
// — otherwise widget A could read widget B's turn frames.
func TestTurnSnapshot_CrossTokenIsolation(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "alpha", "Alpha")
	seedGroup(t, st, "bravo", "Bravo")
	tokA := seedChatToken(t, st, "alpha")
	tokB := seedChatToken(t, st, "bravo")

	// A turn that belongs to alpha's JID only.
	if err := st.PutMessage(core.Message{
		ID: "turnA", ChatJID: "web:alpha", Content: "secret",
		Timestamp: time.Now(), Topic: "tt", TurnID: "turnA",
	}); err != nil {
		t.Fatalf("put turnA: %v", err)
	}

	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	// (a) unknown route-token → 404.
	resp, err := http.Get(srv.URL + "/chat/nope-not-a-token/turnA")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown token status = %d, want 404", resp.StatusCode)
	}

	// (b) tokB is valid but turnA belongs to alpha's JID → 404 (containment).
	resp, err = http.Get(srv.URL + "/chat/" + tokB + "/turnA")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-token turn status = %d, want 404 (isolation breach)", resp.StatusCode)
	}

	// Sanity: tokA (the owner) reaches its own turn.
	resp, err = http.Get(srv.URL + "/chat/" + tokA + "/turnA")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("owner token status = %d, want 200", resp.StatusCode)
	}
}

// A round_done for a different turn sharing the same topic must NOT close
// this turn's per-turn SSE stream; only the matching turn_id closes it.
func TestTurnSSE_RoundDoneFilteredByTurnID(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	tok := seedChatToken(t, st, "main")

	// Two pending turns sharing topic "tt" on the same chat JID.
	now := time.Now()
	if err := st.PutMessage(core.Message{
		ID: "turnA", ChatJID: "web:main", Content: "a",
		Timestamp: now, Topic: "tt", TurnID: "turnA",
	}); err != nil {
		t.Fatalf("put turnA: %v", err)
	}
	// One bot frame for turnA so the snapshot flushes headers immediately
	// (Do() returns once the response head is written).
	if err := st.PutMessage(core.Message{
		ID: "turnA-bot1", ChatJID: "web:main", Content: "hi",
		Timestamp: now.Add(time.Millisecond), Topic: "tt", TurnID: "turnA", BotMsg: true,
	}); err != nil {
		t.Fatalf("put turnA-bot1: %v", err)
	}

	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/chat/"+tok+"/turnA/sse", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		// Wrong turn's round_done — must be ignored by this stream.
		s.hub.publish("main", "tt", "round_done", `{"turn_id":"turnB","status":"ok"}`)
		time.Sleep(50 * time.Millisecond)
		// This turn's round_done — must close the stream.
		s.hub.publish("main", "tt", "round_done", `{"turn_id":"turnA","status":"ok"}`)
	}()

	r := bufio.NewReader(resp.Body)
	sawWrong := false
	closedOnRight := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		line, err := r.ReadString('\n')
		if err != nil {
			closedOnRight = true
			break
		}
		if strings.Contains(line, `"turn_id":"turnB"`) {
			sawWrong = true
		}
	}
	if sawWrong {
		t.Error("stream forwarded a round_done for a different turn (turnB)")
	}
	if !closedOnRight {
		t.Error("stream did not close on matching round_done (turnA)")
	}
}
