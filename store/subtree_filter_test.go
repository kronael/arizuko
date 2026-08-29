package store

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// seedFolders writes one message per folder, all matching the same FTS term.
func seedFolders(t *testing.T, s *Store, folders ...string) {
	t.Helper()
	now := time.Now()
	for i, f := range folders {
		if err := s.PutMessage(core.Message{
			ID: f, ChatJID: "tg:" + f, Sender: "u", Content: "needle",
			Timestamp: now.Add(time.Duration(i) * time.Second), RoutedTo: f,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// A folder name may legally hold `%`, `_` or a capital: isValidFolder rejects
// only `..`, `\`, NUL, empty and over-long segments and reserved names. Under
// SQL LIKE all three widened the scope, so a tenant at `%` read every other
// tenant's message content and a Coach/coach pair read each other — a pair a
// live instance HAS carried (BUGS L122).
func TestFolderScopedReadsAreNotWidenedByLikeMetacharacters(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	seedFolders(t, s, "a_b/one", "aXb/two", "coach/k", "Coach/private", "x/y")

	cases := []struct {
		scope string
		leak  string
	}{
		{"a_b", "aXb/two"},         // _ is a single-character wildcard
		{"%", "x/y"},               // % matches every folder with a separator
		{"coach", "Coach/private"}, // LIKE is case-insensitive for ASCII
	}
	for _, c := range cases {
		got, err := s.FindMessages("needle", c.scope, "", "", 50)
		if err != nil {
			t.Fatalf("scope %q: %v", c.scope, err)
		}
		for _, m := range got {
			if m.ChatJID == "tg:"+c.leak {
				t.Errorf("scope %q read %q's messages", c.scope, c.leak)
			}
		}
	}

	// The exact subtree still reads, or the fix would be a denial of service.
	got, err := s.FindMessages("needle", "a_b", "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ChatJID != "tg:a_b/one" {
		t.Errorf("the folder's own subtree was lost: %+v", got)
	}
}

// ErroredChats reads the same messages table through its own filter.
func TestErroredChatsAreNotWidenedByLikeMetacharacters(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	seedFolders(t, s, "a_b/one", "aXb/two")
	if err := s.MarkMessagesErrored([]string{"a_b/one", "aXb/two"}); err != nil {
		t.Fatal(err)
	}
	got := s.ErroredChats("a_b", false)
	if len(got) != 1 || got[0].ChatJID != "tg:a_b/one" {
		t.Errorf("ErroredChats(a_b) = %+v, want only its own chat", got)
	}
}

// ListRoutes scopes on `target`, which carries a `#topic` fragment as well as
// a `/` subtree, so both separators go through the same predicate.
func TestListRoutesIsNotWidenedByLikeMetacharacters(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	for _, target := range []string{"a_b", "a_b/one", "a_b#t", "aXb", "aXb/two", "aXb#t"} {
		if _, err := s.PutRouteRow(core.Route{Seq: 1, Match: "platform=slack", Target: target}); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range s.ListRoutes("a_b", false) {
		if r.Target == "aXb" || r.Target == "aXb/two" || r.Target == "aXb#t" {
			t.Errorf("ListRoutes(a_b) returned %q", r.Target)
		}
	}
	if n := len(s.ListRoutes("a_b", false)); n != 3 {
		t.Errorf("ListRoutes(a_b) returned %d rows, want its own 3", n)
	}
}
