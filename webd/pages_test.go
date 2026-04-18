package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/onvos/arizuko/core"
)

// GET / (handleGroupsPage) renders header + user name.
func TestGroupsPage_AuthedRenders(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	groups, _ := json.Marshal([]string{"**"})
	h := signUserHeaders(map[string]string{
		"X-User-Sub":    "user:alice",
		"X-User-Name":   "Alice<b>",
		"X-User-Groups": string(groups),
	})

	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	for k, v := range h {
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
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	if !strings.Contains(body, "arizuko") {
		t.Errorf("page missing header")
	}
	if !strings.Contains(body, "Alice&lt;b&gt;") {
		t.Errorf("page should HTML-escape name: %s", body)
	}
	if strings.Contains(body, "Alice<b>") {
		t.Errorf("unsanitized username in page")
	}
}

// Unauthenticated GET / redirects to /auth/login (303).
func TestGroupsPage_UnauthedRedirects(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	cli := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := cli.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/auth/login" {
		t.Errorf("location = %q", loc)
	}
}

// GET /chat/<folder> renders chat page with group-specific content.
func TestChatPage_Renders(t *testing.T) {
	s, _, st := newTestServer(t)
	g := seedGroup(t, st, "main", "MyGroup")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	groups, _ := json.Marshal([]string{"main"})
	h := signUserHeaders(map[string]string{
		"X-User-Sub":    "user:alice",
		"X-User-Name":   "Alice",
		"X-User-Groups": string(groups),
	})
	req, _ := http.NewRequest("GET", srv.URL+"/chat/main", nil)
	for k, v := range h {
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
	buf := make([]byte, 8192)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	if !strings.Contains(body, g.Name) {
		t.Errorf("body missing group name")
	}
	if !strings.Contains(body, g.SlinkToken) {
		t.Errorf("body missing slink token")
	}
}

// /chat/<nonexistent> returns 404 (past folder ACL).
func TestChatPage_UnknownGroup_404(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	groups, _ := json.Marshal([]string{"**"})
	h := signUserHeaders(map[string]string{
		"X-User-Sub":    "user:alice",
		"X-User-Name":   "Alice",
		"X-User-Groups": string(groups),
	})
	req, _ := http.NewRequest("GET", srv.URL+"/chat/ghost", nil)
	for k, v := range h {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// /chat/<folder> without grant → 403.
func TestChatPage_Forbidden(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	groups, _ := json.Marshal([]string{"other"})
	h := signUserHeaders(map[string]string{
		"X-User-Sub":    "user:bob",
		"X-User-Name":   "Bob",
		"X-User-Groups": string(groups),
	})
	req, _ := http.NewRequest("GET", srv.URL+"/chat/main", nil)
	for k, v := range h {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

// GET /x/groups returns HTML partial of group cards.
func TestXGroups_Partial(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	seedGroup(t, st, "other", "Other")

	req := httptest.NewRequest("GET", "/x/groups", nil)
	w := httptest.NewRecorder()
	s.handleXGroups(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `href="/chat/main"`) {
		t.Errorf("missing main card: %s", body)
	}
	if !strings.Contains(body, `href="/chat/other"`) {
		t.Errorf("missing other card: %s", body)
	}
	if !strings.Contains(body, "green") {
		t.Errorf("active groups should be green: %s", body)
	}
}

// GET /x/groups/<folder>/topics returns <option> tags + "new conversation".
func TestXTopics_Partial(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	_ = st.PutMessage(core.Message{
		ID: "m-fresh", ChatJID: "web:main", Content: "recent",
		Timestamp: time.Now(), Topic: "fresh",
	})
	_ = st.PutMessage(core.Message{
		ID: "m-old", ChatJID: "web:main",
		Content: strings.Repeat("x", 60),
		Timestamp: time.Now().Add(-72 * time.Hour), Topic: "old",
	})

	req := httptest.NewRequest("GET", "/x/groups/main/topics", nil)
	req.SetPathValue("folder", "main")
	w := httptest.NewRecorder()
	s.handleXTopics(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `value="fresh"`) {
		t.Errorf("missing fresh topic option: %s", body)
	}
	if !strings.Contains(body, `value="old"`) {
		t.Errorf("missing old topic option: %s", body)
	}
	if !strings.Contains(body, "new conversation") {
		t.Errorf("missing new-conversation option")
	}
	if !strings.Contains(body, "…") {
		t.Errorf("long preview should be truncated with ellipsis: %s", body)
	}
}

// GET /x/groups/<folder>/messages renders HTML <div.msg> nodes.
func TestXMessages_Partial(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	_ = st.PutMessage(core.Message{
		ID: "u1", ChatJID: "web:main", Content: "hello <world>",
		Timestamp: time.Now(), Topic: "t",
	})
	_ = st.PutMessage(core.Message{
		ID: "b1", ChatJID: "web:main", Content: "hi",
		Timestamp: time.Now(), Topic: "t", BotMsg: true,
	})

	req := httptest.NewRequest("GET", "/x/groups/main/messages?topic=t", nil)
	req.SetPathValue("folder", "main")
	w := httptest.NewRecorder()
	s.handleXMessages(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `class="msg user"`) {
		t.Errorf("missing user msg div: %s", body)
	}
	if !strings.Contains(body, `class="msg assistant"`) {
		t.Errorf("missing assistant msg div: %s", body)
	}
	if !strings.Contains(body, "hello &lt;world&gt;") {
		t.Errorf("user content should be HTML-escaped: %s", body)
	}
}

// Invalid before= in /x/groups/<folder>/messages falls through.
func TestXMessages_InvalidBefore(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	req := httptest.NewRequest("GET",
		"/x/groups/main/messages?topic=t&before=bogus", nil)
	req.SetPathValue("folder", "main")
	w := httptest.NewRecorder()
	s.handleXMessages(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}
