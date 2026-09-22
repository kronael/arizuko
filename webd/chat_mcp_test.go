package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/kronael/arizuko/store"
)

// POST /chat/<token>/mcp is the SAME surface as POST /mcp, entered by a different
// door (BUGS L139). A route token authenticates an anonymous principal whose
// destination set has exactly one element, so the tests below pin two things: the
// vocabulary is identical, and the one element is the only folder reachable.

// seedTokenRow mints a route token with an explicit JID, delivery target and link
// context — the three facts a pinned principal is built from.
func seedTokenRow(t *testing.T, st *store.Store, jid, owner, target, linkContext string) string {
	t.Helper()
	raw := store.GenRouteToken()
	rt := store.RouteToken{
		JID: jid, OwnerFolder: owner, TargetFolder: target,
		Context: linkContext, CreatedAt: time.Now(),
	}
	if err := st.InsertRouteToken(raw, rt); err != nil {
		t.Fatalf("InsertRouteToken: %v", err)
	}
	return raw
}

func newChatTokenMCPServer(t *testing.T) (*server, *mockRouter, *httptest.Server, *store.Store) {
	t.Helper()
	s, mr, st := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	return s, mr, srv, st
}

func chatTokenMCPDial(t *testing.T, url string) *mcpclient.Client {
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
			ClientInfo:      mcp.Implementation{Name: "chat-token-mcp-test", Version: "1.0"},
		},
	}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return c
}

func callTool(t *testing.T, c *mcpclient.Client, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: name, Arguments: args},
	})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

// The token door speaks the identity door's vocabulary and nothing else. The old
// send_message / get_round / get_round_status names are gone, and a one-element
// grant set is not an operator, so no routes.* tool appears either.
func TestChatTokenMCP_SameVocabularyAsIdentitySurface(t *testing.T) {
	_, _, srv, st := newChatTokenMCPServer(t)
	seedGroup(t, st, "main", "Main")
	tok := seedChatToken(t, st, "main")
	c := chatTokenMCPDial(t, srv.URL+"/chat/"+tok+"/mcp")

	res, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := make([]string, len(res.Tools))
	for i, tool := range res.Tools {
		got[i] = tool.Name
	}
	sort.Strings(got)
	want := []string{"get_history", "list_groups", "send"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v, want exactly %v", got, want)
	}
}

