package main

import (
	"context"
	"encoding/json"
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
	"github.com/kronael/arizuko/objstore"
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
	if err := s.AddACLRow(core.ACLRow{Principal: "service:dashd", Action: "chats:get", Scope: "**", Effect: "deny"}, ""); err != nil {
		t.Fatal(err)
	}
	w = request("GET", "/dash/groups/team/settings?jid="+url.QueryEscape(own), "")
	if w.Code != 502 || !strings.Contains(w.Body.String(), "403") {
		t.Fatalf("upstream denial = %d %s", w.Code, w.Body.String())
	}
}

// TestGroupHarnessDashboard covers the harness half of the settings page: three
// free-text fields (backend, model, effort) and one "Use instance defaults"
// control. Saving builds ONE document from the non-blank fields and that
// document REPLACES the stored one, so a stored key the form does not show
// (options below) is dropped on save. That is the decision, not an accident.
// The write stays the operator's own store write, so the audit row names them.
func TestGroupHarnessDashboard(t *testing.T) {
	db := routdDB(t)
	seedFolder(t, db, "team")
	seedAdmin(t, db, "alice", "team")
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
	backend := routd.NewServer(routd.OpenOver(db), nil, nil, nil, 0, "").Handler()
	server := httptest.NewServer(backend)
	t.Cleanup(server.Close)
	d := &dash{routd: db, routdURL: server.URL, groupsDir: filepath.Join(appDir, "groups")}
	mux := newMux(d)
	s := store.NewOver(db)
	page := func() string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, adminReq("GET", "/dash/groups/team/settings", "", "alice"))
		if w.Code != 200 {
			t.Fatalf("page: %d %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	save := func(form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, adminReq("POST", "/dash/groups/team/settings", form.Encode(), "alice"))
		return w
	}
	stored := func() core.Group {
		g, _ := s.GroupByFolder("team")
		return g
	}
	audits := func() int { return len(auditRowsFor(t, db, "group.settings")) }

	// No harness stored: empty fields, the control is set.
	html := page()
	for _, want := range []string{
		`name="harness_backend" value=""`, `name="harness_model" value=""`, `name="harness_effort" value=""`,
		`name="harness_default" value="1" checked`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("absent setting: missing %s: %s", want, html)
		}
	}
	if strings.Contains(html, `name="harness"`) {
		t.Fatalf("raw harness field still rendered: %s", html)
	}

	// A stored document prefills the three fields and clears the control.
	const document = `{"backend":"codex","model":"future-model","effort":"high","options":{"future_key":[1,true]}}`
	if err := s.AsUser("alice").SetGroupSettings("team", map[string]json.RawMessage{"harness": json.RawMessage(document), "future_setting": json.RawMessage(`7`)}); err != nil {
		t.Fatal(err)
	}
	html = page()
	for _, want := range []string{
		`name="harness_backend" value="codex"`, `name="harness_model" value="future-model"`, `name="harness_effort" value="high"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("stored document: missing %s: %s", want, html)
		}
	}
	if strings.Contains(html, `name="harness_default" value="1" checked`) {
		t.Fatalf("control set with a stored document: %s", html)
	}

	// Saving builds one document from the form; the stored one is replaced,
	// options is gone; settings the form does not own survive.
	before := audits()
	w := save(url.Values{"agent": {"custom-handler"}, "open": {"1"}, "harness_backend": {"claude"}, "harness_model": {"m1"}, "harness_effort": {"low"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	g := stored()
	if string(g.Harness) != `{"backend":"claude","effort":"low","model":"m1"}` || g.Agent != "custom-handler" || !g.Open {
		t.Fatalf("group: %+v", g)
	}
	items, err := db.Query(t.Context(), objstore.Query{Entity: store.Groups, Partition: store.OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: "team"}})
	if err != nil || len(items) != 1 || !strings.Contains(items[0].Attrs["config"], `"future_setting":7`) {
		t.Fatalf("unrelated setting lost: %v %+v", err, items)
	}
	if rows := auditRowsFor(t, db, "group.settings"); len(rows) != before+1 || rows[0].ActorSub != "alice" {
		t.Fatalf("save audit = %+v, want one new mutation by alice", rows)
	}

	// Blank optional fields are omitted from the document.
	if w = save(url.Values{"harness_backend": {" claude "}}); w.Code != 303 || string(stored().Harness) != `{"backend":"claude"}` {
		t.Fatalf("blank optionals: %d %s", w.Code, stored().Harness)
	}

	// Blank backend with the control unset: refused, nothing written.
	before = audits()
	w = save(url.Values{"harness_model": {"m2"}})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "backend") || string(stored().Harness) != `{"backend":"claude"}` || audits() != before {
		t.Fatalf("blank backend: %d %s harness=%s", w.Code, w.Body.String(), stored().Harness)
	}

	// Use instance defaults removes the setting, whatever the fields hold.
	if w = save(url.Values{"harness_default": {"1"}, "harness_backend": {"codex"}}); w.Code != 303 || len(stored().Harness) != 0 {
		t.Fatalf("defaults: %d %s", w.Code, stored().Harness)
	}
	if !strings.Contains(page(), `name="harness_default" value="1" checked`) {
		t.Fatal("control not set after removal")
	}

	// A caller who is not an admin writes nothing.
	before = audits()
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/groups/team/settings", url.Values{"harness_backend": {"codex"}}.Encode(), "stranger"))
	if w.Code != 403 || audits() != before || len(stored().Harness) != 0 {
		t.Fatalf("unauthorized save: %d", w.Code)
	}
}
