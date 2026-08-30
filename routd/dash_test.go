package routd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
)

// dashSrv builds a routd Server with a seeded DB and its dashboard mounted.
func dashSrv(t *testing.T) (*Server, *DB, http.Handler) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := NewServer(db, nil, nil, nil, time.Minute, "krons.example")
	return s, db, s.Handler()
}

func dashGet(h http.Handler, groups string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "http://k.example/dash/routd/", nil)
	if groups != "" {
		r.Header.Set("X-User-Groups", groups)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// The dashboard is mounted on routd's own mux at its own /dash/ namespace —
// the per-daemon half of 6/28. Before this it did not exist and dashd rendered
// routd's pages from a direct read of routd.db.
func TestDashboardIsMountedOnRoutdsOwnMux(t *testing.T) {
	_, _, h := dashSrv(t)
	w := dashGet(h, `["**"]`)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /dash/routd/ = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want html", ct)
	}
	body := w.Body.String()
	for _, want := range []string{"routd", "Groups", "Routes", "Errored chats", "Pending outbound"} {
		if !strings.Contains(body, want) {
			t.Errorf("the overview does not render %q", want)
		}
	}
	// The breadcrumb roots at the hub, per 6/28's theme section.
	if !strings.Contains(body, `/dash/services/`) {
		t.Error("no breadcrumb back to the hub")
	}
}

// A nil dash keyset is local dev: open, like every /v1 gate. With one armed,
// only an operator gets in — routd must not be the daemon that renders its
// runtime state to a folder-scoped caller.
func TestDashboardGateIsOperatorOnlyWhenArmed(t *testing.T) {
	s, _, h := dashSrv(t)

	if w := dashGet(h, ""); w.Code != http.StatusOK {
		t.Errorf("nil keyset: %d, want 200 (local dev falls open)", w.Code)
	}

	s.SetDashKeySet(&auth.KeySet{})
	if w := dashGet(h, `["atlas"]`); w.Code != http.StatusForbidden {
		t.Errorf("folder-scoped caller: %d, want 403", w.Code)
	}
	if w := dashGet(h, `["**"]`); w.Code != http.StatusOK {
		t.Errorf("operator: %d, want 200", w.Code)
	}
}

