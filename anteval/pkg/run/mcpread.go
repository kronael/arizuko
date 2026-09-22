package run

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/kronael/arizuko/anteval/pkg/check"
	"github.com/kronael/arizuko/groupfolder"
)

// mcpHistory reads one topic through webd's MCP face — the same rows RestMessages
// reads over REST, which is what makes a parity check mean anything.
//
// The tool is `get_history`, and it takes FOLDER and TOPIC, not a chat jid. webd
// registers it in `buildWebMCP`, the one builder both doors call, so it answers
// on POST /mcp and on POST /chat/{token}/mcp alike; the grant set comes from the
// route-token row, never from a request parameter.
//
// The folder is read out of the chat jid with the package that owns that
// grammar. A bare `web:<folder>` is 1:1 with its folder; a SUFFIXED jid is not,
// and JidFolder answers "" for anything it cannot read, which surfaces here as a
// loud error rather than a read of the wrong folder.
func (t *HTTPTarget) mcpHistory(chat, topic string) ([]check.Msg, error) {
	if t.MCPURL == "" {
		return nil, fmt.Errorf("surface not configured")
	}
	folder := groupfolder.JidFolder(chat)
	if folder == "" {
		return nil, fmt.Errorf("mcp read: %q names no folder; get_history takes a folder, "+
			"and a suffixed jid cannot be read back into one", chat)
	}
	if topic == "" {
		return nil, fmt.Errorf("mcp read: get_history requires a topic and this case carries none")
	}

	opts := []transport.StreamableHTTPCOption{
		transport.WithHTTPBasicClient(t.client()),
	}
	if t.Token != "" {
		opts = append(opts, transport.WithHTTPHeaders(map[string]string{
			"Authorization": "Bearer " + t.Token,
		}))
	}
	cl, err := client.NewStreamableHttpClient(t.MCPURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("mcp dial %s: %w", t.MCPURL, err)
	}
	defer cl.Close() //nolint:errcheck // the read is done; a close error is unactionable

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := cl.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
		return nil, fmt.Errorf("mcp initialize %s: %w", t.MCPURL, err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = "get_history"
	req.Params.Arguments = map[string]any{"folder": folder, "topic": topic, "limit": 100}
	res, err := cl.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("mcp get_history: %w", err)
	}
	return historyMsgs(res)
}

// historyMsgs opens the CallToolResult. The payload is a JSON STRING in the
// first text content — webd answers NewToolResultText — so it is decoded twice,
// and `role` is the bot flag REST spells `is_bot_message`.
func historyMsgs(res *mcp.CallToolResult) ([]check.Msg, error) {
	if res == nil || len(res.Content) == 0 {
		return nil, fmt.Errorf("mcp get_history: empty result")
	}
	text, ok := mcp.AsTextContent(res.Content[0])
	if !ok {
		return nil, fmt.Errorf("mcp get_history: first content is not text")
	}
	if res.IsError {
		return nil, fmt.Errorf("mcp get_history: %s", text.Text)
	}
	var out struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		return nil, fmt.Errorf("mcp get_history: %w", err)
	}
	msgs := make([]check.Msg, len(out.Messages))
	for i, m := range out.Messages {
		msgs[i] = check.Msg{FromBot: m.Role == "assistant", Text: m.Content}
	}
	return msgs, nil
}
