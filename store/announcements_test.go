package store

import "testing"

func TestAnnouncements_CRUD(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.InsertAnnouncement(Announcement{
		Service: "store", Version: 30, Body: "upgrade!",
	}); err != nil {
		t.Fatal(err)
	}

	jids := []string{"tg:1", "tg:2"}
	pend, err := s.PendingAnnouncements(jids)
	if err != nil {
		t.Fatal(err)
	}
	if len(pend) != 1 || pend[0].Version != 30 {
		t.Fatalf("pending: %+v", pend)
	}

	if err := s.RecordAnnouncementSent("store", 30, "tg:1"); err != nil {
		t.Fatal(err)
	}
	pend, _ = s.PendingAnnouncements(jids)
	if len(pend) != 1 {
		t.Fatalf("still pending (1/2 sent): %+v", pend)
	}

	s.RecordAnnouncementSent("store", 30, "tg:2")
	pend, _ = s.PendingAnnouncements(jids)
	if len(pend) != 0 {
		t.Fatalf("should be empty, got %+v", pend)
	}
}
