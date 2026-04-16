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
)

func newMCPTestServer(t *testing.T) (*server, *httptest.Server) {
	t.Helper()
	s, _, st := newTestServer(t)
	// Two groups; caller decides which are in the grant list.
	seedGroup(t, st, "main", "Main")
	seedGroup(t, st, "secret", "Secret")
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	return s, srv
}

func mcpDial(t *testing.T, url string, headers map[string]string) *mcpclient.Client {
	t.Helper()
	opts := []transport.StreamableHTTPCOption{}
	if headers != nil {
		opts = append(opts, transport.WithHTTPHeaders(headers))
	}
	c, err := mcpclient.NewStreamableHttpClient(url, opts...)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	_, err = c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name: "webd-test", Version: "1.0",
			},
		},
	})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return c
}

// Unauthenticated POST /mcp → redirect (requireUser sends 303).
func TestMCP_RequiresAuth(t *testing.T) {
	_, srv := newMCPTestServer(t)

	cli := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	req, _ := http.NewRequest("POST", srv.URL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
}

// list_groups filters to folders the user's grants cover.
func TestMCP_ListGroups_FiltersByGrants(t *testing.T) {
	_, srv := newMCPTestServer(t)
	groups, _ := json.Marshal([]string{"main"})
	c := mcpDial(t, srv.URL+"/mcp", map[string]string{
		"X-User-Sub":    "user:1",
		"X-User-Name":   "Alice",
		"X-User-Groups": string(groups),
	})

	ctx := context.Background()
	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: "list_groups"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	body := toolText(t, res)
	if !strings.Contains(body, `"main"`) {
		t.Errorf("want main in result: %s", body)
	}
	if strings.Contains(body, `"secret"`) {
		t.Errorf("secret must be filtered out: %s", body)
	}
}

// send_message with a valid grant: message hits store + router with authed sub.
func TestMCP_SendMessage_AllowedFolder(t *testing.T) {
	s, srv := newMCPTestServer(t)
	groups, _ := json.Marshal([]string{"main"})
	c := mcpDial(t, srv.URL+"/mcp", map[string]string{
		"X-User-Sub":    "user:alice",
		"X-User-Name":   "Alice",
		"X-User-Groups": string(groups),
	})

	ctx := context.Background()
	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "send_message",
			Arguments: map[string]any{
				"folder": "main", "content": "hi bot", "topic": "t1",
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", toolText(t, res))
	}

	msgs, err := s.st.MessagesByTopic("main", "t1", time.Now().Add(time.Second), 10)
	if err != nil {
		t.Fatalf("MessagesByTopic: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	if msgs[0].Sender != "user:alice" {
		t.Errorf("sender = %q, want authed sub", msgs[0].Sender)
	}
	if msgs[0].Name != "Alice" {
		t.Errorf("name = %q", msgs[0].Name)
	}
}

// send_message outside the grant list: forbidden tool-result error.
func TestMCP_SendMessage_ForbiddenFolder(t *testing.T) {
	_, srv := newMCPTestServer(t)
	groups, _ := json.Marshal([]string{"main"})
	c := mcpDial(t, srv.URL+"/mcp", map[string]string{
		"X-User-Sub":    "user:alice",
		"X-User-Groups": string(groups),
	})

	ctx := context.Background()
	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "send_message",
			Arguments: map[string]any{
				"folder": "secret", "content": "leak", "topic": "t1",
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error result, got: %s", toolText(t, res))
	}
	if !strings.Contains(toolText(t, res), "forbidden") {
		t.Errorf("want 'forbidden' in error, got: %s", toolText(t, res))
	}
}

// get_history returns messages authored on the same topic.
func TestMCP_GetHistory(t *testing.T) {
	_, srv := newMCPTestServer(t)
	groups, _ := json.Marshal([]string{"main"})
	c := mcpDial(t, srv.URL+"/mcp", map[string]string{
		"X-User-Sub":    "user:alice",
		"X-User-Groups": string(groups),
	})

	ctx := context.Background()
	_, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "send_message",
			Arguments: map[string]any{
				"folder": "main", "content": "first", "topic": "ring",
			},
		},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "get_history",
			Arguments: map[string]any{
				"folder": "main", "topic": "ring", "limit": 10,
			},
		},
	})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	body := toolText(t, res)
	if !strings.Contains(body, `"first"`) {
		t.Errorf("history missing message: %s", body)
	}
}

// toolText extracts the first text content block.
func toolText(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	if len(r.Content) == 0 {
		return ""
	}
	if tc, ok := r.Content[0].(mcp.TextContent); ok {
		return tc.Text
	}
	b, _ := json.Marshal(r.Content[0])
	return string(b)
}
