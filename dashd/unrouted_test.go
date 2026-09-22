package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

func seedInbound(t *testing.T, db objstore.Store, id, jid, sender string, at time.Time) {
	t.Helper()
	seedRoutdMessage(t, db, core.Message{
		ID: id, ChatJID: jid, Sender: sender, Content: "hi", Timestamp: at, Verb: "message",
	})
}

// seedRoutdMessage appends one message through the writer routd itself uses.
// `messages` is an objstore entity, so a fixture cannot reach it with an INSERT
// naming columns; and the write fans out to the folder, activity and delivery
// indexes the dashboard reads.
func seedRoutdMessage(t *testing.T, db objstore.Store, m core.Message) {
	t.Helper()
	if err := store.NewOver(db).PutMessage(m); err != nil {
		t.Fatalf("seed message %s: %v", m.ID, err)
	}
}

func routesPage(t *testing.T, d *dash, sub string) string {
	t.Helper()
	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, adminReq("GET", "/dash/routes/", "", sub))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /dash/routes/ as %s = %d; body=%q", sub, w.Code, w.Body.String())
	}
	return w.Body.String()
}

// The held set is the operator's view (spec 6/35-unrouted-traffic): a chat no route claims shows
// with a claim form fixed to its JID; a chat an explicit route claims does not.
// The section is operator-only — a held chat belongs to no folder, so an admin
// of one folder has no claim to see it — and the claim form posts the existing
// route write, so nothing here mints a second writer.
func TestRoutes_UnroutedSectionOperatorOnly(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	seedFolderAdmin(t, d, "bob@x", "bob")
	now := time.Now()
	seedRoute(t, routd, core.Route{Seq: 0, Match: "chat_jid=telegram:group/1", Target: "team"})
	seedInbound(t, routd, "r1", "telegram:group/1", "u1", now)
	seedInbound(t, routd, "h1", "telegram:user/7", "u7", now.Add(-time.Minute))
	seedInbound(t, routd, "h2", "telegram:user/7", "u7", now)

	body := routesPage(t, d, "alice@x")
	for _, want := range []string{
		"Unrouted chats",
		`name="match" value="chat_jid=telegram:user/7"`,
		`<option value="team">team</option>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("operator page lacks %q", want)
		}
	}
	if strings.Contains(body, `value="chat_jid=telegram:group/1"`) {
		t.Error("a routed chat is offered for claim")
	}

	body = routesPage(t, d, "bob@x")
	if strings.Contains(body, "Unrouted chats") || strings.Contains(body, "telegram:user/7") {
		t.Error("a folder admin sees the held set")
	}
}

// Claiming a held chat is the existing route write, and the hold ends with it.
func TestRoutes_ClaimWritesTheRoute(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	seedInbound(t, routd, "h1", "telegram:user/7", "u7", time.Now())

	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, adminReq("POST", "/dash/routes/",
		"seq=0&match=chat_jid%3Dtelegram%3Auser%2F7&target=team", "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("claim = %d, want 303; body=%q", w.Code, w.Body.String())
	}
	if n := countRoutes(t, routd, func(r core.Route) bool {
		return r.Match == "chat_jid=telegram:user/7" && r.Target == "team"
	}); n != 1 {
		t.Fatalf("route rows after claim = %d, want 1", n)
	}
	if body := routesPage(t, d, "alice@x"); strings.Contains(body, `value="chat_jid=telegram:user/7"`) {
		t.Error("claimed chat still offered for claim")
	}
}

// routd not answering is an error the operator sees, never an empty table:
// "no unrouted chats" is a statement they act on.
func TestRoutes_UnroutedRoutdDownIsAnError(t *testing.T) {
	d, _ := splitAdminDash(t, "alice@x")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/routes", func(w http.ResponseWriter, _ *http.Request) {
		writeStubJSON(w, []any{})
	})
	mux.HandleFunc("GET /v1/routing/unrouted", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "store_error", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	d.routdURL = srv.URL

	body := routesPage(t, d, "alice@x")
	if !strings.Contains(body, "could not reach routd for unrouted chats") {
		t.Error("routd failure not surfaced")
	}
	if strings.Contains(body, "No unrouted chats") {
		t.Error("routd failure rendered as an empty held set")
	}
}
