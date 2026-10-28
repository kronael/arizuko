package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

type chatTestVerifier struct{}

func (chatTestVerifier) Verify(r *http.Request) (string, []string, string, error) {
	if r.Header.Get("Authorization") != "Bearer test" {
		return "", nil, "", fmt.Errorf("missing token")
	}
	return "service:dashd", []string{"routes:read", "groups:read"}, "", nil
}

func TestChatSettingsDashboardAuthorizationAndRoundTrip(t *testing.T) {
	db := routdDB(t)
	s := store.NewOver(db)
	seedFolder(t, db, "team")
	seedFolder(t, db, "team/child")
	seedFolder(t, db, "other")
	seedAdmin(t, db, "alice", "team")
	const own, child, other = "discord:g/a", "discord:g/child", "discord:g/other"
	for _, route := range []core.Route{{Match: "chat_jid=" + own, Target: "team"}, {Match: "chat_jid=" + child, Target: "team/child"}, {Match: "chat_jid=" + other, Target: "other"}} {
		seedRoute(t, db, route)
	}
	if err := s.SetChatIsGroup(own, true); err != nil {
		t.Fatal(err)
	}
	backend := routd.NewServer(routd.OpenOver(db), nil, nil, chatTestVerifier{}, 0, "").Handler()
	var chatCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/chats/") {
			chatCalls.Add(1)
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	appDir := t.TempDir()
	coreDir := filepath.Join(appDir, "packages", "core")
	if err := os.MkdirAll(coreDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coreDir, "arizuko.yml"), []byte("name: core\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := routd.OpenOver(db).PutInstalledPackage(routd.InstalledPackage{Name: "core", Manifest: map[string][]string{"skill": {}}}); err != nil {
		t.Fatal(err)
	}
	d := &dash{routd: db, routdURL: server.URL, groupsDir: filepath.Join(appDir, "groups"), svc: func(context.Context) (string, error) { return "test", nil }}
	mux := newMux(d)
	request := func(method, path, form string) *httptest.ResponseRecorder {
		t.Helper()
		req := adminReq(method, path, form, "alice")
		req.Header.Set("X-User-Groups", `["team"]`)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	for _, jid := range []string{child, other} {
		before := chatCalls.Load()
		post := request("POST", "/dash/groups/team/settings/chat", url.Values{"jid": {jid}, "thread_replies": {"off"}}.Encode())
		if post.Code != 403 || chatCalls.Load() != before {
			t.Fatalf("forged POST %s: status=%d calls=%d", jid, post.Code, chatCalls.Load()-before)
		}
		get := request("GET", "/dash/groups/team/settings?jid="+url.QueryEscape(jid), "")
		if get.Code != 403 || chatCalls.Load() != before {
			t.Fatalf("forged GET %s: status=%d calls=%d", jid, get.Code, chatCalls.Load()-before)
		}
	}
	form := url.Values{"jid": {own}, "thread_replies": {"off"}}.Encode()
	w := request("POST", "/dash/groups/team/settings/chat", form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("save = %d %s", w.Code, w.Body.String())
	}
	if got, err := s.ChatThreadReplies(own); err != nil || got {
		t.Fatalf("stored = %v %v", got, err)
	}
	w = request("GET", "/dash/groups/team/settings?jid="+url.QueryEscape(own), "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Thread replies: off") {
		t.Fatalf("load = %d %s", w.Code, w.Body.String())
	}
	form = url.Values{"jid": {own}, "thread_replies": {"default"}}.Encode()
	w = request("POST", "/dash/groups/team/settings/chat", form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("reset = %d %s", w.Code, w.Body.String())
	}
	if got, err := s.ChatThreadReplies(own); err != nil || !got {
		t.Fatalf("reset stored = %v %v", got, err)
	}
	if err := s.RemoveACLRow(core.ACLRow{Principal: "service:dashd", Action: "chats:get", Scope: "**", Effect: "allow"}); err != nil {
		t.Fatal(err)
	}
	w = request("GET", "/dash/groups/team/settings?jid="+url.QueryEscape(own), "")
	if w.Code != 502 || !strings.Contains(w.Body.String(), "403") {
		t.Fatalf("upstream denial = %d %s", w.Code, w.Body.String())
	}
}
