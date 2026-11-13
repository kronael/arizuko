package store

import (
	"testing"

	"github.com/kronael/arizuko/objstore"
)

func TestStatusIntentFenced(t *testing.T) {
	s := openTurnEntities(t)
	if ok, err := s.OpenTurnContext("status-turn", "demo", "", "web:demo", "u", "", nil); err != nil || !ok {
		t.Fatalf("open %v %v", ok, err)
	}
	tc, _ := s.TurnContextOf("status-turn")
	tc.FailureCause = "Server storage is full"
	if ok, err := s.FinishControlledTurn(t.Context(), tc, nil, nil); err != nil || !ok {
		t.Fatalf("finish %v %v", ok, err)
	}
	current, _ := s.TurnContextOf(tc.TurnID)
	if current.StatusOwed != "final" || current.FailureCause != tc.FailureCause {
		t.Fatalf("lost final intent: %+v", current)
	}
	for _, field := range []string{"attempt", "phase", "state", "count"} {
		stale := current
		switch field {
		case "attempt":
			stale.AttemptID = "old"
		case "phase":
			stale.StatusOwed = "waiting"
		case "state":
			stale.State = TurnRunning
		case "count":
			stale.RetryCount = 1
		}
		if err := s.AcknowledgeTurnStatus(t.Context(), stale); err != nil {
			t.Fatal(err)
		}
		got, _ := s.TurnContextOf(tc.TurnID)
		if got.StatusOwed != "final" {
			t.Fatalf("stale %s cleared final intent", field)
		}
	}
	if err := s.AcknowledgeTurnStatus(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	got, _ := s.TurnContextOf(tc.TurnID)
	if got.StatusOwed != "" {
		t.Fatalf("intent not acknowledged: %+v", got)
	}
}

func TestStatusFinalIntentWinsCompletion(t *testing.T) {
	s := openTurnEntities(t)
	if ok, err := s.OpenTurnContext("final-turn", "demo", "", "web:demo", "u", "", nil); err != nil || !ok {
		t.Fatalf("open %v %v", ok, err)
	}
	running, _ := s.TurnContextOf("final-turn")
	if ok, err := s.FinishControlledTurn(t.Context(), running, nil, nil); err != nil || !ok {
		t.Fatalf("finish %v %v", ok, err)
	}
	stale, _ := s.TurnContextOf("final-turn")
	current := stale
	current.FailureCause = "Agent stopped unexpectedly"
	if ok, err := s.RecordControlledStatus(t.Context(), current, nil, TurnControlState{Phase: "open", Episode: "failure"}); err != nil || !ok {
		t.Fatalf("failure %v %v", ok, err)
	}
	if ok, err := s.FinishTurnStatus(t.Context(), stale); err != nil || ok {
		t.Fatalf("stale cleanup replaced final: %v %v", ok, err)
	}
	current, _ = s.TurnContextOf(stale.TurnID)
	for _, field := range []string{"attempt", "state"} {
		old := current
		if field == "attempt" {
			old.AttemptID = "old"
		} else {
			old.State = TurnRunning
		}
		if ok, err := s.FinishTurnStatus(t.Context(), old); err != nil || ok {
			t.Fatalf("stale completion %s accepted: %v %v", field, ok, err)
		}
	}
	if ok, err := s.FinishTurnStatus(t.Context(), current); err != nil || !ok {
		t.Fatalf("completion %v %v", ok, err)
	}
	current, _ = s.TurnContextOf(stale.TurnID)
	if current.StatusOwed != "final" {
		t.Fatalf("completion cleared failure intent: %+v", current)
	}
}

func TestFinishTurnStatusClosesLegacyRowWithoutAttempt(t *testing.T) {
	s := openTurnEntities(t)
	if ok, err := s.OpenTurnContext("legacy-turn", "demo", "", "web:demo", "u", "", nil); err != nil || !ok {
		t.Fatalf("open %v %v", ok, err)
	}
	if err := s.SetTurnState("legacy-turn", TurnPendingRetry); err != nil {
		t.Fatal(err)
	}
	key, err := turnKey("legacy-turn")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.TryWriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Update, Entity: TurnContexts, Key: key, Remove: []string{"attempt_id"}}); err != nil || !ok {
		t.Fatalf("drop attempt %v %v", ok, err)
	}
	tc, _ := s.TurnContextOf("legacy-turn")
	if tc.AttemptID != "" {
		t.Fatalf("fixture has an attempt: %q", tc.AttemptID)
	}
	if ok, err := s.FinishTurnStatus(t.Context(), tc); err != nil || !ok {
		t.Fatalf("legacy finish %v %v", ok, err)
	}
	got, _ := s.TurnContextOf("legacy-turn")
	if got.State != TurnDone {
		t.Fatalf("legacy turn left %s", got.State)
	}
}
