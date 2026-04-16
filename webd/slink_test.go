package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// POST without Accept=text/event-stream returns an HTML bubble fragment.
func TestSlinkPost_HTMLBubble(t *testing.T) {
	s, mr, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")

	req := httptest.NewRequest("POST", "/slink/"+g.SlinkToken,
		strings.NewReader("content=hello&topic=t1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("token", g.SlinkToken)
	w := httptest.NewRecorder()

	s.handleSlinkPost(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `class="msg user"`) {
		t.Errorf("body missing user bubble: %q", w.Body.String())
	}
	if got := mr.sent(); len(got) != 1 || got[0].Content != "hello" {
		t.Errorf("router did not receive message: %+v", got)
	}
}

// POST with Accept: text/event-stream holds the connection open and streams
// both the caller's own bubble and later hub publishes.
func TestSlinkPost_SSE_HeldOpen(t *testing.T) {
	s, _, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")

	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	body := strings.NewReader("content=hi&topic=t42")
	req, _ := http.NewRequestWithContext(ctx, "POST",
		srv.URL+"/slink/"+g.SlinkToken, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}

	// Publish a follow-up bot message on the same topic.
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.hub.publish(g.Folder, "t42", "message", `{"role":"assistant","content":"pong"}`)
	}()

	var gotUser, gotAssistant bool
	r := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) && !(gotUser && gotAssistant) {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			break
		}
		if strings.Contains(line, `"role":"user"`) {
			gotUser = true
		}
		if strings.Contains(line, `"role":"assistant"`) {
			gotAssistant = true
		}
	}
	if !gotUser {
		t.Error("expected user bubble on SSE stream")
	}
	if !gotAssistant {
		t.Error("expected assistant message on SSE stream")
	}
}

// POST with Accept: application/json returns {user: {...}} and no SSE frame.
func TestSlinkPost_JSON(t *testing.T) {
	s, mr, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")

	req := httptest.NewRequest("POST", "/slink/"+g.SlinkToken,
		strings.NewReader("content=ping&topic=t-json"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetPathValue("token", g.SlinkToken)
	w := httptest.NewRecorder()

	s.handleSlinkPost(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Result().Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v — body=%s", err, w.Body.String())
	}
	user, ok := got["user"].(map[string]any)
	if !ok {
		t.Fatalf("missing user: %+v", got)
	}
	if user["content"] != "ping" || user["role"] != "user" || user["topic"] != "t-json" {
		t.Errorf("user payload wrong: %+v", user)
	}
	if _, has := got["assistant"]; has {
		t.Errorf("assistant unexpectedly present without ?wait: %+v", got)
	}
	if len(mr.sent()) != 1 {
		t.Errorf("router did not receive exactly one message: %+v", mr.sent())
	}
}

// POST ?wait=<sec> with Accept: application/json blocks until an assistant
// reply is published on the hub, then returns {user, assistant}.
func TestSlinkPost_JSON_WaitReturnsAssistant(t *testing.T) {
	s, _, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")

	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	// Publish an assistant reply shortly after POST arrives.
	go func() {
		time.Sleep(80 * time.Millisecond)
		s.hub.publish(g.Folder, "t-wait", "message",
			`{"role":"assistant","content":"pong","id":"bot-1"}`)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "POST",
		srv.URL+"/slink/"+g.SlinkToken+"?wait=2",
		strings.NewReader("content=ping&topic=t-wait"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	user, _ := got["user"].(map[string]any)
	if user == nil || user["content"] != "ping" {
		t.Fatalf("missing/bad user: %+v", got)
	}
	assistant, _ := got["assistant"].(map[string]any)
	if assistant == nil {
		t.Fatalf("missing assistant (wait mode should return it): %+v", got)
	}
	if assistant["content"] != "pong" || assistant["role"] != "assistant" {
		t.Errorf("assistant payload wrong: %+v", assistant)
	}
}

// POST ?wait=<sec> returns only {user} when no assistant replies in time.
func TestSlinkPost_JSON_WaitTimesOut(t *testing.T) {
	s, _, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")

	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "POST",
		srv.URL+"/slink/"+g.SlinkToken+"?wait=1",
		strings.NewReader("content=silent&topic=t-timeout"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	if elapsed < 900*time.Millisecond {
		t.Errorf("returned too fast (%s) — should have waited ~1s", elapsed)
	}
	if elapsed > 2500*time.Millisecond {
		t.Errorf("returned too slow (%s)", elapsed)
	}

	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := got["user"]; !ok {
		t.Errorf("missing user: %+v", got)
	}
	if _, ok := got["assistant"]; ok {
		t.Errorf("unexpected assistant on timeout: %+v", got)
	}
}

// Bad token → 404.
func TestSlinkPost_BadToken(t *testing.T) {
	s, _, _ := newTestServer(t)
	req := httptest.NewRequest("POST", "/slink/nope",
		strings.NewReader("content=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("token", "nope")
	w := httptest.NewRecorder()
	s.handleSlinkPost(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}
