package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

// newIntegServer wires webd against a routd.db-schema store — the DB webd opens
// in production (store.OpenRoutd in main.go) — and a
// mock router. webd talks SSE + POST, not websockets — the spec's "websocket
// echo" shape is mapped onto the real slink transport.
type integInst struct {
	*testutils.Inst
	srv *server
	mr  *mockRouter
	st  *store.Store
}

func newIntegServer(t *testing.T) *integInst {
	t.Helper()
	inst := testutils.NewRoutdInstance(t)
	st := inst.Store

	mr := newMockRouterWithStore(st)
	t.Cleanup(mr.close)

	rc := chanlib.NewRouterClient(mr.srv.URL)
	rc.SetToken("test-token")

	// routerURL mirrors production, where compose always pins ROUTER_URL —
	// see newTestServer.
	cfg := config{assistantName: "assistant", routerURL: mr.srv.URL}
	return &integInst{Inst: inst, srv: newServer(cfg, st, newHub(), rc, nil, nil), mr: mr, st: st}
}

// TestE2ESlinkWebsocketEcho — release-gate E2E via the webd route-token
// surface (make test-e2e): mint a chat token, POST through it, and verify
// the full round-trip — row lands in `messages` + router gets it →
// gated-side /send callback publishes an assistant frame → the same client
// (subscribed via SSE) receives it. Adapted to webd's actual SSE transport
// (the spec's "websocket echo" shape mapped onto slink's real transport).
func TestE2ESlinkWebsocketEcho(t *testing.T) {
	ii := newIntegServer(t)
	_ = seedGroup(t, ii.st, "echo", "Echo")
	tok := seedChatToken(t, ii.st, "echo")

	srv := httptest.NewServer(ii.srv.handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// POST with Accept: text/event-stream holds the stream open.
	body := strings.NewReader("content=hello&topic=echo1")
	req, _ := http.NewRequestWithContext(ctx, "POST",
		srv.URL+"/chat/"+tok, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}

	// Inbound message landed in the DB.
	testutils.WaitFor(t, `the inbound "hello" in web:echo`, time.Second, func() bool {
		return inboundID(t, ii.Store, "web:echo", "hello") != ""
	})

	// Router received the inbound.
	if got := ii.mr.sent(); len(got) != 1 || got[0].Content != "hello" {
		t.Fatalf("router did not receive inbound: %+v", got)
	}

	// Simulate gated sending an assistant message back via webd's /send
	// callback. reply_to pins the assistant onto the inbound's topic so the
	// SSE subscriber on that topic receives it.
	inboundID := inboundID(t, ii.Store, "web:echo", "hello")
	if inboundID == "" {
		t.Fatal("find inbound id: the row webd appended is not readable")
	}
	go func() {
		time.Sleep(80 * time.Millisecond)
		payload, _ := json.Marshal(map[string]string{
			"chat_jid": "web:echo", "content": "pong", "reply_to": inboundID,
		})
		cb, _ := http.NewRequest("POST", srv.URL+"/send",
			strings.NewReader(string(payload)))
		cb.Header.Set("Content-Type", "application/json")
		// chanlib.Auth gate: empty secret + no header → accepted in tests.
		http.DefaultClient.Do(cb)
	}()

	// Read stream for up to 2s, assert user bubble and assistant pong.
	var gotUser, gotAssistant bool
	r := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && (!gotUser || !gotAssistant) {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			break
		}
		if strings.Contains(line, `"role":"user"`) && strings.Contains(line, "hello") {
			gotUser = true
		}
		if strings.Contains(line, `"role":"assistant"`) && strings.Contains(line, "pong") {
			gotAssistant = true
		}
	}
	if !gotUser {
		t.Error("SSE stream missing user bubble")
	}
	if !gotAssistant {
		t.Error("SSE stream missing assistant echo from /send callback")
	}
	// Note: assistant message persistence is NOT verified here — routd/gated is
	// the sole appender (webd's /send only publishes to hub). The mock doesn't
	// simulate gated writing to the DB.
}

// TestE2ESlinkMCPBridge — release-gate E2E via the webd route-token surface
// (make test-e2e): exercises the MCP-over-HTTP endpoint with proxyd-signed
// user headers. Confirms the send tool routes through webd → store + router,
// mirroring the websocket/MCP bridge intent.
func TestE2ESlinkMCPBridge(t *testing.T) {
	ii := newIntegServer(t)
	seedGroup(t, ii.st, "alpha", "Alpha")

	srv := httptest.NewServer(ii.srv.handler())
	defer srv.Close()

	groups, _ := json.Marshal([]string{"alpha"})
	c, err := mcpclient.NewStreamableHttpClient(srv.URL+"/mcp",
		transport.WithHTTPHeaders(signUserHeaders(map[string]string{
			"X-User-Sub":    "user:integ",
			"X-User-Name":   "Integ",
			"X-User-Groups": string(groups),
		})))
	if err != nil {
		t.Fatalf("new mcp client: %v", err)
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
				Name: "integ-test", Version: "1.0",
			},
		},
	}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "send",
			Arguments: map[string]any{
				"folder": "alpha", "content": "bridge-ping", "topic": "tb",
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}

	testutils.AssertMessage(t, ii.Store, "web:alpha", "bridge-ping")
	if got := ii.mr.sent(); len(got) != 1 || got[0].Content != "bridge-ping" {
		t.Fatalf("router did not receive MCP-bridged message: %+v", got)
	}

	// Round-trip: result text must carry message id + folder, confirming the
	// tool return actually propagated back through the MCP client.
	if len(res.Content) == 0 {
		t.Fatal("empty tool result content")
	}
	text := ""
	if tc, ok := res.Content[0].(mcp.TextContent); ok {
		text = tc.Text
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("tool result not JSON: %v — %q", err, text)
	}
	if payload["folder"] != "alpha" {
		t.Errorf("tool result folder = %v, want alpha", payload["folder"])
	}
	if _, ok := payload["id"].(string); !ok {
		t.Errorf("tool result missing id: %v", payload)
	}
}

// inboundID answers the id of a chat's message carrying content, or "" when the
// chat holds none. `messages` is an objstore entity, so the lookup is a read of
// the chat's partition rather than a query naming columns.
func inboundID(t *testing.T, st *store.Store, chatJID, content string) string {
	t.Helper()
	msgs, err := st.MessagesSince(chatJID, time.Time{})
	if err != nil {
		return ""
	}
	for _, m := range msgs {
		if m.Content == content {
			return m.ID
		}
	}
	return ""
}
