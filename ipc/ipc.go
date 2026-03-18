package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/onvos/arizuko/auth"
	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/mountsec"
	"github.com/robfig/cron/v3"
)

type GatedFns struct {
	SendMessage      func(jid, text string) error
	SendDocument     func(jid, path, filename string) error
	ClearSession     func(folder string)
	InjectMessage    func(jid, content, sender, senderName string) (string, error)
	RegisterGroup    func(jid string, group core.Group) error
	GetGroups        func() map[string]core.Group
	DelegateToChild  func(folder, prompt, jid string, depth int) error
	DelegateToParent func(folder, prompt, jid string, depth int) error
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
}

func ServeMCP(sockPath string, gated GatedFns, db StoreFns, folder string) (func(), error) {
	os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	os.Chmod(sockPath, 0600)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				srv := buildMCPServer(gated, db, folder)
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

func buildMCPServer(gated GatedFns, db StoreFns, folder string) *server.MCPServer {
	id := auth.Resolve(folder)
	srv := server.NewMCPServer("nanoclaw", "1.0")

	// send_message
	srv.AddTool(mcp.NewTool("send_message",
		mcp.WithDescription("Send a text message to a chat"),
		mcp.WithString("chatJid", mcp.Required()),
		mcp.WithString("text", mcp.Required()),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := gated.SendMessage(req.GetString("chatJid", ""), req.GetString("text", "")); err != nil {
			return toolErr(err.Error())
		}
		return toolOK()
	})

	// send_file
	srv.AddTool(mcp.NewTool("send_file",
		mcp.WithDescription("Send a file to a chat"),
		mcp.WithString("chatJid", mcp.Required()),
		mcp.WithString("filepath", mcp.Required()),
		mcp.WithString("filename"),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		jid := req.GetString("chatJid", "")
		fp := req.GetString("filepath", "")
		name := req.GetString("filename", "")
		rel := strings.TrimPrefix(strings.TrimPrefix(fp, "/workspace/group/"), "/workspace/group")
		localPath := filepath.Join(gated.GroupsDir, folder, rel)
		hostPath := filepath.Join(gated.HostGroupsDir, folder, rel)
		if _, err := mountsec.ValidateFilePath(localPath, filepath.Join(gated.GroupsDir, folder)); err != nil {
			return toolErr("path outside group dir")
		}
		if !strings.HasPrefix(hostPath, filepath.Join(gated.HostGroupsDir, folder)+"/") {
			return toolErr("path outside group dir")
		}
		if err := gated.SendDocument(jid, localPath, name); err != nil {
			return toolErr(err.Error())
		}
		return toolOK()
	})

	// reset_session
	srv.AddTool(mcp.NewTool("reset_session",
		mcp.WithDescription("Clear the agent session for a group"),
		mcp.WithString("groupFolder", mcp.Required()),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		gf := req.GetString("groupFolder", "")
		if gf == "" {
			return toolErr("missing groupFolder")
		}
		if err := auth.Authorize(id, "reset_session", auth.AuthzTarget{TargetFolder: gf}); err != nil {
			return toolErr(err.Error())
		}
		gated.ClearSession(gf)
		return toolOK()
	})

	// inject_message
	srv.AddTool(mcp.NewTool("inject_message",
		mcp.WithDescription("Inject a message into the store for a chat"),
		mcp.WithString("chatJid", mcp.Required()),
		mcp.WithString("content", mcp.Required()),
		mcp.WithString("sender"),
		mcp.WithString("senderName"),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := auth.Authorize(id, "inject_message", auth.AuthzTarget{}); err != nil {
			return toolErr(err.Error())
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

	// register_group
	srv.AddTool(mcp.NewTool("register_group",
		mcp.WithDescription("Register a new agent group"),
		mcp.WithString("jid", mcp.Required()),
		mcp.WithString("name", mcp.Required()),
		mcp.WithString("folder", mcp.Required()),
		mcp.WithString("parent"),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if gated.RegisterGroup == nil {
			return toolErr("register_group not configured")
		}
		jid := req.GetString("jid", "")
		gfld := req.GetString("folder", "")

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
			Name:    req.GetString("name", ""),
			Folder:  gfld,
			AddedAt: time.Now(),
			Parent:  req.GetString("parent", ""),
		}
		if err := gated.RegisterGroup(jid, gr); err != nil {
			return toolErr(err.Error())
		}
		slog.Info("group registered", "jid", jid, "folder", gfld, "sourceGroup", folder)
		return toolJSON(map[string]any{"registered": true})
	})

	// escalate_group
	srv.AddTool(mcp.NewTool("escalate_group",
		mcp.WithDescription("Escalate a task to the parent group"),
		mcp.WithString("prompt", mcp.Required()),
		mcp.WithString("chatJid", mcp.Required()),
		mcp.WithNumber("depth"),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := auth.Authorize(id, "escalate_group", auth.AuthzTarget{}); err != nil {
			return toolErr(err.Error())
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
		slog.Info("escalating to parent", "sourceGroup", folder, "parent", parent, "depth", depth)
		if err := gated.DelegateToParent(parent, prompt, chatJid, depth+1); err != nil {
			return toolErr(err.Error())
		}
		return toolJSON(map[string]any{"queued": true, "parent": parent})
	})

	// delegate_group
	srv.AddTool(mcp.NewTool("delegate_group",
		mcp.WithDescription("Delegate a task to a child group"),
		mcp.WithString("group", mcp.Required()),
		mcp.WithString("prompt", mcp.Required()),
		mcp.WithString("chatJid", mcp.Required()),
		mcp.WithNumber("depth"),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
		slog.Info("delegating to child", "sourceGroup", folder, "child", target, "depth", depth)
		if err := gated.DelegateToChild(target, prompt, chatJid, depth+1); err != nil {
			return toolErr(err.Error())
		}
		return toolJSON(map[string]any{"queued": true})
	})

	// get_routes
	srv.AddTool(mcp.NewTool("get_routes",
		mcp.WithDescription("Get routes for a JID"),
		mcp.WithString("jid", mcp.Required()),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := auth.Authorize(id, "get_routes", auth.AuthzTarget{}); err != nil {
			return toolErr(err.Error())
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

	// set_routes
	srv.AddTool(mcp.NewTool("set_routes",
		mcp.WithDescription("Replace all routes for a JID"),
		mcp.WithString("jid", mcp.Required()),
		mcp.WithString("routes", mcp.Required()),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := auth.Authorize(id, "set_routes", auth.AuthzTarget{}); err != nil {
			return toolErr(err.Error())
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

	// add_route
	srv.AddTool(mcp.NewTool("add_route",
		mcp.WithDescription("Add a single route for a JID"),
		mcp.WithString("jid", mcp.Required()),
		mcp.WithString("route", mcp.Required()),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := auth.Authorize(id, "add_route", auth.AuthzTarget{}); err != nil {
			return toolErr(err.Error())
		}
		if db.AddRoute == nil || gated.GetGroups == nil {
			return toolErr("add_route not configured")
		}
		jid := req.GetString("jid", "")
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

	// delete_route
	srv.AddTool(mcp.NewTool("delete_route",
		mcp.WithDescription("Delete a route by ID"),
		mcp.WithNumber("id", mcp.Required()),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := auth.Authorize(id, "delete_route", auth.AuthzTarget{}); err != nil {
			return toolErr(err.Error())
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

	// schedule_task
	srv.AddTool(mcp.NewTool("schedule_task",
		mcp.WithDescription("Schedule a recurring or one-time task"),
		mcp.WithString("targetJid", mcp.Required()),
		mcp.WithString("prompt", mcp.Required()),
		mcp.WithString("cron"),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if db.CreateTask == nil || gated.GetGroups == nil {
			return toolErr("schedule_task not configured")
		}
		targetJid := req.GetString("targetJid", "")
		cronExpr := req.GetString("cron", "")

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
			p := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
			sched, err := p.Parse(cronExpr)
			if err != nil {
				return toolErr(fmt.Sprintf("invalid cron %q: %v", cronExpr, err))
			}
			t := sched.Next(time.Now())
			nextRun = &t
		}

		taskID := fmt.Sprintf("task-%d-%s", time.Now().UnixMilli(), uuid.New().String()[:8])
		task := core.Task{
			ID: taskID, Owner: targetFolder, ChatJID: targetJid,
			Prompt: req.GetString("prompt", ""), Cron: cronExpr,
			NextRun: nextRun, Status: core.TaskActive, Created: time.Now(),
		}
		if err := db.CreateTask(task); err != nil {
			return toolErr(err.Error())
		}
		slog.Info("task created via mcp", "taskId", taskID, "sourceGroup", folder,
			"targetFolder", targetFolder)
		return toolJSON(map[string]any{"taskId": taskID})
	})

	// pause_task
	srv.AddTool(mcp.NewTool("pause_task",
		mcp.WithDescription("Pause a scheduled task"),
		mcp.WithString("taskId", mcp.Required()),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

	// resume_task
	srv.AddTool(mcp.NewTool("resume_task",
		mcp.WithDescription("Resume a paused scheduled task"),
		mcp.WithString("taskId", mcp.Required()),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

	// cancel_task
	srv.AddTool(mcp.NewTool("cancel_task",
		mcp.WithDescription("Cancel and delete a scheduled task"),
		mcp.WithString("taskId", mcp.Required()),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

	// list_tasks
	srv.AddTool(mcp.NewTool("list_tasks",
		mcp.WithDescription("List scheduled tasks visible to this group"),
	), func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if db.ListTasks == nil {
			return toolErr("list_tasks not configured")
		}
		return toolJSON(db.ListTasks(folder, id.Tier == 0))
	})

	return srv
}
