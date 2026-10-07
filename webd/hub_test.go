package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
)

// subscribe wires a channel that receives publishes for folder/topic.
func TestHub_SubscribePublishReceive(t *testing.T) {
	h := newHub()
	ch, unsub := h.subscribe("main", "t1")
	defer unsub()

	h.publish("main", "t1", "message", `{"hi":1}`)
	select {
	case frame := <-ch:
		if !strings.Contains(frame, "event: message") || !strings.Contains(frame, `{"hi":1}`) {
			t.Errorf("frame = %q", frame)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("no frame received")
	}
}

// unsub removes the subscriber and deletes empty buckets.
func TestHub_UnsubCleansUp(t *testing.T) {
	h := newHub()
	_, unsub := h.subscribe("main", "t1")
	if len(h.subs) != 1 {
		t.Fatalf("expected 1 key, got %d", len(h.subs))
	}
	unsub()
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs["main/t1"]; ok {
		t.Error("subs map should drop empty bucket")
	}
}

// Publishes to an unrelated topic don't hit this subscriber.
func TestHub_TopicIsolation(t *testing.T) {
	h := newHub()
	ch, unsub := h.subscribe("main", "a")
	defer unsub()
	h.publish("main", "b", "message", `x`)
	h.publish("other", "a", "message", `x`)
	select {
	case frame := <-ch:
		t.Errorf("should not receive cross-topic frame: %q", frame)
	case <-time.After(80 * time.Millisecond):
		// ok — no frame.
	}
}

// When subscriber count per key exceeds cap, extra subscribe returns closed channel.
func TestHub_SubscribeCapPerKey(t *testing.T) {
	h := newHub()
	unsubs := make([]func(), 0, maxSubsPerKey)
	for range maxSubsPerKey {
		_, u := h.subscribe("f", "t")
		unsubs = append(unsubs, u)
	}
	ch, unsub := h.subscribe("f", "t")
	defer unsub()
	// Overflow channel must be closed.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("overflow channel should be closed")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("overflow channel should be closed immediately")
	}
	for _, u := range unsubs {
		u()
	}
}

// canSubscribe reports true when the hub is not at key cap.
func TestHub_CanSubscribe(t *testing.T) {
	h := newHub()
	if !h.canSubscribe() {
		t.Error("empty hub should allow subscribe")
	}
}

// Slow subscribers silently drop — publish doesn't block even when buffers full.
func TestHub_PublishDropsSlowSubscribers(t *testing.T) {
	h := newHub()
	_, unsub := h.subscribe("f", "t")
	defer unsub()
	done := make(chan struct{})
	go func() {
		for range 1000 {
			h.publish("f", "t", "m", "x")
		}
		close(done)
	}()
	select {
	case <-done:
		// ok — publish returned even with single slow subscriber.
	case <-time.After(1 * time.Second):
		t.Fatal("publish blocked on slow subscriber")
	}
}

// concurrent subscribe/unsub/publish doesn't race (go test -race catches it).
func TestHub_Concurrent(t *testing.T) {
	h := newHub()
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			for range 50 {
				_, u := h.subscribe("f", "t")
				h.publish("f", "t", "m", "x")
				u()
			}
		})
	}
	wg.Wait()
}

// GET /chat/stream with a valid token but no proxyd transit proof → 403.
// Holding the token is not enough to open a subscription: the request must have
// come through the front door. With a JWKS configured this is the only thing
// separating a widget visitor from a caller aimed straight at webd's port.
//
// It used to assert the same 403 for a request with no headers at all, which
// passed only because the fixture's nil KeySet made the old chatTransit gate
// fail on the MISSING stamp rather than on a missing proof. Configure the
// KeySet, and the test now fails if the proof stops being required.
func TestSlinkStream_Forbidden(t *testing.T) {
	s, _, st := newTestServer(t)
	k, err := auth.NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	s.ks = auth.NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})
	seedGroup(t, st, "main", "Main")
	tok := seedChatToken(t, st, "main")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/chat/stream?token=" + tok + "&topic=t")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

