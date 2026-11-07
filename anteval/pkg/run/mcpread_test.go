package run

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
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

func TestMcpHistoryRequiresFolderAndTopic(t *testing.T) {
	target := &HTTPTarget{MCPURL: "http://127.0.0.1:1", Token: "t"}
	if _, err := target.mcpHistory("", "topic"); err == nil {
		t.Fatal("an empty folder was accepted")
	}
	if _, err := target.mcpHistory("web:acme", ""); err == nil {
		t.Fatal("an empty topic was accepted; get_history requires one")
	}
}

func TestMcpMessagesUsesConfiguredFolder(t *testing.T) {
	m := server.NewMCPServer("test", "1")
	var folder string
	m.AddTool(mcp.NewTool("get_history"), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		folder = req.GetString("folder", "")
		return mcp.NewToolResultText(`{"messages":[]}`), nil
	})
	s := httptest.NewServer(server.NewStreamableHTTPServer(m, server.WithStateLess(true)))
	defer s.Close()
	target := &HTTPTarget{MCPURL: s.URL, Folder: "eval/child"}
	if _, err := target.McpMessages("web:other/suffix", "topic"); err != nil {
		t.Fatal(err)
	}
	if folder != "eval/child" {
		t.Fatalf("folder = %q", folder)
	}
}
