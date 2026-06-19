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
		GrantACL: func(p, sc, a, e string) error {
			h.GrantCalls = append(h.GrantCalls, aclCall{p, sc, a, e})
			return nil
		},
		RevokeACL: func(p, sc, a, e string) error {
			h.RevokeCalls = append(h.RevokeCalls, aclCall{p, sc, a, e})
			return nil
		},
		CreateInvite: func(targetGlob, issuedBySub string, maxUses int, expiresAt *time.Time) (ipc.InviteInfo, error) {
			inv, err := s.CreateInvite(targetGlob, issuedBySub, maxUses, expiresAt)
			if err != nil {
				return ipc.InviteInfo{}, err
			}
			return inviteToInfo(inv), nil
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
		RevokeInvite:       s.RevokeInvite,
		AddGroupWatcher:    s.AddGroupWatcher,
		RemoveGroupWatcher: s.RemoveGroupWatcher,
	}

	db := ipc.StoreFns{
		PutMessage:          s.PutMessage,
		DefaultFolderForJID: defaultFolder,
		CreateTask:          s.CreateTask,
		GetTask:             s.GetTask,
		ListTasks:           s.ListTasks,
		UpdateTaskStatus:    s.SetTaskStatus,
		DeleteTask:          s.DeleteTask,
		ListRoutes:          s.ListRoutes,
		AddRoute:            s.AddRoute,
		GetRoute:            s.GetRoute,
		DeleteRoute: func(id int64) error {
			_, err := s.DeleteRouteRow(id)
			return err
		},
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
		SetWebRoute: func(pathPrefix, access, redirectTo, folder string) error {
			return s.SetWebRoute(store.WebRoute{
				PathPrefix: pathPrefix, Access: access, RedirectTo: redirectTo,
				Folder: folder, CreatedAt: time.Now(),
			})
		},
		DelWebRoute:   s.DelWebRoute,
		WebRouteOwner: s.WebRouteOwner,
		ListWebRoutes: func(folder string) []ipc.WebRoute {
			rows := s.ListWebRoutes(folder)
			out := make([]ipc.WebRoute, len(rows))
			for i, r := range rows {
				out[i] = ipc.WebRoute{
					PathPrefix: r.PathPrefix, Access: r.Access, RedirectTo: r.RedirectTo,
					Folder: r.Folder, CreatedAt: r.CreatedAt.Format(time.RFC3339),
				}
			}
			return out
		},
		ListACL: s.ListACL,
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

func inviteToInfo(inv *store.Invite) ipc.InviteInfo {
	return ipc.InviteInfo{
		Token: inv.Token, TargetGlob: inv.TargetGlob, IssuedBySub: inv.IssuedBySub,
		IssuedAt: inv.IssuedAt, ExpiresAt: inv.ExpiresAt,
		MaxUses: inv.MaxUses, UsedCount: inv.UsedCount,
	}
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

func TestMCP_RouteManagement(t *testing.T) {
	h := newFullMCPHarness(t, "hq")

	t.Run("add_route", func(t *testing.T) {
		res := h.call(t, "add_route", map[string]any{
			"route": `{"match":"room=42","target":"hq/sub","seq":10}`,
		})
		if !contentContains(res, `"id"`) {
			t.Fatalf("add_route result missing id: %v", res.Content)
		}
		rows := h.S.ListRoutes("hq", true)
		found := false
		for _, r := range rows {
			if r.Target == "hq/sub" && r.Match == "room=42" {
				found = true
			}
		}
		if !found {
			t.Fatalf("route not persisted: %+v", rows)
		}
	})

	t.Run("list_routes", func(t *testing.T) {
		res := h.call(t, "list_routes", nil)
		if !contentContains(res, "hq/sub") {
			t.Fatalf("list_routes missing added route: %v", res.Content)
		}
	})

	t.Run("delete_route", func(t *testing.T) {
		var rid int64
		for _, r := range h.S.ListRoutes("hq", true) {
			if r.Target == "hq/sub" {
				rid = r.ID
			}
		}
		if rid == 0 {
			t.Fatal("route id not found before delete")
		}
		h.call(t, "delete_route", map[string]any{"id": float64(rid)})
		for _, r := range h.S.ListRoutes("hq", true) {
			if r.ID == rid {
				t.Fatalf("route %d still present after delete", rid)
			}
		}
	})
}

func TestMCP_TaskManagement(t *testing.T) {
	h := newFullMCPHarness(t, "hq")

	t.Run("schedule_task", func(t *testing.T) {
		res := h.call(t, "schedule_task", map[string]any{
			"targetJid": "telegram:42", "prompt": "standup", "cron": "0 9 * * *",
		})
		if !contentContains(res, "taskId") {
			t.Fatalf("schedule_task missing taskId: %v", res.Content)
		}
		if len(h.S.ListTasks("hq", true)) == 0 {
			t.Fatal("task not persisted")
		}
	})

	t.Run("list_tasks", func(t *testing.T) {
		res := h.call(t, "list_tasks", nil)
		if !contentContains(res, "standup") {
			t.Fatalf("list_tasks missing scheduled task: %v", res.Content)
		}
	})
}

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
		invs, err := h.S.ListInvites("agent:hq")
		if err != nil || len(invs) != 1 {
			t.Fatalf("invite not persisted: invs=%+v err=%v", invs, err)
		}
		token = invs[0].Token
	})

	t.Run("invite_list", func(t *testing.T) {
		res := h.call(t, "invite_list", nil)
		if !contentContains(res, token) {
			t.Fatalf("invite_list missing created invite %q: %v", token, res.Content)
		}
	})

	t.Run("invite_revoke", func(t *testing.T) {
		h.call(t, "invite_revoke", map[string]any{"token": token})
		invs, _ := h.S.ListInvites("agent:hq")
		if len(invs) != 0 {
			t.Fatalf("invite still present after revoke: %+v", invs)
		}
	})
}

