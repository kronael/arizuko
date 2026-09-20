package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kronael/arizuko/store"
)

// The /slink/ shim must preserve the method and be readable cross-origin.
// A 301 lets a client re-issue a POST as GET (RFC 9110 §15.4.2, and the Fetch
// spec mandates it), and an unwrapped redirect carries no Access-Control-*
// header, so a browser SDK could neither follow it nor read it (BUGS L117).
func TestSlinkShimPreservesPOSTAndCarriesCORS(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	raw := store.GenRouteToken()
	if err := st.InsertRouteToken(raw, store.RouteToken{
		JID: "web:main", OwnerFolder: "main", TargetFolder: "main",
	}); err != nil {
		t.Fatalf("InsertRouteToken: %v", err)
	}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	body, _ := json.Marshal(map[string]string{"content": "ping", "topic": "tw"})
	req, _ := http.NewRequest("POST", srv.URL+"/slink/"+raw, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://acme.example")

	// No redirect following: the hop itself is what this asserts.
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPermanentRedirect {
		t.Errorf("status = %d, want 308 — a 301 downgrades the POST to GET", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/chat/"+raw {
		t.Errorf("Location = %q, want %q", got, "/chat/"+raw)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q — a browser cannot read this hop", got, "*")
	}
}

// End-to-end through the hop: a client that follows redirects the way a browser
// does must land on the chat handler as a POST and get a turn back.
func TestSlinkShimSendSurvivesTheHop(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	raw := store.GenRouteToken()
	if err := st.InsertRouteToken(raw, store.RouteToken{
		JID: "web:main", OwnerFolder: "main", TargetFolder: "main",
	}); err != nil {
		t.Fatalf("InsertRouteToken: %v", err)
	}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	body, _ := json.Marshal(map[string]string{"content": "ping", "topic": "tw"})
	req, _ := http.NewRequest("POST", srv.URL+"/slink/"+raw, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d after the hop, want 200", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["turn_id"] == nil {
		t.Errorf("no turn_id: %v — the hop reached a handler that did not accept the message", out)
	}
}

// The shipped SDK asks for the surface directly. It reached the mux through the
// compatibility hop, which costs a round trip on every call and puts a
// method-preserving redirect on the critical path of a send (BUGS L117).
func TestBrowserSDKAsksForTheChatSurface(t *testing.T) {
	src, err := os.ReadFile("assets/arizuko-client.js")
	if err != nil {
		t.Fatalf("read SDK: %v", err)
	}
	if bytes.Contains(src, []byte("'/slink/")) {
		t.Error("the SDK builds a request path from '/slink/'; it must ask for /chat/ directly")
	}
	// Non-vacuity: the constant the five call sites are built from is present,
	// so a rename cannot make the check above pass by deleting the surface.
	if !bytes.Contains(src, []byte("const SURFACE = '/chat/';")) {
		t.Error("SURFACE is not declared; this test no longer checks what it names")
	}
	if n := strings.Count(string(src), "SURFACE +"); n != 5 {
		t.Errorf("SURFACE feeds %d call sites, want 5 (send, status, snapshot, stream, config)", n)
	}
}
