package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/onvos/arizuko/auth"
	"github.com/onvos/arizuko/core"
	grantslib "github.com/onvos/arizuko/grants"
	"github.com/onvos/arizuko/mountsec"
	"github.com/onvos/arizuko/router"
	"github.com/robfig/cron/v3"
)

type GatedFns struct {
	SendMessage      func(jid, text string) (string, error)
	SendReply        func(jid, text, replyToId string) (string, error)
	SendDocument     func(jid, path, filename, caption string) error
	ClearSession     func(folder string)
	InjectMessage    func(jid, content, sender, senderName string) (string, error)
	RegisterGroup    func(jid string, group core.Group) error
	SeedGroupDir     func(folder string) error
	GetGroups        func() map[string]core.Group
	DelegateToChild  func(folder, prompt, jid string, depth int, rules []string) error
	DelegateToParent func(folder, prompt, jid string, depth int, rules []string) error
	SpawnGroup       func(parentFolder, childJID string) (core.Group, error)
	GroupsDir        string
	HostGroupsDir    string
	WebDir           string
}

type StoreFns struct {
	CreateTask        func(t core.Task) error
	GetTask           func(id string) (core.Task, bool)
	UpdateTaskStatus  func(id, status string) error
	DeleteTask        func(id string) error
	ListTasks         func(folder string, isRoot bool) []core.Task
	GetRoutes         func(jid string) []core.Route
	ListRoutes        func(folder string, isRoot bool) []core.Route
	SetRoutes         func(jid string, routes []core.Route) error
	AddRoute          func(jid string, r core.Route) (int64, error)
	DeleteRoute       func(id int64) error
	GetRoute          func(id int64) (core.Route, bool)
	StoreOutbound     func(entry core.OutboundEntry) error
	GetLastReplyID    func(jid, topic string) string
	SetLastReplyID    func(jid, topic, replyID string)
	GetGrants         func(folder string) []string
	SetGrants         func(folder string, rules []string) error
	MessagesBefore    func(jid string, before time.Time, limit int) ([]core.Message, error)
	JIDRoutedToFolder func(jid, folder string) bool
}

func ServeMCP(sockPath string, gated GatedFns, db StoreFns, folder string, rules []string) (func(), error) {
	os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	os.Chmod(sockPath, 0666) // agent container runs as uid=1000, gated as root
	slog.Info("mcp server listening", "folder", folder, "sock", sockPath)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			slog.Debug("mcp connection accepted", "folder", folder)
			go func() {
				srv := buildMCPServer(gated, db, folder, rules)
				stdioSrv := server.NewStdioServer(srv)
				stdioSrv.Listen(ctx, conn, conn)
				conn.Close()
			}()
		}
	}()

	return func() {
		cancel()
		ln.Close()
		os.Remove(sockPath)
	}, nil
}

func toolErr(msg string) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultError(msg), nil
}

func toolJSON(v any) (*mcp.CallToolResult, error) {
	data, _ := json.Marshal(v)
	return mcp.NewToolResultText(string(data)), nil
}

func toolOK() (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText("ok"), nil
}

func recordOutbound(db StoreFns, jid, text, replyToID, platformID, folder string) {
	if platformID == "" {
		return
	}
	if db.SetLastReplyID != nil {
		db.SetLastReplyID(jid, "", platformID)
	}
	if db.StoreOutbound != nil {
		db.StoreOutbound(core.OutboundEntry{
			ChatJID: jid, Content: text, Source: "mcp",
			GroupFolder: folder, ReplyToID: replyToID, PlatformMsgID: platformID,
		})
	}
}

func normalizeJID(jid string) string {
	if strings.HasPrefix(jid, "whatsapp:") && !strings.Contains(jid, "@") {
		return jid + "@s.whatsapp.net"
	}
	return jid
}

func isRouteTypeValid(t string) bool {
	switch t {
	case "command", "verb", "pattern", "keyword", "sender", "default":
		return true
	}
	return false
}

