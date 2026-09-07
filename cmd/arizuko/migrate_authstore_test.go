package main

// The one-way move, on a database built to the shape the live instances carry.
//
// It is rehearsed against snapshots of all three before it is believed, but a
// rehearsal is not a regression test: the snapshots are not in the tree and
// their contents change. These bodies pin the two things the rehearsal cannot
// — that the tool refuses what the entities cannot hold, and that it tells a
// migrated database apart from an unmigrated one now that an entity keeps the
// name of the table it replaces.

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/objstore/sqlstore"
)

// authstoreLegacySchema is the shape measured on krons, marinade and sloth on
// 2026-09-07: byte-identical on all three.
const authstoreLegacySchema = `
CREATE TABLE signing_keys (
  kid TEXT PRIMARY KEY, priv_pem TEXT NOT NULL, pub_pem TEXT NOT NULL,
  active INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, retired_at TEXT);
CREATE TABLE auth_users (
  user_id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE TABLE oauth_identities (
  user_id TEXT NOT NULL, provider TEXT NOT NULL, provider_sub TEXT NOT NULL,
  linked_at TEXT NOT NULL, UNIQUE(user_id, provider), UNIQUE(provider, provider_sub));
CREATE TABLE refresh_tokens (
  token_hash TEXT PRIMARY KEY, family_id TEXT NOT NULL, sub TEXT NOT NULL,
  scope TEXT NOT NULL DEFAULT '', aud TEXT NOT NULL DEFAULT '',
  issued_at TEXT NOT NULL, expires_at TEXT NOT NULL, used_at TEXT, revoked_at TEXT);`

// legacyDB writes a file-backed database named file, carrying schema, and
// returns its path plus the open handle. It is a file rather than a memory
// database because a migration tool opens the path itself.
func legacyDB(t *testing.T, file, schema string) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), file)
	if err := db_utils.CreateDBFile(path); err != nil {
		t.Fatal(err)
	}
	db, err := db_utils.Open(db_utils.FileDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return path, db
}

