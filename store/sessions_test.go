package store

import (
	"errors"
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestForkTopic_WritesLineage(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.ForkTopic("main", "", "#deploy", "child-uuid-1", false); err != nil {
		t.Fatalf("ForkTopic: %v", err)
	}
	lin, ok := s.TopicLineage("main", "#deploy")
	if !ok {
		t.Fatal("TopicLineage: row missing after fork")
	}
	if lin.ParentTopic == nil || *lin.ParentTopic != "" {
		t.Errorf("parent_topic = %v, want \"\"", lin.ParentTopic)
	}
	if lin.ForkedAt == "" || lin.ObservedCursor == "" {
		t.Errorf("forked_at=%q observed_cursor=%q both must be seeded",
			lin.ForkedAt, lin.ObservedCursor)
	}
	sid, ok := s.GetSession("main", "#deploy")
	if !ok || sid != "child-uuid-1" {
		t.Errorf("GetSession child: ok=%v sid=%q, want true/child-uuid-1", ok, sid)
	}
}

func TestForkTopic_ErrExistsNoForce(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.ForkTopic("main", "", "#deploy", "uuid-a", false); err != nil {
		t.Fatal(err)
	}
	err = s.ForkTopic("main", "", "#deploy", "uuid-b", false)
	if !errors.Is(err, core.ErrTopicExists) {
		t.Errorf("err = %v, want ErrTopicExists", err)
	}
	sid, _ := s.GetSession("main", "#deploy")
	if sid != "uuid-a" {
		t.Errorf("session id changed: %q, want uuid-a", sid)
	}
}

func TestForkTopic_ForceOverwrites(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.ForkTopic("main", "", "#deploy", "uuid-a", false); err != nil {
		t.Fatal(err)
	}
	if err := s.ForkTopic("main", "#hotfix", "#deploy", "uuid-b", true); err != nil {
		t.Fatal(err)
	}
	sid, _ := s.GetSession("main", "#deploy")
	if sid != "uuid-b" {
		t.Errorf("session id = %q, want uuid-b after force", sid)
	}
	lin, _ := s.TopicLineage("main", "#deploy")
	if lin.ParentTopic == nil || *lin.ParentTopic != "#hotfix" {
		t.Errorf("parent_topic = %v, want #hotfix after force", lin.ParentTopic)
	}
}

// Spec 5/F-topic-lineage: EnsureTopicLineage returns inserted=true exactly on the
// row-creating call. The gateway uses that signal to decide whether
// to cp the parent session file.
func TestEnsureTopicLineage_InsertedFlag(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ins, err := s.EnsureTopicLineage("main", "#deploy", "", "uuid-1")
	if err != nil || !ins {
		t.Fatalf("first call: inserted=%v err=%v, want true/nil", ins, err)
	}
	ins, err = s.EnsureTopicLineage("main", "#deploy", "", "uuid-2")
	if err != nil || ins {
		t.Errorf("second call: inserted=%v err=%v, want false/nil", ins, err)
	}
	// Main topic ("") is skipped — no row, no insert.
	ins, err = s.EnsureTopicLineage("main", "", "", "uuid-3")
	if err != nil || ins {
		t.Errorf("main topic: inserted=%v err=%v, want false/nil", ins, err)
	}
}
