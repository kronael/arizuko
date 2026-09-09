package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"

	"github.com/kronael/arizuko/audit"
)

// What a group delete takes away, and what it leaves.
//
// AN ENTITY HAS NO FOREIGN KEY, so nothing here rides on a cascade: every row a
// delete removes is removed by store/folder_scope.go's purge, explicitly, by
// the same containment predicate for every scope. web_routes and route_tokens
// are two of the scopes it names (BUGS L253).

// seedWebRoute writes one web_route item. routd owns every write of this
// entity (web_routes.go says so), so the fixture addresses the coordinate and
// the attribute names that file EXPORTS rather than minting a second spelling.
func seedWebRoute(t *testing.T, s *Store, prefix, folder string) {
	t.Helper()
	key, err := WebRouteKey(prefix)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Put, Entity: WebRoutes, Key: key,
		Attrs: map[string]string{
			AttrWebAccess:    "public",
			AttrWebFolder:    folder,
			AttrWebCreatedAt: core.NowTS(),
		},
	}); err != nil {
		t.Fatalf("seed web route %s: %v", prefix, err)
	}
}

func countWebRoutes(t *testing.T, s *Store, folder string) int {
	t.Helper()
	all, err := s.AllWebRoutes()
	if err != nil {
		t.Fatalf("AllWebRoutes: %v", err)
	}
	n := 0
	for _, wr := range all {
		if wr.Folder == folder {
			n++
		}
	}
	return n
}

func openFKStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestWebRoutes_PurgedOnGroupDelete: deleting a group removes its web_routes.
func TestWebRoutes_PurgedOnGroupDelete(t *testing.T) {
	s := openFKStore(t)
	if err := s.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatal(err)
	}
	seedWebRoute(t, s, "/pub/acme/", "acme")
	if got := countWebRoutes(t, s, "acme"); got != 1 {
		t.Fatalf("seed: want 1 web_route, got %d", got)
	}

	if _, err := s.DeleteGroup("acme"); err != nil {
		t.Fatal(err)
	}
	if got := countWebRoutes(t, s, "acme"); got != 0 {
		t.Fatalf("after group delete: want 0 web_routes, got %d", got)
	}
}

// TestRouteTokens_PurgedOnGroupDelete: deleting a group removes the delivery
// bearers it owns, so a revoked folder's token stops resolving.
func TestRouteTokens_PurgedOnGroupDelete(t *testing.T) {
	s := openFKStore(t)
	if err := s.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatal(err)
	}
	raw := GenRouteToken()
	if err := s.InsertRouteToken(raw, RouteToken{
		JID:         "web:acme",
		OwnerFolder: "acme",
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.ListRouteTokens("acme"); len(got) != 1 {
		t.Fatalf("seed: want 1 route_token, got %d", len(got))
	}

	if _, err := s.DeleteGroup("acme"); err != nil {
		t.Fatal(err)
	}
	if got := s.ListRouteTokens("acme"); len(got) != 0 {
		t.Fatalf("after group delete: want 0 route_tokens, got %d", len(got))
	}
	if _, ok := s.LookupRouteToken(raw); ok {
		t.Fatal("after group delete: token still resolves (the purge did not fire)")
	}
}

// TestFK_HistoryTablesStrandOnGroupDelete verifies that history tables
// (messages, audit_log, cost_log) are NOT deleted on group removal — per spec
// 5/8 they must strand. This pins the absence of a FK on these tables.
func TestFK_HistoryTablesStrandOnGroupDelete(t *testing.T) {
	s := openFKStore(t)
	db := s.DB()
	if err := s.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO messages (id, chat_jid, sender, content, timestamp)
		 VALUES ('m1', 'acme', 'u', 'hi', datetime('now'))`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := audit.EmitTo(t.Context(), s.Obj(), audit.Event{
		Category: "mutation", Action: "group.test", Actor: "system",
		Folder: "acme", Outcome: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteGroup("acme"); err != nil {
		t.Fatal(err)
	}
	// History strands by spec.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE chat_jid='acme'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("messages stranded count: want 1, got %d", n)
	}
	stranded := 0
	for _, r := range auditRowsFor(t, s, "group.test") {
		if r.Folder == "acme" {
			stranded++
		}
	}
	if stranded != 1 {
		t.Fatalf("stranded audit rows: want 1, got %d", stranded)
	}
}
