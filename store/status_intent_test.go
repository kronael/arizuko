package store

import "testing"

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
