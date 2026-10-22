package store

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// An outbound bot row carries a synthetic primary id, so the only handle on it
// the platform ever returns is `platform_id`. routd matches a reply's parent on
// `id OR platform_id` to promote it to verb=mention (spec 5/L); a row written
// without the column makes that lookup miss and the agent never re-attends.
func TestPutMessage_KeepsThePlatformID(t *testing.T) {
	s := openMem(t)
	ts := "1780300379.765170"
	if err := s.PutMessage(core.Message{
		ID: "mcp-test1", ChatJID: "slack:T/channel/C0B4", Sender: "atlas/support",
		Content: "hi", FromMe: true, BotMsg: true, PlatformID: ts, Timestamp: time.Now()}); err != nil {
		t.Fatalf("PutMessage: %v", err)
	}
	m, ok := s.MessageByID("slack:T/channel/C0B4", "mcp-test1")
	if !ok {
		t.Fatal("the row did not land")
	}
	got := m.PlatformID
	if got != ts {
		t.Fatalf("platform_id = %q, want %q — the reply-to-bot lookup would miss the row", got, ts)
	}
}
