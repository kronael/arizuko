package audit

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/kronael/arizuko/store"
	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func resetInit(t *testing.T) {
	t.Cleanup(func() {
		logMu.Lock()
		logDB = nil
		logInstance = ""
		logMu.Unlock()
	})
}

func TestEmit_NoInit(t *testing.T) {
	// No Init → Emit returns 0, no panic.
	if got := Emit(context.Background(), Event{
		Category: CategoryAuthN, Action: "login", Actor: "u:1",
	}); got != 0 {
		t.Errorf("Emit without Init returned id=%d, want 0", got)
	}
}

func TestEmit_Inserts(t *testing.T) {
	db := openTestDB(t)
	resetInit(t)
	Init(db, "test")

	id := Emit(context.Background(), Event{
		Category: CategoryAuthN, Action: "login",
		Actor: "user:google:114alice", ActorSub: "google:114alice",
		Surface: SurfaceREST, Outcome: OutcomeOK,
		ParamsSummary: map[string]any{"method": "oauth"},
	})
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	var category, action, actor, outcome, params, instance string
	err := db.QueryRow(
		`SELECT category, action, actor, outcome, params_summary, instance
		 FROM audit_log WHERE id = ?`, id).
		Scan(&category, &action, &actor, &outcome, &params, &instance)
	if err != nil {
		t.Fatal(err)
	}
	if category != CategoryAuthN || action != "login" {
		t.Errorf("cat/action=%q/%q want authn/login", category, action)
	}
	if instance != "test" {
		t.Errorf("instance=%q want test", instance)
	}
	if !strings.Contains(params, "oauth") {
		t.Errorf("params=%q missing oauth", params)
	}
}

func TestEmitInTx_RollsBackOnTxFailure(t *testing.T) {
	db := openTestDB(t)
	resetInit(t)
	Init(db, "test")

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := EmitInTx(context.Background(), tx, Event{
		Category: CategoryMutation, Action: "route.create",
		Actor: "operator:ondrej", Outcome: OutcomeOK,
		Resource: "routes/42",
	}); err != nil {
		t.Fatal(err)
	}
	// Roll back the tx — audit row must NOT survive.
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n)
	if n != 0 {
		t.Errorf("audit_log rows after rollback=%d, want 0", n)
	}
}

func TestEmitInTx_PersistsOnCommit(t *testing.T) {
	db := openTestDB(t)
	resetInit(t)
	Init(db, "test")

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := EmitInTx(context.Background(), tx, Event{
		Category: CategorySecret, Action: "secret.set",
		Actor: "operator:ondrej", Outcome: OutcomeOK,
		Resource: "secrets/folder/atlas/support/OPENAI_API_KEY",
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var action string
	db.QueryRow(`SELECT action FROM audit_log LIMIT 1`).Scan(&action)
	if action != "secret.set" {
		t.Errorf("action=%q want secret.set", action)
	}
}

func TestRedactParams_HidesSensitiveKeys(t *testing.T) {
	in := map[string]any{
		"value":   "shhh",
		"api_key": "sk-abcdef0123456789",
		"token":   "tok_xyz",
		"folder":  "atlas/support",
	}
	out := redactParams(in)
	if s, _ := out["api_key"].(string); !strings.HasPrefix(s, "<redacted:") {
		t.Errorf("api_key not redacted: %v", out["api_key"])
	}
	if s, _ := out["token"].(string); !strings.HasPrefix(s, "<redacted:") {
		t.Errorf("token not redacted: %v", out["token"])
	}
	if out["folder"] != "atlas/support" {
		t.Errorf("folder mangled: %v", out["folder"])
	}
}

func TestMarshalParams_TruncatesOversize(t *testing.T) {
	huge := strings.Repeat("a", 1000)
	out := marshalParams(map[string]any{"x": huge})
	if len(out) > 512 {
		t.Errorf("len=%d want ≤512", len(out))
	}
}

func TestEmit_DeniedOutcome(t *testing.T) {
	db := openTestDB(t)
	resetInit(t)
	Init(db, "test")

	id := Emit(context.Background(), Event{
		Category: CategoryAuthZ, Action: "authz.deny",
		Actor: "agent:atlas/support", Outcome: OutcomeDenied,
		Resource: "mcp/secret.set", Folder: "atlas/support",
	})
	if id == 0 {
		t.Fatal("denied event not inserted")
	}
	var outcome string
	db.QueryRow(`SELECT outcome FROM audit_log WHERE id = ?`, id).Scan(&outcome)
	if outcome != OutcomeDenied {
		t.Errorf("outcome=%q want denied", outcome)
	}
}