// list_groups returning a single row IS the pinned JID, said in the language the
// surface already speaks: three groups exist, the token sees one.
func TestChatTokenMCP_ListGroupsShowsOnlyItsOwn(t *testing.T) {
	_, _, srv, st := newChatTokenMCPServer(t)
	seedGroup(t, st, "main", "Main")
	seedGroup(t, st, "secret", "Secret")
	seedGroup(t, st, "main/sub", "Sub")
	tok := seedChatToken(t, st, "main")
	c := chatTokenMCPDial(t, srv.URL+"/chat/"+tok+"/mcp")

	var out struct {
		Groups []struct {
			Folder string `json:"folder"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(toolText(t, callTool(t, c, "list_groups", nil))), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Groups) != 1 || out.Groups[0].Folder != "main" {
		t.Fatalf("groups = %+v, want exactly [main]", out.Groups)
	}
}

// SECURITY: a route-token caller must not reach any folder but its own. The grant
// set is built from the ROW, so a client-supplied folder cannot widen it.
func TestChatTokenMCP_SendCrossFolderRefused(t *testing.T) {
	_, mr, srv, st := newChatTokenMCPServer(t)
	seedGroup(t, st, "main", "Main")
	seedGroup(t, st, "secret", "Secret")
	tok := seedChatToken(t, st, "main")
	c := chatTokenMCPDial(t, srv.URL+"/chat/"+tok+"/mcp")

	res := callTool(t, c, "send", map[string]any{
		"folder": "secret", "content": "leak", "topic": "t1",
	})
	if !res.IsError {
		t.Fatalf("cross-folder send allowed: %s", toolText(t, res))
	}
	if !strings.Contains(toolText(t, res), "forbidden") {
		t.Errorf("want 'forbidden', got: %s", toolText(t, res))
	}
	if n := len(mr.sent()); n != 0 {
		t.Fatalf("router received %d messages, want 0 — refusal must not deliver", n)
	}
}

// SECURITY: the same containment on the read side.
func TestChatTokenMCP_GetHistoryCrossFolderRefused(t *testing.T) {
	_, _, srv, st := newChatTokenMCPServer(t)
	seedGroup(t, st, "main", "Main")
	seedGroup(t, st, "secret", "Secret")
	tok := seedChatToken(t, st, "main")
	c := chatTokenMCPDial(t, srv.URL+"/chat/"+tok+"/mcp")

	res := callTool(t, c, "get_history", map[string]any{"folder": "secret", "topic": "t1"})
	if !res.IsError {
		t.Fatalf("cross-folder get_history allowed: %s", toolText(t, res))
	}
	if !strings.Contains(toolText(t, res), "forbidden") {
		t.Errorf("want 'forbidden', got: %s", toolText(t, res))
	}
}

// SECURITY: auth.MatchGroups is segment-wise, so the one-element grant `main`
// admits `main` alone. A token never descends into a subfolder — that subfolder is
// a different chat with its own JID, which this token's recorded JID cannot address
// (5/33-paths-roles decision 8, the same rule requireFolder answers with).
func TestChatTokenMCP_SendSubfolderRefused(t *testing.T) {
	_, mr, srv, st := newChatTokenMCPServer(t)
	seedGroup(t, st, "main", "Main")
	seedGroup(t, st, "main/sub", "Sub")
	tok := seedChatToken(t, st, "main")
	c := chatTokenMCPDial(t, srv.URL+"/chat/"+tok+"/mcp")

	res := callTool(t, c, "send", map[string]any{
		"folder": "main/sub", "content": "descend", "topic": "t1",
	})
	if !res.IsError {
		t.Fatalf("subfolder send allowed: %s", toolText(t, res))
	}
	if n := len(mr.sent()); n != 0 {
		t.Fatalf("router received %d messages, want 0", n)
	}
}

// send delivers to its own folder and reports the unified {id, topic, folder}.
func TestChatTokenMCP_SendOwnFolder(t *testing.T) {
	_, mr, srv, st := newChatTokenMCPServer(t)
	seedGroup(t, st, "main", "Main")
	tok := seedChatToken(t, st, "main")
	c := chatTokenMCPDial(t, srv.URL+"/chat/"+tok+"/mcp")

	res := callTool(t, c, "send", map[string]any{
		"folder": "main", "content": "hello from a peer agent", "topic": "t1",
	})
	if res.IsError {
		t.Fatalf("send error: %s", toolText(t, res))
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(toolText(t, res)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["id"] == "" || got["topic"] != "t1" || got["folder"] != "main" {
		t.Fatalf("send result = %v", got)
	}
	sent := mr.sent()
	if len(sent) != 1 {
		t.Fatalf("router got %d messages, want 1", len(sent))
	}
	if sent[0].Content != "hello from a peer agent" {
		t.Errorf("content = %q", sent[0].Content)
	}
	if !strings.HasPrefix(sent[0].Sender, "anon:") {
		t.Errorf("sender = %q, want an anon: principal", sent[0].Sender)
	}
}

// The destination JID is READ off the token row, never rebuilt from the folder:
// a suffixed token delivers to web:main/sfx, which "web:"+folder never produces.
func TestChatTokenMCP_SendAddressesRecordedJID(t *testing.T) {
	_, mr, srv, st := newChatTokenMCPServer(t)
	seedGroup(t, st, "main", "Main")
	tok := seedTokenRow(t, st, "web:main/sfx", "main", "main", "")
	c := chatTokenMCPDial(t, srv.URL+"/chat/"+tok+"/mcp")

	if res := callTool(t, c, "send", map[string]any{
		"folder": "main", "content": "pinned", "topic": "t1",
	}); res.IsError {
		t.Fatalf("send error: %s", toolText(t, res))
	}
	sent := mr.sent()
	if len(sent) != 1 {
		t.Fatalf("router got %d messages, want 1", len(sent))
	}
	if sent[0].ChatJID != "web:main/sfx" {
		t.Fatalf("ChatJID = %q, want the recorded web:main/sfx", sent[0].ChatJID)
	}
}

// The per-link context (spec 5/W) keeps its meaning on the collapsed surface — it
// rides `send` exactly as it rides POST /chat/<token>.
func TestChatTokenMCP_SendCarriesLinkContext(t *testing.T) {
	_, mr, srv, st := newChatTokenMCPServer(t)
	seedGroup(t, st, "main", "Main")
	tok := seedTokenRow(t, st, "web:main", "main", "main",
		"bug reports from the acme site; triage, don't chat")
	c := chatTokenMCPDial(t, srv.URL+"/chat/"+tok+"/mcp")

	if res := callTool(t, c, "send", map[string]any{
		"folder": "main", "content": "it crashed", "topic": "t1",
	}); res.IsError {
		t.Fatalf("send error: %s", toolText(t, res))
	}
	sent := mr.sent()
	if len(sent) != 1 {
		t.Fatalf("router got %d messages, want 1", len(sent))
	}
	if sent[0].LinkContext != "bug reports from the acme site; triage, don't chat" {
		t.Fatalf("LinkContext = %q", sent[0].LinkContext)
	}
}

// get_history reads back the chat send wrote to — on a suffixed token that is the
// recorded JID, not "web:"+folder, so the two tools agree on one destination.
func TestChatTokenMCP_GetHistoryReadsPinnedChat(t *testing.T) {
	_, _, srv, st := newChatTokenMCPServer(t)
	seedGroup(t, st, "main", "Main")
	tok := seedTokenRow(t, st, "web:main/sfx", "main", "main", "")
	c := chatTokenMCPDial(t, srv.URL+"/chat/"+tok+"/mcp")

	if res := callTool(t, c, "send", map[string]any{
		"folder": "main", "content": "round one", "topic": "ring",
	}); res.IsError {
		t.Fatalf("send error: %s", toolText(t, res))
	}
	body := toolText(t, callTool(t, c, "get_history",
		map[string]any{"folder": "main", "topic": "ring", "limit": 10}))
	if !strings.Contains(body, "round one") {
		t.Fatalf("get_history missed the message it just sent: %s", body)
	}
}

func TestChatTokenMCP_BadToken(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/chat/nope/mcp", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
