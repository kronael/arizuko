package audit

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/kronael/arizuko/db_utils/dbtest"
	"github.com/kronael/arizuko/objstore/sqlstore"
)

// openTestStore reserves a database and prepares it the way an owner's opener
// does — through the one applier, so a test records into the entity a daemon
// records into rather than into a shape only tests have.
func openTestStore(t *testing.T) (*sql.DB, *sqlstore.Store) {
	t.Helper()
	db := dbtest.Open(t)
	s := sqlstore.New(db)
	if err := EnsureEntities(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return db, s
}

// openTestDB is openTestStore for a test that only needs the store.
func openTestDB(t *testing.T) *sqlstore.Store {
	t.Helper()
	_, s := openTestStore(t)
	return s
}

func resetInit(t *testing.T) {
	t.Cleanup(func() {
		logMu.Lock()
		logStore = nil
		logInstance = ""
		logMu.Unlock()
	})
}

// only reports the single row the store holds, read back through the same
// method an operator's page reads it through.
func only(t *testing.T, s *sqlstore.Store) Row {
	t.Helper()
	rows, err := Query(context.Background(), s, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("the store holds %d rows, want exactly 1", len(rows))
	}
	return rows[0]
}

func TestEmit_NoInit(t *testing.T) {
	// No Init → Emit records nothing and answers no id, without panicking.
	if got := Emit(context.Background(), Event{
		Category: CategoryAuthN, Action: "login", Actor: "u:1",
	}); got != "" {
		t.Errorf("Emit without Init returned id=%q, want none", got)
	}
}

func TestEmit_Inserts(t *testing.T) {
	s := openTestDB(t)
	resetInit(t)
	Init(s, "test")

	id := Emit(context.Background(), Event{
		Category: CategoryAuthN, Action: "login",
		Actor: "user:google:114alice", ActorSub: "google:114alice",
		Surface: SurfaceREST, Outcome: OutcomeOK,
		ParamsSummary: map[string]any{"method": "oauth"},
	})
	if id == "" {
		t.Fatal("expected an id")
	}
	row := only(t, s)
	if row.ID != id {
		t.Errorf("the row reads back as %q, want the id Emit answered %q", row.ID, id)
	}
	if row.Category != CategoryAuthN || row.Action != "login" {
		t.Errorf("cat/action=%q/%q want authn/login", row.Category, row.Action)
	}
	if row.Instance != "test" {
		t.Errorf("instance=%q want test", row.Instance)
	}
	if !strings.Contains(row.ParamsSummary, "oauth") {
		t.Errorf("params=%q missing oauth", row.ParamsSummary)
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
	s := openTestDB(t)
	resetInit(t)
	Init(s, "test")

	id := Emit(context.Background(), Event{
		Category: CategoryAuthZ, Action: "authz.deny",
		Actor: "agent:atlas/support", Outcome: OutcomeDenied,
		Resource: "mcp/secret.set", Folder: "atlas/support",
	})
	if id == "" {
		t.Fatal("denied event not recorded")
	}
	if got := only(t, s).Outcome; got != OutcomeDenied {
		t.Errorf("outcome=%q want denied", got)
	}
}
