// MCP tool-category coverage over the real ipc.ServeMCP socket. Each
// TestMCP_* boots a store + MCP server wired the way routd wires it, then
// drives one tool family over the wire and asserts both the MCP result and
// the resulting store state. The harness runs as folder "hq" (tier 0) so
// auth.AuthorizeStructural permits every action; per-tier denial is covered
// in topic_lineage_test.go.

package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
)

type socialCall struct {
	JID, Target, Extra string
}

type aclCall struct {
	Principal, Scope, Action, Effect string
}

// fullMCPHarness wires every GatedFns/StoreFns method the MCP tools under
// test need, capturing social/ACL calls for assertion.
type fullMCPHarness struct {
	S      *store.Store
	Client *mcpclient.Client
	Folder string

	LikeCalls   []socialCall
	DeleteCalls []socialCall
	EditCalls   []socialCall
	GrantCalls  []aclCall
	RevokeCalls []aclCall
}

func newFullMCPHarness(t *testing.T, folder string) *fullMCPHarness {
	t.Helper()
	tmp := t.TempDir()
	s, err := store.Open(filepath.Join(tmp, "store"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	h := &fullMCPHarness{S: s, Folder: folder}
	if err := s.PutGroup(core.Group{Folder: folder, AddedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	defaultFolder := func(jid string) string {
		if jid == "" {
			return ""
		}
		return folder
	}

	gated := ipc.GatedFns{
		Like: func(jid, target, reaction string) error {
			h.LikeCalls = append(h.LikeCalls, socialCall{jid, target, reaction})
			return nil
		},
		Delete: func(jid, target string) error {
			h.DeleteCalls = append(h.DeleteCalls, socialCall{jid, target, ""})
			return nil
		},
		Edit: func(jid, target, content string) error {
			h.EditCalls = append(h.EditCalls, socialCall{jid, target, content})
			return nil
		},
		CreateInvite: func(targetGlob, issuedBySub string, maxUses int, expiresAt *time.Time) (ipc.InviteInfo, error) {
			inv, rawToken, err := s.CreateInvite(targetGlob, issuedBySub, maxUses, expiresAt)
			if err != nil {
				return ipc.InviteInfo{}, err
			}
			info := inviteToInfo(inv)
			info.Token = rawToken
			return info, nil
		},
		ListInvites: func(issuedBy string) ([]ipc.InviteInfo, error) {
			invs, err := s.ListInvites(issuedBy)
			if err != nil {
				return nil, err
			}
			out := make([]ipc.InviteInfo, len(invs))
			for i := range invs {
				out[i] = inviteToInfo(&invs[i])
			}
			return out, nil
		},
		RevokeInvite:       s.RevokeInviteByRef,
		AddGroupWatcher:    s.AddGroupWatcher,
		RemoveGroupWatcher: s.RemoveGroupWatcher,
	}

	db := ipc.StoreFns{
		PutMessage:          s.PutMessage,
		DefaultFolderForJID: defaultFolder,
		GetTask:             s.GetTask,
		ListTasks:           s.ListTasks,
		MessagesBefore:   s.MessagesBefore,
		MessagesByThread: s.MessagesByThread,
		FindMessages: func(q, scope, sender, since string, limit int) ([]ipc.FoundMessage, error) {
			rows, err := s.FindMessages(q, scope, sender, since, limit)
			if err != nil {
				return nil, err
			}
			out := make([]ipc.FoundMessage, len(rows))
			for i, r := range rows {
				out[i] = ipc.FoundMessage{
					ChatJID: r.ChatJID, Sender: r.Sender, Timestamp: r.Timestamp,
					IsFromMe: r.IsFromMe, IsBotMessage: r.IsBotMessage,
					Content: r.Content, Rank: r.Rank,
				}
			}
			return out, nil
		},
	}

	sock := filepath.Join(tmp, "mcp.sock")
	stop, err := ipc.ServeMCP(sock, gated, db, folder, true, -1, "")
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

// inviteToInfo maps a store row (no raw-token field, I1) to the wire DTO.
// Token stays unset here — the Create closure fills it in from the raw
// bearer CreateInvite returns separately; a row read back via ListInvites
// never has one to fill.
func inviteToInfo(inv *store.Invite) ipc.InviteInfo {
	return ipc.InviteInfo{
		Ref:         inv.Ref,
		TargetGlob:  inv.TargetGlob,
		IssuedBySub: inv.IssuedBySub,
		IssuedAt:    inv.IssuedAt, ExpiresAt: inv.ExpiresAt,
		MaxUses: inv.MaxUses, UsedCount: inv.UsedCount,
	}
}

// firstJSONString decodes the first TextContent as a JSON object and returns
// field key as a string. Used to recover a value (like invite_create's raw
// token) that a read surface never round-trips — it exists only in this one
// response.
func firstJSONString(t *testing.T, res *mcp.CallToolResult, key string) string {
	t.Helper()
	for _, c := range res.Content {
		tc, ok := c.(mcp.TextContent)
		if !ok {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(tc.Text), &m); err != nil {
			continue
		}
		if v, ok := m[key].(string); ok {
			return v
		}
	}
	t.Fatalf("no JSON field %q in tool result: %v", key, res.Content)
	return ""
}

func (h *fullMCPHarness) call(t *testing.T, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := h.Client.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: name, Arguments: args},
	})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s returned error: %v", name, res.Content)
	}
	return res
}

func TestMCP_SocialActions(t *testing.T) {
	h := newFullMCPHarness(t, "hq")

	t.Run("like", func(t *testing.T) {
		h.call(t, "like", map[string]any{
			"chatJid": "telegram:42", "targetId": "m1", "reaction": "👍",
		})
		if len(h.LikeCalls) != 1 || h.LikeCalls[0].Extra != "👍" {
			t.Fatalf("LikeCalls = %+v", h.LikeCalls)
		}
	})

	t.Run("delete", func(t *testing.T) {
		h.call(t, "delete", map[string]any{"chatJid": "telegram:42", "targetId": "m1"})
		if len(h.DeleteCalls) != 1 || h.DeleteCalls[0].Target != "m1" {
			t.Fatalf("DeleteCalls = %+v", h.DeleteCalls)
		}
	})

	t.Run("edit", func(t *testing.T) {
		h.call(t, "edit", map[string]any{
			"chatJid": "telegram:42", "targetId": "m1", "content": "new",
		})
		if len(h.EditCalls) != 1 || h.EditCalls[0].Extra != "new" {
			t.Fatalf("EditCalls = %+v", h.EditCalls)
		}
	})
}

// Route management (add_route/list_routes/delete_route) moved from the ipc
// ServeMCP surface to the routd routes resreg seam (spec 5/16); parity is covered
// by routd/routes_resource_test.go. This ipc-level harness no longer serves them.

func TestMCP_InviteTools(t *testing.T) {
	h := newFullMCPHarness(t, "hq")
	var token string

	t.Run("invite_create", func(t *testing.T) {
		res := h.call(t, "invite_create", map[string]any{
			"target_glob": "hq/sub", "max_uses": float64(1),
		})
		if !contentContains(res, "token") {
			t.Fatalf("invite_create missing token: %v", res.Content)
		}
		// The raw bearer is shown ONLY in the create response (I1: the DB
		// stores just its hash) — read it from the tool result, not the store.
		token = firstJSONString(t, res, "token")
		if token == "" {
			t.Fatalf("invite_create returned an empty token: %v", res.Content)
		}
		invs, err := h.S.ListInvites("agent:hq")
		if err != nil || len(invs) != 1 {
			t.Fatalf("invite not persisted: invs=%+v err=%v", invs, err)
		}
		if invs[0].Ref != store.TokenRef(token) {
			t.Fatalf("persisted ref %q != TokenRef(created token)", invs[0].Ref)
		}
	})

	// invite_list identifies the invite by ref; the bearer was shown once at
	// create and is not readable back on any surface.
	t.Run("invite_list", func(t *testing.T) {
		res := h.call(t, "invite_list", nil)
		if !contentContains(res, store.TokenRef(token)) {
			t.Fatalf("invite_list missing created invite ref: %v", res.Content)
		}
		if contentContains(res, token) {
			t.Fatalf("invite_list leaked the bearer %q: %v", token, res.Content)
		}
	})

	t.Run("invite_revoke", func(t *testing.T) {
		h.call(t, "invite_revoke", map[string]any{"ref": store.TokenRef(token)})
		invs, _ := h.S.ListInvites("agent:hq")
		if len(invs) != 0 {
			t.Fatalf("invite still present after revoke: %+v", invs)
		}
	})
}


func TestMCP_MessageInspection(t *testing.T) {
	h := newFullMCPHarness(t, "hq")
	if err := h.S.PutMessage(core.Message{
		ID: "m1", ChatJID: "telegram:42", Sender: "u1", Name: "U1",
		Content: "hello world", Timestamp: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("inspect_messages", func(t *testing.T) {
		res := h.call(t, "inspect_messages", map[string]any{
			"chat_jid": "telegram:42", "limit": float64(10),
		})
		if !contentContains(res, "hello world") {
			t.Fatalf("inspect_messages missing seeded row: %v", res.Content)
		}
	})

	t.Run("find_messages", func(t *testing.T) {
		res := h.call(t, "find_messages", map[string]any{
			"query": "hello", "limit": float64(5),
		})
		if res.IsError {
			t.Fatalf("find_messages errored: %v", res.Content)
		}
	})
}

// web_route MCP tools moved to routd's resreg resource (spec 5/16 pilot);
// coverage lives in routd/web_routes_resource_test.go (real socket + seam).

func TestMCP_GroupObservation(t *testing.T) {
	h := newFullMCPHarness(t, "hq")

	t.Run("observe_group", func(t *testing.T) {
		h.call(t, "observe_group", map[string]any{"source": "world/other"})
		got := h.S.WatchedSources("hq")
		if len(got) != 1 || got[0] != "world/other" {
			t.Fatalf("WatchedSources = %+v", got)
		}
	})

	t.Run("unobserve_group", func(t *testing.T) {
		h.call(t, "unobserve_group", map[string]any{"source": "world/other"})
		if len(h.S.WatchedSources("hq")) != 0 {
			t.Fatalf("watcher still present: %+v", h.S.WatchedSources("hq"))
		}
	})
}
