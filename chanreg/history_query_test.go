package chanreg

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestHistoryQueryEscapesJID(t *testing.T) {
	got := make(chan url.Values, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.URL.Query()
		w.WriteHeader(http.StatusOK)
	}))
	defer s.Close()
	c := NewHTTPChannel(&Entry{
		Name: "x", URL: s.URL, Capabilities: map[string]bool{"fetch_history": true},
	}, nil)
	jid := "x:a+b&limit=999#fragment?before=wrong"
	before := time.Date(2026, 11, 21, 12, 0, 0, 0, time.FixedZone("offset", 3600))
	if _, err := c.FetchHistory(t.Context(), jid, before, 10); err != nil {
		t.Fatal(err)
	}
	query := <-got
	if query.Get("jid") != jid || query.Get("limit") != "10" ||
		query.Get("before") != before.UTC().Format(time.RFC3339) {
		t.Fatalf("history query = %v", query)
	}
}
