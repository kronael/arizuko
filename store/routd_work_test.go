package store

import (
	"fmt"
	"github.com/kronael/arizuko/objstore"
	"testing"
	"time"
)

func seedApproval(t *testing.T, s *Store, id, status string) {
	t.Helper()
	key, err := PendingActionKey(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Put, Entity: PendingActions, Key: key, Attrs: map[string]string{AttrPendStatus: status}}); err != nil {
		t.Fatal(err)
	}
}
func TestApprovalWorkReadsOnlyTransitions(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	for i := range 40 {
		seedApproval(t, s, fmt.Sprint(i), "held")
	}
	c := &stateReadCounter{Store: s.Obj()}
	reader := NewOver(c)
	if work, err := reader.DueWork(t.Context(), "approvals", time.Now()); err != nil || len(work) != 0 || c.rows != 0 {
		t.Fatalf("held work=%v rows=%d err=%v", work, c.rows, err)
	}
	seedApproval(t, s, "0", "approved")
	c.rows = 0
	work, err := reader.DueWork(t.Context(), "approvals", time.Now())
	if err != nil || len(work) != 1 || c.rows != 2 {
		t.Fatalf("verdict work=%v rows=%d err=%v", work, c.rows, err)
	}
	if err := reader.AcknowledgeWork(t.Context(), work[0]); err != nil {
		t.Fatal(err)
	}
	c.rows = 0
	if work, err := reader.DueWork(t.Context(), "approvals", time.Now()); err != nil || len(work) != 0 || c.rows != 0 {
		t.Fatalf("acknowledged work=%v rows=%d err=%v", work, c.rows, err)
	}
}
func TestApprovalWorkKeepsConcurrentVerdict(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	seedApproval(t, s, "one", "approved")
	work, err := s.DueWork(t.Context(), "approvals", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	seedApproval(t, s, "one", "rejected")
	if err := s.AcknowledgeWork(t.Context(), work[0]); err != nil {
		t.Fatal(err)
	}
	after, err := s.DueWork(t.Context(), "approvals", time.Now())
	if err != nil || len(after) != 1 || after[0].Attrs["generation"] == work[0].Attrs["generation"] {
		t.Fatalf("lost new verdict: %v %v", after, err)
	}
}
func TestApprovalWorkRefusedWriteSchedulesNothing(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	seedApproval(t, s, "one", "held")
	key, _ := PendingActionKey("one")
	ok, err := s.TryWriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Update, Entity: PendingActions, Key: key, Attrs: map[string]string{AttrPendStatus: "approved"}, Cond: objstore.Cond{Item: objstore.ItemAbsent}})
	if err != nil || ok {
		t.Fatalf("refusal %v %v", ok, err)
	}
	if work, err := s.DueWork(t.Context(), "approvals", time.Now()); err != nil || len(work) != 0 {
		t.Fatalf("refused write scheduled %v %v", work, err)
	}
}
func TestApprovalWorkMigrationRunsOnce(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	key, _ := PendingActionKey("legacy")
	if ok, err := s.obj.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Put, Entity: PendingActions, Key: key, Attrs: map[string]string{AttrPendStatus: "approved"}}}); err != nil || !ok {
		t.Fatalf("seed %v %v", ok, err)
	}
	if err := s.MigrateStateIndexes(t.Context()); err != nil {
		t.Fatal(err)
	}
	work, err := s.DueWork(t.Context(), "approvals", time.Now())
	if err != nil || len(work) != 1 {
		t.Fatalf("migrated %v %v", work, err)
	}
	if err := s.AcknowledgeWork(t.Context(), work[0]); err != nil {
		t.Fatal(err)
	}
	c := &stateReadCounter{Store: s.Obj()}
	if err := NewOver(c).MigrateStateIndexes(t.Context()); err != nil || c.rows != 7 {
		t.Fatalf("repeat reads=%d err=%v", c.rows, err)
	}
	if work, err := s.DueWork(t.Context(), "approvals", time.Now()); err != nil || len(work) != 0 {
		t.Fatalf("migration recreated %v %v", work, err)
	}
}

func TestApprovalWorkPassIsBounded(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	for i := range 201 {
		seedApproval(t, s, fmt.Sprint(i), "approved")
	}
	c := &stateReadCounter{Store: s.Obj()}
	work, err := NewOver(c).DueWork(t.Context(), "approvals", time.Now())
	if err != nil || len(work) != 200 || c.rows != 400 {
		t.Fatalf("batch work=%d reads=%d err=%v", len(work), c.rows, err)
	}
	for _, it := range work {
		if err := s.AcknowledgeWork(t.Context(), it); err != nil {
			t.Fatal(err)
		}
	}
	left, err := s.DueWork(t.Context(), "approvals", time.Now())
	if err != nil || len(left) != 1 {
		t.Fatalf("left=%d err=%v", len(left), err)
	}
}
