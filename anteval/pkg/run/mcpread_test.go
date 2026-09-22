package run

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// webd answers NewToolResultText, so get_history's payload is a JSON STRING
// inside the first text content — decoded twice, not once. A reader that treats
// the envelope as the payload finds no messages and reports an empty history,
// which in a parity check reads as "MCP does not have it" rather than as a bug
// in the reader.
func TestHistoryMsgsOpensTheEnvelope(t *testing.T) {
	res := mcp.NewToolResultText(
		`{"messages":[{"role":"user","content":"ping"},{"role":"assistant","content":"pong"}],"count":2}`)

	msgs, err := historyMsgs(res)
	if err != nil {
		t.Fatalf("historyMsgs: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2: %+v", len(msgs), msgs)
	}
	// role is the bot flag REST spells is_bot_message; getting this backwards
	// makes a bot-only check pass on the human's own prompt.
	if msgs[0].FromBot {
		t.Errorf("role=user read as a bot message: %+v", msgs[0])
	}
	if !msgs[1].FromBot {
		t.Errorf("role=assistant read as a human message: %+v", msgs[1])
	}
	if msgs[1].Text != "pong" {
		t.Errorf("text = %q, want %q", msgs[1].Text, "pong")
	}
}

// A tool error carries its message in the same text content, so a reader that
// does not check IsError reports the error STRING as the history and a check
// that greps for a sentinel simply never finds it.
func TestHistoryMsgsRaisesAToolError(t *testing.T) {
	if _, err := historyMsgs(mcp.NewToolResultError("access_denied")); err == nil {
		t.Fatal("a tool error was read as history")
	}
}

// The MCP face reads by folder, and a suffixed jid cannot be read back into
// one (BUGS L139 is that bug in production). Answering the wrong folder's
// history would make a parity check pass against another group's messages.
func TestMcpHistoryRefusesAJidItCannotResolve(t *testing.T) {
	target := &HTTPTarget{MCPURL: "http://127.0.0.1:1", Token: "t"}
	if _, err := target.mcpHistory("web:acme/tok", "topic"); err == nil {
		t.Fatal("a suffixed jid was accepted; get_history would read the wrong folder")
	}
	if _, err := target.mcpHistory("web:acme", ""); err == nil {
		t.Fatal("an empty topic was accepted; get_history requires one")
	}
}
