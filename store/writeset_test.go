package store

// The write set is what a handler hands its writes to instead of performing
// them, and these are the two rules that make a batch answerable: one guarded
// write per set, and a refusal that says what the CALLER meant by it.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// A set holds at most ONE guarded write. A driver answers a batch with one bit,
// so a second condition's meaning could not be told from the first's — and a
// caller that silently picked one would report the wrong thing on exactly the
// race it was guarding against.
func TestWriteSet_RefusesASecondGuardedWrite(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	ws := &WriteSet{}
	first := errors.New("the first condition")
	if err := s.RemoveACLRowInto(ws, core.ACLRow{
		Principal: "user:a", Action: "read", Scope: "acme",
	}, first); err != nil {
		t.Fatalf("the first guarded write: %v", err)
	}
	err := s.RemoveACLRowInto(ws, core.ACLRow{
		Principal: "user:b", Action: "read", Scope: "acme",
	}, errors.New("the second condition"))
	if err == nil {
		t.Fatal("a second guarded write was accepted; one batch reports one bit")
	}
	if !strings.Contains(err.Error(), "the first condition") {
		t.Errorf("the refusal %q does not name the meaning already held", err)
	}
}

// A refused batch answers what the guarded write said its refusal MEANS, and it
// writes nothing at all — the companion write goes with it. That is the whole
// guarantee the transaction used to give.
func TestWriteSet_RefusedBatchAnswersTheCallerAndWritesNothing(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	seedFolder(t, s, "acme")
	ctx := context.Background()

	notFound := errors.New("no matching grant")
	ws := &WriteSet{}
	// A companion that cannot refuse, beside a revoke of a grant nobody holds.
	if err := s.PutACLRowInto(ws, core.ACLRow{
		Principal: "user:companion", Action: "read", Scope: "acme", GrantedBy: "test",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveACLRowInto(ws, core.ACLRow{
		Principal: "user:absent", Action: "read", Scope: "acme",
	}, notFound); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(ctx, ws); !errors.Is(err, notFound) {
		t.Fatalf("Apply = %v, want the caller's own refusal", err)
	}
	rows, err := s.ListACL("user:companion")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("the companion grant landed under a refused batch: %+v", rows)
	}
}

// Apply sends the set and the writes named beside it — resreg's audit row — as
// ONE batch, so a caller that buffered nothing still records its call.
func TestWriteSet_ApplySendsTheCompanionOfAnEmptySet(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	ctx := context.Background()

	ws := &WriteSet{}
	if ws.Len() != 0 {
		t.Fatalf("a fresh set holds %d writes", ws.Len())
	}
	key, err := aclKey(aclDefaults(core.ACLRow{
		Principal: "user:alone", Action: "read", Scope: "acme", Effect: "allow",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(ctx, ws, objstore.Write{
		Op: objstore.Merge, Entity: ACL, Key: key,
		Init: map[string]string{attrACLGrantedBy: "test"},
	}); err != nil {
		t.Fatalf("Apply with an empty set: %v", err)
	}
	rows, err := s.ListACL("user:alone")
	if err != nil || len(rows) != 1 {
		t.Errorf("the companion write landed %d rows (%v), want 1", len(rows), err)
	}
}
