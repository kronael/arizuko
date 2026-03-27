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
	"github.com/robfig/cron/v3"
)

type GatedFns struct {
	SendMessage      func(jid, text string) (string, error)
	SendReply        func(jid, text, replyToId string) (string, error)
	SendDocument     func(jid, path, filename string) error
	ClearSession     func(folder string)
	InjectMessage    func(jid, content, sender, senderName string) (string, error)
	RegisterGroup    func(jid string, group core.Group) error
	SeedGroupDir     func(folder string) error
	GetGroups        func() map[string]core.Group
	DelegateToChild  func(folder, prompt, jid string, depth int, rules []string) error
	DelegateToParent func(folder, prompt, jid string, depth int, rules []string) error
	SpawnGroup       func(parentJID, childJID string) (core.Group, error)
	GroupsDir        string
	HostGroupsDir    string
}

type StoreFns struct {
	CreateTask       func(t core.Task) error
	GetTask          func(id string) (core.Task, bool)
	UpdateTaskStatus func(id, status string) error
	DeleteTask       func(id string) error
	ListTasks        func(folder string, isRoot bool) []core.Task
	GetRoutes        func(jid string) []core.Route
	SetRoutes        func(jid string, routes []core.Route) error
	AddRoute         func(jid string, r core.Route) (int64, error)
	DeleteRoute      func(id int64) error
	GetRoute         func(id int64) (core.Route, bool)
	StoreOutbound    func(entry core.OutboundEntry) error
	GetLastReplyID   func(jid, topic string) string
	SetLastReplyID   func(jid, topic, replyID string)
	GetGrants        func(folder string) []string
	SetGrants        func(folder string, rules []string) error
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

// normalizeJID appends @s.whatsapp.net to bare WhatsApp IDs.
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

func groupFolderByJid(groups map[string]core.Group, jid string) string {
	if g, ok := groups[jid]; ok {
		return g.Folder
	}
	return ""
}

// toolDesc appends matching rules to a description for manifest clarity.
func toolDesc(base string, rules []string, action string) string {
	matching := grantslib.MatchingRules(rules, action)
	if len(matching) == 0 {
		return base
	}
	data, _ := json.Marshal(matching)
	return base + "\ngrants: " + string(data)
}

// workspaceRel translates /home/node/... (or legacy /workspace/group/...) to
// a relative path under the group dir. Returns an error for other prefixes.
func workspaceRel(fp string) (string, error) {
	switch {
	case strings.HasPrefix(fp, "/home/node/"):
		return strings.TrimPrefix(fp, "/home/node/"), nil
	case strings.HasPrefix(fp, "/workspace/group/"):
		return strings.TrimPrefix(fp, "/workspace/group/"), nil
	default:
		return "", fmt.Errorf("filepath must be under ~/  (/home/node) or /workspace/group")
	}
}

func buildMCPServer(gated GatedFns, db StoreFns, folder string, rules []string) *server.MCPServer {
	id := auth.Resolve(folder)
	srv := server.NewMCPServer("arizuko", "1.0")

	grantedTools := []string{
		"send_message", "send_reply", "send_file", "reset_session",
		"inject_message", "register_group", "escalate_group", "delegate_group",
		"get_routes", "set_routes", "add_route", "delete_route",
		"schedule_task", "pause_task", "resume_task", "cancel_task", "list_tasks",
	}
	var skipped []string
	for _, t := range grantedTools {
		if len(grantslib.MatchingRules(rules, t)) == 0 {
			skipped = append(skipped, t)
		}
	}
	if len(skipped) > 0 {
		slog.Debug("mcp tools not registered (no grant match)", "folder", folder, "tools", skipped)
	}

	// send_message
	if len(grantslib.MatchingRules(rules, "send_message")) > 0 {
		srv.AddTool(mcp.NewTool("send_message",
			mcp.WithDescription(toolDesc("Send a text message to a chat", rules, "send_message")),
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("text", mcp.Required()),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
			// Update MCP reply chain state
			if db.SetLastReplyID != nil && platformID != "" {
				db.SetLastReplyID(jid, "", platformID)
			}
			if db.StoreOutbound != nil && platformID != "" {
				db.StoreOutbound(core.OutboundEntry{
					ChatJID:       jid,
					Content:       text,
					Source:        "mcp",
					GroupFolder:   folder,
					PlatformMsgID: platformID,
				})
			}
			return toolOK()
		})
	}

	// send_reply
	if len(grantslib.MatchingRules(rules, "send_reply")) > 0 {
		srv.AddTool(mcp.NewTool("send_reply",
			mcp.WithDescription(toolDesc("Send a reply to a chat, optionally threading to a message", rules, "send_reply")),
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("text", mcp.Required()),
			mcp.WithString("replyToId"),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chatJid", "")
			if !grantslib.CheckAction(rules, "send_reply", map[string]string{"jid": jid}) {
				return toolErr("send_reply: not permitted")
			}
			if gated.SendReply == nil {
				return toolErr("send_reply not configured")
			}
			text := req.GetString("text", "")
			replyToID := req.GetString("replyToId", "")
			// Auto-inject replyTo from last MCP-sent message if not provided
			if replyToID == "" && db.GetLastReplyID != nil {
				replyToID = db.GetLastReplyID(jid, "")
			}
			platformID, err := gated.SendReply(jid, text, replyToID)
			if err != nil {
				return toolErr(err.Error())
			}
			// Update MCP reply chain state
			if db.SetLastReplyID != nil && platformID != "" {
				db.SetLastReplyID(jid, "", platformID)
			}
			if db.StoreOutbound != nil && platformID != "" {
				db.StoreOutbound(core.OutboundEntry{
					ChatJID:       jid,
					Content:       text,
					Source:        "mcp",
					GroupFolder:   folder,
					ReplyToID:     replyToID,
					PlatformMsgID: platformID,
				})
			}
			return toolOK()
		})
	}

	// send_file
	if len(grantslib.MatchingRules(rules, "send_file")) > 0 {
		srv.AddTool(mcp.NewTool("send_file",
			mcp.WithDescription(toolDesc("Send a file to a chat", rules, "send_file")),
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("filepath", mcp.Required()),
			mcp.WithString("filename"),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chatJid", "")
			if !grantslib.CheckAction(rules, "send_file", map[string]string{"jid": jid}) {
				return toolErr("send_file: not permitted")
			}
			if gated.SendDocument == nil {
				return toolErr("send_file not configured")
			}
			fp := req.GetString("filepath", "")
			name := req.GetString("filename", "")
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
			if err := gated.SendDocument(jid, localPath, name); err != nil {
				return toolErr(err.Error())
			}
			return toolOK()
		})
	}

	// reset_session
	if len(grantslib.MatchingRules(rules, "reset_session")) > 0 {
		srv.AddTool(mcp.NewTool("reset_session",
			mcp.WithDescription(toolDesc("Clear the agent session for a group", rules, "reset_session")),
			mcp.WithString("groupFolder", mcp.Required()),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			gf := req.GetString("groupFolder", "")
			if gf == "" {
				return toolErr("missing groupFolder")
			}
			if !grantslib.CheckAction(rules, "reset_session", nil) {
				return toolErr("reset_session: not permitted")
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
	}

	// inject_message
	if len(grantslib.MatchingRules(rules, "inject_message")) > 0 {
		srv.AddTool(mcp.NewTool("inject_message",
			mcp.WithDescription(toolDesc("Inject a message into the store for a chat", rules, "inject_message")),
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("content", mcp.Required()),
			mcp.WithString("sender"),
			mcp.WithString("senderName"),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if !grantslib.CheckAction(rules, "inject_message", nil) {
				return toolErr("inject_message: not permitted")
			}
			if gated.InjectMessage == nil {
				return toolErr("inject_message not configured")
			}
			jid := req.GetString("chatJid", "")
			sender := req.GetString("sender", "system")
			if sender == "" {
				sender = "system"
			}
			senderName := req.GetString("senderName", "system")
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
	}

	// register_group
	if len(grantslib.MatchingRules(rules, "register_group")) > 0 {
		srv.AddTool(mcp.NewTool("register_group",
			mcp.WithDescription(toolDesc(
				"Register a new agent group. Set fromPrototype=true to copy this group's prototype/ directory into a new child folder (folder derived from jid); otherwise provide an explicit folder.",
				rules, "register_group")),
			mcp.WithString("jid", mcp.Required()),
			mcp.WithString("name"),
			mcp.WithBoolean("fromPrototype"),
			mcp.WithString("folder"),
			mcp.WithString("parent"),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if gated.RegisterGroup == nil {
				return toolErr("register_group not configured")
			}
			if !grantslib.CheckAction(rules, "register_group", nil) {
				return toolErr("register_group: not permitted")
			}
			jid := req.GetString("jid", "")
			name := req.GetString("name", jid)

			if req.GetBool("fromPrototype", false) {
				if gated.SpawnGroup == nil {
					return toolErr("register_group: fromPrototype not configured")
				}
				parentJID := ""
				for pjid, g := range gated.GetGroups() {
					if g.Folder == folder {
						parentJID = pjid
						break
					}
				}
				child, err := gated.SpawnGroup(parentJID, jid)
				if err != nil {
					return toolErr(err.Error())
				}
				if name != jid {
					child.Name = name
					gated.RegisterGroup(jid, child) //nolint
				}
				if gated.SeedGroupDir != nil {
					if err := gated.SeedGroupDir(child.Folder); err != nil {
						slog.Warn("register_group: seed group dir", "folder", child.Folder, "err", err)
					}
				}
				slog.Info("group registered from prototype", "jid", jid, "folder", child.Folder, "sourceGroup", folder)
				return toolJSON(map[string]any{"registered": true, "folder": child.Folder, "jid": child.JID})
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
			if p, ok := groups[jid]; ok {
				parent = p.Folder
			}
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
				JID:     jid,
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
	}

	// escalate_group
	if len(grantslib.MatchingRules(rules, "escalate_group")) > 0 {
		srv.AddTool(mcp.NewTool("escalate_group",
			mcp.WithDescription(toolDesc("Escalate a task to the parent group", rules, "escalate_group")),
			mcp.WithString("prompt", mcp.Required()),
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithNumber("depth"),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if !grantslib.CheckAction(rules, "escalate_group", nil) {
				return toolErr("escalate_group: not permitted")
			}
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
			// Get last reply ID for threading
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
	}

	// refresh_groups — tier ≤ 2
	if id.Tier <= 2 {
		srv.AddTool(mcp.NewTool("refresh_groups",
			mcp.WithDescription("Reload the list of registered groups"),
		), func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if gated.GetGroups == nil {
				return toolErr("refresh_groups not configured")
			}
			groups := gated.GetGroups()
			type groupInfo struct {
				JID    string `json:"jid"`
				Folder string `json:"folder"`
				Name   string `json:"name"`
				Parent string `json:"parent,omitempty"`
			}
			out := make([]groupInfo, 0, len(groups))
			for jid, g := range groups {
				out = append(out, groupInfo{JID: jid, Folder: g.Folder, Name: g.Name, Parent: g.Parent})
			}
			return toolJSON(out)
		})
	}

	// delegate_group
	if len(grantslib.MatchingRules(rules, "delegate_group")) > 0 {
		srv.AddTool(mcp.NewTool("delegate_group",
			mcp.WithDescription(toolDesc("Delegate a task to a child group", rules, "delegate_group")),
			mcp.WithString("group", mcp.Required()),
			mcp.WithString("prompt", mcp.Required()),
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithNumber("depth"),
			mcp.WithString("grants"),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			target := req.GetString("group", "")
			if !grantslib.CheckAction(rules, "delegate_group", nil) {
				return toolErr("delegate_group: not permitted")
			}
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
	}

	// get_routes
	if len(grantslib.MatchingRules(rules, "get_routes")) > 0 {
		srv.AddTool(mcp.NewTool("get_routes",
			mcp.WithDescription(toolDesc("Get routes for a JID", rules, "get_routes")),
			mcp.WithString("jid", mcp.Required()),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if !grantslib.CheckAction(rules, "get_routes", nil) {
				return toolErr("get_routes: not permitted")
			}
			if db.GetRoutes == nil || gated.GetGroups == nil {
				return toolErr("get_routes not configured")
			}
			jid := req.GetString("jid", "")
			if jid == "" {
				return toolErr("jid required")
			}
			tf := groupFolderByJid(gated.GetGroups(), jid)
			if err := auth.Authorize(id, "get_routes", auth.AuthzTarget{RouteTarget: tf}); err != nil {
				return toolErr(err.Error())
			}
			return toolJSON(map[string]any{"jid": jid, "routes": db.GetRoutes(jid)})
		})
	}

	// set_routes
	if len(grantslib.MatchingRules(rules, "set_routes")) > 0 {
		srv.AddTool(mcp.NewTool("set_routes",
			mcp.WithDescription(toolDesc("Replace all routes for a JID", rules, "set_routes")),
			mcp.WithString("jid", mcp.Required()),
			mcp.WithString("routes", mcp.Required()),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if !grantslib.CheckAction(rules, "set_routes", nil) {
				return toolErr("set_routes: not permitted")
			}
			if db.SetRoutes == nil || gated.GetGroups == nil {
				return toolErr("set_routes not configured")
			}
			jid := req.GetString("jid", "")
			if jid == "" {
				return toolErr("jid required")
			}
			tf := groupFolderByJid(gated.GetGroups(), jid)
			if err := auth.Authorize(id, "set_routes", auth.AuthzTarget{RouteTarget: tf}); err != nil {
				return toolErr(err.Error())
			}
			var routes []core.Route
			if err := json.Unmarshal([]byte(req.GetString("routes", "")), &routes); err != nil {
				return toolErr("invalid routes json: " + err.Error())
			}
			if err := db.SetRoutes(jid, routes); err != nil {
				return toolErr(err.Error())
			}
			slog.Info("routes set", "jid", jid, "count", len(routes))
			return toolJSON(map[string]any{"updated": true, "count": len(routes)})
		})
	}

	// add_route
	if len(grantslib.MatchingRules(rules, "add_route")) > 0 {
		srv.AddTool(mcp.NewTool("add_route",
			mcp.WithDescription(toolDesc("Add a single route for a JID", rules, "add_route")),
			mcp.WithString("jid", mcp.Required()),
			mcp.WithString("route", mcp.Required()),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if !grantslib.CheckAction(rules, "add_route", nil) {
				return toolErr("add_route: not permitted")
			}
			if db.AddRoute == nil || gated.GetGroups == nil {
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
	}

	// delete_route
	if len(grantslib.MatchingRules(rules, "delete_route")) > 0 {
		srv.AddTool(mcp.NewTool("delete_route",
			mcp.WithDescription(toolDesc("Delete a route by ID", rules, "delete_route")),
			mcp.WithNumber("id", mcp.Required()),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if !grantslib.CheckAction(rules, "delete_route", nil) {
				return toolErr("delete_route: not permitted")
			}
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
			if err := auth.Authorize(id, "delete_route", auth.AuthzTarget{RouteTarget: route.Target}); err != nil {
				return toolErr(err.Error())
			}
			if err := db.DeleteRoute(rid); err != nil {
				return toolErr(err.Error())
			}
			slog.Info("route deleted", "id", rid)
			return toolJSON(map[string]any{"deleted": true, "id": rid})
		})
	}

	// schedule_task
	if len(grantslib.MatchingRules(rules, "schedule_task")) > 0 {
		srv.AddTool(mcp.NewTool("schedule_task",
			mcp.WithDescription(toolDesc(
				"Schedule a recurring or one-time task. cron: cron expression or interval in ms",
				rules, "schedule_task")),
			mcp.WithString("targetJid", mcp.Required()),
			mcp.WithString("prompt", mcp.Required()),
			mcp.WithString("cron"),
			mcp.WithString("contextMode"),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if !grantslib.CheckAction(rules, "schedule_task", nil) {
				return toolErr("schedule_task: not permitted")
			}
			if db.CreateTask == nil || gated.GetGroups == nil {
				return toolErr("schedule_task not configured")
			}
			targetJid := req.GetString("targetJid", "")
			cronExpr := req.GetString("cron", "")
			contextMode := req.GetString("contextMode", "group")
			if contextMode != "isolated" {
				contextMode = "group"
			}

			groups := gated.GetGroups()
			targetGroup, ok := groups[targetJid]
			if !ok {
				return toolErr("target group not registered")
			}
			targetFolder := targetGroup.Folder

			if err := auth.Authorize(id, "schedule_task", auth.AuthzTarget{TaskOwner: targetFolder}); err != nil {
				return toolErr(err.Error())
			}

			var nextRun *time.Time
			if cronExpr != "" {
				// interval ms: pure integer string
				if ms, err := strconv.ParseInt(cronExpr, 10, 64); err == nil && ms > 0 {
					t := time.Now().Add(time.Duration(ms) * time.Millisecond)
					nextRun = &t
				} else {
					p := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
					sched, err := p.Parse(cronExpr)
					if err != nil {
						return toolErr(fmt.Sprintf("invalid cron %q: %v", cronExpr, err))
					}
					t := sched.Next(time.Now())
					nextRun = &t
				}
			}

			taskID := fmt.Sprintf("task-%d-%s", time.Now().UnixMilli(), uuid.New().String()[:8])
			task := core.Task{
				ID: taskID, Owner: targetFolder, ChatJID: targetJid,
				Prompt: req.GetString("prompt", ""), Cron: cronExpr,
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
	}

	// pause_task
	if len(grantslib.MatchingRules(rules, "pause_task")) > 0 {
		srv.AddTool(mcp.NewTool("pause_task",
			mcp.WithDescription(toolDesc("Pause a scheduled task", rules, "pause_task")),
			mcp.WithString("taskId", mcp.Required()),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if !grantslib.CheckAction(rules, "pause_task", nil) {
				return toolErr("pause_task: not permitted")
			}
			if db.GetTask == nil || db.UpdateTaskStatus == nil {
				return toolErr("pause_task not configured")
			}
			taskID := req.GetString("taskId", "")
			task, ok := db.GetTask(taskID)
			if !ok {
				return toolErr("task not found")
			}
			if err := auth.Authorize(id, "pause_task", auth.AuthzTarget{TaskOwner: task.Owner}); err != nil {
				return toolErr(err.Error())
			}
			if err := db.UpdateTaskStatus(taskID, core.TaskPaused); err != nil {
				return toolErr(err.Error())
			}
			slog.Info("task paused via mcp", "taskId", taskID, "sourceGroup", folder)
			return toolJSON(map[string]any{"ok": true})
		})
	}

	// resume_task
	if len(grantslib.MatchingRules(rules, "resume_task")) > 0 {
		srv.AddTool(mcp.NewTool("resume_task",
			mcp.WithDescription(toolDesc("Resume a paused scheduled task", rules, "resume_task")),
			mcp.WithString("taskId", mcp.Required()),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if !grantslib.CheckAction(rules, "resume_task", nil) {
				return toolErr("resume_task: not permitted")
			}
			if db.GetTask == nil || db.UpdateTaskStatus == nil {
				return toolErr("resume_task not configured")
			}
			taskID := req.GetString("taskId", "")
			task, ok := db.GetTask(taskID)
			if !ok {
				return toolErr("task not found")
			}
			if err := auth.Authorize(id, "resume_task", auth.AuthzTarget{TaskOwner: task.Owner}); err != nil {
				return toolErr(err.Error())
			}
			if err := db.UpdateTaskStatus(taskID, core.TaskActive); err != nil {
				return toolErr(err.Error())
			}
			slog.Info("task resumed via mcp", "taskId", taskID, "sourceGroup", folder)
			return toolJSON(map[string]any{"ok": true})
		})
	}

	// cancel_task
	if len(grantslib.MatchingRules(rules, "cancel_task")) > 0 {
		srv.AddTool(mcp.NewTool("cancel_task",
			mcp.WithDescription(toolDesc("Cancel and delete a scheduled task", rules, "cancel_task")),
			mcp.WithString("taskId", mcp.Required()),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if !grantslib.CheckAction(rules, "cancel_task", nil) {
				return toolErr("cancel_task: not permitted")
			}
			if db.GetTask == nil || db.DeleteTask == nil {
				return toolErr("cancel_task not configured")
			}
			taskID := req.GetString("taskId", "")
			task, ok := db.GetTask(taskID)
			if !ok {
				return toolErr("task not found")
			}
			if err := auth.Authorize(id, "cancel_task", auth.AuthzTarget{TaskOwner: task.Owner}); err != nil {
				return toolErr(err.Error())
			}
			if err := db.DeleteTask(taskID); err != nil {
				return toolErr(err.Error())
			}
			slog.Info("task cancelled via mcp", "taskId", taskID, "sourceGroup", folder)
			return toolJSON(map[string]any{"ok": true})
		})
	}

	// list_tasks
	if len(grantslib.MatchingRules(rules, "list_tasks")) > 0 {
		srv.AddTool(mcp.NewTool("list_tasks",
			mcp.WithDescription(toolDesc("List scheduled tasks visible to this group", rules, "list_tasks")),
		), func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if !grantslib.CheckAction(rules, "list_tasks", nil) {
				return toolErr("list_tasks: not permitted")
			}
			if db.ListTasks == nil {
				return toolErr("list_tasks not configured")
			}
			return toolJSON(db.ListTasks(folder, id.Tier == 0))
		})
	}

	// get_grants / set_grants — tier 0-1 only
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

	return srv
}
