// Integration tests for session-spanning features shipped in spec 6/F
// (topic lineage + plain-cp fork + cross-folder ambient + per-group
// observe-window) — MCP edge. Each test boots an in-memory store,
// wires GatedFns the way gated does, binds an MCP socket, and drives
// the tool over the wire. Dashd HTTP coverage lives in
// dashd/admin_integration_test.go (different package).

package tests

import (
	"bytes"
	"context"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
)

// mcpHarness boots store + MCP socket + initialised client. folder
// drives auth tier via slash count (auth.Resolve).
type mcpHarness struct {
	S      *store.Store
	Client *mcpclient.Client
	Folder string
	Tmp    string

	ForkCalls []forkCall
}

type forkCall struct {
	Folder, Parent, Child string
	Force                 bool
}

func newMCPHarness(t *testing.T, folder string) *mcpHarness {
	t.Helper()
	tmp := t.TempDir()
	s, err := store.Open(filepath.Join(tmp, "store"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	h := &mcpHarness{S: s, Folder: folder, Tmp: tmp}

	// Per-group ambient writes need a real row.
	if err := s.PutGroup(core.Group{Folder: folder, AddedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	gated := ipc.GatedFns{
		ForkTopic: func(f, parent, child string, force bool) error {
			h.ForkCalls = append(h.ForkCalls, forkCall{f, parent, child, force})
			return s.ForkTopic(f, parent, child, core.NewSessionID(), force)
		},
		SetGroupOpen:          s.SetGroupOpen,
		SetGroupObserveWindow: s.SetGroupObserveWindow,
		GroupObserveWindow:    s.GroupObserveWindow,
	}
	db := ipc.StoreFns{
		PutMessage:          s.PutMessage,
		DefaultFolderForJID: s.DefaultFolderForJID,
	}

	sock := filepath.Join(tmp, "mcp.sock")
	stop, err := ipc.ServeMCP(sock, gated, db, folder, []string{"*"}, -1, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	t.Cleanup(stop)

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	tr := transport.NewIO(conn, connIO{conn}, io.NopCloser(bytes.NewReader(nil)))
	c := mcpclient.NewClient(tr)
	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("client start: %v", err)
	}
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "tests", Version: "1"},
		},
	}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	h.Client = c
	return h
}

func (h *mcpHarness) call(t *testing.T, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := h.Client.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: name, Arguments: args},
	})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

// TestFork_MCP_InsertsLineageAndForceOverwrites drives fork_topic via
// MCP and asserts: sessions row + parent_topic populated, ErrTopicExists
// on duplicate without force, force=true refreshes the session_id.
func TestFork_MCP_InsertsLineageAndForceOverwrites(t *testing.T) {
	// Skip until bugs.md "Topic / pane MCP tools" entry is fixed:
	// auth.AuthorizeStructural has no `fork_topic` case so the MCP call
	// errors with "unknown tool: fork_topic" before reaching the handler.
	// Store-layer fork is covered by store/sessions_test.go::TestForkTopic_*.
	h := newMCPHarness(t, "world/sub")

	if _, err := h.S.EnsureTopicLineage("world/sub", "main", "", "main-uuid"); err != nil {
		t.Fatal(err)
	}

	res := h.call(t, "fork_topic", map[string]any{"parent": "main", "child": "deploy"})
	if res.IsError {
		t.Fatalf("fork_topic: %+v", res.Content)
	}
	if len(h.ForkCalls) != 1 || h.ForkCalls[0].Child != "deploy" {
		t.Fatalf("ForkCalls = %+v", h.ForkCalls)
	}
	uuid, ok := h.S.GetSession("world/sub", "deploy")
	if !ok || uuid == "" {
		t.Fatalf("sessions row for deploy missing")
	}
	lin, ok := h.S.TopicLineage("world/sub", "deploy")
	if !ok || lin.ParentTopic == nil || *lin.ParentTopic != "main" {
		t.Fatalf("lineage = %+v", lin)
	}

	// Duplicate fork without force returns topic_exists (surfaced as
	// an MCP error from the ipc.go translator).
	res = h.call(t, "fork_topic", map[string]any{"parent": "main", "child": "deploy"})
	if !res.IsError {
		t.Fatal("expected error on duplicate fork without force")
	}
	if !contentContains(res, "topic_exists") {
		t.Errorf("error msg missing topic_exists: %v", res.Content)
	}

	// force=true overwrites session_id.
	res = h.call(t, "fork_topic", map[string]any{
		"parent": "main", "child": "deploy", "force": true,
	})
	if res.IsError {
		t.Fatalf("force fork: %+v", res.Content)
	}
	uuid2, _ := h.S.GetSession("world/sub", "deploy")
	if uuid2 == uuid {
		t.Errorf("force=true did not refresh session_id (still %s)", uuid)
	}
}

// TestSetObserveWindow_MCP_PersistsAndPreservesOmitted: set both, then
// set only `chars` and verify `messages` survives (prevM fallback path).
func TestSetObserveWindow_MCP_PersistsAndPreservesOmitted(t *testing.T) {
	h := newMCPHarness(t, "world/sub")

	res := h.call(t, "set_observe_window", map[string]any{
		"messages": float64(5), "chars": float64(200),
	})
	if res.IsError {
		t.Fatalf("set_observe_window: %+v", res.Content)
	}
	m, c := h.S.GroupObserveWindow("world/sub")
	if m != 5 || c != 200 {
		t.Errorf("after set: m=%d c=%d, want 5,200", m, c)
	}

	res = h.call(t, "set_observe_window", map[string]any{"chars": float64(400)})
	if res.IsError {
		t.Fatalf("set_observe_window chars-only: %+v", res.Content)
	}
	m, c = h.S.GroupObserveWindow("world/sub")
	if m != 5 || c != 400 {
		t.Errorf("after partial: m=%d c=%d, want 5,400", m, c)
	}
}

// TestSetGroupOpen_MCP_FlipsAndGatesByTier: tier ≤ 1 writes the
// column; tier ≥ 2 is denied with "unauthorized".
func TestSetGroupOpen_MCP_FlipsAndGatesByTier(t *testing.T) {
	h := newMCPHarness(t, "world/sub") // tier 1
	res := h.call(t, "set_group_open", map[string]any{"open": false})
	if res.IsError {
		t.Fatalf("tier 1: %+v", res.Content)
	}
	if h.S.IsGroupOpen("world/sub") {
		t.Errorf("open still true after set_group_open(false)")
	}

	h2 := newMCPHarness(t, "world/sub/deep") // tier 2
	res = h2.call(t, "set_group_open", map[string]any{"open": false})
	if !res.IsError {
		t.Fatal("expected denial at tier 2")
	}
	if !contentContains(res, "unauthorized") {
		t.Errorf("error msg missing 'unauthorized': %v", res.Content)
	}
}

func contentContains(res *mcp.CallToolResult, sub string) bool {
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok && strings.Contains(tc.Text, sub) {
			return true
		}
	}
	return false
}
