package main

// seed_test.go lands routd's rows through the writers onbod itself uses, and
// reads them back through the readers the daemon reads with. A fixture that
// goes in through the same door production writes with cannot agree with a bug
// in that door, and one that asserts through the same reader cannot agree with
// a bug in the read.

import (
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// seedFolder creates one group, with the member edge PutGroupRow writes beside
// the row — the floor every folder is born holding.
func seedFolder(t *testing.T, db *store.Store, folder string) {
	t.Helper()
	if err := db.PutGroupRow(core.Group{
		Folder: folder, AddedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed group %s: %v", folder, err)
	}
}

// seedGrant grants one acl row, audit-free: the handler under test is what has
// to record the act.
func seedGrant(t *testing.T, db *store.Store, principal, action, scope string) {
	t.Helper()
	if err := db.PutACLRow(core.ACLRow{
		Principal: principal, Action: action, Scope: scope, Effect: "allow",
	}); err != nil {
		t.Fatalf("seed grant %s %s on %s: %v", principal, action, scope, err)
	}
}

// seedMembership links one principal under another.
func seedMembership(t *testing.T, db *store.Store, child, parent, addedBy string) {
	t.Helper()
	if err := db.PutMembership(child, parent, addedBy); err != nil {
		t.Fatalf("seed membership %s under %s: %v", child, parent, err)
	}
}

// seedRoute writes one routing rule with NO attribution, which is the state a
// rule written before the act was recorded is in.
func seedRoute(t *testing.T, db *store.Store, seq int, match, target string) {
	t.Helper()
	if _, err := db.PutRouteRow(core.Route{
		Seq: seq, Match: match, Target: target,
	}); err != nil {
		t.Fatalf("seed route %d %s -> %s: %v", seq, match, target, err)
	}
}

// routeRows is the whole route table with the two attribution attributes on it,
// which is the projection `arizuko export` reads and the only one that carries
// them.
func routeRows(t *testing.T, db *store.Store) []resources.RoutesRow {
	t.Helper()
	m, err := resreg.GetResource(resreg.On(db.Obj()), "routes")
	if err != nil {
		t.Fatalf("read the route table: %v", err)
	}
	rows, ok := m["routes"].([]resources.RoutesRow)
	if !ok {
		t.Fatalf("route rows came back as %T", m["routes"])
	}
	return rows
}

// routesMatching selects the rules a predicate names.
func routesMatching(t *testing.T, db *store.Store, match func(resources.RoutesRow) bool) []resources.RoutesRow {
	t.Helper()
	var out []resources.RoutesRow
	for _, r := range routeRows(t, db) {
		if match(r) {
			out = append(out, r)
		}
	}
	return out
}

// countRoutes is the blast-radius probe: the whole point is how MANY rules an
// act wrote, not just whether the one it was about exists.
func countRoutes(t *testing.T, db *store.Store) int {
	t.Helper()
	return len(routeRows(t, db))
}

// routeAttribution reads the (added_by, added_via) pair off the rule matching
// `match`. No such rule → ("", "", false); a rule written before the act was
// recorded carries neither, and reads as ("", "").
func routeAttribution(t *testing.T, db *store.Store, match string) (string, string, bool) {
	t.Helper()
	for _, r := range routeRows(t, db) {
		if r.Match == match {
			return r.AddedBy, r.AddedVia, true
		}
	}
	return "", "", false
}

// routeTarget is the folder one rule sends its chat to, or "" when no rule
// matches.
func routeTarget(t *testing.T, db *store.Store, match string) string {
	t.Helper()
	for _, r := range routeRows(t, db) {
		if r.Match == match {
			return r.Target
		}
	}
	return ""
}

// routeIDInto is the handle of a rule aimed at one folder, or 0 when there is
// none — the id every route endpoint is addressed by.
func routeIDInto(t *testing.T, db *store.Store, target string) int64 {
	t.Helper()
	for _, r := range routeRows(t, db) {
		if r.Target == target {
			return int64(r.ID)
		}
	}
	return 0
}

// routeExists reports whether a rule still carries one handle.
func routeExists(t *testing.T, db *store.Store, id int64) bool {
	t.Helper()
	for _, r := range routeRows(t, db) {
		if int64(r.ID) == id {
			return true
		}
	}
	return false
}

// deleteFolder removes a world through the ONE delete: the purge that takes the
// subtree and every row conferring on it or routing into it.
func deleteFolder(t *testing.T, db *store.Store, folder string) {
	t.Helper()
	if _, _, err := db.DeleteGroup(folder); err != nil {
		t.Fatalf("delete group %s: %v", folder, err)
	}
}

// routeTokenItems is every credential in the table, delivery and pairing alike.
// The attributes it reads back are route_tokens' own — `jid` and `owner_folder`
// are the `db:` tags resreg/resources/route_tokens.go declares, and store writes
// them under those names.
func routeTokenItems(t *testing.T, db *store.Store) []objstore.Item {
	t.Helper()
	items, err := db.Obj().Query(t.Context(), objstore.Query{
		Entity: store.RouteTokens, Partition: store.OwnerRoutd,
	})
	if err != nil {
		t.Fatalf("read the route tokens: %v", err)
	}
	return items
}

// pairingTokensFor is the pairing credentials minted for one chat. A pairing
// token is the one a delivery read does NOT answer, so the two exported reads
// together say which kind each row is: the whole table minus the delivery
// bearers.
func pairingTokensFor(t *testing.T, db *store.Store, jid string) []objstore.Item {
	t.Helper()
	delivery := map[string]bool{}
	rows, err := db.ExportRouteTokens(t.Context())
	if err != nil {
		t.Fatalf("read the delivery tokens: %v", err)
	}
	for _, r := range rows {
		delivery[r.TokenHash] = true
	}
	var out []objstore.Item
	for _, it := range routeTokenItems(t, db) {
		if it.Attrs["jid"] == jid && !delivery[it.Key.Sort] {
			out = append(out, it)
		}
	}
	return out
}

// folderName answers the folder one group holds, or "" when there is no such
// group — the shape a "does the world exist" read compares against.
func folderName(t *testing.T, db *store.Store, folder string) string {
	t.Helper()
	g, ok := db.GroupByFolder(folder)
	if !ok {
		return ""
	}
	return g.Folder
}

// firstScope is one scope a principal holds, or "" when it holds none. The
// callers grant exactly one, so which one is not a question they ask.
func firstScope(t *testing.T, db *store.Store, principal string) string {
	t.Helper()
	rows, err := db.ListACL(principal)
	if err != nil {
		t.Fatalf("read the grants of %s: %v", principal, err)
	}
	if len(rows) == 0 {
		return ""
	}
	return rows[0].Scope
}

// parentCount is how many principals one child sits under.
func parentCount(t *testing.T, db *store.Store, child string) int {
	t.Helper()
	edges, err := db.ParentEdges(child)
	if err != nil {
		t.Fatalf("read the parents of %s: %v", child, err)
	}
	return len(edges)
}

// boolCount renders a presence answer as the count a caller compares.
func boolCount(present bool) int {
	if present {
		return 1
	}
	return 0
}

// countRouteTokens is every credential in the table, delivery and pairing alike
// — the count "was anything minted" asks.
func countRouteTokens(t *testing.T, db *store.Store) int {
	t.Helper()
	n, err := db.CountRouteTokens(t.Context())
	if err != nil {
		t.Fatalf("count the route tokens: %v", err)
	}
	return n
}

// adminScopes lists the scopes one principal administers.
func adminScopes(t *testing.T, db *store.Store, principal string) []string {
	t.Helper()
	rows, err := db.ListACL(principal)
	if err != nil {
		t.Fatalf("read the grants of %s: %v", principal, err)
	}
	var out []string
	for _, r := range rows {
		if r.Action == "admin" {
			out = append(out, r.Scope)
		}
	}
	return out
}

// edgeAddedBy is what a membership edge records about who wrote it. Only one
// parent is ever seeded per child here, so which edge is not a question the
// callers ask.
func edgeAddedBy(t *testing.T, db *store.Store, child string) string {
	t.Helper()
	edges, err := db.ParentEdges(child)
	if err != nil {
		t.Fatalf("read the parents of %s: %v", child, err)
	}
	if len(edges) == 0 {
		return ""
	}
	return edges[0].AddedBy
}

// seedACLScar writes one grant item WITHOUT going through store's writer, for
// the reader defences whose input no writer here can produce. The coordinate is
// acl's own — the six fields of the grant, joined by store.KeySep.
func seedACLScar(t *testing.T, db *store.Store, row core.ACLRow) {
	t.Helper()
	sort := strings.Join([]string{
		row.Principal, row.Action, row.Scope, row.Params, row.Predicate, row.Effect,
	}, store.KeySep)
	if _, err := db.Obj().GuardedBatch(t.Context(), nil, []objstore.Write{{
		Op: objstore.Put, Entity: store.ACL,
		Key:   objstore.Key{Partition: store.OwnerRoutd, Sort: sort},
		Attrs: map[string]string{"granted_at": core.NowTS(), "grant_option": "0"},
	}}); err != nil {
		t.Fatalf("seed the acl scar %+v: %v", row, err)
	}
}
