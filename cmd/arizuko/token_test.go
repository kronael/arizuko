package main

import (
	"crypto/ecdsa"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/groupfolder"
)

// TestMintBearer mints against a fixture auth.db (the same signing_keys shape
// authd persists) and verifies the token with the key's public half — the
// exact offline-verify path routd runs on /v1/messages.
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
	adb, err := sql.Open("sqlite", filepath.Join(storeDir, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adb.Exec(`CREATE TABLE signing_keys (kid TEXT, priv_pem TEXT, pub_pem TEXT, active INT, created_at TEXT, retired_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := adb.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mintBearer(storeDir, "eval", "user:cli", []string{"messages:read"}, time.Hour); err == nil {
		t.Fatal("expected error with no active key")
	}
}

// seedAuthDB writes a minimal signing_keys row matching authd's schema.
func seedAuthDB(t *testing.T, storeDir string, key *auth.SigningKey) {
	t.Helper()
	adb, err := sql.Open("sqlite", filepath.Join(storeDir, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	// test fixture; close error unactionable
	defer adb.Close() //nolint:errcheck
	der, err := x509.MarshalPKCS8PrivateKey(key.Priv)
	if err != nil {
		t.Fatal(err)
	}
	privPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if _, err := adb.Exec(`CREATE TABLE signing_keys (kid TEXT, priv_pem TEXT, pub_pem TEXT, active INT, created_at TEXT, retired_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := adb.Exec(`INSERT INTO signing_keys (kid, priv_pem, active, created_at) VALUES (?, ?, 1, ?)`,
		key.Kid, privPEM, time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
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