func TestMCP_ACLTools(t *testing.T) {
	h := newFullMCPHarness(t, "hq")

	t.Run("add_acl", func(t *testing.T) {
		h.call(t, "add_acl", map[string]any{
			"principal": "alice@x", "scope": "hq/sub",
			"action": "admin", "effect": "allow",
		})
		if len(h.GrantCalls) != 1 || h.GrantCalls[0].Principal != "alice@x" {
			t.Fatalf("GrantCalls = %+v", h.GrantCalls)
		}
	})

	t.Run("remove_acl", func(t *testing.T) {
		h.call(t, "remove_acl", map[string]any{
			"principal": "alice@x", "scope": "hq/sub",
			"action": "admin", "effect": "allow",
		})
		if len(h.RevokeCalls) != 1 || h.RevokeCalls[0].Scope != "hq/sub" {
			t.Fatalf("RevokeCalls = %+v", h.RevokeCalls)
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

func TestMCP_WebRouteTools(t *testing.T) {
	h := newFullMCPHarness(t, "hq")

	t.Run("set_web_route", func(t *testing.T) {
		h.call(t, "set_web_route", map[string]any{
			"path": "/pub/hq/docs", "access": "public",
		})
		if len(h.S.ListWebRoutes("hq")) != 1 {
			t.Fatalf("web route not persisted: %+v", h.S.ListWebRoutes("hq"))
		}
	})

	t.Run("list_web_routes", func(t *testing.T) {
		res := h.call(t, "list_web_routes", nil)
		if !contentContains(res, "/pub/hq/docs") {
			t.Fatalf("list_web_routes missing route: %v", res.Content)
		}
	})

	t.Run("del_web_route", func(t *testing.T) {
		h.call(t, "del_web_route", map[string]any{"path": "/pub/hq/docs"})
		if len(h.S.ListWebRoutes("hq")) != 0 {
			t.Fatalf("web route still present after delete: %+v", h.S.ListWebRoutes("hq"))
		}
	})
}

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
