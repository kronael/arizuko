package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/kronael/arizuko/core"
)

func newSlinkMCPServer(t *testing.T) (*server, *mockRouter, *httptest.Server, core.Group) {
	t.Helper()
	s, mr, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	return s, mr, srv, g
}

func slinkMCPDial(t *testing.T, url string) *mcpclient.Client {
	t.Helper()
	c, err := mcpclient.NewStreamableHttpClient(url, []transport.StreamableHTTPCOption{}...)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name: "slink-mcp-test", Version: "1.0",
			},
		},
	}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return c
}

// tools/list returns exactly 3 tools and no more.
func TestSlinkMCP_ToolSurface(t *testing.T) {
	_, _, srv, g := newSlinkMCPServer(t)
	c := slinkMCPDial(t, srv.URL+"/slink/"+g.SlinkToken+"/mcp")

	ctx := context.Background()
	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) != 3 {
		names := make([]string, len(res.Tools))
		for i, tool := range res.Tools {
			names[i] = tool.Name
		}
		t.Fatalf("tools = %v, want exactly 3 (send_message, steer, get_round)", names)
	}
	want := map[string]bool{"send_message": false, "steer": false, "get_round": false}
	for _, tool := range res.Tools {
		if _, ok := want[tool.Name]; !ok {
			t.Errorf("unexpected tool %q", tool.Name)
		}
		want[tool.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing tool %q", name)
		}
	}
}

// send_message: stores a row, returns {turn_id, topic, folder}, hits router.
func TestSlinkMCP_SendMessage(t *testing.T) {
	s, mr, srv, g := newSlinkMCPServer(t)
	c := slinkMCPDial(t, srv.URL+"/slink/"+g.SlinkToken+"/mcp")

	ctx := context.Background()
	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "send_message",
			Arguments: map[string]any{
				"content": "hello from a peer agent",
				"topic":   "round-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", toolText(t, res))
	}
	body := toolText(t, res)
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", body, err)
	}
	if got["topic"] != "round-1" {
		t.Errorf("topic = %v", got["topic"])
	}
	if got["folder"] != g.Folder {
		t.Errorf("folder = %v", got["folder"])
	}
	turnID, _ := got["turn_id"].(string)
	if turnID == "" {
		t.Errorf("missing turn_id")
	}

	msgs, err := s.st.MessagesByTopic(g.Folder, "round-1", time.Now().Add(time.Second), 10)
	if err != nil {
		t.Fatalf("MessagesByTopic: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	if msgs[0].Content != "hello from a peer agent" {
		t.Errorf("content = %q", msgs[0].Content)
	}
	if !strings.HasPrefix(msgs[0].Sender, "anon:") {
		t.Errorf("sender = %q, want anon prefix", msgs[0].Sender)
	}
	if sent := mr.sent(); len(sent) != 1 || sent[0].Content != "hello from a peer agent" {
		t.Errorf("router did not receive message: %+v", sent)
	}
}

// steer: extends an existing round on the same topic.
func TestSlinkMCP_Steer(t *testing.T) {
	s, _, srv, g := newSlinkMCPServer(t)
	c := slinkMCPDial(t, srv.URL+"/slink/"+g.SlinkToken+"/mcp")

	ctx := context.Background()
	if _, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "send_message",
			Arguments: map[string]any{
				"content": "first turn",
				"topic":   "round-2",
			},
		},
	}); err != nil {
		t.Fatalf("send_message: %v", err)
	}

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "steer",
			Arguments: map[string]any{
				"turn_id": "round-2",
				"content": "actually wait",
			},
		},
	})
	if err != nil {
		t.Fatalf("steer: %v", err)
	}
	if res.IsError {
		t.Fatalf("steer error: %s", toolText(t, res))
	}

	msgs, err := s.st.MessagesByTopic(g.Folder, "round-2", time.Now().Add(time.Second), 10)
	if err != nil {
		t.Fatalf("MessagesByTopic: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
}

// get_round (no wait): returns existing frames synchronously.
func TestSlinkMCP_GetRound_NoWait(t *testing.T) {
	s, _, srv, g := newSlinkMCPServer(t)
	c := slinkMCPDial(t, srv.URL+"/slink/"+g.SlinkToken+"/mcp")

	// Seed one user + one bot frame on topic "round-3".
	if _, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "send_message",
			Arguments: map[string]any{
				"content": "ping",
				"topic":   "round-3",
			},
		},
	}); err != nil {
		t.Fatalf("send_message: %v", err)
	}
	if err := s.st.PutMessage(core.Message{
		ID:        "bot-r3",
		ChatJID:   "web:" + g.Folder,
		Sender:    "assistant",
		Content:   "pong",
		Timestamp: time.Now(),
		BotMsg:    true,
		Topic:     "round-3",
	}); err != nil {
		t.Fatalf("PutMessage bot: %v", err)
	}

	res, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "get_round",
			Arguments: map[string]any{"turn_id": "round-3"},
		},
	})
	if err != nil {
		t.Fatalf("get_round: %v", err)
	}
	if res.IsError {
		t.Fatalf("get_round error: %s", toolText(t, res))
	}
	body := toolText(t, res)
	var got struct {
		Frames []map[string]any `json:"frames"`
		Done   bool             `json:"done"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", body, err)
	}
	if !got.Done {
		t.Errorf("done = false, want true (assistant frame present)")
	}
	if len(got.Frames) != 2 {
		t.Fatalf("frames = %d, want 2; body=%s", len(got.Frames), body)
	}
}

// get_round (wait=true): subscribes and returns when an assistant frame
// publishes after the call.
func TestSlinkMCP_GetRound_Wait(t *testing.T) {
	s, _, srv, g := newSlinkMCPServer(t)
	c := slinkMCPDial(t, srv.URL+"/slink/"+g.SlinkToken+"/mcp")

	if _, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "send_message",
			Arguments: map[string]any{
				"content": "are you there?",
				"topic":   "round-4",
			},
		},
	}); err != nil {
		t.Fatalf("send_message: %v", err)
	}

	go func() {
		time.Sleep(80 * time.Millisecond)
		s.hub.publish(g.Folder, "round-4", "message",
			`{"id":"bot-r4","role":"assistant","content":"yes","created_at":"2026-05-01T00:00:00Z"}`)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "get_round",
			Arguments: map[string]any{
				"turn_id": "round-4",
				"wait":    true,
			},
		},
	})
	if err != nil {
		t.Fatalf("get_round wait: %v", err)
	}
	if res.IsError {
		t.Fatalf("get_round error: %s", toolText(t, res))
	}
	body := toolText(t, res)
	var got struct {
		Frames []map[string]any `json:"frames"`
		Done   bool             `json:"done"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", body, err)
	}
	if !got.Done {
		t.Errorf("done = false, want true after assistant publish; body=%s", body)
	}
	sawAssistant := false
	for _, f := range got.Frames {
		if r, _ := f["role"].(string); r == "assistant" {
			sawAssistant = true
		}
	}
	if !sawAssistant {
		t.Errorf("no assistant frame in result: %s", body)
	}
}

// Bad token → 404 (matches handleSlinkPost behaviour).
func TestSlinkMCP_BadToken(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/slink/nope/mcp", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
