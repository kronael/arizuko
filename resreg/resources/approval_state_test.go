package resources

import (
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
	"testing"
	"time"
)

func TestApplyVerdictSchedulesApprovalWork(t *testing.T) {
	raw := openMem(t)
	s := store.NewOver(raw)
	backend := resreg.On(raw)
	row := PendingActionsRow{ID: "one", GroupFolder: "world", Tool: "delete", Status: "held"}
	apply := func() {
		t.Helper()
		if _, err := resreg.Apply(t.Context(), backend, resreg.SubsystemRoutd, routdChecksum(t, raw), false, map[string]any{PendingActionsName: []PendingActionsRow{row}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	apply()
	if work, err := s.DueWork(t.Context(), "approvals", time.Now()); err != nil || len(work) != 0 {
		t.Fatalf("held work %v %v", work, err)
	}
	row.Status = "approved"
	apply()
	work, err := s.DueWork(t.Context(), "approvals", time.Now())
	if err != nil || len(work) != 1 || work[0].Key.Sort != "one" {
		t.Fatalf("verdict work %v %v", work, err)
	}
	if err := s.AcknowledgeWork(t.Context(), work[0]); err != nil {
		t.Fatal(err)
	}
	if work, err := s.DueWork(t.Context(), "approvals", time.Now()); err != nil || len(work) != 0 {
		t.Fatalf("acked work %v %v", work, err)
	}
}
