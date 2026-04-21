package store

import (
	"testing"
	"testing/fstest"
)

func TestScanAnnouncements(t *testing.T) {
	fsys := fstest.MapFS{
		"m/0030-foo.md":  {Data: []byte("hello")},
		"m/0031-bar.md":  {Data: []byte("world")},
		"m/0032-baz.sql": {Data: []byte("-- ignore")},
		"m/README":       {Data: []byte("skip")},
	}
	got := scanAnnouncements(fsys, "m", "store")
	if len(got) != 2 {
		t.Fatalf("want 2, got %d: %+v", len(got), got)
	}
	if got[0].Version != 30 || got[0].Body != "hello" {
		t.Errorf("first: %+v", got[0])
	}
	if got[1].Version != 31 || got[1].Body != "world" {
		t.Errorf("second: %+v", got[1])
	}
}

func TestRecordSent_Ledger(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.RecordSent("store", 30, "tg:1"); err != nil {
		t.Fatal(err)
	}
	// idempotent
	if err := s.RecordSent("store", 30, "tg:1"); err != nil {
		t.Fatal(err)
	}

	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM announcement_sent
		WHERE service=? AND version=? AND user_jid=?`,
		"store", 30, "tg:1").Scan(&n)
	if n != 1 {
		t.Fatalf("want 1 row, got %d", n)
	}
}