// A login and everything it rotated into becomes one family carrying the two
// numbers the correlated subqueries used to compute.
func TestMigrateAuthstoreDerivesFamiliesFromRotations(t *testing.T) {
	path, db := legacyDB(t, "auth.db", authstoreLegacySchema)
	k, err := auth.NewSigningKey("1780785261-8c676f92")
	if err != nil {
		t.Fatal(err)
	}
	privPEM, pubPEM, err := authstore.EncodeKey(k.Priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO signing_keys (kid, priv_pem, pub_pem, active, created_at)
		 VALUES (?, ?, ?, 1, '2026-06-06T22:34:21Z')`, k.Kid, privPEM, pubPEM); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO auth_users (user_id, name, created_at)
		 VALUES ('google:alice', 'Alice', '2026-06-16T11:33:44Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO oauth_identities (user_id, provider, provider_sub, linked_at)
		 VALUES ('google:alice', 'google', 'alice', '2026-06-16T11:33:44Z')`); err != nil {
		t.Fatal(err)
	}
	// One rotating family plus one that never rotated, which is the shape
	// marinade carries: 72 rows in one lineage and six singletons.
	for i, spent := range []string{"2026-06-16T15:32:11Z", "2026-06-16T15:33:00Z", ""} {
		used := any(nil)
		if spent != "" {
			used = spent
		}
		if _, err := db.Exec(
			`INSERT INTO refresh_tokens
			   (token_hash, family_id, sub, scope, aud, issued_at, expires_at, used_at)
			 VALUES (?, 'fam-a', 'google:alice', '**', '', ?, '2026-07-16T15:32:02Z', ?)`,
			"hash-a-"+string(rune('0'+i)),
			[]string{"2026-06-16T15:32:02Z", "2026-06-16T15:32:11Z", "2026-06-16T15:33:00Z"}[i],
			used); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(
		`INSERT INTO refresh_tokens
		   (token_hash, family_id, sub, scope, aud, issued_at, expires_at)
		 VALUES ('hash-b', 'fam-b', 'google:bob', '', '', '2026-06-16T14:47:34Z',
		         '2026-07-16T14:47:34Z')`); err != nil {
		t.Fatal(err)
	}

	if err := migrateAuthstore(path, false); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	st := authstore.New(sqlstore.New(db))
	rows, err := st.SessionsByFamily("", 0, time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("4 tokens produced %d families, want 2: %+v", len(rows), rows)
	}
	byFamily := map[string]int{}
	for _, r := range rows {
		byFamily[r.FamilyID] = r.Rotations
		if r.Status != "active" {
			t.Fatalf("family %s is %q inside its window", r.FamilyID, r.Status)
		}
	}
	if byFamily["fam-a"] != 3 || byFamily["fam-b"] != 1 {
		t.Fatalf("rotations came out %v, want fam-a 3 and fam-b 1", byFamily)
	}
	for _, r := range rows {
		if r.FamilyID != "fam-a" {
			continue
		}
		if r.StartedAt != "2026-06-16T15:32:02Z" {
			t.Fatalf("started_at = %q, want the earliest issue", r.StartedAt)
		}
		// The family takes its lifecycle from the UNSPENT token, which is the
		// row the listing used to read.
		if r.RenewedAt != "2026-06-16T15:33:00Z" {
			t.Fatalf("renewed_at = %q, want the unspent token's issue", r.RenewedAt)
		}
	}
	keys, err := st.SigningKeysWithMaterial()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || !keys[0].Active || keys[0].Key.Kid != k.Kid {
		t.Fatalf("the migrated key reads back as %+v", keys)
	}
	if !keys[0].Key.Priv.Equal(k.Priv) {
		t.Fatal("the migrated private key is not the one that was stored")
	}
	meta, err := st.SigningKeyMetadata(time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(meta) != 1 || !meta[0].Active || meta[0].Kid != k.Kid {
		t.Fatalf("the migrated key reads back as %+v", meta)
	}
	owner, name, _, subs, ok := st.OAuthIdentityForSub("google:alice")
	if !ok || owner != "google:alice" || name != "Alice" || len(subs) != 1 {
		t.Fatalf("the migrated identity reads back as (%q, %q, %v, ok=%v)", owner, name, subs, ok)
	}

	// The old rows are set aside rather than dropped: they are the evidence an
	// operator needs if a session stops working.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM refresh_tokens_v1`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("refresh_tokens_v1 holds %d rows (%v), want the 4 that were read", n, err)
	}
	// And a re-run is a no-op rather than a failure.
	if err := migrateAuthstore(path, false); err != nil {
		t.Fatalf("a second run must be idempotent: %v", err)
	}
}

// An entity keeps the name of the table it replaces, so presence alone cannot
// tell a migrated database from an unmigrated one. Running the tool on a
// database that holds only entities must report that there is nothing to do —
// not read the three-column entity as if it carried `kid`.
func TestMigrateAuthstoreLeavesAnAlreadyMigratedDatabaseAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	if err := db_utils.CreateDBFile(path); err != nil {
		t.Fatal(err)
	}
	db, err := db_utils.Open(db_utils.FileDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := sqlstore.New(db)
	for _, entity := range authstore.Entities {
		if err := s.CreateEntity(t.Context(), entity); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := authstore.New(s).StartRefreshFamily("alice", nil, "", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := migrateAuthstore(path, false); err != nil {
		t.Fatalf("a database that holds only entities must be left alone: %v", err)
	}
	// Anti-vacuity: the session that was there is still there, so "nothing to
	// migrate" did not mean "nothing was read".
	rows, err := authstore.New(s).SessionsByFamily("", 0, time.Now())
	if err != nil || len(rows) != 1 {
		t.Fatalf("the existing session came back as %+v (%v)", rows, err)
	}
}

// A family the entities cannot represent stops the run, by name, and writes
// nothing. Two live credentials in one lineage is the shape that matters: the
// import must not pick one of them to call current.
func TestMigrateAuthstoreRefusesAForkedFamily(t *testing.T) {
	path, db := legacyDB(t, "auth.db", authstoreLegacySchema)
	for _, hash := range []string{"h1", "h2"} {
		if _, err := db.Exec(
			`INSERT INTO refresh_tokens
			   (token_hash, family_id, sub, scope, aud, issued_at, expires_at)
			 VALUES (?, 'forked', 'alice', '', '', '2026-06-16T15:32:02Z',
			         '2026-07-16T15:32:02Z')`, hash); err != nil {
			t.Fatal(err)
		}
	}
	err := migrateAuthstore(path, true)
	if err == nil {
		t.Fatal("a family with two unspent tokens must stop a dry run")
	}
	if !strings.Contains(err.Error(), "forked") {
		t.Fatalf("the refusal must name the family, got %q", err)
	}
	// A dry run that refuses has set nothing aside, so the source is intact.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM refresh_tokens`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("the source table holds %d rows (%v), want the 2 it started with", n, err)
	}
}
