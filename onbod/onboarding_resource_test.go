package main

// Z3 coverage: the onboarding bearer is hashed at rest and appears in NO read
// projection, while redemption with the RAW token still works — including for a
// row that existed before the migration.

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
	"github.com/kronael/arizuko/store"
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

// GET /v1/onboarding must not echo the stored hash — nor, obviously, a bearer.
// Asserted on the raw response bytes so a future field rename can't sneak one
// back in under a different key.
func TestOnboardingListLeaksNoToken(t *testing.T) {
	db := testDB(t)
	raw := "live-onboard-tok"
	db.Exec(`INSERT INTO onboarding (jid, status, token_ref, token_expires, created)
		VALUES ('telegram:1', 'awaiting_message', ?, '2099-01-01T00:00:00Z', '2026-01-01')`,
		store.TokenRef(raw))

	a := &admin{db: db}
	mux := http.NewServeMux()
	a.mountOnboarding(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/v1/onboarding", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, raw) {
		t.Error("REST list leaked the raw bearer")
	}
	if strings.Contains(body, store.TokenRef(raw)) {
		t.Error("REST list leaked token_ref")
	}
	if strings.Contains(body, "token_ref") {
		t.Errorf("REST list mentions token_ref: %s", body)
	}
	// It must still be a useful queue view.
	if !strings.Contains(body, "telegram:1") || !strings.Contains(body, "awaiting_message") {
		t.Errorf("list lost its payload: %s", body)
	}
}

// The MCP face is derived from the same declaration (an MCPDoc entry is what
// makes an action surface as a tool), so it must be equally clean — no tool
// takes or advertises the token.
func TestOnboardingMCPDeclLeaksNoToken(t *testing.T) {
	for action, desc := range resources.OnboardingMCPDoc {
		if strings.Contains(strings.ToLower(desc), "token_ref") {
			t.Errorf("MCPDoc[%s] describes token_ref", action)
		}
	}
	for action, args := range resources.OnboardingMCPArgs {
		for _, arg := range args {
			if strings.Contains(arg.Name, "token") {
				t.Errorf("MCPArgs[%s] takes arg %q", action, arg.Name)
			}
		}
	}
}

// Every REST endpoint has an MCP twin (5/17): both faces, one handler. A
// missing MCPDoc entry silently drops the action from tools/list.
func TestOnboardingHasBothFaces(t *testing.T) {
	for _, e := range resources.OnboardingEndpoints {
		if _, ok := resources.OnboardingMCPDoc[e.Action]; !ok {
			t.Errorf("%s %s (action %s) has no MCP twin", e.Verb, e.Path, e.Action)
		}
		if _, ok := resources.OnboardingMCPArgs[e.Action]; !ok {
			t.Errorf("action %s has no MCP arg list", e.Action)
		}
	}
}

// A row written before the migration (plaintext token, legacy table shape) must
// still redeem with its original link after openOwnedDB migrates + backfills.
func TestPreMigrationRowStillRedeems(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store", "onbod.db")
	mustMkdir(t, filepath.Dir(path))

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
		INSERT INTO onboarding (jid, status, created, token, token_expires)
			VALUES ('telegram:legacy', 'awaiting_message', '2026-01-01', 'pre-migration-tok', '2099-01-01T00:00:00Z');
		DELETE FROM migrations WHERE service='onbod' AND version >= 4;
	`); err != nil {
		t.Fatalf("stage legacy shape: %v", err)
	}
	db.Close()

	// Re-open: 0004 reshapes, BackfillOnboardingTokenRefs hashes forward.
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
	var ref, expires string
	if err := db.QueryRow(
		`SELECT token_ref, token_expires FROM onboarding WHERE jid='telegram:legacy'`).Scan(&ref, &expires); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if ref != store.TokenRef("pre-migration-tok") {
		t.Errorf("token_ref = %q, want the hash of the original token", ref)
	}
	if expires != "2099-01-01T00:00:00Z" {
		t.Errorf("token_expires = %q, want it carried along", expires)
	}
	// onboarding_legacy is gone — the backfill cleaned up after itself.
	var legacy int
	db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='onboarding_legacy'`).Scan(&legacy)
	if legacy != 0 {
		t.Error("onboarding_legacy survived the backfill")
	}

	// The original link still resolves.
	if jid, ok := jidForToken(db, "pre-migration-tok"); !ok || jid != "telegram:legacy" {
		t.Errorf("jidForToken(original) = %q,%v — pre-migration link broke", jid, ok)
	}
	if _, ok := claimByToken(db, "pre-migration-tok", "github:alice"); !ok {
		t.Error("claimByToken(original) failed — pre-migration link broke")
	}
}

