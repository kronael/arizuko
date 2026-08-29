package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// createRecord sends no client rkey, so a 5xx or a dropped connection may have
// written the record already and re-sending posts it twice. Such a call is
// tried once; a read, and any method addressed by target id, keeps the full
// retry (BUGS L108).
func TestCreateRecordIsNotReplayedOnA5xx(t *testing.T) {
	cases := []struct {
		nsid      string
		wantCalls int
	}{
		{"com.atproto.repo.createRecord", 1},
		{"com.atproto.repo.getRecord", 3},
		{"com.atproto.repo.deleteRecord", 3},
	}
	for _, c := range cases {
		var calls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(http.StatusInternalServerError)
		}))
		bc := &bskyClient{cfg: config{Service: srv.URL}, http: srv.Client()}
		_ = bc.xrpc("POST", c.nsid, nil, map[string]any{"x": 1}, nil)
		if calls != c.wantCalls {
			t.Errorf("%s: %d HTTP requests, want %d", c.nsid, calls, c.wantCalls)
		}
		srv.Close()
	}
}
