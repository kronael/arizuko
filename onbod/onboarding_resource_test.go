package main

// Z3 coverage: the onboarding bearer never appears in a read projection. Since
// onbod 0006 there is no column for it to appear in either — the 5/31 fold left
// token_ref/token_expires inert and they were dropped (BUGS F40) — so what is
// pinned here is that the projection stays token-free and that a row written
// before the reshape still survives the migrate chain.

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

// The registered row type must not carry the credential column at all —
// absent, not merely tagged out, so no later edit can surface it.
func TestOnboardingRowHasNoTokenField(t *testing.T) {
	rt := reflect.TypeFor[resources.OnboardingRow]()
	for i := range rt.NumField() {
		f := rt.Field(i)
		if strings.Contains(strings.ToLower(f.Name), "tokenref") {
			t.Errorf("OnboardingRow.%s exposes the bearer hash", f.Name)
		}
		if f.Tag.Get("db") == "token_ref" || f.Tag.Get("db") == "token" {
			t.Errorf("OnboardingRow.%s maps db column %q", f.Name, f.Tag.Get("db"))
		}
	}
}

// GET /v1/onboarding must carry no token-shaped field at all. Asserted on the
// raw response bytes so a future field rename cannot sneak one back in under a
// different key, and counted first so an empty table cannot pass it vacuously.
func TestOnboardingListLeaksNoToken(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO onboarding (jid, status, created)
		VALUES ('telegram:1', 'awaiting_message', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}

	a := &admin{db: db}
	mux := http.NewServeMux()
	a.mountOnboarding(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/v1/onboarding", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "telegram:1") {
		t.Fatalf("fixture is vacuous — the seeded row is not in the response: %s", body)
	}
	if strings.Contains(body, "token") {
		t.Errorf("REST list carries a token-shaped field: %s", body)
	}
}

// A row written before the migration (plaintext token, legacy table shape) must
// survive openOwnedDB's migrate + backfill with its facts intact. Its link is no
// longer redeemable anywhere — the fold moved redemption to /pair/{token} (spec
// 5/31) — but the ROW is admission state, and losing it would strand the chat
// behind its own jid primary key.
func TestPreMigrationRowSurvivesTheBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store", "onbod.db")
	mustSeedDB(t, path)

	// Migrate once to get the current schema, then put the table back into its
	// pre-Z3 shape with a live plaintext token, exactly as a real instance
	// looked before 0004 ran.
	db, err := openOwnedDB(path)
	if err != nil {
		t.Fatalf("openOwnedDB: %v", err)
	}
	if _, err := db.Exec(`
		DROP TABLE onboarding;
		CREATE TABLE onboarding (jid TEXT PRIMARY KEY, status TEXT NOT NULL,
			prompted_at TEXT, created TEXT NOT NULL, token TEXT, token_expires TEXT,
			user_sub TEXT, gate TEXT, queued_at TEXT, admitted_at TEXT);
		INSERT INTO onboarding (jid, status, created, token, token_expires, gate)
			VALUES ('telegram:legacy', 'awaiting_message', '2026-01-01', 'pre-migration-tok', '2099-01-01T00:00:00Z', 'invite_required');
		DELETE FROM migrations WHERE service='onbod' AND version >= 4;
	`); err != nil {
		t.Fatalf("stage legacy shape: %v", err)
	}
	db.Close()

	// Re-open: 0004 reshapes, 0006 drops the token columns, CarryOnboardingLegacy
	// moves the row across and cleans up after itself.
	db, err = openOwnedDB(path)
	if err != nil {
		t.Fatalf("reopen after legacy stage: %v", err)
	}
	defer db.Close()

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM onboarding`).Scan(&n)
	if n != 1 {
		t.Fatalf("row count after migration = %d, want 1 (row was dropped, not carried)", n)
	}
	var status, gate string
	if err := db.QueryRow(
		`SELECT status, COALESCE(gate,'') FROM onboarding WHERE jid='telegram:legacy'`).
		Scan(&status, &gate); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if status != "awaiting_message" || gate != "invite_required" {
		t.Errorf("admission facts lost: status=%q gate=%q", status, gate)
	}
	// F40: the bearer columns do not survive the chain — 0006 drops both, and
	// the carry-forward must not name them or it would fail outright.
	for _, col := range []string{"token", "token_ref", "token_expires"} {
		if _, err := db.Query(`SELECT ` + col + ` FROM onboarding`); err == nil {
			t.Errorf("column %s survived onbod 0006", col)
		}
	}
	// onboarding_legacy is gone — the carry cleaned up after itself.
	var legacy int
	db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='onboarding_legacy'`).Scan(&legacy)
	if legacy != 0 {
		t.Error("onboarding_legacy survived the carry-forward")
	}
}

// The retired hand-rolled handlers are gone; the resreg mount serves the same
// paths with the same scope policy.
func TestOnboardingRESTGateRequiresScope(t *testing.T) {
	db := testDB(t)
	a := &admin{db: db, ks: &auth.KeySet{}}
	err := a.onboardingRESTGate(resreg.Execution{
		Action: resreg.ActionList,
		Caller: resreg.Caller{Claims: map[string]string{"scopes": "gates:read"}},
	}, "", nil)
	if err == nil {
		t.Error("wrong scope was allowed")
	}
	if err := a.onboardingRESTGate(resreg.Execution{
		Action: resreg.ActionList,
		Caller: resreg.Caller{Claims: map[string]string{"scopes": "invites:write"}},
	}, "", nil); err != nil {
		t.Errorf("invites:write refused: %v", err)
	}
}