// A consumed row's NULL token must NOT become a resolvable ref (hashing "" would
// mint one shared handle for every consumed row).
func TestBackfillLeavesNullTokenNull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store", "onbod.db")
	mustMkdir(t, filepath.Dir(path))
	db, err := openOwnedDB(path)
	if err != nil {
		t.Fatalf("openOwnedDB: %v", err)
	}
	if _, err := db.Exec(`
		DROP TABLE onboarding;
		CREATE TABLE onboarding (jid TEXT PRIMARY KEY, status TEXT NOT NULL,
			prompted_at TEXT, created TEXT NOT NULL, token TEXT, token_expires TEXT,
			user_sub TEXT, gate TEXT, queued_at TEXT, admitted_at TEXT);
		INSERT INTO onboarding (jid, status, created, token, user_sub)
			VALUES ('telegram:done', 'token_used', '2026-01-01', NULL, 'github:bob');
		DELETE FROM migrations WHERE service='onbod' AND version >= 4;
	`); err != nil {
		t.Fatalf("stage legacy shape: %v", err)
	}
	db.Close()

	db, err = openOwnedDB(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	var nulls int
	db.QueryRow(`SELECT COUNT(*) FROM onboarding WHERE token_ref IS NULL`).Scan(&nulls)
	if nulls != 1 {
		t.Errorf("consumed row's token_ref = not NULL; want NULL preserved")
	}
	if _, ok := jidForToken(db, ""); ok {
		t.Error("empty token resolved a row — NULL was hashed into a live ref")
	}
}

// An unknown token must refuse visibly, not fall through silently.
func TestUnknownTokenRefusesLoudly(t *testing.T) {
	db := testDB(t)
	db.Exec(`INSERT INTO onboarding (jid, status, token_ref, token_expires, created)
		VALUES ('telegram:1', 'awaiting_message', ?, '2099-01-01T00:00:00Z', '2026-01-01')`,
		store.TokenRef("the-real-one"))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/onboard?token=not-the-real-one", nil)
	req.Header.Set("X-User-Sub", "github:mallory")
	handleOnboard(w, req, db, db, config{})

	if !strings.Contains(w.Body.String(), "invalid") {
		t.Errorf("unknown token did not produce a refusal page: %s", w.Body.String())
	}
	// It must not have bound the impostor to the waiting JID.
	var sub, status string
	db.QueryRow(`SELECT COALESCE(user_sub,''), status FROM onboarding WHERE jid='telegram:1'`).Scan(&sub, &status)
	if sub != "" {
		t.Errorf("unknown token bound user_sub=%q", sub)
	}
	if status != "awaiting_message" {
		t.Errorf("unknown token moved status to %q", status)
	}
}

// Presenting the ref instead of the raw token must not redeem: the stored value
// is a verifier, not a second bearer.
func TestStoredRefIsNotItselfRedeemable(t *testing.T) {
	db := testDB(t)
	ref := store.TokenRef("real-tok")
	db.Exec(`INSERT INTO onboarding (jid, status, token_ref, token_expires, created)
		VALUES ('telegram:1', 'awaiting_message', ?, '2099-01-01T00:00:00Z', '2026-01-01')`, ref)

	if _, ok := jidForToken(db, ref); ok {
		t.Error("the stored ref redeemed as if it were the bearer")
	}
	if jid, ok := jidForToken(db, "real-tok"); !ok || jid != "telegram:1" {
		t.Errorf("raw token failed to resolve: %q,%v", jid, ok)
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
