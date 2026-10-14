package routd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// TestSessionResolver_FederatesOverHTTP: NewSessionResolver, backed by routd's
// real runed client, fetches GET /v1/spawn_sessions/recent and maps the wire rows to
// core.SessionRecord. This is the path that replaced the runed.db sibling read:
// no DB handle, pure HTTP federation.
func TestSessionResolver_FederatesOverHTTP(t *testing.T) {
	var gotPath string
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(runedv1.RecentSessionsResponse{
			// The id and both timestamps are the shape runed now writes:
			// a time-keyed string id, and core.TS's fixed nine-digit
			// fraction, which time.RFC3339 parses on the way back in.
			Sessions: []runedv1.RecentSessionRecord{{
				ID: "2026-01-01T10:00:00.000000000Z-0000002a", GroupFolder: "demo",
				SessionID: "abc123def456",
				StartedAt: "2026-01-01T10:00:00.000000000Z",
				EndedAt:   "2026-01-01T10:05:00.000000000Z",
				Result:    "ok", Error: "boom", MessageCount: 7,
			}},
		})
	}))
	defer srv.Close()

	client := runedv1.NewClient(srv.URL, "svc-token", 5*time.Second)
	res := NewSessionResolver(client).RecentSessions("demo", 1)

	if gotPath != "/v1/spawn_sessions/recent?folder=demo&n=1" {
		t.Fatalf("request path/query = %q want /v1/spawn_sessions/recent?folder=demo&n=1", gotPath)
	}
	if gotAuth != "Bearer svc-token" {
		t.Fatalf("auth header = %q want Bearer svc-token", gotAuth)
	}
	if len(res) != 1 {
		t.Fatalf("records=%d want 1: %+v", len(res), res)
	}
	r := res[0]
	if r.Folder != "demo" || r.SessionID != "abc123def456" || r.Result != "ok" ||
		r.Error != "boom" || r.MsgCount != 7 {
		t.Fatalf("mapped record wrong: %+v", r)
	}
	if r.ID != "2026-01-01T10:00:00.000000000Z-0000002a" {
		t.Fatalf("id = %q, want the wire id verbatim: it is runed's sort key and "+
			"a caller quoting it back has to get the same string", r.ID)
	}
	if r.StartedAt.IsZero() || r.EndedAt == nil {
		t.Fatalf("timestamps not parsed: started=%v ended=%v", r.StartedAt, r.EndedAt)
	}
}

// TestSessionResolver_Unreachable: an unreachable runed (or any transport
// error) yields nil — advisory only, never a panic. Mirrors routd's old
// absent-runed.db guard.
func TestSessionResolver_Unreachable(t *testing.T) {
	// A client pointed at a closed server: the request fails at transport.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	client := runedv1.NewClient(url, "svc-token", 200*time.Millisecond)
	if got := NewSessionResolver(client).RecentSessions("demo", 1); got != nil {
		t.Fatalf("unreachable runed: want nil, got %+v", got)
	}
}
