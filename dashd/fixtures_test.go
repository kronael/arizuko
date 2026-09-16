package main

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

// Test fixtures for the two owner stores dashd reads. They go through the
// DAEMONS' OWN openers — a fixture built from routd.OpenMem/runed.OpenMem
// cannot drift, because there is no second statement of the schema to drift
// from.
//
// The hand-written CREATE TABLEs these replace had drifted twice with the whole
// suite green (BUGS F44): runedDB's `spawns` was missing `kind` AND still
// carried `mcp_token_jti`, so every runed-page test passed against a schema the
// daemon has not had for weeks — wrong in both directions at once. Adding one
// `kind` read to the page query turned every one of them red.

// routdDB is an empty routd store. Every table dashd's admin and page handlers
// read — groups, messages, routes, scheduled_tasks, task_run_logs, sessions,
// acl, secrets, audit_log, installed_packages, chat_proactive, user_profiles,
// pending_actions — comes from routd's own opener.
func routdDB(t *testing.T) objstore.Store {
	t.Helper()
	d, err := routd.OpenMem()
	if err != nil {
		t.Fatalf("routd.OpenMem: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d.Obj()
}

// The seeds below land routd's rows through the writers routd itself uses.
// That is strictly better than addressing the items directly: a fixture that
// goes in through the same door production writes with cannot agree with a bug
// in that door. Each one picks the AUDIT-FREE twin, because the handler under
// test is what has to emit the audit row — a fixture that emitted one would let
// an audit-free handler pass.

// seedGrant grants one acl row.
func seedGrant(t *testing.T, h objstore.Store, row core.ACLRow) {
	t.Helper()
	if err := store.NewOver(h).PutACLRow(row); err != nil {
		t.Fatalf("seed grant %s %s on %s: %v", row.Principal, row.Action, row.Scope, err)
	}
}

// seedAdmin grants one principal `admin` on one scope — the row requireAdmin
// consults.
func seedAdmin(t *testing.T, h objstore.Store, principal, scope string) {
	t.Helper()
	seedGrant(t, h, core.ACLRow{
		Principal: principal, Action: "admin", Scope: scope, Effect: "allow",
	})
}

// seedFolder creates one group, with the member edge PutGroupRow writes beside
// the row — the floor every folder is born holding.
func seedFolder(t *testing.T, h objstore.Store, folder string) {
	t.Helper()
	if err := store.NewOver(h).PutGroupRow(core.Group{
		Folder: folder, AddedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed group %s: %v", folder, err)
	}
}

// seedScheduledTask creates one scheduled task. A caller-chosen id that is
// already taken is refused, so a fixture cannot silently replace one it seeded
// earlier in the same test.
func seedScheduledTask(t *testing.T, h objstore.Store, task core.Task) {
	t.Helper()
	if task.Created.IsZero() {
		task.Created = time.Now().UTC()
	}
	if err := store.NewOver(h).CreateTask(task); err != nil {
		t.Fatalf("seed task %s: %v", task.ID, err)
	}
}

// seedRoute writes one routing rule and answers the id the mint handed it —
// the handle every route handler is addressed by.
func seedRoute(t *testing.T, h objstore.Store, r core.Route) int64 {
	t.Helper()
	id, err := store.NewOver(h).PutRouteRow(r)
	if err != nil {
		t.Fatalf("seed route %d %s -> %s: %v", r.Seq, r.Match, r.Target, err)
	}
	return id
}

// seedRouteToken mints one delivery credential for a jid. The raw bearer is
// derived from the jid so two tokens in one test never collide on the digest
// that IS their coordinate.
func seedRouteToken(t *testing.T, h objstore.Store, tok store.RouteToken) {
	t.Helper()
	if tok.CreatedAt.IsZero() {
		tok.CreatedAt = time.Now().UTC()
	}
	if err := store.NewOver(h).InsertRouteToken("raw-"+tok.JID, tok); err != nil {
		t.Fatalf("seed route token %s: %v", tok.JID, err)
	}
}

// countGrants counts the grants one principal holds for one action.
func countGrants(t *testing.T, h objstore.Store, principal, action string) int {
	t.Helper()
	rows, err := store.NewOver(h).ListACL(principal)
	if err != nil {
		t.Fatalf("read the grants of %s: %v", principal, err)
	}
	n := 0
	for _, r := range rows {
		if r.Action == action {
			n++
		}
	}
	return n
}

// countRoutes counts the rules matching a predicate, read through the table the
// router resolves.
func countRoutes(t *testing.T, h objstore.Store, match func(core.Route) bool) int {
	t.Helper()
	routes, err := store.NewOver(h).RouteTable()
	if err != nil {
		t.Fatalf("read the route table: %v", err)
	}
	n := 0
	for _, r := range routes {
		if match(r) {
			n++
		}
	}
	return n
}

// countTasks counts the scheduled tasks matching a predicate, over every owner.
func countTasks(t *testing.T, h objstore.Store, match func(core.Task) bool) int {
	t.Helper()
	n := 0
	for _, task := range store.NewOver(h).ListTasks("", true) {
		if match(task) {
			n++
		}
	}
	return n
}

// countRouteTokens counts one folder's delivery credentials whose jid matches.
func countRouteTokens(t *testing.T, h objstore.Store, folder string, match func(store.RouteToken) bool) int {
	t.Helper()
	n := 0
	for _, tok := range store.NewOver(h).ListRouteTokens(folder) {
		if match(tok) {
			n++
		}
	}
	return n
}

// hasFolder reports whether one group exists.
func hasFolder(t *testing.T, h objstore.Store, folder string) bool {
	t.Helper()
	_, ok := store.NewOver(h).GroupByFolder(folder)
	return ok
}

// secretRows is every stored secret, with its blob AS IT IS AT REST — the
// projection `arizuko export` reads, and the only one that carries the sealed
// text, because every other reader decrypts.
func secretRows(t *testing.T, h objstore.Store) []store.ArchiveSecretRow {
	t.Helper()
	rows, err := store.NewOver(h).ExportSecretRows(t.Context())
	if err != nil {
		t.Fatalf("read the stored secrets: %v", err)
	}
	return rows
}

// rawSecretValue is one secret's blob at rest.
func rawSecretValue(t *testing.T, h objstore.Store, scope store.SecretScope, scopeID, key string) string {
	t.Helper()
	for _, r := range secretRows(t, h) {
		if r.ScopeKind == string(scope) && r.ScopeID == scopeID && r.Key == key {
			return r.Value
		}
	}
	t.Fatalf("no secret stored at %s/%s/%s", scope, scopeID, key)
	return ""
}

// countSecrets counts the stored secrets a predicate selects.
func countSecrets(t *testing.T, h objstore.Store, match func(store.ArchiveSecretRow) bool) int {
	t.Helper()
	n := 0
	for _, r := range secretRows(t, h) {
		if match == nil || match(r) {
			n++
		}
	}
	return n
}
