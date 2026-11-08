package store

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// The errored flag is a claim about NOW, so a clean turn has to take it away.
// Without the dual the flag is monotonic for the life of an instance and the
// count only ever grows (BUGS L304).
func TestClearChatErrored_TakesTheFlagAway(t *testing.T) {
	s := openMem(t)
	const jid = "telegram:user/7"
	m := core.Message{
		ID: "m1", ChatJID: jid, Sender: "alice", Content: "hello",
		Timestamp: time.Now().UTC(),
	}
	if err := s.PutMessage(m); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkChatErrored(jid); err != nil {
		t.Fatal(err)
	}
	errored, err := s.ErroredChatJIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(errored) != 1 || errored[0] != jid {
		t.Fatalf("precondition: errored chats = %v, want [%s]", errored, jid)
	}

	// The cursor advance is NOT the clear: the failure path takes it too.
	if err := s.SetAgentCursor(jid, MessageCursor(m)); err != nil {
		t.Fatalf("SetAgentCursor: %v", err)
	}
	if flagged, _ := s.ErroredChatJIDs(); len(flagged) != 1 {
		t.Fatalf("the cursor advance cleared the flag; the failure path advances too: %v", flagged)
	}

	if err := s.ClearChatErrored(jid); err != nil {
		t.Fatalf("ClearChatErrored: %v", err)
	}

	errored, err = s.ErroredChatJIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(errored) != 0 {
		t.Errorf("errored chats after the clear = %v, want none", errored)
	}
	// The clear must not take the cursor with it.
	got, err := s.AgentCursor(jid)
	if err != nil || got != MessageCursor(m) {
		t.Errorf("agent cursor = %q (err=%v), want %q", got, err, MessageCursor(m))
	}
}
