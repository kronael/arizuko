package store

import (
	"context"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// BUGS A4, the sharp half: individual scan failures used to be SKIPPED
// (`if err == nil { out = append(...) }`), so a row that would not scan simply
// vanished from the policy. That is not a degradation, it is an ESCALATION —
// drop a DENY row and the allow rows above it survive, so the caller is granted
// something an operator explicitly forbade.
//
// Forced by writing an item whose coordinate splits into FIVE parts: the six
// fields are the key, so a short one names a grant nobody wrote and aclRowFrom
// refuses that item and nothing else.
func TestACLReadRefusesToReturnAPartialPolicy(t *testing.T) {
	s := openMem(t)
	rows := []core.ACLRow{
		{Principal: "google:alice", Action: "*", Scope: "atlas/**", Effect: "allow"},
		{Principal: "google:alice", Action: "mcp:send", Scope: "atlas/secret", Effect: "deny"},
	}
	for _, r := range rows {
		if err := s.AddACLRow(r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ACLRowsFor([]string{"google:alice"})
	if err != nil || len(got) != 2 {
		t.Fatalf("fixture: rows=%d err=%v, want 2 rows", len(got), err)
	}

	// Corrupt ONLY the deny row: re-address it at a five-part coordinate, which
	// is the one item ACLRowsFor's prefix read still reaches and cannot read.
	deny := aclKeyOf(t, core.ACLRow{
		Principal: "google:alice", Action: "mcp:send", Scope: "atlas/secret", Effect: "deny"})
	short := strings.Join([]string{"google:alice", "mcp:send", "atlas/secret", "", "deny"}, KeySep)
	if err := s.WriteEntities(context.Background(), nil,
		objstore.Write{Op: objstore.Delete, Entity: ACL, Key: deny},
		objstore.Write{Op: objstore.Put, Entity: ACL,
			Key:   objstore.Key{Partition: OwnerRoutd, Sort: short},
			Attrs: map[string]string{attrACLGrantOption: "0"}},
	); err != nil {
		t.Fatal(err)
	}

	got, err = s.ACLRowsFor([]string{"google:alice"})
	if err == nil {
		t.Fatalf("a row that would not scan was silently dropped: got %d rows, no error", len(got))
	}
	if len(got) != 0 {
		t.Errorf("partial policy returned alongside the error: %+v", got)
	}
	if !strings.Contains(err.Error(), "read rows for principals") {
		t.Errorf("error should name the failing reader, got: %v", err)
	}
}

// aclKeyOf is the coordinate of one grant, for a fixture that has to address an
// item the writers already wrote.
func aclKeyOf(t *testing.T, r core.ACLRow) objstore.Key {
	t.Helper()
	k, err := aclKey(r)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
