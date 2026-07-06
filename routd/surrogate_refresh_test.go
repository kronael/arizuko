package routd

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth/surrogate"
	"github.com/kronael/arizuko/store"
)

// brokerDB opens a routd.db (with the 0017 oauth columns), sets the keyring, and
// seeds one user-scoped surrogate-OAuth row. The engine's token endpoint is the
// httptest handler; hits counts refresh calls.
func brokerDB(t *testing.T, handler http.HandlerFunc) (*DB, *int32) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetSecretKeys([]byte("k"))

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	db.SetSurrogate(surrogate.NewEngineWith(
		map[string]surrogate.Provider{"github": {TokenURL: srv.URL, SecretKey: "GITHUB_TOKEN", Scopes: []string{"repo"}}},
		map[string]surrogate.ClientCreds{"github": {ID: "id", Secret: "sec"}},
	))
	return db, &hits
}

func seedOAuthRow(t *testing.T, db *DB, value, refresh string, expiresAt time.Time) {
	t.Helper()
	seed := store.New(db.SQL())
	seed.SetSecretKeys([]byte("k"))
	if err := seed.PutOAuthSecret("github:alice", "GITHUB_TOKEN", value, "github", refresh, expiresAt, "repo"); err != nil {
		t.Fatal(err)
	}
}

// Acceptance (b): a token still in its validity window is returned verbatim; the
// broker does not hit the refresh endpoint.
func TestBroker_FreshTokenNoRefresh(t *testing.T) {
	db, hits := brokerDB(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("refresh endpoint must not be hit for a fresh token")
	})
	seedOAuthRow(t, db, "old-access", "refresh-1", time.Now().Add(time.Hour))

	got, err := db.ConnectorSecrets("main", "github:alice", []string{"GITHUB_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if got["GITHUB_TOKEN"] != "old-access" {
		t.Errorf("GITHUB_TOKEN = %q, want old-access", got["GITHUB_TOKEN"])
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("refresh hits = %d, want 0", n)
	}
}

// Acceptance (c): a token within 60s of expiry is refreshed at call time; the
// row is updated and the fresh token is returned.
func TestBroker_RefreshNearExpiry(t *testing.T) {
	db, hits := brokerDB(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "refresh-1" {
			t.Errorf("refresh request form = %v", r.Form)
		}
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"refresh-2","expires_in":3600,"scope":"repo"}`))
	})
	seedOAuthRow(t, db, "old-access", "refresh-1", time.Now().Add(30*time.Second))

	got, err := db.ConnectorSecrets("main", "github:alice", []string{"GITHUB_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if got["GITHUB_TOKEN"] != "new-access" {
		t.Errorf("GITHUB_TOKEN = %q, want new-access", got["GITHUB_TOKEN"])
	}
	if n := atomic.LoadInt32(hits); n != 1 {
		t.Errorf("refresh hits = %d, want 1", n)
	}
	// Row updated in place (value + refresh + expiry).
	reader := store.New(db.SQL())
	reader.SetSecretKeys([]byte("k"))
	rows, _ := reader.UserOAuthSecrets("github:alice", []string{"GITHUB_TOKEN"})
	if len(rows) != 1 || rows[0].Value != "new-access" || rows[0].Refresh != "refresh-2" {
		t.Fatalf("row after refresh = %+v", rows)
	}
	if time.Until(rows[0].ExpiresAt) < 30*time.Minute {
		t.Errorf("expires_at not pushed out: %v", rows[0].ExpiresAt)
	}
}

// Acceptance (d): a rejected refresh_token nulls the row's oauth columns, drops
// the key, and surfaces a reconnect error.
func TestBroker_RefreshRejectedSignalsReconnect(t *testing.T) {
	db, _ := brokerDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad_refresh_token"}`))
	})
	seedOAuthRow(t, db, "old-access", "revoked", time.Now().Add(30*time.Second))

	got, err := db.ConnectorSecrets("main", "github:alice", []string{"GITHUB_TOKEN"})
	if err == nil || !strings.Contains(err.Error(), "reconnect") {
		t.Fatalf("err = %v, want a reconnect error", err)
	}
	if _, ok := got["GITHUB_TOKEN"]; ok {
		t.Error("revoked key must be dropped from the resolved set")
	}
	// expires_at + refresh_val nulled; value + provider survive.
	var exp, ref sql.NullString
	if err := db.SQL().QueryRow(
		`SELECT expires_at, refresh_val FROM secrets WHERE scope_id='github:alice' AND key='GITHUB_TOKEN'`,
	).Scan(&exp, &ref); err != nil {
		t.Fatal(err)
	}
	if exp.Valid || ref.Valid {
		t.Errorf("oauth columns not nulled after revoke: exp=%v ref=%v", exp, ref)
	}
}
