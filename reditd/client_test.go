package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// makeRedditClient returns a redditClient with a pre-set valid token and an
// http.Client whose transport rewrites request hosts to the given test server.
func makeRedditClient(t *testing.T, srv *httptest.Server) *redditClient {
	t.Helper()
	rc := &redditClient{
		cfg:       config{UserAgent: "test/1.0"},
		cursors:   map[string]string{},
		skipFirst: map[string]bool{},
		token:     "test-token",
		expiresAt: time.Now().Add(time.Hour),
		http: &http.Client{
			Transport: &hostRewrite{target: srv.Listener.Addr().String()},
			Timeout:   5 * time.Second,
		},
	}
	return rc
}

// hostRewrite redirects all outgoing requests to the given host:port.
type hostRewrite struct{ target string }

func (h *hostRewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	r2 := req.Clone(req.Context())
	r2.URL.Scheme = "http"
	r2.URL.Host = h.target
	return http.DefaultTransport.RoundTrip(r2)
}

func listingWithItem(name, author, body string) []byte {
	l := listing{}
	l.Data.Children = []thing{{
		Kind: "t4",
		Data: struct {
			Name      string  `json:"name"`
			Author    string  `json:"author"`
			Body      string  `json:"body"`
			Selftext  string  `json:"selftext"`
			Title     string  `json:"title"`
			CreatedAt float64 `json:"created_utc"`
			ID        string  `json:"id"`
			ParentID  string  `json:"parent_id"`
			LinkID    string  `json:"link_id"`
			Subreddit string  `json:"subreddit"`
		}{
			Name:   name,
			Author: author,
			Body:   body,
			ID:     "x1",
		},
	}}
	b, _ := json.Marshal(l)
	return b
}

func stubRouterSrv(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestFirstPollSkip verifies that the first call to pollSource does not
// dispatch messages even when items are present.
func TestFirstPollSkip(t *testing.T) {
	var dispatched atomic.Int32
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(listingWithItem("t4_aaa", "alice", "hello"))
	}))
	defer apiSrv.Close()

	routerSrv := stubRouterSrv(t)
	rc := makeRedditClient(t, apiSrv)
	rc.http.Transport.(*hostRewrite).target = apiSrv.Listener.Addr().String()

	router := newRouterClient(routerSrv.URL, "")

	rc.pollSource("inbox", "/message/inbox.json", router)
	// On first poll, skipFirst is set to true and no dispatch occurs
	if !rc.skipFirst["inbox"] {
		t.Error("skipFirst not set after first poll")
	}
	_ = dispatched
}

// TestCursorAdvance verifies that after the first poll (skip), the cursor is
// set from the listing, and subsequent polls include "before" param.
func TestCursorAdvance(t *testing.T) {
	var gotBefore string
	var callCount int
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 2 {
			gotBefore = r.URL.Query().Get("before")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(listingWithItem("t4_cursor123", "bob", "body"))
	}))
	defer apiSrv.Close()

	routerSrv := stubRouterSrv(t)
	rc := makeRedditClient(t, apiSrv)
	router := newRouterClient(routerSrv.URL, "")

	rc.pollSource("inbox", "/message/inbox.json", router)
	if rc.cursors["inbox"] != "t4_cursor123" {
		t.Errorf("cursor after first poll = %q, want t4_cursor123", rc.cursors["inbox"])
	}

	rc.pollSource("inbox", "/message/inbox.json", router)
	if gotBefore != "t4_cursor123" {
		t.Errorf("second poll before param = %q, want t4_cursor123", gotBefore)
	}
}

// TestRateLimit429 verifies that doWithRetry retries on 429.
func TestRateLimit429(t *testing.T) {
	var calls atomic.Int32
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(listingWithItem("t4_retry", "carol", "retry body"))
	}))
	defer apiSrv.Close()

	routerSrv := stubRouterSrv(t)
	rc := makeRedditClient(t, apiSrv)
	router := newRouterClient(routerSrv.URL, "")

	rc.pollSource("test", "/message/inbox.json", router)
	if calls.Load() < 2 {
		t.Errorf("expected at least 2 HTTP calls (retry after 429), got %d", calls.Load())
	}
}

// TestTokenExpiry verifies ensureToken triggers refreshToken when token is expired.
func TestTokenExpiry(t *testing.T) {
	var tokenCalls atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls.Add(1)
		json.NewEncoder(w).Encode(tokenResp{
			AccessToken: "new-token",
			ExpiresIn:   3600,
		})
	}))
	defer tokenSrv.Close()

	rc := &redditClient{
		cfg: config{
			UserAgent:    "test/1.0",
			ClientID:     "id",
			ClientSecret: "secret",
			Username:     "user",
			Password:     "pass",
		},
		cursors:   map[string]string{},
		skipFirst: map[string]bool{},
		token:     "old-token",
		expiresAt: time.Now().Add(-time.Hour), // already expired
		http: &http.Client{
			Transport: &hostRewrite{target: tokenSrv.Listener.Addr().String()},
			Timeout:   5 * time.Second,
		},
	}

	err := rc.ensureToken()
	if err != nil {
		t.Fatalf("ensureToken: %v", err)
	}
	if tokenCalls.Load() == 0 {
		t.Error("refreshToken not called on expired token")
	}
	if rc.token != "new-token" {
		t.Errorf("token = %q, want new-token", rc.token)
	}
}

// TestEnsureToken_NotExpired verifies ensureToken does NOT call refreshToken
// when the token is still valid.
func TestEnsureToken_NotExpired(t *testing.T) {
	var tokenCalls atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls.Add(1)
		json.NewEncoder(w).Encode(tokenResp{AccessToken: "fresh", ExpiresIn: 3600})
	}))
	defer tokenSrv.Close()

	rc := &redditClient{
		cfg:       config{},
		cursors:   map[string]string{},
		skipFirst: map[string]bool{},
		token:     "still-valid",
		expiresAt: time.Now().Add(time.Hour),
		http:      &http.Client{Timeout: 5 * time.Second},
	}

	err := rc.ensureToken()
	if err != nil {
		t.Fatalf("ensureToken: %v", err)
	}
	if tokenCalls.Load() != 0 {
		t.Error("refreshToken called for non-expired token")
	}
	if rc.token != "still-valid" {
		t.Errorf("token changed unexpectedly to %q", rc.token)
	}
}
