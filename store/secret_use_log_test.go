package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// secretUses reads the whole entity back. There is no production reader (the
// file header says so), so the test names the read it makes rather than
// inventing a method nothing else would call.
func secretUses(t *testing.T, s *Store) []objstore.Item {
	t.Helper()
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: SecretUseLog, Partition: secretUsePartition,
	})
	if err != nil {
		t.Fatalf("read the secret uses: %v", err)
	}
	return items
}

func TestLogSecretUse_RoundTrip(t *testing.T) {
	s := openEntities(t)

	ts := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	row := SecretUseRow{
		TS:        ts,
		SpawnID:   "spawn-1",
		CallerSub: "github:alice",
		Folder:    "atlas/eng",
		Tool:      "github_pr",
		Key:       "GITHUB_TOKEN",
		Scope:     "user",
		Status:    "ok",
		LatencyMS: 42,
	}
	if err := s.LogSecretUse(row); err != nil {
		t.Fatalf("LogSecretUse: %v", err)
	}

	items := secretUses(t, s)
	if len(items) != 1 {
		t.Fatalf("recorded %d uses, want 1", len(items))
	}
	a := items[0].Attrs
	if a[attrUseSpawnID] != "spawn-1" || a[attrUseCallerSub] != "github:alice" ||
		a[attrUseFolder] != "atlas/eng" || a[attrUseTool] != "github_pr" ||
		a[attrUseKey] != "GITHUB_TOKEN" || a[attrUseScope] != "user" ||
		a[attrUseStatus] != "ok" || a[attrUseLatency] != "42" {
		t.Errorf("item mismatch: %v", a)
	}
}

// TestLogSecretUse_TheIDCarriesTheInstant pins that the time is the sort key and
// is not stored beside it. Two declarations of one fact are two things that can
// disagree, and the ordering guarantee rests on the key.
func TestLogSecretUse_TheIDCarriesTheInstant(t *testing.T) {
	s := openEntities(t)

	ts := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	if err := s.LogSecretUse(SecretUseRow{
		TS: ts, Tool: "t", Key: "K", Scope: "missing", Status: "ok",
	}); err != nil {
		t.Fatalf("LogSecretUse: %v", err)
	}
	items := secretUses(t, s)
	if len(items) != 1 {
		t.Fatalf("recorded %d uses, want 1", len(items))
	}
	id := items[0].Key.Sort
	if len(id) != SecretUseIDWidth {
		t.Fatalf("id %q is %d bytes, want %d", id, len(id), SecretUseIDWidth)
	}
	if got := id[:tsWidth]; got != core.TS(ts) {
		t.Errorf("id carries the instant %q, want %q", got, core.TS(ts))
	}
	if _, stored := items[0].Attrs["ts"]; stored {
		t.Errorf("the item stores the time a second time, beside the key that already is it")
	}
}

// TestLogSecretUse_AuditPairAtomic: one call must produce BOTH the use and its
// audit_log row — the "every event has an audit row" invariant. They are now one
// GuardedBatch rather than one transaction, so they commit together or not at
// all on every engine (spec 6/40).
func TestLogSecretUse_AuditPairAtomic(t *testing.T) {
	s := openEntities(t)

	row := SecretUseRow{
		SpawnID: "spawn-9", CallerSub: "github:bob", Folder: "atlas/eng",
		Tool: "github_pr", Key: "GITHUB_TOKEN", Scope: "user", Status: "ok",
	}
	if err := s.LogSecretUse(row); err != nil {
		t.Fatalf("LogSecretUse: %v", err)
	}

	if n := len(secretUses(t, s)); n != 1 {
		t.Errorf("recorded uses: want 1, got %d", n)
	}

	nAudit := 0
	for _, r := range auditRowsFor(t, s, "secret.read") {
		if r.ActorSub == "github:bob" {
			nAudit++
		}
	}
	if nAudit != 1 {
		t.Errorf("recorded rows: want 1, got %d (audit pair not atomic)", nAudit)
	}
}

// TestLogSecretUse_ARefusedPairRecordsNeither is the other half of the same
// invariant, and the one a batch makes checkable: a use whose id is already
// taken must not leave an audit row behind claiming a read that recorded
// nothing.
func TestLogSecretUse_ARefusedPairRecordsNeither(t *testing.T) {
	s := openEntities(t)

	// Claim an id, then make the live write reach for the same one.
	ts := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	id := core.TS(ts) + "-00000001"
	if _, err := s.Obj().GuardedBatch(context.Background(), nil, []objstore.Write{
		secretUseWrite(id, SecretUseRow{Tool: "t", Key: "K", Scope: "missing", Status: "ok"}),
	}); err != nil {
		t.Fatal(err)
	}
	before := len(auditRowsFor(t, s, "secret.read"))
	_, again := s.Obj().GuardedBatch(context.Background(), nil, []objstore.Write{
		secretUseWrite(id, SecretUseRow{Tool: "t", Key: "K", Scope: "missing", Status: "ok"}),
		mustAuditWrite(t),
	})
	if again != nil {
		t.Fatalf("batch: %v", again)
	}
	if got := len(auditRowsFor(t, s, "secret.read")); got != before {
		t.Fatalf("the refused batch left %d audit rows behind, want %d", got, before)
	}
	if n := len(secretUses(t, s)); n != 1 {
		t.Fatalf("recorded %d uses, want the 1 that committed", n)
	}
}

