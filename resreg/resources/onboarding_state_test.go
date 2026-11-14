package resources

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

func TestApplyGateWakesDeferredAdmission(t *testing.T) {
	raw := openMem(t)
	s := store.NewOver(raw)
	if err := s.PutGate("*", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertOnboarding("chat", "sender"); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueOnboarding("chat", "user", "*"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	work, err := s.DueAdmissionGates(t.Context(), now)
	if err != nil || len(work) != 1 {
		t.Fatalf("initial work: %v %v", work, err)
	}
	if ids, err := s.AdmitGate(t.Context(), work[0], now); err != nil || len(ids) != 0 {
		t.Fatalf("closed quota: %v %v", ids, err)
	}
	if work, err := s.DueAdmissionGates(t.Context(), now); err != nil || len(work) != 0 {
		t.Fatalf("not deferred: %v %v", work, err)
	}
	backend := resreg.On(raw)
	if _, err := resreg.Apply(t.Context(), backend, resreg.SubsystemOnbod, onbodChecksum(t, raw), false, map[string]any{
		OnboardingGatesName: []OnboardingGatesRow{{Gate: "*", LimitPerDay: 1, Enabled: 1}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	work, err = s.DueAdmissionGates(t.Context(), now)
	if err != nil || len(work) != 1 {
		t.Fatalf("apply failed to wake gate: %v %v", work, err)
	}
	if ids, err := s.AdmitGate(t.Context(), work[0], now); err != nil || len(ids) != 1 || ids[0] != "chat" {
		t.Fatalf("admission: %v %v", ids, err)
	}
	if _, err := resreg.Apply(t.Context(), backend, resreg.SubsystemOnbod, onbodChecksum(t, raw), false, map[string]any{
		OnboardingGatesName: []OnboardingGatesRow{},
	}, nil); err != nil {
		t.Fatal(err)
	}
	work, err = s.DueAdmissionGates(t.Context(), now)
	if err != nil || len(work) != 1 {
		t.Fatalf("delete failed to wake gate: %v %v", work, err)
	}
	if _, err := s.AdmitGate(t.Context(), work[0], now); err != nil {
		t.Fatal(err)
	}
	if work, err := s.DueAdmissionGates(t.Context(), now); err != nil || len(work) != 0 {
		t.Fatalf("deleted gate work remains: %v %v", work, err)
	}
}