// The overview counts what routd owns, through the SAME readers its /v1
// handlers use — so the page cannot drift from the API.
func TestOverviewCountsRoutdsOwnState(t *testing.T) {
	s, db, h := dashSrv(t)
	for _, f := range []string{"acme", "acme/support"} {
		if err := db.PutGroup(core.Group{Folder: f}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.AddRoute(core.Route{Seq: 1, Match: "platform=slack", Target: "acme"}); err != nil {
		t.Fatal(err)
	}

	snap := s.snapshot()
	if snap.Groups != 2 {
		t.Errorf("Groups = %d, want 2", snap.Groups)
	}
	if snap.Routes != 1 {
		t.Errorf("Routes = %d, want 1", snap.Routes)
	}
	if !strings.Contains(dashGet(h, `["**"]`).Body.String(), ">2<") {
		t.Error("the rendered page does not carry the group count")
	}
}

// An errored chat is the one figure an operator acts on from the overview, so
// it gets rows and not only a count.
func TestErroredChatsRenderAsRows(t *testing.T) {
	if got := erroredTable(nil); !strings.Contains(got, "No errored chats") {
		t.Errorf("empty table = %q, want the clean-state line", got)
	}
	rows := []ErroredChat{
		{ChatJID: "slack:T1/channel/C1", RoutedTo: "acme", Count: 3, LastAt: time.Now()},
		{ChatJID: "tg:2", RoutedTo: "acme/support", Count: 1, LastAt: time.Now().Add(-time.Hour)},
	}
	got := erroredTable(rows)
	for _, want := range []string{"slack:T1/channel/C1", "acme/support", "<table>"} {
		if !strings.Contains(got, want) {
			t.Errorf("the errored table omits %q", want)
		}
	}
	// Newest first: the recent failure is the actionable one.
	if strings.Index(got, "slack:T1") > strings.Index(got, "tg:2") {
		t.Error("errored rows are not newest-first")
	}
}

// The preview is capped, and says so — an instance with a broken adapter can
// carry hundreds and the overview is a summary, not the errored page.
func TestErroredPreviewIsCappedAndSaysSo(t *testing.T) {
	var rows []ErroredChat
	for i := range erroredPreview + 7 {
		rows = append(rows, ErroredChat{
			ChatJID: "tg:" + string(rune('a'+i%26)), RoutedTo: "acme",
			Count: 1, LastAt: time.Now().Add(-time.Duration(i) * time.Minute),
		})
	}
	got := erroredTable(rows)
	if n := strings.Count(got, "<tr><td>"); n != erroredPreview {
		t.Errorf("rendered %d rows, want the %d cap", n, erroredPreview)
	}
	if !strings.Contains(got, "7 more not shown") {
		t.Error("the cap is silent; an operator cannot tell the table is truncated")
	}
}

// A chat JID is operator-supplied data rendered into HTML. It must be escaped,
// or a crafted channel name is stored XSS on the operator's own dashboard.
func TestRenderedValuesAreEscaped(t *testing.T) {
	got := erroredTable([]ErroredChat{{
		ChatJID:  `<script>alert(1)</script>`,
		RoutedTo: `a"onmouseover="x`,
		Count:    1, LastAt: time.Now(),
	}})
	if strings.Contains(got, "<script>") {
		t.Error("a chat jid rendered as live markup")
	}
	if strings.Contains(got, `"onmouseover="`) {
		t.Error("a folder name broke out of its attribute")
	}
}

// dashPost drives a dashboard mutation with a same-origin POST.
func dashPost(h http.Handler, path, form, groups string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "http://k.example"+path, strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://k.example")
	if groups != "" {
		r.Header.Set("X-User-Groups", groups)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// proxyd matches longest-prefix, so routing /dash/routd/ to routd captures the
// POST controls under it too. dashd served retry and replay from a raw UPDATE
// on routd.db; if routd did not answer them, the takeover would leave an
// operator with two dead buttons — 6/30's rule is that a copy leaves dashd only
// once the replacement is live.
func TestDashboardServesTheControlsItTookOver(t *testing.T) {
	_, _, h := dashSrv(t)
	for _, p := range []string{"/dash/routd/retry", "/dash/routd/replay"} {
		w := dashPost(h, p, "chat_jid=x&id=x", `["**"]`)
		if w.Code == http.StatusNotFound || w.Code == http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d; the path routes to routd and nothing answers it", p, w.Code)
		}
	}
}

// Retry clears the chat's errored rows so the loop feeds them again, and says
// so when there was nothing to clear — a retry that retried nothing must not
// redirect to a page that looks unchanged for no stated reason.
func TestRetryClearsErroredRowsAndReportsAnEmptyRetry(t *testing.T) {
	_, db, h := dashSrv(t)
	if err := db.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatal(err)
	}
	m := core.Message{ID: "m1", ChatJID: "tg:1", Sender: "u", Content: "x",
		Timestamp: time.Now(), RoutedTo: "acme"}
	if err := db.PutMessage(m); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessagesErrored([]string{"m1"}); err != nil {
		t.Fatal(err)
	}

	w := dashPost(h, "/dash/routd/retry", "chat_jid=tg:1", `["**"]`)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("retry = %d, want 303", w.Code)
	}
	errored, err := db.ErroredChats("", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(errored) != 0 {
		t.Errorf("errored chats after retry = %d, want 0", len(errored))
	}

	// Second retry has nothing to clear.
	again := dashPost(h, "/dash/routd/retry", "chat_jid=tg:1", `["**"]`)
	if again.Code == http.StatusSeeOther {
		t.Error("a retry that cleared nothing reported success")
	}
}

// The controls are writes: operator-only AND same-origin, or a third-party page
// can drive an operator's browser into clearing a chat's error state.
func TestControlsRefuseNonOperatorAndCrossOrigin(t *testing.T) {
	s, _, h := dashSrv(t)
	s.SetDashKeySet(&auth.KeySet{})

	if w := dashPost(h, "/dash/routd/retry", "chat_jid=tg:1", `["acme"]`); w.Code != http.StatusForbidden {
		t.Errorf("folder-scoped caller: %d, want 403", w.Code)
	}
	r := httptest.NewRequest(http.MethodPost, "http://k.example/dash/routd/retry",
		strings.NewReader("chat_jid=tg:1"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://evil.example")
	r.Header.Set("X-User-Groups", `["**"]`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-origin operator write: %d, want 403", w.Code)
	}
}
