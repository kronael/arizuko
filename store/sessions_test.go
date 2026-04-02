package store

import (
	"strings"
	"testing"
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
