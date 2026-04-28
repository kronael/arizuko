package store

import "testing"

func TestRecordTurnResult_Idempotent(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	first, err := s.RecordTurnResult("g1", "msg-1", "sess-a", "success")
	if err != nil {
		t.Fatal(err)
	}
	if !first {
		t.Error("first call should record")
	}

	second, err := s.RecordTurnResult("g1", "msg-1", "sess-b", "success")
	if err != nil {
		t.Fatal(err)
	}
	if second {
		t.Error("duplicate (folder, turn_id) must collapse")
	}
}

func TestRecordTurnResult_DistinctTurns(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for _, id := range []string{"m1", "m2", "m3"} {
		ok, err := s.RecordTurnResult("g1", id, "s", "success")
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("turn %s should be recorded", id)
		}
	}
}

func TestRecordTurnResult_DifferentFolders(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	a, _ := s.RecordTurnResult("g1", "msg-1", "", "success")
	b, _ := s.RecordTurnResult("g2", "msg-1", "", "success")
	if !a || !b {
		t.Error("same turn_id in different folders must both record")
	}
}
