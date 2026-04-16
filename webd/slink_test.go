package main

import (
	"bufio"
	"context"
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
