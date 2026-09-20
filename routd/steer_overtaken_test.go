package routd

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// A slash command is consumed by steer and nowhere else. It used to be read off
// the batch's newest row, so the agent's own reply landing between the command
// and the next poll made steer inspect the reply, refuse it (handleCommand
// refuses a bot row), and let the command reach the turn as plain text. On krons
// that swallowed anteval's `/new` twice; the same hole loses a user's /stop or
// /root in any chat that answers before the next poll (BUGS L56).
func TestNewestUserMsg_SkipsBotRowsAfterACommand(t *testing.T) {
	base := time.Date(2026, 8, 20, 11, 58, 42, 0, time.UTC)
	msgs := []core.Message{
		{ID: "cmd", Content: "/new", Timestamp: base},
		{ID: "reply", Content: "Page is published", BotMsg: true, Timestamp: base.Add(6 * time.Minute)},
	}

	i := newestUserIdx(msgs)
	if i < 0 {
		t.Fatal("no user message found in a batch that contains one")
	}
	if msgs[i].ID != "cmd" {
		t.Errorf("steered %q; the bot reply overtook the command again", msgs[i].ID)
	}
}

// The newest USER row wins when several are present — a later command supersedes
// an earlier one, exactly as it did when the batch had no bot rows in it.
func TestNewestUserMsg_TakesTheLatestUserRow(t *testing.T) {
	base := time.Date(2026, 8, 20, 11, 0, 0, 0, time.UTC)
	msgs := []core.Message{
		{ID: "old", Content: "/stop", Timestamp: base},
		{ID: "bot", BotMsg: true, Timestamp: base.Add(time.Minute)},
		{ID: "new", Content: "/new", Timestamp: base.Add(2 * time.Minute)},
	}

	i := newestUserIdx(msgs)
	if i < 0 || msgs[i].ID != "new" {
		t.Errorf("got index %d, want the latest user row", i)
	}
}

// An all-bot batch has nothing to steer on. Returning the bot row would hand
// handleStickyCommand a message it never checks for BotMsg.
func TestNewestUserMsg_AllBotBatchSteersNothing(t *testing.T) {
	msgs := []core.Message{
		{ID: "b1", BotMsg: true, Timestamp: time.Now()},
		{ID: "b2", BotMsg: true, Timestamp: time.Now().Add(time.Second)},
	}
	if i := newestUserIdx(msgs); i >= 0 {
		t.Errorf("steered bot row %q; an all-bot batch must steer nothing", msgs[i].ID)
	}
}
