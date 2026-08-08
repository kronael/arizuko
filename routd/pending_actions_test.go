package routd

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
)

func memDB(t *testing.T) *DB {
	t.Helper()
	d, err := OpenMem()
	if err != nil {
		t.Fatalf("OpenMem: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestArgsHash_OrderIndependentValueSensitive(t *testing.T) {
	a := ArgsHash(map[string]any{"host": "x.com", "folder": "atlas"})
	b := ArgsHash(map[string]any{"folder": "atlas", "host": "x.com"})
	if a != b {
		t.Fatal("key order must not change the hash — the agent's map is unordered")
	}
	if ArgsHash(map[string]any{"host": "y.com", "folder": "atlas"}) == a {
		t.Fatal("a changed value must change the hash — that IS the edited-args rule")
	}
}

func TestPendingAction_RoundTrip(t *testing.T) {
	d := memDB(t)
	p := PendingAction{ID: "a1", GroupFolder: "atlas", Tool: "mcp:delete", ArgsHash: "h"}
	if err := d.PutPendingAction(p); err != nil {
		t.Fatalf("PutPendingAction: %v", err)
	}
	got, ok := d.PendingAction("a1")
	if !ok {
		t.Fatal("row not found")
	}
	if got.Status != PendingHeld || got.Tool != "mcp:delete" || got.CreatedAt == "" {
		t.Fatalf("unexpected row: %+v", got)
	}
}

func TestPendingAction_RejectsIncomplete(t *testing.T) {
	d := memDB(t)
	if err := d.PutPendingAction(PendingAction{GroupFolder: "atlas", Tool: "t"}); err == nil {
		t.Fatal("a row with no id must be refused, not written")
	}
}

// TestConsumeApprovedAction_OneShot is the release rule: an approved call passes
// exactly once. A second re-issue must find nothing and be held again.
func TestConsumeApprovedAction_OneShot(t *testing.T) {
	d := memDB(t)
	hash := ArgsHash(map[string]any{"target": "prod"})
	if err := d.PutPendingAction(PendingAction{
		ID: "a1", GroupFolder: "atlas", Tool: "mcp:delete", ArgsHash: hash,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ResolvePendingAction("a1", PendingApproved, "google:op", ""); err != nil {
		t.Fatalf("ResolvePendingAction: %v", err)
	}
	if _, ok := d.ConsumeApprovedAction("atlas", "mcp:delete", hash); !ok {
		t.Fatal("first re-issue should be released")
	}
	if _, ok := d.ConsumeApprovedAction("atlas", "mcp:delete", hash); ok {
		t.Fatal("second re-issue must NOT be released — one-shot")
	}
	got, _ := d.PendingAction("a1")
	if got.Status != PendingReleased {
		t.Fatalf("status = %q, want released", got.Status)
	}
}

func TestConsumeApprovedAction_ArgsDeviationMisses(t *testing.T) {
	d := memDB(t)
	approved := ArgsHash(map[string]any{"target": "staging"})
	if err := d.PutPendingAction(PendingAction{
		ID: "a1", GroupFolder: "atlas", Tool: "mcp:delete", ArgsHash: approved,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ResolvePendingAction("a1", PendingApproved, "google:op", ""); err != nil {
		t.Fatal(err)
	}
	other := ArgsHash(map[string]any{"target": "prod"})
	if _, ok := d.ConsumeApprovedAction("atlas", "mcp:delete", other); ok {
		t.Fatal("edited args must NOT ride an approval granted for different args")
	}
}

func TestConsumeApprovedAction_FolderBound(t *testing.T) {
	d := memDB(t)
	hash := ArgsHash(map[string]any{"x": 1})
	if err := d.PutPendingAction(PendingAction{
		ID: "a1", GroupFolder: "atlas", Tool: "mcp:delete", ArgsHash: hash,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ResolvePendingAction("a1", PendingApproved, "google:op", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.ConsumeApprovedAction("other", "mcp:delete", hash); ok {
		t.Fatal("another folder must not consume this folder's approval")
	}
}

func TestResolvePendingAction_OnlyHeldMoves(t *testing.T) {
	d := memDB(t)
	if err := d.PutPendingAction(PendingAction{
		ID: "a1", GroupFolder: "atlas", Tool: "t", ArgsHash: "h",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ResolvePendingAction("a1", PendingApproved, "op", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ResolvePendingAction("a1", PendingRejected, "op2", ""); err == nil {
		t.Fatal("a resolved row must not be re-resolved")
	}
}

func TestResolvePendingAction_RejectsUnknownVerdict(t *testing.T) {
	d := memDB(t)
	if _, err := d.ResolvePendingAction("a1", "maybe", "op", ""); err == nil {
		t.Fatal("only approved/rejected are verdicts")
	}
}

func TestPendingAction_ExpiryIsLazy(t *testing.T) {
	d := memDB(t)
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if err := d.PutPendingAction(PendingAction{
		ID: "a1", GroupFolder: "atlas", Tool: "t", ArgsHash: "h", ExpiresAt: past,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := d.PendingAction("a1")
	if got.Status != PendingExpired {
		t.Fatalf("status = %q, want expired without any GC job", got.Status)
	}
}

func TestListPendingActions_FolderAndStatus(t *testing.T) {
	d := memDB(t)
	for _, p := range []PendingAction{
		{ID: "a1", GroupFolder: "atlas", Tool: "t1", ArgsHash: "h1"},
		{ID: "b1", GroupFolder: "other", Tool: "t2", ArgsHash: "h2"},
	} {
		if err := d.PutPendingAction(p); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := d.ListPendingActions("atlas", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "a1" {
		t.Fatalf("folder filter leaked: %+v", rows)
	}
	all, err := d.ListPendingActions("", PendingHeld)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 held rows across folders, got %d", len(all))
	}
}

func TestStringArgs_SkipsNonScalars(t *testing.T) {
	got := stringArgs(map[string]any{
		"host": "x.com", "on": true, "n": float64(3), "obj": map[string]any{"a": 1},
	})
	if got["host"] != "x.com" || got["on"] != "true" || got["n"] != "3" {
		t.Fatalf("scalar flattening wrong: %+v", got)
	}
	if _, ok := got["obj"]; ok {
		t.Fatal("a non-scalar arg has no glob meaning and must not be matchable")
	}
}

// TestHoldGate_HoldApproveRelease is spec 5/19's acceptance path end to end at
// the gate: a hold rule suspends the call and records a row; an approval
// releases the SAME call one-shot; the next identical call is held again.
func TestHoldGate_HoldApproveRelease(t *testing.T) {
	d := memDB(t)
	srv := &Server{db: d}
	if err := d.AddACLRow(core.ACLRow{
		Principal: "folder:atlas", Action: auth.HoldPrefix + "mcp:delete",
		Scope: "atlas", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	gate := srv.holdGate(turnMCP{folder: "atlas"})
	if gate == nil {
		t.Fatal("a normal folder turn must get a gate")
	}
	args := map[string]any{"target": "prod"}

	id, held := gate("mcp:delete", args)
	if !held || id == "" {
		t.Fatal("a matching hold rule must suspend the call")
	}
	row, ok := d.PendingAction(id)
	if !ok || row.Tool != "mcp:delete" || row.GroupFolder != "atlas" {
		t.Fatalf("held row not recorded: %+v", row)
	}

	if _, held := gate("mcp:send", args); held {
		t.Fatal("an unheld tool must run inline")
	}

	if _, err := d.ResolvePendingAction(id, PendingApproved, "google:op", ""); err != nil {
		t.Fatal(err)
	}
	if _, held := gate("mcp:delete", args); held {
		t.Fatal("the approved re-issue must pass")
	}
	if _, held := gate("mcp:delete", args); !held {
		t.Fatal("the NEXT call must be held again — approval is one-shot")
	}
}

// TestHoldGate_ElevatedTurnHasNoGate — an elevated /root turn IS the operator
// acting. Holding their call for their own approval is a deadlock.
func TestHoldGate_ElevatedTurnHasNoGate(t *testing.T) {
	srv := &Server{db: memDB(t)}
	if srv.holdGate(turnMCP{folder: "atlas", elevated: true}) != nil {
		t.Fatal("an elevated turn must not be gated")
	}
}

func TestHoldGate_NoRuleNoOverhead(t *testing.T) {
	srv := &Server{db: memDB(t)}
	gate := srv.holdGate(turnMCP{folder: "atlas"})
	if _, held := gate("mcp:delete", map[string]any{"a": 1}); held {
		t.Fatal("no hold rule must mean inline execution")
	}
}

// TestArgsHash_KeyCannotForgeSeparators: the agent chooses argument NAMES, so a
// key containing the separators must not reproduce another map's serialization.
// Writing the key raw made `{"a=\"1\"\nb": "x"}` hash identically to
// `{"a":"1","b":"x"}` — an agent could steer an operator's approval onto a
// different call. Both sides are JSON-encoded now.
func TestArgsHash_KeyCannotForgeSeparators(t *testing.T) {
	plain := ArgsHash(map[string]any{"a": "1", "b": "x"})
	forged := ArgsHash(map[string]any{"a=\"1\"\nb": "x"})
	if plain == forged {
		t.Fatal("a crafted argument NAME forged the separator and collided")
	}
	newline := ArgsHash(map[string]any{"a\nb": "x"})
	split := ArgsHash(map[string]any{"a": nil, "b": "x"})
	if newline == split {
		t.Fatal("a newline in a key collided with two separate keys")
	}
}