// The same request WITH proxyd's transit bearer streams. Anonymous — no
// X-User-*, no grant — which is the widget's ordinary case and the one that
// answered 403 on every instance until specs/6/22 step 1.
func TestChatStream_AnonThroughProxydAllowed(t *testing.T) {
	s, _, st := newTestServer(t)
	k, err := auth.NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	s.ks = auth.NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})
	transit, err := k.Sign(auth.TokenClaims{Sub: "service:proxyd", Typ: "service"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	seedGroup(t, st, "main", "Main")
	tok := seedChatToken(t, st, "main")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/chat/stream?token="+tok+"&topic=t", nil)
	req.Header.Set("Authorization", "Bearer "+transit)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 — an anonymous visitor through proxyd must stream", resp.StatusCode)
	}
}

// GET /chat/stream missing topic → 400; missing/unknown token → 404.
func TestSlinkStream_MissingParams(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/chat/stream")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("no topic: status = %d, want 400", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/chat/stream?topic=t")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("no token: status = %d, want 404", resp.StatusCode)
	}
}

// GET /chat/stream with an overlong topic → 400.
func TestSlinkStream_TopicTooLong(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	topic := strings.Repeat("x", maxTopicLen+1)
	resp, err := http.Get(srv.URL + "/chat/stream?topic=" + topic)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// GET /chat/stream with chat transit streams events keyed on the token's JID.
func TestSlinkStream_SlinkSigOK(t *testing.T) {
	s, _, st := newTestServer(t)
	_ = seedGroup(t, st, "main", "Main")
	tok := seedChatToken(t, st, "main")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET",
		srv.URL+"/chat/stream?token="+tok+"&topic=tt", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content-type = %q", resp.Header.Get("Content-Type"))
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		s.hub.publish("web:main", "tt", "message", `{"role":"assistant","id":"b1"}`)
	}()

	r := bufio.NewReader(resp.Body)
	got := false
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			break
		}
		if strings.Contains(line, `"role":"assistant"`) {
			got = true
			break
		}
	}
	if !got {
		t.Error("expected SSE frame on stream")
	}
}

// GET /chat/stream with user-sig auth + grant for the token's folder also works.
func TestSlinkStream_UserSigOK(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	tok := seedChatToken(t, st, "main")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	groups, _ := json.Marshal([]string{"main"})
	req, _ := http.NewRequestWithContext(ctx, "GET",
		srv.URL+"/chat/stream?token="+tok+"&topic=tt2", nil)
	for k, v := range signUserHeaders(map[string]string{
		"X-User-Sub":    "user:alice",
		"X-User-Name":   "Alice",
		"X-User-Groups": string(groups),
	}) {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// Reconnect with Last-Event-Id replays any messages since the anchor.
func TestSlinkStream_Replay(t *testing.T) {
	s, _, st := newTestServer(t)
	_ = seedGroup(t, st, "main", "Main")
	tok := seedChatToken(t, st, "main")

	now := time.Now()
	// Anchor — the one the client already saw.
	_ = st.PutMessage(core.Message{
		ID: "anchor", ChatJID: "web:main", Content: "anchor",
		Timestamp: now.Add(-2 * time.Second), Topic: "tt-replay",
	})
	// Missed messages after anchor.
	_ = st.PutMessage(core.Message{
		ID: "missed1", ChatJID: "web:main", Content: "missed-user",
		Timestamp: now.Add(-1 * time.Second), Topic: "tt-replay",
	})
	_ = st.PutMessage(core.Message{
		ID: "missed2", ChatJID: "web:main", Content: "missed-bot",
		Timestamp: now, Topic: "tt-replay", BotMsg: true,
	})

	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET",
		srv.URL+"/chat/stream?token="+tok+"&topic=tt-replay", nil)
	req.Header.Set("Last-Event-Id", "anchor")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	r := bufio.NewReader(resp.Body)
	gotMissed1, gotMissed2 := false, false
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) && (!gotMissed1 || !gotMissed2) {
		line, err := r.ReadString('\n')
		if err != nil {
			break
		}
		if strings.Contains(line, "missed-user") {
			gotMissed1 = true
		}
		if strings.Contains(line, "missed-bot") {
			gotMissed2 = true
		}
	}
	if !gotMissed1 || !gotMissed2 {
		t.Errorf("missed replay: user=%v bot=%v", gotMissed1, gotMissed2)
	}
}
