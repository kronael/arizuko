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

	got, _, err := db.ConnectorSecrets("main", "github:alice", []string{"GITHUB_TOKEN"})
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

	got, _, err := db.ConnectorSecrets("main", "github:alice", []string{"GITHUB_TOKEN"})
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
		// invalid_grant is the RFC 6749 §5.2 dead-refresh-token signal (F9): only it
		// reconnects; other OAuth errors are transient and keep the credential.
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	})
	seedOAuthRow(t, db, "old-access", "revoked", time.Now().Add(30*time.Second))

	got, _, err := db.ConnectorSecrets("main", "github:alice", []string{"GITHUB_TOKEN"})
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

// F69: the reactive half. RefreshConnectorSecrets forces the refresh regardless
// of expires_at — a token with an hour of stated life left is exactly the case
// the 401 retry exists for (a provider whose expires_in is optimistic), and the
// proactive near-expiry check would refresh nothing.
func TestBroker_ForcedRefreshIgnoresExpiry(t *testing.T) {
	db, hits := brokerDB(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("refresh_token") != "refresh-1" {
			t.Errorf("refresh request form = %v", r.Form)
		}
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"refresh-2","expires_in":3600,"scope":"repo"}`))
	})
	seedOAuthRow(t, db, "old-access", "refresh-1", time.Now().Add(time.Hour))

	// The proactive path leaves it alone — that is the gap.
	if got, _, err := db.ConnectorSecrets("main", "github:alice", []string{"GITHUB_TOKEN"}); err != nil {
		t.Fatal(err)
	} else if got["GITHUB_TOKEN"] != "old-access" || atomic.LoadInt32(hits) != 0 {
		t.Fatalf("proactive path refreshed a healthy-looking token: %q hits=%d",
			got["GITHUB_TOKEN"], atomic.LoadInt32(hits))
	}

	got, err := db.RefreshConnectorSecrets("main", "github:alice", []string{"GITHUB_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if got["GITHUB_TOKEN"] != "new-access" {
		t.Errorf("GITHUB_TOKEN = %q, want new-access", got["GITHUB_TOKEN"])
	}
	if n := atomic.LoadInt32(hits); n != 1 {
		t.Errorf("refresh hits = %d, want 1", n)
	}
}

// A forced refresh writes NO secret_use_log row: it is a retry of a call the
// ConnectorSecrets that opened it already audited, and a second row would read
// as a second use of the credential.
func TestBroker_ForcedRefreshWritesNoAuditRow(t *testing.T) {
	db, _ := brokerDB(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"refresh-2","expires_in":3600,"scope":"repo"}`))
	})
	seedOAuthRow(t, db, "old-access", "refresh-1", time.Now().Add(time.Hour))

	if _, err := db.RefreshConnectorSecrets("main", "github:alice", []string{"GITHUB_TOKEN"}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.SQL().QueryRow(`SELECT count(*) FROM secret_use_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("secret_use_log rows = %d, want 0 (the retry must not double-count the use)", n)
	}
}