func TestLogSecretUse_AutoTimestamp(t *testing.T) {
	s := openEntities(t)

	if err := s.LogSecretUse(SecretUseRow{Tool: "t", Key: "K", Scope: "missing", Status: "ok"}); err != nil {
		t.Fatalf("LogSecretUse: %v", err)
	}
	items := secretUses(t, s)
	if len(items) != 1 {
		t.Fatalf("recorded %d uses, want 1", len(items))
	}
	if _, err := time.Parse(time.RFC3339Nano, items[0].Key.Sort[:tsWidth]); err != nil {
		t.Errorf("the id's instant is not a time: %q (%v)", items[0].Key.Sort, err)
	}
}

// mustAuditWrite renders the audit row LogSecretUse would put in the same
// batch, through the same renderer, so the refusal test exercises the real pair
// rather than a pair assembled for it.
func mustAuditWrite(t *testing.T) objstore.Write {
	t.Helper()
	_, w := audit.WriteFor(audit.Event{
		Category: audit.CategorySecret,
		Action:   "secret.read",
		Actor:    "github:bob",
		ActorSub: "github:bob",
		Surface:  audit.SurfaceMCP,
		Resource: "secrets/missing//K",
		Outcome:  audit.OutcomeOK,
	})
	return w
}

// A broker secret read happens on the MCP surface — routd's
// ResolveConnectorSecrets is the one production caller, so a surface query
// for `mcp` finds these rows.
func TestSecretUseLogStampsTheMCPSurface(t *testing.T) {
	s := openMem(t)
	if err := s.LogSecretUse(SecretUseRow{
		Tool: "t", Key: "K", Scope: "folder", Folder: "eng",
		CallerSub: "github:bob", Status: "ok",
	}); err != nil {
		t.Fatalf("LogSecretUse: %v", err)
	}
	rows, err := audit.Query(context.Background(), s.Obj(), audit.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("audit.Query: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.Action == "secret.read" {
			found = true
			if r.Surface != audit.SurfaceMCP {
				t.Errorf("surface = %q, want %q", r.Surface, audit.SurfaceMCP)
			}
		}
	}
	if !found {
		t.Fatal("no secret.read audit row; this test checks nothing")
	}
}

// The `audit_log` twin is the ONLY read surface a secret use has: this entity
// has no reader (the file header says so), and the answer to "who used which
// secret, with which tool" is served from the twin on both faces —
// `GET /v1/audit?category=secret` and `query_audit` (BUGS L13).
//
// So every field of SecretUseRow must survive into that row. Dropping one from
// the event loses a forensic answer with nothing failing: the write still
// succeeds, the row still appears, and only the missing column says so. Each
// field is pinned here by name, and retiring the entity (audit/PLAN.md round 3)
// is safe only while this passes.
func TestSecretUseLogAuditTwinCarriesEveryField(t *testing.T) {
	s := openMem(t)
	use := SecretUseRow{
		SpawnID: "spawn-7", CallerSub: "github:bob", Folder: "eng/sre",
		Tool: "gh_search", Key: "GH_TOKEN", Scope: "folder",
		Status: "ok", LatencyMS: 42,
	}
	if err := s.LogSecretUse(use); err != nil {
		t.Fatalf("LogSecretUse: %v", err)
	}

	rows, err := audit.Query(context.Background(), s.Obj(), audit.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("audit.Query: %v", err)
	}
	var row audit.Row
	for _, r := range rows {
		if r.Action == "secret.read" {
			row = r
			break
		}
	}
	if row.Action == "" {
		t.Fatal("no secret.read audit row; this test checks nothing")
	}

	// Key and Scope ride the resource coordinate; the rest are columns.
	for _, c := range []struct{ field, got, want string }{
		{"category", row.Category, audit.CategorySecret},
		{"actor", row.Actor, use.CallerSub},
		{"actor_sub", row.ActorSub, use.CallerSub},
		{"folder", row.Folder, use.Folder},
		{"scope", row.Scope, use.Scope},
		{"resource (key)", row.Resource, "secrets/folder/eng/sre/GH_TOKEN"},
		{"outcome", row.Outcome, audit.OutcomeOK},
		{"surface", row.Surface, audit.SurfaceMCP},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.field, c.got, c.want)
		}
	}
	if row.DurationMS != use.LatencyMS {
		t.Errorf("duration_ms = %d, want %d", row.DurationMS, use.LatencyMS)
	}
	if row.ID == "" {
		t.Error("id is empty: the row carries no instant, so no window can page it")
	}
	// Tool and SpawnID are the two that live in params_summary, which is where
	// a dropped field is least visible.
	for _, want := range []string{use.Tool, use.SpawnID} {
		if !strings.Contains(row.ParamsSummary, want) {
			t.Errorf("params_summary = %q, missing %q", row.ParamsSummary, want)
		}
	}
}