func folderForJid(db StoreFns, jid string) string {
	if db.GetRoutes == nil {
		return ""
	}
	for _, r := range db.GetRoutes(jid) {
		if r.Type == "default" {
			return r.Target
		}
	}
	return ""
}

func toolDesc(base string, rules []string, action string) string {
	matching := grantslib.MatchingRules(rules, action)
	if len(matching) == 0 {
		return base
	}
	data, _ := json.Marshal(matching)
	return base + "\ngrants: " + string(data)
}

func workspaceRel(fp string) (string, error) {
	if strings.HasPrefix(fp, "/home/node/") {
		return strings.TrimPrefix(fp, "/home/node/"), nil
	}
	return "", fmt.Errorf("filepath must be under ~/ (/home/node)")
}

func readVhosts(webDir string) (map[string]string, error) {
	p := filepath.Join(webDir, "vhosts.json")
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]string), nil
		}
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func writeVhosts(webDir string, m map[string]string) error {
	if err := os.MkdirAll(webDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(webDir, "vhosts.json"), data, 0644)
}

func buildMCPServer(gated GatedFns, db StoreFns, folder string, rules []string) *server.MCPServer {
	id := auth.Resolve(folder)
	srv := server.NewMCPServer("arizuko", "1.0")

	// register adds a tool if any rule references it, auto-annotates the
	// description with matching grants, and wraps the handler with a runtime
	// CheckAction(nil-params) gate. For tools with per-param grants (send_*),
	// the handler does its own param-aware CheckAction and registerRaw skips
	// the wrapper.
	registerRaw := func(name, desc string, opts []mcp.ToolOption, h server.ToolHandlerFunc) {
		if len(grantslib.MatchingRules(rules, name)) == 0 {
			return
		}
		all := append([]mcp.ToolOption{mcp.WithDescription(toolDesc(desc, rules, name))}, opts...)
		srv.AddTool(mcp.NewTool(name, all...), h)
	}
	granted := func(name, desc string, opts []mcp.ToolOption, h server.ToolHandlerFunc) {
		registerRaw(name, desc, opts, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if !grantslib.CheckAction(rules, name, nil) {
				return toolErr(name + ": not permitted")
			}
			return h(ctx, req)
		})
	}

	registerRaw("send_message", "Send a text message to a chat",
		[]mcp.ToolOption{
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("text", mcp.Required()),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chatJid", "")
			if !grantslib.CheckAction(rules, "send_message", map[string]string{"jid": jid}) {
				return toolErr("send_message: not permitted")
			}
			if gated.SendMessage == nil {
				return toolErr("send_message not configured")
			}
			text := req.GetString("text", "")
			snippet := text
			if len(snippet) > 60 {
				snippet = snippet[:60]
			}
			slog.Info("send_message", "folder", folder, "jid", jid, "text", snippet)
			platformID, err := gated.SendMessage(jid, text)
			if err != nil {
				return toolErr(err.Error())
			}
			recordOutbound(db, jid, text, "", platformID, folder)
			return toolOK()
		})

	registerRaw("send_reply", "Send a reply to a chat, optionally threading to a message",
		[]mcp.ToolOption{
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("text", mcp.Required()),
			mcp.WithString("replyToId"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chatJid", "")
			if !grantslib.CheckAction(rules, "send_reply", map[string]string{"jid": jid}) {
				return toolErr("send_reply: not permitted")
			}
			if gated.SendReply == nil {
				return toolErr("send_reply not configured")
			}
			text := req.GetString("text", "")
			replyToID := req.GetString("replyToId", "")
			if replyToID == "" && db.GetLastReplyID != nil {
				replyToID = db.GetLastReplyID(jid, "")
			}
			platformID, err := gated.SendReply(jid, text, replyToID)
			if err != nil {
				return toolErr(err.Error())
			}
			recordOutbound(db, jid, text, replyToID, platformID, folder)
			return toolOK()
		})

	registerRaw("send_file", "Send a file to a chat",
		[]mcp.ToolOption{
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("filepath", mcp.Required()),
			mcp.WithString("filename"),
			mcp.WithString("caption"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chatJid", "")
			if !grantslib.CheckAction(rules, "send_file", map[string]string{"jid": jid}) {
				return toolErr("send_file: not permitted")
			}
			if gated.SendDocument == nil {
				return toolErr("send_file not configured")
			}
			fp := req.GetString("filepath", "")
			name := req.GetString("filename", "")
			caption := req.GetString("caption", "")
			rel, err := workspaceRel(fp)
			if err != nil {
				return toolErr(err.Error())
			}
			localPath := filepath.Join(gated.GroupsDir, folder, rel)
			hostPath := filepath.Join(gated.HostGroupsDir, folder, rel)
			if _, err := mountsec.ValidateFilePath(localPath, filepath.Join(gated.GroupsDir, folder)); err != nil {
				return toolErr("path outside group dir")
			}
			if !strings.HasPrefix(hostPath, filepath.Join(gated.HostGroupsDir, folder)+"/") {
				return toolErr("path outside group dir")
			}
			slog.Info("send_file", "folder", folder, "jid", jid, "path", localPath)
			if err := gated.SendDocument(jid, localPath, name, caption); err != nil {
				return toolErr(err.Error())
			}
			return toolOK()
		})

	granted("reset_session", "Clear the agent session for a group",
		[]mcp.ToolOption{mcp.WithString("groupFolder", mcp.Required())},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			gf := req.GetString("groupFolder", "")
			if gf == "" {
				return toolErr("missing groupFolder")
			}
			if gated.ClearSession == nil {
				return toolErr("reset_session not configured")
			}
			if err := auth.Authorize(id, "reset_session", auth.AuthzTarget{TargetFolder: gf}); err != nil {
				return toolErr(err.Error())
			}
			slog.Info("reset_session", "folder", folder, "targetFolder", gf)
			gated.ClearSession(gf)
			return toolOK()
		})

	granted("inject_message", "Inject a message into the store for a chat",
		[]mcp.ToolOption{
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("content", mcp.Required()),
			mcp.WithString("sender"),
			mcp.WithString("senderName"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if gated.InjectMessage == nil {
				return toolErr("inject_message not configured")
			}
			jid := req.GetString("chatJid", "")
			sender := req.GetString("sender", "")
			if sender == "" {
				sender = "system"
			}
			senderName := req.GetString("senderName", "")
			if senderName == "" {
				senderName = "system"
			}
			mid, err := gated.InjectMessage(jid, req.GetString("content", ""), sender, senderName)
			if err != nil {
				return toolErr(err.Error())
			}
			slog.Info("message injected", "id", mid, "chatJid", jid, "sourceGroup", folder)
			return toolJSON(map[string]any{"injected": true, "id": mid})
		})

	granted("register_group",
		"Register a new agent group at an explicit folder and add a default route from jid to that folder. Set fromPrototype=true to also copy this group's prototype/ directory into the child.",
		[]mcp.ToolOption{
			mcp.WithString("jid", mcp.Required()),
			mcp.WithString("name"),
			mcp.WithBoolean("fromPrototype"),
			mcp.WithString("folder"),
			mcp.WithString("parent"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if gated.RegisterGroup == nil {
				return toolErr("register_group not configured")
			}
			jid := req.GetString("jid", "")
			name := req.GetString("name", jid)

			if req.GetBool("fromPrototype", false) {
				if gated.SpawnGroup == nil {
					return toolErr("register_group: fromPrototype not configured")
				}
				// Parent is the calling agent's own folder
				child, err := gated.SpawnGroup(folder, jid)
				if err != nil {
					return toolErr(err.Error())
				}
				if name != jid {
					child.Name = name
					if err := gated.RegisterGroup(jid, child); err != nil {
						slog.Warn("register_group: update name", "jid", jid, "err", err)
					}
				}
				if gated.SeedGroupDir != nil {
					if err := gated.SeedGroupDir(child.Folder); err != nil {
						slog.Warn("register_group: seed group dir", "folder", child.Folder, "err", err)
					}
				}
				slog.Info("group registered from prototype", "jid", jid, "folder", child.Folder, "sourceGroup", folder)
				return toolJSON(map[string]any{"registered": true, "folder": child.Folder, "jid": jid})
			}

			gfld := req.GetString("folder", "")
			if gfld == "" {
				return toolErr("folder required when fromPrototype is false")
			}
			if err := auth.Authorize(id, "register_group", auth.AuthzTarget{TargetFolder: gfld}); err != nil {
				return toolErr(err.Error())
			}
			groups := gated.GetGroups()
			parent := folder
			if pg, ok := groups[parent]; ok && pg.Config.MaxChildren >= 0 {
				if pg.Config.MaxChildren == 0 {
					return toolErr("spawning disabled for this group")
				}
				n := 0
				for _, g := range groups {
					if auth.IsDirectChild(parent, g.Folder) {
						n++
					}
				}
				if n >= pg.Config.MaxChildren {
					return toolErr(fmt.Sprintf("max_children limit reached (%d)", pg.Config.MaxChildren))
				}
			}
			gr := core.Group{
				Name:    name,
				Folder:  gfld,
				AddedAt: time.Now(),
				Parent:  req.GetString("parent", ""),
			}
			if err := gated.RegisterGroup(jid, gr); err != nil {
				return toolErr(err.Error())
			}
			if gated.SeedGroupDir != nil {
				if err := gated.SeedGroupDir(gfld); err != nil {
					slog.Warn("register_group: seed group dir", "folder", gfld, "err", err)
				}
			}
			slog.Info("group registered", "jid", jid, "folder", gfld, "sourceGroup", folder)
			return toolJSON(map[string]any{"registered": true, "folder": gfld, "jid": jid})
		})

	granted("escalate_group", "Escalate a task to the parent group",
		[]mcp.ToolOption{
			mcp.WithString("prompt", mcp.Required()),
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithNumber("depth"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if gated.DelegateToParent == nil {
				return toolErr("escalate_group not configured")
			}
			prompt := req.GetString("prompt", "")
			chatJid := req.GetString("chatJid", "")
			depth := req.GetInt("depth", 0)
			if depth >= 1 {
				return toolErr(fmt.Sprintf("delegation depth %d exceeds limit 1", depth))
			}
			idx := strings.LastIndex(folder, "/")
			if idx == -1 {
				return toolErr("unauthorized: no parent group")
			}
			parent := folder[:idx]
			var replyTo string
			if db.GetLastReplyID != nil {
				replyTo = db.GetLastReplyID(chatJid, "")
			}
			slog.Info("escalating to parent", "sourceGroup", folder, "parent", parent, "depth", depth, "replyTo", replyTo)
			wrapped := fmt.Sprintf("<escalation_origin folder=%q jid=%q reply_to=%q/>\n%s", folder, chatJid, replyTo, prompt)
			if err := gated.DelegateToParent(parent, wrapped, chatJid, depth+1, nil); err != nil {
				return toolErr(err.Error())
			}
			return toolJSON(map[string]any{"queued": true, "parent": parent})
		})

	if id.Tier <= 2 {
		srv.AddTool(mcp.NewTool("refresh_groups",
			mcp.WithDescription("Reload the list of registered groups"),
		), func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if gated.GetGroups == nil {
				return toolErr("refresh_groups not configured")
			}
			groups := gated.GetGroups()
			type groupInfo struct {
				Folder string `json:"folder"`
				Name   string `json:"name"`
				Parent string `json:"parent,omitempty"`
			}
			out := make([]groupInfo, 0, len(groups))
			for _, g := range groups {
				out = append(out, groupInfo{Folder: g.Folder, Name: g.Name, Parent: g.Parent})
			}
			return toolJSON(out)
		})
	}

	granted("delegate_group", "Delegate a task to a child group",
		[]mcp.ToolOption{
			mcp.WithString("group", mcp.Required()),
			mcp.WithString("prompt", mcp.Required()),
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithNumber("depth"),
			mcp.WithString("grants"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			target := req.GetString("group", "")
			if err := auth.Authorize(id, "delegate_group", auth.AuthzTarget{TargetFolder: target}); err != nil {
				return toolErr(err.Error())
			}
			if gated.DelegateToChild == nil {
				return toolErr("delegate_group not configured")
			}
			prompt := req.GetString("prompt", "")
			chatJid := req.GetString("chatJid", "")
			depth := req.GetInt("depth", 0)
			if depth >= 1 {
				return toolErr(fmt.Sprintf("delegation depth %d exceeds limit 1", depth))
			}

			var delegateRules []string
			if grantStr := req.GetString("grants", ""); grantStr != "" {
				var requested []string
				if err := json.Unmarshal([]byte(grantStr), &requested); err != nil {
					return toolErr("invalid grants json: " + err.Error())
				}
				delegateRules = grantslib.NarrowRules(rules, requested)
			}

			slog.Info("delegating to child", "sourceGroup", folder, "child", target, "depth", depth)
			if err := gated.DelegateToChild(target, prompt, chatJid, depth+1, delegateRules); err != nil {
				return toolErr(err.Error())
			}
			return toolJSON(map[string]any{"queued": true})
		})

	granted("get_routes", "Get routes for a JID",
		[]mcp.ToolOption{mcp.WithString("jid", mcp.Required())},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.GetRoutes == nil {
				return toolErr("get_routes not configured")
			}
			jid := req.GetString("jid", "")
			if jid == "" {
				return toolErr("jid required")
			}
			tf := folderForJid(db, jid)
			if err := auth.Authorize(id, "get_routes", auth.AuthzTarget{RouteTarget: tf}); err != nil {
				return toolErr(err.Error())
			}
			return toolJSON(map[string]any{"jid": jid, "routes": db.GetRoutes(jid)})
		})

	granted("list_routes", "List all routes visible to this group", nil,
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.ListRoutes == nil {
				return toolErr("list_routes not configured")
			}
			return toolJSON(map[string]any{"routes": db.ListRoutes(folder, id.Tier == 0)})
		})

	granted("set_routes", "Replace all routes for a JID",
		[]mcp.ToolOption{
			mcp.WithString("jid", mcp.Required()),
			mcp.WithString("routes", mcp.Required()),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.SetRoutes == nil {
				return toolErr("set_routes not configured")
			}
			jid := req.GetString("jid", "")
			if jid == "" {
				return toolErr("jid required")
			}
			tf := folderForJid(db, jid)
			if err := auth.Authorize(id, "set_routes", auth.AuthzTarget{RouteTarget: tf}); err != nil {
				return toolErr(err.Error())
			}
			var routes []core.Route
			if err := json.Unmarshal([]byte(req.GetString("routes", "")), &routes); err != nil {
				return toolErr("invalid routes json: " + err.Error())
			}
			// Self-harm guard: if the caller's own default route for this
			// jid exists and is being removed from the new list, refuse.
			if db.GetRoutes != nil {
				for _, old := range db.GetRoutes(jid) {
					if old.Type != "default" || old.Target != id.Folder {
						continue
					}
					kept := false
					for _, n := range routes {
						if n.Type == "default" && n.Target == id.Folder {
							kept = true
							break
						}
					}
					if !kept {
						return toolErr("cannot remove own default route via set_routes")
					}
				}
			}
			if err := db.SetRoutes(jid, routes); err != nil {
				return toolErr(err.Error())
			}
			slog.Info("routes set", "jid", jid, "count", len(routes))
			return toolJSON(map[string]any{"updated": true, "count": len(routes)})
		})

	granted("add_route", "Add a single route for a JID",
		[]mcp.ToolOption{
			mcp.WithString("jid", mcp.Required()),
			mcp.WithString("route", mcp.Required()),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.AddRoute == nil {
				return toolErr("add_route not configured")
			}
			jid := normalizeJID(req.GetString("jid", ""))
			if jid == "" {
				return toolErr("jid required")
			}
			var route core.Route
			if err := json.Unmarshal([]byte(req.GetString("route", "")), &route); err != nil {
				return toolErr("invalid route json")
			}
			if route.Target == "" {
				return toolErr("route.target required")
			}
			if !isRouteTypeValid(route.Type) {
				return toolErr("invalid route type")
			}
			if err := auth.Authorize(id, "add_route", auth.AuthzTarget{RouteTarget: route.Target}); err != nil {
				return toolErr(err.Error())
			}
			rid, err := db.AddRoute(jid, route)
			if err != nil {
				return toolErr(err.Error())
			}
			slog.Info("route added", "jid", jid, "id", rid, "type", route.Type)
			return toolJSON(map[string]any{"id": rid, "jid": jid})
		})

	granted("delete_route", "Delete a route by ID",
		[]mcp.ToolOption{mcp.WithNumber("id", mcp.Required())},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.DeleteRoute == nil || db.GetRoute == nil {
				return toolErr("delete_route not configured")
			}
			rid := int64(req.GetInt("id", 0))
			if rid == 0 {
				return toolErr("id required")
			}
			route, ok := db.GetRoute(rid)
			if !ok {
				return toolErr(fmt.Sprintf("route not found: %d", rid))
			}
			if route.Type == "default" && route.Target == id.Folder {
				return toolErr("cannot delete own default route")
			}
			if err := auth.Authorize(id, "delete_route", auth.AuthzTarget{RouteTarget: route.Target}); err != nil {
				return toolErr(err.Error())
			}
			if err := db.DeleteRoute(rid); err != nil {
				return toolErr(err.Error())
			}
			slog.Info("route deleted", "id", rid)
			return toolJSON(map[string]any{"deleted": true, "id": rid})
		})

	granted("schedule_task",
		"Schedule a recurring or one-time task. cron: cron expression or interval in ms",
		[]mcp.ToolOption{
			mcp.WithString("targetJid", mcp.Required()),
			mcp.WithString("prompt", mcp.Required()),
			mcp.WithString("cron"),
			mcp.WithString("contextMode"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.CreateTask == nil {
				return toolErr("schedule_task not configured")
			}
			targetJid := req.GetString("targetJid", "")
			cronExpr := req.GetString("cron", "")
			contextMode := req.GetString("contextMode", "group")
			if contextMode != "isolated" {
				contextMode = "group"
			}

			targetFolder := folderForJid(db, targetJid)
			if targetFolder == "" {
				return toolErr("target group not registered")
			}

			if err := auth.Authorize(id, "schedule_task", auth.AuthzTarget{TaskOwner: targetFolder}); err != nil {
				return toolErr(err.Error())
			}

			var nextRun *time.Time
			var cronStore string // value written to DB cron column
			if cronExpr != "" {
				// interval ms: pure integer string
				if ms, err := strconv.ParseInt(cronExpr, 10, 64); err == nil && ms > 0 {
					t := time.Now().Add(time.Duration(ms) * time.Millisecond)
					nextRun = &t
					cronStore = cronExpr
				} else if t, err := time.Parse(time.RFC3339, cronExpr); err == nil {
					// once: ISO 8601 timestamp → run once at that time
					nextRun = &t
					cronStore = "" // empty cron → timed marks completed after firing
				} else {
					p := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
					sched, err := p.Parse(cronExpr)
					if err != nil {
						return toolErr(fmt.Sprintf("invalid cron %q: %v", cronExpr, err))
					}
					t := sched.Next(time.Now())
					nextRun = &t
					cronStore = cronExpr
				}
			}

			taskID := fmt.Sprintf("task-%d-%s", time.Now().UnixMilli(), uuid.New().String()[:8])
			task := core.Task{
				ID: taskID, Owner: targetFolder, ChatJID: targetJid,
				Prompt: req.GetString("prompt", ""), Cron: cronStore,
				NextRun: nextRun, Status: core.TaskActive, Created: time.Now(),
				ContextMode: contextMode,
			}
			if err := db.CreateTask(task); err != nil {
				return toolErr(err.Error())
			}
			slog.Info("task created via mcp", "taskId", taskID, "sourceGroup", folder,
				"targetFolder", targetFolder, "contextMode", contextMode)
			return toolJSON(map[string]any{"taskId": taskID})
		})

	taskOps := []struct {
		name string
		desc string
		exec func(taskID string) error
	}{
		{"pause_task", "Pause a scheduled task", func(tid string) error {
			return db.UpdateTaskStatus(tid, core.TaskPaused)
		}},
		{"resume_task", "Resume a paused scheduled task", func(tid string) error {
			return db.UpdateTaskStatus(tid, core.TaskActive)
		}},
		{"cancel_task", "Cancel and delete a scheduled task", func(tid string) error {
			return db.DeleteTask(tid)
		}},
	}
	for _, op := range taskOps {
		op := op
		if db.GetTask == nil {
			continue
		}
		granted(op.name, op.desc,
			[]mcp.ToolOption{mcp.WithString("taskId", mcp.Required())},
			func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				taskID := req.GetString("taskId", "")
				task, ok := db.GetTask(taskID)
				if !ok {
					return toolErr("task not found")
				}
				if err := auth.Authorize(id, op.name, auth.AuthzTarget{TaskOwner: task.Owner}); err != nil {
					return toolErr(err.Error())
				}
				if err := op.exec(taskID); err != nil {
					return toolErr(err.Error())
				}
				slog.Info("task "+op.name+" via mcp", "taskId", taskID, "sourceGroup", folder)
				return toolJSON(map[string]any{"ok": true})
			})
	}

	granted("list_tasks", "List scheduled tasks visible to this group", nil,
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.ListTasks == nil {
				return toolErr("list_tasks not configured")
			}
			return toolJSON(db.ListTasks(folder, id.Tier == 0))
		})

	if id.Tier <= 1 {
		srv.AddTool(mcp.NewTool("get_grants",
			mcp.WithDescription("Get grant rules for a folder (tier 0-1 only)"),
			mcp.WithString("folder", mcp.Required()),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.GetGrants == nil {
				return toolErr("get_grants not configured")
			}
			gf := req.GetString("folder", "")
			if gf == "" {
				return toolErr("folder required")
			}
			if err := auth.Authorize(id, "get_grants", auth.AuthzTarget{TargetFolder: gf}); err != nil {
				return toolErr(err.Error())
			}
			return toolJSON(map[string]any{"folder": gf, "rules": db.GetGrants(gf)})
		})

		srv.AddTool(mcp.NewTool("set_grants",
			mcp.WithDescription("Set grant rules for a folder (tier 0-1 only)"),
			mcp.WithString("folder", mcp.Required()),
			mcp.WithString("rules", mcp.Required()),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.SetGrants == nil {
				return toolErr("set_grants not configured")
			}
			gf := req.GetString("folder", "")
			if gf == "" {
				return toolErr("folder required")
			}
			if err := auth.Authorize(id, "set_grants", auth.AuthzTarget{TargetFolder: gf}); err != nil {
				return toolErr(err.Error())
			}
			var newRules []string
			if err := json.Unmarshal([]byte(req.GetString("rules", "")), &newRules); err != nil {
				return toolErr("invalid rules json: " + err.Error())
			}
			if err := db.SetGrants(gf, newRules); err != nil {
				return toolErr(err.Error())
			}
			slog.Info("grants set", "folder", gf, "count", len(newRules), "sourceGroup", folder)
			return toolJSON(map[string]any{"ok": true, "folder": gf, "count": len(newRules)})
		})
	}

	if db.MessagesBefore != nil {
		srv.AddTool(mcp.NewTool("get_history",
			mcp.WithDescription("Fetch message history for a chat. Returns XML same as injected <messages> block."),
			mcp.WithString("chat_jid", mcp.Required()),
			mcp.WithNumber("limit"),
			mcp.WithString("before"),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chat_jid", "")
			if jid == "" {
				return toolErr("chat_jid required")
			}
			if id.Tier > 0 && db.JIDRoutedToFolder != nil && !db.JIDRoutedToFolder(jid, folder) {
				return toolErr("access_denied: jid not routed to your group")
			}
			limitVal := req.GetInt("limit", 100)
			if limitVal <= 0 || limitVal > 200 {
				limitVal = 100
			}
			var before time.Time
			if beforeStr := req.GetString("before", ""); beforeStr != "" {
				t, err := time.Parse(time.RFC3339, beforeStr)
				if err != nil {
					return toolErr("invalid before timestamp: " + err.Error())
				}
				before = t
			}
			msgs, err := db.MessagesBefore(jid, before, limitVal)
			if err != nil {
				return toolErr("get_history: " + err.Error())
			}
			oldest := ""
			if len(msgs) > 0 {
				oldest = msgs[0].Timestamp.Format(time.RFC3339)
			}
			return toolJSON(map[string]any{
				"messages": router.FormatMessages(msgs),
				"count":    len(msgs),
				"oldest":   oldest,
			})
		})
	}

	if id.Tier == 0 {
		granted("set_web_host", "Set a virtual host mapping (hostname → folder)",
			[]mcp.ToolOption{
				mcp.WithString("hostname", mcp.Required()),
				mcp.WithString("folder", mcp.Required()),
			},
			func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				hostname := req.GetString("hostname", "")
				targetFolder := req.GetString("folder", "")
				if hostname == "" {
					return toolErr("hostname required")
				}
				if strings.Contains(hostname, "/") || strings.Contains(hostname, "://") {
					return toolErr("hostname must not contain scheme or path")
				}
				if targetFolder == "" {
					return toolErr("folder required")
				}
				groupDir := filepath.Join(gated.GroupsDir, targetFolder)
				if fi, err := os.Stat(groupDir); err != nil || !fi.IsDir() {
					return toolErr("folder does not exist: " + targetFolder)
				}
				vhosts, err := readVhosts(gated.WebDir)
				if err != nil {
					return toolErr("read vhosts: " + err.Error())
				}
				vhosts[hostname] = targetFolder
				if err := writeVhosts(gated.WebDir, vhosts); err != nil {
					return toolErr("write vhosts: " + err.Error())
				}
				slog.Info("set_web_host", "hostname", hostname, "folder", targetFolder, "sourceGroup", folder)
				return toolJSON(vhosts)
			})
	}

	if id.Tier <= 1 {
		granted("get_web_host", "Get virtual host mapping for a folder",
			[]mcp.ToolOption{mcp.WithString("folder")},
			func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				targetFolder := req.GetString("folder", folder)
				if targetFolder == "" {
					targetFolder = folder
				}
				if id.Tier > 0 && targetFolder != folder {
					return toolErr("get_web_host: can only query own folder")
				}
				vhosts, err := readVhosts(gated.WebDir)
				if err != nil {
					return toolErr("read vhosts: " + err.Error())
				}
				for h, f := range vhosts {
					if f == targetFolder {
						return toolJSON(map[string]any{"hostname": h, "folder": f})
					}
				}
				return toolJSON(map[string]any{"hostname": "", "folder": targetFolder})
			})
	}

	return srv
}
