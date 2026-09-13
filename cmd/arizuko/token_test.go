package main

import (
	"crypto/ecdsa"
	"reflect"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/sqlstore"
	"github.com/kronael/arizuko/store"
)

// TestMintBearer mints against a fixture store holding the key the way authd
// installs it, and verifies the token with the key's public half — the exact
// offline-verify path routd runs on /v1/messages.
func TestMintBearer(t *testing.T) {
	storeDir := t.TempDir()
	key, err := auth.NewSigningKey("test-kid")
	if err != nil {
		t.Fatal(err)
	}
	seedAuthDB(t, storeDir, key)

	tok, exp, err := mintBearer(storeDir, "eval", "user:cli", []string{"messages:write", "messages:read"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.After(time.Now().Add(50 * time.Minute)) {
		t.Errorf("expiry %v not ~1h out", exp)
	}

	ks := auth.NewKeySet(map[string]*ecdsa.PublicKey{"test-kid": &key.Priv.PublicKey})
	s, err := auth.VerifyToken(tok, ks)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if s.Sub != "user:cli" || s.Typ != "user" {
		t.Errorf("sub=%q typ=%q", s.Sub, s.Typ)
	}
	if !reflect.DeepEqual(s.Scope, []string{"messages:write", "messages:read"}) {
		t.Errorf("scope = %v", s.Scope)
	}
	if s.Extra["arz/folder"] != "eval" {
		t.Errorf("folder claim = %q", s.Extra["arz/folder"])
	}
}

func TestMintBearerNoActiveKey(t *testing.T) {
	storeDir := t.TempDir()
	openAuthStore(t, storeDir)
	if _, _, err := mintBearer(storeDir, "eval", "user:cli", []string{"messages:read"}, time.Hour); err == nil {
		t.Fatal("expected error with no active key")
	}
}

// openAuthStore prepares authd's entities at the SAME path production uses —
// store.OwnerDBPath, not a hand-built join. A fixture that built `storeDir/auth.db`
// agreed with a mintBearer looking at the same pre-5/16-step-7 flat path, so it
// passed while `arizuko token issue bearer` was broken on every live instance.
func openAuthStore(t *testing.T, storeDir string) *authstore.Store {
	t.Helper()
	path := store.OwnerDBPath(storeDir, store.OwnerAuthd)
	if err := db_utils.CreateDBFile(path); err != nil {
		t.Fatal(err)
	}
	adb, err := db_utils.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// test fixture; close error unactionable
	t.Cleanup(func() { adb.Close() }) //nolint:errcheck
	d := sqlstore.New(adb)
	for _, entity := range authstore.Entities {
		if err := d.CreateEntity(t.Context(), entity); err != nil {
			t.Fatalf("make %s: %v", entity, err)
		}
	}
	return authstore.New(d)
}

// seedAuthDB installs one signing key the way authd installs it: through
// authstore, which owns the item shape. A fixture that wrote its own shape
// would prove nothing about the key mintBearer reads.
func seedAuthDB(t *testing.T, storeDir string, key *auth.SigningKey) {
	t.Helper()
	if err := openAuthStore(t, storeDir).RotateActiveKey(key, time.Now()); err != nil {
		t.Fatalf("install the signing key: %v", err)
	}
}

// TestTokenLandsInRoutdDB proves `arizuko token issue`/`revoke` write
// route_tokens into routd.db (its post-split owner, the DB routd resolves
// against) and leave messages.db untouched — a CLI-issued /chat or /hook URL
// written to the frozen monolith silently never resolves.
func TestTokenLandsInRoutdDB(t *testing.T) {
	dir := setupSplitStore(t)

	s, err := store.OpenRoutd(dir)
	if err != nil {
		t.Fatalf("OpenRoutd: %v", err)
	}
	if err := s.PutGroupRow(core.Group{Folder: "main", AddedAt: time.Now()}); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	tokenIssue(s, "", []string{"chat", "main"})
	s.Close()

	// The jid is an ATTRIBUTE — the coordinate is the digest — so the match reads
	// it off the item rather than out of the key.
	jidIsWebMain := func(it objstore.Item) bool { return it.Attrs["jid"] == "web:main" }
	if n := countItems(t, dir, store.OwnerRoutd, store.RouteTokens, jidIsWebMain); n != 1 {
		t.Errorf("routd.db route_tokens = %d, want 1", n)
	}
	if n := countItems(t, dir, "messages.db", store.RouteTokens,
		func(objstore.Item) bool { return true }); n != 0 {
		t.Errorf("messages.db route_tokens = %d, want 0 (CLI must not write the monolith)", n)
	}

	// revoke path also targets routd.db.
	s2, err := store.OpenRoutd(dir)
	if err != nil {
		t.Fatalf("OpenRoutd: %v", err)
	}
	tokenRevoke(s2, []string{"web:main"})
	s2.Close()
	if n := countItems(t, dir, store.OwnerRoutd, store.RouteTokens, jidIsWebMain); n != 0 {
		t.Errorf("routd.db route_tokens after revoke = %d, want 0", n)
	}
}

func TestJidFolder(t *testing.T) {
	cases := []struct {
		jid  string
		want string
	}{
		{"web:solo", "solo"},
		{"web:acme/eng", "acme/eng"},                         // multi-segment folder
		{"web:acme/eng/submissions", "acme/eng/submissions"}, // whole rest; suffix needs explicit owner_folder
		{"web:", ""},
		{"hook:solo/gh-webhook", "solo"},
		{"hook:acme/eng/gh-webhook", "acme/eng"},
		{"hook:solo", "solo"}, // no source segment
		{"telegram:foo", ""},  // unrecognised prefix
		{"", ""},
	}
	for _, c := range cases {
		if got := groupfolder.JidFolder(c.jid); got != c.want {
			t.Errorf("JidFolder(%q) = %q, want %q", c.jid, got, c.want)
		}
	}
}
