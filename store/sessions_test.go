package store

import (
	"strings"
	"testing"
	"time"
)

func TestFlushSysMsgs_AllPresent(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.EnqueueSysMsg("main", "gateway", "new-session", "session started")
	s.EnqueueSysMsg("main", "scheduler", "cron-fire", "task ran")
	s.EnqueueSysMsg("main", "gateway", "new-day", "2026-04-01")

	xml := s.FlushSysMsgs("main")

	if count := strings.Count(xml, "<system "); count != 3 {
		t.Errorf("expected 3 <system> tags, got %d; xml:\n%s", count, xml)
	}
	for _, want := range []string{
		`origin="gateway"`,
		`origin="scheduler"`,
		`event="new-session"`,
		`event="cron-fire"`,
		`event="new-day"`,
		"session started",
		"task ran",
		"2026-04-01",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("xml missing %q; got:\n%s", want, xml)
		}
	}

	// Table should be empty after flush
	xml2 := s.FlushSysMsgs("main")
	if xml2 != "" {
		t.Errorf("expected empty after flush, got %q", xml2)
	}
}

func TestFlushSysMsgs_IsolatesFolder(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.EnqueueSysMsg("alpha", "gateway", "new-session", "")
	s.EnqueueSysMsg("beta", "gateway", "new-session", "")

	xml := s.FlushSysMsgs("alpha")
	if count := strings.Count(xml, "<system "); count != 1 {
		t.Errorf("alpha: expected 1 tag, got %d", count)
	}

	// beta should still have its message
	xml = s.FlushSysMsgs("beta")
	if count := strings.Count(xml, "<system "); count != 1 {
		t.Errorf("beta: expected 1 tag, got %d", count)
	}
}

func TestFlushSysMsgs_EmptyFolder(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	xml := s.FlushSysMsgs("nonexistent")
	if xml != "" {
		t.Errorf("expected empty for nonexistent folder, got %q", xml)
	}
}

func TestFlushSysMsgs_OrderPreserved(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.EnqueueSysMsg("main", "a", "first", "")
	s.EnqueueSysMsg("main", "b", "second", "")
	s.EnqueueSysMsg("main", "c", "third", "")

	xml := s.FlushSysMsgs("main")
	iFirst := strings.Index(xml, `event="first"`)
	iSecond := strings.Index(xml, `event="second"`)
	iThird := strings.Index(xml, `event="third"`)

	if iFirst >= iSecond || iSecond >= iThird {
		t.Errorf("events not in insertion order; positions: first=%d second=%d third=%d",
			iFirst, iSecond, iThird)
	}
}

func TestRecordSession_StartTimeIsCallerProvided(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	t0 := time.Now().Add(-5 * time.Minute).Truncate(time.Second)
	rowID, err := s.RecordSession("grp", "sid-1", t0)
	if err != nil || rowID == 0 {
		t.Fatalf("RecordSession failed: rowID=%d err=%v", rowID, err)
	}
	if err := s.EndSession(rowID, "sid-1", "ok", "", 1); err != nil {
		t.Fatal(err)
	}

	recs := s.RecentSessions("grp", 1)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	if !recs[0].StartedAt.Equal(t0) {
		t.Errorf("started_at = %v, want %v", recs[0].StartedAt, t0)
	}
	if recs[0].EndedAt == nil {
		t.Error("ended_at should be set")
	}
}

func TestEndSession_SetsEndedAtAndFields(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	start := time.Now().Add(-1 * time.Minute).Truncate(time.Second)
	rowID, _ := s.RecordSession("grp", "sid-1", start)
	s.EndSession(rowID, "sid-1", "ok", "", 3)

	recs := s.RecentSessions("grp", 1)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record")
	}
	r := recs[0]
	if r.EndedAt == nil || !r.EndedAt.After(r.StartedAt) {
		t.Errorf("ended_at should be after started_at; got started=%v ended=%v",
			r.StartedAt, r.EndedAt)
	}
	if r.Result != "ok" || r.MsgCount != 3 {
		t.Errorf("result=%q msgCount=%d, want ok/3", r.Result, r.MsgCount)
	}
}

func TestEndSession_BackfillsSessionID(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rowID, _ := s.RecordSession("grp", "", time.Now())
	s.EndSession(rowID, "sid-learned", "ok", "", 1)

	recs := s.RecentSessions("grp", 1)
	if len(recs) != 1 || recs[0].SessionID != "sid-learned" {
		t.Errorf("session id not backfilled; got %+v", recs)
	}
}

func TestEndSession_PreservesSessionIDWhenEmpty(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rowID, _ := s.RecordSession("grp", "sid-orig", time.Now())
	// Unclean exit: no new session id learned, but error reported.
	s.EndSession(rowID, "", "error", "container crashed", 0)

	recs := s.RecentSessions("grp", 1)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record")
	}
	if recs[0].SessionID != "sid-orig" {
		t.Errorf("session id clobbered; got %q want sid-orig", recs[0].SessionID)
	}
	if recs[0].EndedAt == nil {
		t.Error("ended_at should be set even on unclean exit")
	}
	if recs[0].Result != "error" || recs[0].Error != "container crashed" {
		t.Errorf("result=%q err=%q, want error/container crashed", recs[0].Result, recs[0].Error)
	}
}

func TestRecordSession_PerRunRows(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Three runs of the same continued session.
	for i := 0; i < 3; i++ {
		rowID, _ := s.RecordSession("grp", "sid-1", time.Now())
		s.EndSession(rowID, "sid-1", "ok", "", 1)
	}

	recs := s.RecentSessions("grp", 10)
	if len(recs) != 3 {
		t.Fatalf("expected 3 rows for continued session, got %d", len(recs))
	}
	for _, r := range recs {
		if r.SessionID != "sid-1" {
			t.Errorf("row has session_id=%q, want sid-1", r.SessionID)
		}
	}
}
