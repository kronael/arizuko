package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
)

// The SSE hub's state exists only in webd's memory — a DB snapshot cannot see a
// live subscriber, which is exactly why 6/28 puts the page in the owning daemon.
func TestHubStatusCountsLiveSubscribers(t *testing.T) {
	h := newHub()
	if got := h.status(); len(got) != 0 {
		t.Fatalf("a fresh hub reported %d keys", len(got))
	}
	_, cancelA := h.subscribe("web:acme", "t1")
	_, cancelB := h.subscribe("web:acme", "t1")
	_, cancelC := h.subscribe("web:other", "t2")

	got := h.status()
	if len(got) != 2 {
		t.Fatalf("keys = %d, want 2", len(got))
	}
	// Sorted, so the assertion does not depend on map order.
	if got[0].Subs != 2 || got[1].Subs != 1 {
		t.Errorf("subscriber counts = %+v, want 2 then 1", got)
	}

	cancelA()
	cancelB()
	cancelC()
	for _, k := range h.status() {
		if k.Subs != 0 {
			t.Errorf("key %q still reports %d subscribers after cancel", k.Key, k.Subs)
		}
	}
}

// A count means little without the ceiling it is approaching.
func TestTileShowsTheCapAndWarnsAtIt(t *testing.T) {
	var b strings.Builder
	webdTile(&b, "SSE keys", 3, 10)
	if !strings.Contains(b.String(), "3/10") {
		t.Errorf("tile = %q, want the cap rendered", b.String())
	}
	if strings.Contains(b.String(), "warn") {
		t.Error("a tile under its cap is marked warn")
	}
	var at strings.Builder
	webdTile(&at, "SSE keys", 10, 10)
	if !strings.Contains(at.String(), "warn") {
		t.Error("a tile at its cap is not marked warn")
	}
}

func TestWebdDashboardIsOperatorOnly(t *testing.T) {
	s := &server{ks: &auth.KeySet{}, hub: newHub()}
	r := httptest.NewRequest(http.MethodGet, "http://k.example/dash/webd/", nil)
	r.Header.Set("X-User-Groups", `["acme"]`)
	w := httptest.NewRecorder()
	s.handleDash(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("folder-scoped caller = %d, want 403", w.Code)
	}
	ok := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "http://k.example/dash/webd/", nil)
	r2.Header.Set("X-User-Groups", `["**"]`)
	s.handleDash(ok, r2)
	if ok.Code != http.StatusOK {
		t.Errorf("operator = %d, want 200", ok.Code)
	}
}

// An SSE key is built from a chat JID, which is platform-supplied.
func TestHubTableEscapes(t *testing.T) {
	got := hubTable([]hubStatus{{Key: `<script>x</script>`, Subs: 1}})
	if strings.Contains(got, "<script>") {
		t.Error("an SSE key rendered as live markup")
	}
}
