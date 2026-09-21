package routd

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

// A command was steered only when it was the NEWEST non-bot row of its batch.
// Anything landing after it before the next poll — a second human message,
// another person in a group chat, a timed cron row — left the command in the
// trigger batch, and the agent received it as plain text. `/root` then ran
// unelevated, which reads as the platform ignoring the command (BUGS L336).
func TestSteerRowIdx_FindsACommandThatIsNotTheNewestRow(t *testing.T) {
	msgs := []core.Message{
		{ID: "m1", Content: "/root mint the token", Sender: "u1"},
		{ID: "m2", Content: "and also look at this", Sender: "u1"},
	}
	if got := steerRowIdx(msgs); got != 0 {
		t.Fatalf("steerRowIdx = %d, want 0: the /root row is not the newest, and steering the newest leaves it to reach the agent as text", got)
	}
}

// The OLDEST command, not the newest. The cursor advances to the steered row,
// so a newer pick would walk past an older command and lose it (BUGS L104 is
// the same invariant from the other side).
func TestSteerRowIdx_TakesTheOldestCommandOfTwo(t *testing.T) {
	msgs := []core.Message{
		{ID: "m1", Content: "hello", Sender: "u1"},
		{ID: "m2", Content: "/status", Sender: "u1"},
		{ID: "m3", Content: "/ping", Sender: "u1"},
	}
	if got := steerRowIdx(msgs); got != 1 {
		t.Fatalf("steerRowIdx = %d, want 1 (the /status row); a newer pick advances the cursor past it", got)
	}
}

// With no command in the batch the selection is unchanged — the newest non-bot
// row. A regression here would re-route every ordinary turn.
func TestSteerRowIdx_NoCommandKeepsTheNewestUserRow(t *testing.T) {
	msgs := []core.Message{
		{ID: "m1", Content: "first", Sender: "u1"},
		{ID: "m2", Content: "second", Sender: "u1"},
		{ID: "m3", Content: "bot reply", Sender: "folder", BotMsg: true},
	}
	if got := steerRowIdx(msgs); got != 1 {
		t.Fatalf("steerRowIdx = %d, want 1: no command, so the newest NON-BOT row", got)
	}
}

// A bot row and a timed-cron row are not commands even when their text looks
// like one — handleCommand refuses both, and the finder must agree or it would
// steer a row the handler then declines, consuming it for nothing.
func TestIsSteerCommand_RefusesBotAndCronRows(t *testing.T) {
	if IsSteerCommand(core.Message{Content: "/status", Sender: "folder", BotMsg: true}) {
		t.Error("a bot row counted as a command")
	}
	if IsSteerCommand(core.Message{Content: "/status", Sender: "timed-nightly"}) {
		t.Error("a timed cron row counted as a command")
	}
	if IsSteerCommand(core.Message{Content: "/notacommand", Sender: "u1"}) {
		t.Error("an unknown slash word counted as a command")
	}
	if !IsSteerCommand(core.Message{Content: "/root do it", Sender: "u1"}) {
		t.Error("/root did not count as a command")
	}
}
