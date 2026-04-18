package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// splitFolderSuffix extracts folder + suffix from a /api|x/groups path rest,
// and refuses an empty folder to prevent ACL bypass via an empty grant.
func TestSplitFolderSuffix(t *testing.T) {
	cases := []struct {
		rest, folder, suffix string
	}{
		{"atlas/topics", "atlas", "topics"},
		{"atlas/messages", "atlas", "messages"},
		{"atlas/typing", "atlas", "typing"},
		{"atlas/content/topics", "atlas/content", "topics"},
		{"/topics", "/topics", ""},      // empty folder rejected
		{"/messages", "/messages", ""},  // empty folder rejected
		{"unknown", "unknown", ""},
		{"main/unknown", "main/unknown", ""},
	}
	for _, c := range cases {
		f, s := splitFolderSuffix(c.rest)
		if f != c.folder || s != c.suffix {
			t.Errorf("splitFolderSuffix(%q) = (%q,%q), want (%q,%q)",
				c.rest, f, s, c.folder, c.suffix)
		}
	}
}

// routeAPIGroups dispatches by suffix: topics/messages/typing, otherwise 404.
func TestRouteAPIGroups(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	groups, _ := json.Marshal([]string{"main"})
	h := signUserHeaders(map[string]string{
		"X-User-Sub":    "user:alice",
		"X-User-Name":   "Alice",
		"X-User-Groups": string(groups),
	})

	do := func(method, path string) int {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do %s %s: %v", method, path, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if c := do("GET", "/api/groups/main/topics"); c != 200 {
		t.Errorf("topics = %d", c)
	}
	if c := do("GET", "/api/groups/main/messages?topic=t"); c != 200 {
		t.Errorf("messages = %d", c)
	}
	if c := do("POST", "/api/groups/main/typing"); c != http.StatusAccepted {
		t.Errorf("typing = %d", c)
	}
	if c := do("GET", "/api/groups/main/bogus"); c != 404 {
		t.Errorf("bogus = %d, want 404", c)
	}
	// GET /api/groups/main/typing — method doesn't match → 404.
	if c := do("GET", "/api/groups/main/typing"); c != 404 {
		t.Errorf("GET typing = %d, want 404", c)
	}
}

// routeXGroups handles topics/messages, not typing.
func TestRouteXGroups(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	groups, _ := json.Marshal([]string{"main"})
	h := signUserHeaders(map[string]string{
		"X-User-Sub":    "user:alice",
		"X-User-Name":   "Alice",
		"X-User-Groups": string(groups),
	})
	do := func(path string) int {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := do("/x/groups/main/topics"); c != 200 {
		t.Errorf("topics = %d", c)
	}
	if c := do("/x/groups/main/messages?topic=t"); c != 200 {
		t.Errorf("messages = %d", c)
	}
	if c := do("/x/groups/main/typing"); c != 404 {
		t.Errorf("typing = %d, want 404", c)
	}
}

// API path resists ACL bypass via empty folder name.
func TestRouteAPIGroups_EmptyFolderRejected(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	// User with ** grant — which would cover "" — still shouldn't reach
	// any handler because splitFolderSuffix refuses empty folders.
	groups, _ := json.Marshal([]string{"**"})
	h := signUserHeaders(map[string]string{
		"X-User-Sub":    "user:alice",
		"X-User-Name":   "Alice",
		"X-User-Groups": string(groups),
	})
	req, _ := http.NewRequest("GET", srv.URL+"/api/groups//topics", nil)
	for k, v := range h {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	// Either 404 or 403 is acceptable — key is that handleAPITopics
	// does not happily return data on an empty folder.
	if resp.StatusCode != 404 && resp.StatusCode != 403 {
		t.Errorf("status = %d, want 404/403 for empty folder", resp.StatusCode)
	}
}

// requireUser rejects unsigned headers (forged identity attempt).
func TestRequireUser_RejectsForged(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	cli := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	req, _ := http.NewRequest("GET", srv.URL+"/api/groups", nil)
	// Headers without a valid sig.
	req.Header.Set("X-User-Sub", "user:evil")
	req.Header.Set("X-User-Groups", `["**"]`)
	req.Header.Set("X-User-Sig", "wrongsig")
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 redirect", resp.StatusCode)
	}
}

// folderParam strips leading slash from path value (as set by routeAPIGroups).
func TestFolderParam(t *testing.T) {
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("folder", "/atlas/content")
	if got := folderParam(req); got != "atlas/content" {
		t.Errorf("folderParam = %q", got)
	}
	req.SetPathValue("folder", "plain")
	if got := folderParam(req); got != "plain" {
		t.Errorf("folderParam = %q", got)
	}
}

// GET /static/style.css served via embedded FS.
func TestStaticFiles(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/static/style.css")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "css") &&
		!strings.Contains(ct, "text") {
		t.Errorf("content-type = %q", ct)
	}
}

// hmacVerify: empty secret or empty sig fails; good sig passes.
func TestHMACVerify(t *testing.T) {
	if hmacVerify("", "msg", "sig") {
		t.Error("empty secret should fail")
	}
	if hmacVerify("sec", "msg", "") {
		t.Error("empty sig should fail")
	}
	if !hmacVerify(testHMACSecret, "x",
		computeSig(t, testHMACSecret, "x")) {
		t.Error("valid sig should pass")
	}
}

// verifySlinkSig: token+folder+correct sig passes.
func TestVerifySlinkSig(t *testing.T) {
	req := httptest.NewRequest("GET", "/slink/stream", nil)
	h := signSlinkHeaders("mytok", "main")
	for k, v := range h {
		req.Header.Set(k, v)
	}
	if !verifySlinkSig(testHMACSecret, req) {
		t.Error("signed slink should verify")
	}
	// Missing folder → fail.
	req.Header.Del("X-Folder")
	if verifySlinkSig(testHMACSecret, req) {
		t.Error("missing folder should fail")
	}
}

// anonSender derives an ID from X-Forwarded-For.
func TestAnonSender(t *testing.T) {
	r := httptest.NewRequest("POST", "/x", nil)
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	a := anonSender(r)
	if !strings.HasPrefix(a, "anon:") || len(a) < 10 {
		t.Errorf("anonSender = %q", a)
	}
	// Same IP → same anon id.
	r2 := httptest.NewRequest("POST", "/x", nil)
	r2.Header.Set("X-Forwarded-For", "1.2.3.4")
	if a != anonSender(r2) {
		t.Error("same IP should produce same anon id")
	}
	// Different IP → different id.
	r3 := httptest.NewRequest("POST", "/x", nil)
	r3.Header.Set("X-Forwarded-For", "9.9.9.9")
	if a == anonSender(r3) {
		t.Error("different IP should differ")
	}
}

// tokenHash returns 8 hex chars for non-empty input, "" for empty.
func TestTokenHash(t *testing.T) {
	if tokenHash("") != "" {
		t.Error("empty token → empty hash")
	}
	h := tokenHash("secrettoken")
	if len(h) != 8 {
		t.Errorf("hash len = %d, want 8", len(h))
	}
	if tokenHash("secrettoken") != h {
		t.Error("hash not stable")
	}
}
