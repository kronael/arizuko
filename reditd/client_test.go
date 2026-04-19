package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onvos/arizuko/chanlib"
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
		files:     chanlib.NewURLCache(100),
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
	t := thing{Kind: "t4"}
	t.Data.Name = name
	t.Data.Author = author
	t.Data.Body = body
	t.Data.ID = "x1"
	l := listing{}
	l.Data.Children = []thing{t}
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
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(listingWithItem("t4_aaa", "alice", "hello"))
	}))
	defer apiSrv.Close()

	routerSrv := stubRouterSrv(t)
	rc := makeRedditClient(t, apiSrv)
	rc.http.Transport.(*hostRewrite).target = apiSrv.Listener.Addr().String()

	router := chanlib.NewRouterClient(routerSrv.URL, "")

	rc.pollSource("inbox", "/message/inbox.json", router)
	// On first poll, skipFirst is set to true and no dispatch occurs
	if !rc.skipFirst["inbox"] {
		t.Error("skipFirst not set after first poll")
	}
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
	router := chanlib.NewRouterClient(routerSrv.URL, "")

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
	router := chanlib.NewRouterClient(routerSrv.URL, "")

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

// TestExtractAttachments_ImagePost verifies attachment extraction from an image post.
func TestExtractAttachments_ImagePost(t *testing.T) {
	rc := makeRedditClient(t, httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
	rc.cfg.ListenURL = "http://reditd:9006"

	th := thing{Kind: "t3"}
	th.Data.URL = "https://i.redd.it/abc123.jpg"
	th.Data.PostHint = "image"

	atts := rc.extractAttachments(th)
	if len(atts) != 1 {
		t.Fatalf("got %d attachments, want 1", len(atts))
	}
	if atts[0].Mime != "image/jpeg" {
		t.Errorf("mime = %q, want image/jpeg", atts[0].Mime)
	}
	if atts[0].Filename != "abc123.jpg" {
		t.Errorf("filename = %q", atts[0].Filename)
	}
	if !strings.HasPrefix(atts[0].URL, "http://reditd:9006/files/") {
		t.Errorf("url = %q, expected proxy URL", atts[0].URL)
	}
}

// TestExtractAttachments_Video verifies attachment extraction from a video post.
func TestExtractAttachments_Video(t *testing.T) {
	rc := makeRedditClient(t, httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
	rc.cfg.ListenURL = "http://reditd:9006"

	th := thing{Kind: "t3"}
	th.Data.Media = &struct {
		RedditVideo *struct {
			FallbackURL string `json:"fallback_url"`
		} `json:"reddit_video"`
	}{
		RedditVideo: &struct {
			FallbackURL string `json:"fallback_url"`
		}{FallbackURL: "https://v.redd.it/abc/DASH_720.mp4"},
	}

	atts := rc.extractAttachments(th)
	if len(atts) != 1 {
		t.Fatalf("got %d attachments, want 1", len(atts))
	}
	if atts[0].Mime != "video/mp4" {
		t.Errorf("mime = %q", atts[0].Mime)
	}
}

// TestExtractAttachments_Gallery verifies gallery post extraction.
func TestExtractAttachments_Gallery(t *testing.T) {
	rc := makeRedditClient(t, httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
	rc.cfg.ListenURL = "http://reditd:9006"

	th := thing{Kind: "t3"}
	th.Data.IsGallery = true
	th.Data.GalleryData = &struct {
		Items []galleryItem `json:"items"`
	}{
		Items: []galleryItem{{MediaID: "img1"}, {MediaID: "img2"}},
	}
	th.Data.MediaMetadata = map[string]mediaMetaItem{
		"img1": {Status: "valid", Mime: "image/png", S: struct {
			U string `json:"u"`
			X int    `json:"x"`
			Y int    `json:"y"`
		}{U: "https://preview.redd.it/img1.png"}},
		"img2": {Status: "valid", Mime: "image/jpeg", S: struct {
			U string `json:"u"`
			X int    `json:"x"`
			Y int    `json:"y"`
		}{U: "https://preview.redd.it/img2.jpg"}},
	}

	atts := rc.extractAttachments(th)
	if len(atts) != 2 {
		t.Fatalf("got %d attachments, want 2", len(atts))
	}
	if atts[0].Filename != "img1.png" {
		t.Errorf("first filename = %q", atts[0].Filename)
	}
	if atts[1].Filename != "img2.jpg" {
		t.Errorf("second filename = %q", atts[1].Filename)
	}
}

// TestExtractAttachments_NoMedia verifies no attachments for text-only posts.
func TestExtractAttachments_NoMedia(t *testing.T) {
	rc := makeRedditClient(t, httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
	th := thing{Kind: "t4"}
	th.Data.Body = "just text"
	atts := rc.extractAttachments(th)
	if len(atts) != 0 {
		t.Errorf("got %d attachments, want 0", len(atts))
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

// TestFetchHistory_Subreddit verifies FetchHistory returns listing items
// oldest-first, mapped to InboundMsg shape, with platform-capped source.
func TestFetchHistory_Subreddit(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/r/golang/new.json") {
			http.Error(w, "wrong path", 404)
			return
		}
		if r.URL.Query().Get("limit") == "" {
			t.Error("limit param missing")
		}
		w.Header().Set("Content-Type", "application/json")
		// Two items; reddit returns newest-first so "newer" comes first.
		l := listing{}
		newer := thing{Kind: "t3"}
		newer.Data.Name = "t3_newer"
		newer.Data.Author = "alice"
		newer.Data.Title = "hello"
		newer.Data.Subreddit = "golang"
		newer.Data.CreatedAt = 1_700_000_200
		older := thing{Kind: "t3"}
		older.Data.Name = "t3_older"
		older.Data.Author = "bob"
		older.Data.Title = "world"
		older.Data.Subreddit = "golang"
		older.Data.CreatedAt = 1_700_000_100
		l.Data.Children = []thing{newer, older}
		json.NewEncoder(w).Encode(l)
	}))
	defer apiSrv.Close()

	rc := makeRedditClient(t, apiSrv)
	resp, err := rc.FetchHistory(chanlib.HistoryRequest{ChatJID: "reddit:r_golang", Limit: 50})
	if err != nil {
		t.Fatalf("FetchHistory: %v", err)
	}
	if resp.Source != "platform-capped" || resp.Cap != "1000" {
		t.Errorf("Source=%q Cap=%q", resp.Source, resp.Cap)
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("messages=%d", len(resp.Messages))
	}
	// Oldest-first ordering.
	if resp.Messages[0].ID != "t3_older" || resp.Messages[1].ID != "t3_newer" {
		t.Errorf("order wrong: %q, %q", resp.Messages[0].ID, resp.Messages[1].ID)
	}
	if resp.Messages[0].ChatJID != "reddit:r_golang" {
		t.Errorf("ChatJID=%q", resp.Messages[0].ChatJID)
	}
	if resp.Messages[0].Verb != "post" {
		t.Errorf("Verb=%q", resp.Messages[0].Verb)
	}
}

func TestFetchHistory_UserJID_Unsupported(t *testing.T) {
	rc := &redditClient{}
	resp, err := rc.FetchHistory(chanlib.HistoryRequest{ChatJID: "reddit:alice"})
	if err != nil {
		t.Fatalf("FetchHistory: %v", err)
	}
	if resp.Source != "unsupported" {
		t.Errorf("Source=%q", resp.Source)
	}
	if len(resp.Messages) != 0 {
		t.Errorf("want empty, got %d", len(resp.Messages))
	}
}

func TestFetchHistory_BeforeFilter(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		l := listing{}
		a := thing{Kind: "t3"}
		a.Data.Name = "t3_a"
		a.Data.Title = "a"
		a.Data.CreatedAt = 1_000
		b := thing{Kind: "t3"}
		b.Data.Name = "t3_b"
		b.Data.Title = "b"
		b.Data.CreatedAt = 3_000
		l.Data.Children = []thing{b, a}
		json.NewEncoder(w).Encode(l)
	}))
	defer apiSrv.Close()

	rc := makeRedditClient(t, apiSrv)
	resp, err := rc.FetchHistory(chanlib.HistoryRequest{
		ChatJID: "reddit:r_x",
		Before:  time.Unix(2_000, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Messages) != 1 || resp.Messages[0].ID != "t3_a" {
		t.Errorf("before filter wrong: %+v", resp.Messages)
	}
}
