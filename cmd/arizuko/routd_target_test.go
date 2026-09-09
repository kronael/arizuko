package main

import (
	"bytes"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"context"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/sqlstore"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
	"strings"
)

// setupSplitStore creates BOTH a migrated routd.db (routd owns acl/secrets in the
// split) and a migrated messages.db in dir, returning the dir. The CLI grant/
// secret writers must land in routd.db; messages.db must stay untouched.
func setupSplitStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// routd.Open migrates routd.db (creates acl/secrets/... tables).
	rd, err := routd.Create(dir)
	if err != nil {
		t.Fatalf("routd.Open: %v", err)
	}
	rd.Close()
	// store.Open migrates messages.db (the monolith we must NOT write).
	ms, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	ms.Close()
	return dir
}

// countRows opens a raw handle on the named owner's DB under the store dir
// (store/<owner>/<owner>.db) and returns the COUNT(*) of q. Pass "" as owner
// for a flat file such as the retired messages.db.
func countRows(t *testing.T, dir, owner, q string, args ...any) int {
	t.Helper()
	file := filepath.Join(dir, owner)
	if _, ok := store.OwnerDBs[owner]; ok {
		file = store.OwnerDBPath(dir, owner)
	}
	db, err := db_utils.Open(db_utils.FileDSN(file))
	if err != nil {
		t.Fatalf("open %s: %v", file, err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count %s (%s): %v", file, q, err)
	}
	return n
}

// countItems opens a raw handle on the named owner's DB under the store dir and
// counts the items of one entity whose attributes and coordinate satisfy match.
// Pass "" as owner for a flat file such as the retired messages.db.
//
// It is the entity twin of countRows, and every assertion below that names one
// of routd's thirteen goes through it: a `SELECT … WHERE principal=` has no
// column to name, and the question those tests ask — "did the row land HERE and
// not THERE" — is the same question over items.
func countItems(t *testing.T, dir, owner, entity string, match func(objstore.Item) bool) int {
	t.Helper()
	file := filepath.Join(dir, owner)
	if _, ok := store.OwnerDBs[owner]; ok {
		file = store.OwnerDBPath(dir, owner)
	}
	db, err := db_utils.Open(db_utils.FileDSN(file))
	if err != nil {
		t.Fatalf("open %s: %v", file, err)
	}
	defer db.Close()
	items, err := sqlstore.New(db).Query(context.Background(), objstore.Query{
		Entity: entity, Partition: store.OwnerRoutd,
	})
	if err != nil {
		t.Fatalf("read %s in %s: %v", entity, file, err)
	}
	n := 0
	for _, it := range items {
		if match(it) {
			n++
		}
	}
	return n
}

// keyIs matches an item whose coordinate joins to exactly these fields.
func keyIs(parts ...string) func(objstore.Item) bool {
	want := strings.Join(parts, store.KeySep)
	return func(it objstore.Item) bool { return it.Key.Sort == want }
}

// keyLeads matches an item whose coordinate begins with these fields.
func keyLeads(parts ...string) func(objstore.Item) bool {
	want := strings.Join(parts, store.KeySep) + store.KeySep
	return func(it objstore.Item) bool { return strings.HasPrefix(it.Key.Sort, want) }
}

// TestGrantLandsInRoutdDB proves `arizuko grant` (runGrant via mustOpenACL/
// OpenRoutd) writes the acl row into routd.db and leaves messages.db empty.
func TestGrantLandsInRoutdDB(t *testing.T) {
	dir := setupSplitStore(t)

	s, err := store.OpenRoutd(dir)
	if err != nil {
		t.Fatalf("OpenRoutd: %v", err)
	}
	var out bytes.Buffer
	if err := runGrant(s, "github:42", "main", "", &out); err != nil {
		t.Fatalf("runGrant: %v", err)
	}
	if err := runGrant(s, "github:42", "**", "", &out); err != nil {
		t.Fatalf("operator grant: %v", err)
	}
	s.Close()

	if n := countItems(t, dir, store.OwnerRoutd, store.ACL, keyLeads("github:42")); n != 1 {
		t.Errorf("routd.db acl rows = %d, want 1", n)
	}
	if n := countItems(t, dir, store.OwnerRoutd, store.ACLMembership,
		keyIs("github:42", "role:operator")); n != 1 {
		t.Errorf("routd.db membership rows = %d, want 1", n)
	}
	// messages.db must be untouched by the CLI grant path.
	if n := countItems(t, dir, "messages.db", store.ACL, keyLeads("github:42")); n != 0 {
		t.Errorf("messages.db acl rows = %d, want 0 (CLI must not write the monolith)", n)
	}
	if n := countItems(t, dir, "messages.db", store.ACLMembership, keyLeads("github:42")); n != 0 {
		t.Errorf("messages.db membership rows = %d, want 0", n)
	}
}

// TestUngrantRemovesFromRoutdDB proves the CLI revoke path deletes from routd.db.
func TestUngrantRemovesFromRoutdDB(t *testing.T) {
	dir := setupSplitStore(t)

	s, err := store.OpenRoutd(dir)
	if err != nil {
		t.Fatalf("OpenRoutd: %v", err)
	}
	var out bytes.Buffer
	runGrant(s, "u1", "main", "", &out)
	runGrant(s, "u1", "**", "", &out)
	if err := runUngrant(s, "u1", "main", &out); err != nil {
		t.Fatalf("runUngrant: %v", err)
	}
	if err := runUngrant(s, "u1", "**", &out); err != nil {
		t.Fatalf("runUngrant operator: %v", err)
	}
	s.Close()

	if n := countItems(t, dir, store.OwnerRoutd, store.ACL, keyLeads("u1")); n != 0 {
		t.Errorf("routd.db acl rows after ungrant = %d, want 0", n)
	}
	if n := countItems(t, dir, store.OwnerRoutd, store.ACLMembership, keyLeads("u1")); n != 0 {
		t.Errorf("routd.db membership rows after ungrant = %d, want 0", n)
	}
}

// TestSecretLandsInRoutdDB proves `arizuko secret set` (runSecretSet via
// PutSecretRow) writes into routd.db, not messages.db.
func TestSecretLandsInRoutdDB(t *testing.T) {
	dir := setupSplitStore(t)

	s, err := store.OpenRoutd(dir)
	if err != nil {
		t.Fatalf("OpenRoutd: %v", err)
	}
	s.SetSecretKeys([]byte("cli-test-key")) // the store refuses unsealed writes (BUGS S2)
	var out bytes.Buffer
	if err := runSecretSet(s, store.ScopeFolder, "main", "GITHUB_TOKEN", "ghp-xyz", &out); err != nil {
		t.Fatalf("runSecretSet: %v", err)
	}
	s.Close()

	if n := countItems(t, dir, store.OwnerRoutd, store.Secrets,
		keyIs("folder", "main", "GITHUB_TOKEN")); n != 1 {
		t.Errorf("routd.db secret rows = %d, want 1", n)
	}
	if n := countItems(t, dir, "messages.db", store.Secrets,
		keyIs("folder", "main", "GITHUB_TOKEN")); n != 0 {
		t.Errorf("messages.db secret rows = %d, want 0 (CLI must not write the monolith)", n)
	}

	// Delete path also targets routd.db.
	s2, err := store.OpenRoutd(dir)
	if err != nil {
		t.Fatalf("OpenRoutd: %v", err)
	}
	if err := runSecretDelete(s2, store.ScopeFolder, "main", "GITHUB_TOKEN", &out); err != nil {
		t.Fatalf("runSecretDelete: %v", err)
	}
	s2.Close()
	if n := countItems(t, dir, store.OwnerRoutd, store.Secrets,
		keyIs("folder", "main", "GITHUB_TOKEN")); n != 0 {
		t.Errorf("routd.db secret rows after delete = %d, want 0", n)
	}
}

// TestGroupListReadsRoutdDB proves `arizuko group list` reads the group set out
// of routd.db — the DB every other `group` verb already WRITES via mustOpenACL.
// The read used to go through store.Open, so it printed the frozen messages.db
// group set and silently disagreed with the writes beside it (BUGS.md Q1).
func TestGroupListReadsRoutdDB(t *testing.T) {
	dir := setupSplitStore(t)

	rs, err := store.OpenRoutd(dir)
	if err != nil {
		t.Fatalf("OpenRoutd: %v", err)
	}
	if err := rs.PutGroupRow(core.Group{Folder: "live/eng", AddedAt: time.Now()}); err != nil {
		t.Fatalf("PutGroupRow: %v", err)
	}
	rs.Close()

	// A group only the retired monolith knows about must NOT appear.
	ms, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := ms.PutGroupRow(core.Group{Folder: "stale/monolith", AddedAt: time.Now()}); err != nil {
		t.Fatalf("seed monolith group: %v", err)
	}
	ms.Close()

	// The read `group list` performs, on the handle mustOpenACL returns.
	ls, err := store.OpenRoutd(dir)
	if err != nil {
		t.Fatalf("OpenRoutd: %v", err)
	}
	defer ls.Close()
	got := ls.AllGroups()
	if _, ok := got["live/eng"]; !ok {
		t.Errorf("group list missed routd.db group live/eng; got %v", keysOf(got))
	}
	if _, ok := got["stale/monolith"]; ok {
		t.Errorf("group list surfaced the frozen messages.db group set: %v", keysOf(got))
	}
}

func keysOf(m map[string]core.Group) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
