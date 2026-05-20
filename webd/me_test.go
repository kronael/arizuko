package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// authHeaders returns signed user headers granting access to all folders.
func meAuthHeaders(sub, name string) map[string]string {
	groups, _ := json.Marshal([]string{"**"})
	return signUserHeaders(map[string]string{
		"X-User-Sub":    sub,
		"X-User-Name":   name,
		"X-User-Groups": string(groups),
	})
}

func meRequest(t *testing.T, srv *httptest.Server, method, path string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	cli := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	buf := make([]byte, 32768)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}

// GET /me/ returns 200 with shell when authed.
func TestMeIndex_Authed(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	resp := meRequest(t, srv, "GET", "/me/", meAuthHeaders("user:alice", "Alice"))
	body := readBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "me-shell") {
		t.Errorf("body missing me-shell class")
	}
	if !strings.Contains(body, "/me/x/folders") {
		t.Errorf("body missing folder tree hx-get")
	}
}

// GET /me/ without auth redirects to /auth/login.
func TestMeIndex_Unauthed(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	resp, err := (&http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}).Get(srv.URL + "/me/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
}

// GET /me/chats returns conversation list page.
func TestMeChats_Authed(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	resp := meRequest(t, srv, "GET", "/me/chats", meAuthHeaders("user:alice", "Alice"))
	body := readBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "me-shell") {
		t.Errorf("missing shell class")
	}
}

// GET /me/x/folders returns folder tree fragment.
func TestMeXFolders_Partial(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "atlas", "Atlas")
	seedGroup(t, st, "atlas/eng", "Eng")

	groups, _ := json.Marshal([]string{"**"})
	h := signUserHeaders(map[string]string{
		"X-User-Sub":    "user:alice",
		"X-User-Name":   "Alice",
		"X-User-Groups": string(groups),
	})
	req := httptest.NewRequest("GET", "/me/x/folders", nil)
	for k, v := range h {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.handleMeXFolders(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "atlas") {
		t.Errorf("missing atlas in tree: %s", body)
	}
}

// GET /me/x/chats returns chat rows.
func TestMeXChats_Partial(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	_ = st.PutMessage(core.Message{
		ID: "m1", ChatJID: "web:main", Content: "hello",
		Timestamp: time.Now(), Topic: "topic1",
	})

	req := httptest.NewRequest("GET", "/me/x/chats", nil)
	groups, _ := json.Marshal([]string{"**"})
	for k, v := range signUserHeaders(map[string]string{
		"X-User-Sub": "u", "X-User-Name": "u", "X-User-Groups": string(groups),
	}) {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.handleMeXChats(w, req)
	body := w.Body.String()
	if !strings.Contains(body, "main") {
		t.Errorf("missing folder in chat list: %s", body)
	}
	if !strings.Contains(body, "topic1") {
		t.Errorf("missing topic in chat list: %s", body)
	}
}

// GET /me/chats/{folder}/{topic} returns thread page.
func TestMeThread_Renders(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	_ = st.PutMessage(core.Message{
		ID: "m1", ChatJID: "web:main", Content: "hi there",
		Timestamp: time.Now(), Topic: "t1",
	})
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	resp := meRequest(t, srv, "GET", "/me/chats/main/t1", meAuthHeaders("user:alice", "Alice"))
	body := readBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "send-form") {
		t.Errorf("missing send form: %s", body)
	}
	if !strings.Contains(body, "hi there") {
		t.Errorf("missing message content: %s", body)
	}
	if !strings.Contains(body, "/sse") {
		t.Errorf("missing sse connect: %s", body)
	}
}

// POST /me/chats/{folder}/{topic}/send writes message and returns fragment.
func TestMeSend_WritesMessage(t *testing.T) {
	s, mr, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	form := url.Values{"message": {"hello world"}}
	req, _ := http.NewRequest("POST", srv.URL+"/me/chats/main/t1/send",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range meAuthHeaders("user:alice", "Alice") {
		req.Header.Set(k, v)
	}
	cli := &http.Client{}
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	if !strings.Contains(body, "hello world") {
		t.Errorf("fragment missing message: %s", body)
	}
	if !strings.Contains(body, `class="msg user"`) {
		t.Errorf("fragment missing user msg class: %s", body)
	}
	// Message forwarded to router
	sent := mr.sent()
	if len(sent) == 0 {
		t.Errorf("expected message sent to router")
	}

}

// POST /me/chats/{folder}/{topic}/send without auth redirects.
func TestMeSend_Unauthed(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	form := url.Values{"message": {"hello"}}
	req, _ := http.NewRequest("POST", srv.URL+"/me/chats/main/t1/send",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	cli := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
}

// GET /me/settings returns settings form.
func TestMeSettings_Renders(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	resp := meRequest(t, srv, "GET", "/me/settings", meAuthHeaders("user:alice", "Alice"))
	body := readBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(body, "settings") {
		t.Errorf("missing settings heading: %s", body)
	}
	if !strings.Contains(body, "user:alice") {
		t.Errorf("missing sub in settings: %s", body)
	}
}

// PATCH /me/settings returns 200.
func TestMeSettingsPatch(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	req, _ := http.NewRequest("PATCH", srv.URL+"/me/settings",
		strings.NewReader("name=Alice"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range meAuthHeaders("user:alice", "Alice") {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// meFolderTopic correctly splits path values.
func TestMeFolderTopic(t *testing.T) {
	cases := []struct {
		raw    string
		folder string
		topic  string
	}{
		{"main/t1", "main", "t1"},
		{"org/eng/t2", "org/eng", "t2"},
		{"main/t1/send", "main", "t1"},
		{"main/t1/sse", "main", "t1"},
		{"bare", "", ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/", nil)
		req.SetPathValue("folder", c.raw)
		f, t2 := meFolderTopic(req)
		if f != c.folder || t2 != c.topic {
			t.Errorf("meFolderTopic(%q) = (%q,%q), want (%q,%q)",
				c.raw, f, t2, c.folder, c.topic)
		}
	}
}
