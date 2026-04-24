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
	"golang.org/x/sys/unix"
)

type GatedFns struct {
	SendMessage         func(jid, text string) (string, error)
	SendReply           func(jid, text, replyToId string) (string, error)
	SendDocument        func(jid, path, filename, caption string) error
	Post                func(jid, content string, mediaPaths []string) (string, error)
	React               func(jid, targetID, reaction string) error
	DeletePost          func(jid, targetID string) error
	ClearSession        func(folder string)
	InjectMessage       func(jid, content, sender, senderName string) (string, error)
	RegisterGroup       func(jid string, group core.Group) error
	SetupGroup          func(folder string) error
	GetGroups           func() map[string]core.Group
	EnqueueMessageCheck func(jid string)
	SpawnGroup          func(parentFolder, childJID string) (core.Group, error)
	UpdateGroupConfig   func(folder string, cfg core.GroupConfig) error
	FetchPlatformHistory func(jid string, before time.Time, limit int) (PlatformHistory, error)
	GroupsDir           string
	WebDir              string
}

// PlatformHistory is the decoded channel-side history response surfaced to
// the agent. Source is "platform", "platform-capped", "cache-only",
// "unsupported", or "cache" (adapter-unreachable fallback).
type PlatformHistory struct {
	Source   string         `json:"source"`
	Cap      string         `json:"cap,omitempty"`
	Messages []core.Message `json:"messages"`
}

type StoreFns struct {
	CreateTask          func(t core.Task) error
	GetTask             func(id string) (core.Task, bool)
	UpdateTaskStatus    func(id, status string) error
	DeleteTask          func(id string) error
	ListTasks           func(folder string, isRoot bool) []core.Task
	ListRoutes          func(folder string, isRoot bool) []core.Route
	SetRoutes           func(folder string, routes []core.Route) error
	AddRoute            func(r core.Route) (int64, error)
	DeleteRoute         func(id int64) error
	GetRoute            func(id int64) (core.Route, bool)
	DefaultFolderForJID func(jid string) string
	PutMessage          func(m core.Message) error
	GetLastReplyID      func(jid, topic string) string
	SetLastReplyID      func(jid, topic, replyID string)
	GetGrants           func(folder string) []string
	SetGrants           func(folder string, rules []string) error
	MessagesBefore      func(jid string, before time.Time, limit int) ([]core.Message, error)
	JIDRoutedToFolder   func(jid, folder string) bool
	ErroredChats        func(folder string, isRoot bool) []ErroredChat
	TaskRunLogs         func(taskID string, limit int) []TaskRunLog
	RecentSessions      func(folder string, n int) []core.SessionRecord
	GetSession          func(folder, topic string) (string, bool)
}

// ErroredChat mirrors store.ErroredChat to avoid an ipc→store import.
type ErroredChat struct {
	ChatJID  string    `json:"chat_jid"`
	Count    int       `json:"count"`
	LastAt   time.Time `json:"last_at"`
	RoutedTo string    `json:"routed_to"`
}

// TaskRunLog mirrors store.TaskRunLog.
type TaskRunLog struct {
	ID         int64     `json:"id"`
	TaskID     string    `json:"task_id"`
	RunAt      time.Time `json:"run_at"`
	DurationMS int64     `json:"duration_ms"`
	Status     string    `json:"status"`
	Result     string    `json:"result,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// maxMCPConns bounds concurrent in-flight MCP connections per group.
const maxMCPConns = 8

// ServeMCP binds the group MCP socket (0660, chowned to expectedUID),
// bounds accept fan-out, and verifies each peer via SO_PEERCRED. Only
// connections whose kernel-attested peer uid matches expectedUID are
// accepted. expectedUID <= 0 disables the check (dev/tests).
func ServeMCP(sockPath string, gated GatedFns, db StoreFns, folder string, rules []string, expectedUID int) (func(), error) {
	os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	os.Chmod(sockPath, 0o660)
	if expectedUID > 0 {
		os.Chown(sockPath, expectedUID, -1)
	}
	slog.Info("mcp server listening",
		"folder", folder, "sock", sockPath, "peer_uid", expectedUID)

	// Build server once; every connection multiplexes onto it.
	srv := buildMCPServer(gated, db, folder, rules)
	stdioSrv := server.NewStdioServer(srv)

	ctx, cancel := context.WithCancel(context.Background())
	sem := make(chan struct{}, maxMCPConns)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			select {
			case sem <- struct{}{}:
			default:
				slog.Warn("mcp conn limit reached; rejecting",
					"folder", folder, "limit", maxMCPConns)
				conn.Close()
				continue
			}
			go func(c net.Conn) {
				defer func() { <-sem; c.Close() }()
				if expectedUID > 0 {
					cred, err := peerCred(c)
					if err != nil {
						slog.Warn("mcp peer cred read failed",
							"folder", folder, "err", err)
						return
					}
					if int(cred.Uid) != expectedUID {
						slog.Warn("mcp peer uid mismatch",
							"folder", folder,
							"want", expectedUID, "got", cred.Uid,
							"pid", cred.Pid)
						return
					}
				}
				stdioSrv.Listen(ctx, c, c)
			}(conn)
		}
	}()

	return func() {
		cancel()
		ln.Close()
		os.Remove(sockPath)
	}, nil
}

// peerCred returns SO_PEERCRED for a unix-socket peer — kernel-attested
// pid/uid/gid of the connecting process.
func peerCred(c net.Conn) (*unix.Ucred, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return nil, fmt.Errorf("not a unix conn")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return nil, fmt.Errorf("syscall conn: %w", err)
	}
	var cred *unix.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptUcred(int(fd),
			unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return nil, err
	}
	return cred, cerr
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

func recordOutbound(db StoreFns, jid, text, replyToID, platformID, folder, topic string) {
	if platformID != "" && db.SetLastReplyID != nil {
		db.SetLastReplyID(jid, topic, platformID)
	}
	if db.PutMessage != nil {
		db.PutMessage(core.Message{
			ID:        core.MsgID("mcp"),
			ChatJID:   jid,
			Sender:    folder,
			Content:   text,
			Timestamp: time.Now(),
			FromMe:    true,
			BotMsg:    true,
			ReplyToID: platformID,
			RoutedTo:  jid,
			Topic:     topic,
		})
	}
}

func folderForJid(db StoreFns, jid string) string {
	if db.DefaultFolderForJID == nil {
		return ""
	}
	return db.DefaultFolderForJID(jid)
}

// routeTargetWithin reports whether target refers to owner's own folder
// or a descendant. Typed prefixes (folder:, daemon:, builtin:) are
// normalized before comparison; daemon:/builtin: targets are never
// considered inside a user folder.
func routeTargetWithin(target, owner string) bool {
	switch {
	case strings.HasPrefix(target, "folder:"):
		target = strings.TrimPrefix(target, "folder:")
	case strings.HasPrefix(target, "daemon:"), strings.HasPrefix(target, "builtin:"):
		return false
	}
	return target == owner || strings.HasPrefix(target, owner+"/")
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
	fp := filepath.Join(webDir, "vhosts.json")
	tmp := fp + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, fp); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func buildMCPServer(gated GatedFns, db StoreFns, folder string, rules []string) *server.MCPServer {
	id := auth.Resolve(folder)
	srv := server.NewMCPServer("arizuko", "1.0")

	// registerRaw adds a tool if any rule matches; granted additionally
	// wraps with a CheckAction(nil-params) gate. Send-* tools use
	// registerRaw because they do param-aware CheckAction themselves.
	registerRaw := func(name, desc string, opts []mcp.ToolOption, h server.ToolHandlerFunc) {
		matching := grantslib.MatchingRules(rules, name)
		if len(matching) == 0 {
			return
		}
		data, _ := json.Marshal(matching)
		desc = desc + "\ngrants: " + string(data)
		all := append([]mcp.ToolOption{mcp.WithDescription(desc)}, opts...)
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

	registerRaw("send_message", "Deliver a new top-level message to a chat. Use for the normal reply to the user's last message or for a proactive notification. Not for threaded replies to a specific earlier message (send_reply) or file delivery (send_file — its caption replaces this call).",
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
			recordOutbound(db, jid, text, "", platformID, folder, "")
			return toolOK()
		})

	registerRaw("send_reply", "Deliver a message threaded to a specific earlier message (quote/reply UI on the platform). Use when disambiguating which message you're answering in an active chat, or when replyToId is known. Defaults to the last outbound reply id if omitted. Not for fresh top-level messages (send_message).",
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
			recordOutbound(db, jid, text, replyToID, platformID, folder, "")
			return toolOK()
		})

	registerRaw("send_file", "Deliver a file from the group workspace (~/) to a chat. Works on every platform whose channel registered the tool — don't second-guess by platform name (telegram, discord, whatsapp all supported). Use when the user asked for a file (image, doc, CSV, audio) or when output would exceed a chat-reasonable length. `caption` IS the accompanying message — never follow with send_message. Not for inline text the user can read in-chat.",
		[]mcp.ToolOption{
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("filepath", mcp.Required()),
			mcp.WithString("filename"),
			mcp.WithString("caption", mcp.Description("Message text to accompany the file. This IS the message — do not output separate text.")),
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
			groupRoot := filepath.Join(gated.GroupsDir, folder)
			if _, err := mountsec.ValidateFilePath(localPath, groupRoot); err != nil {
				return toolErr("path outside group dir")
			}
			slog.Info("send_file", "folder", folder, "jid", jid, "path", localPath)
			if err := gated.SendDocument(jid, localPath, name, caption); err != nil {
				return toolErr(err.Error())
			}
			return toolOK()
		})

	granted("reset_session", "Drop the Claude session for a group so the next message starts fresh context. Use when the user asks for /new, when context is confused/polluted, or before a topic switch. Not for injecting content (inject_message) — this discards, it doesn't add.",
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

	granted("inject_message", "Write a synthetic inbound message into the store as if received from chat, triggering the normal agent loop. Use for programmatic prompts, tests, or scheduling one-off runs from tool code. Not for clearing context (reset_session) or sending output to users (send_message).",
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
		"Create a child agent group and route a jid to it. Use when onboarding a new chat into its own isolated workspace/session, or when spinning up a sub-agent from this group's prototype/ (fromPrototype=true). Not for promoting work up (escalate_group) or handing a task to an existing child (delegate_group).",
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
				if gated.SetupGroup != nil {
					if err := gated.SetupGroup(child.Folder); err != nil {
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
			if pg, ok := groups[folder]; ok {
				if err := auth.CheckSpawnAllowed(pg, groups); err != nil {
					return toolErr(err.Error())
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
			if gated.SetupGroup != nil {
				if err := gated.SetupGroup(gfld); err != nil {
					slog.Warn("register_group: seed group dir", "folder", gfld, "err", err)
				}
			}
			slog.Info("group registered", "jid", jid, "folder", gfld, "sourceGroup", folder)
			return toolJSON(map[string]any{"registered": true, "folder": gfld, "jid": jid})
		})

	granted("escalate_group", "Hand a prompt up to this group's parent folder; the parent responds back through this child. Use when the request exceeds this group's authority/tier or needs operator review. Not for peer/child handoff (delegate_group) or creating a new group (register_group). Depth capped at 1.",
		[]mcp.ToolOption{
			mcp.WithString("prompt", mcp.Required()),
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithNumber("depth"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.PutMessage == nil || gated.EnqueueMessageCheck == nil {
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
			// Return address: response goes back to this child via local channel
			fwdFrom := "local:" + folder
			if err := db.PutMessage(core.Message{
				ID:            core.MsgID("escalate"),
				ChatJID:       "local:" + parent,
				Sender:        "escalate",
				Content:       wrapped,
				Timestamp:     time.Now(),
				ForwardedFrom: fwdFrom,
			}); err != nil {
				return toolErr(err.Error())
			}
			gated.EnqueueMessageCheck("local:" + parent)
			return toolJSON(map[string]any{"queued": true, "parent": parent})
		})

	if id.Tier <= 2 {
		srv.AddTool(mcp.NewTool("refresh_groups",
			mcp.WithDescription("Return folder/name/parent for every registered group. Use to discover delegation targets or audit the group tree. Not for routing details (inspect_routing) or per-group tasks (inspect_tasks)."),
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

	granted("delegate_group", "Hand a prompt down to a specific child group for async execution; the child runs in its own session and workspace. Use to offload specialist work to an existing child without blocking this chat. Not for parent handoff (escalate_group) or creating the child (register_group). Depth capped at 1.",
		[]mcp.ToolOption{
			mcp.WithString("group", mcp.Required()),
			mcp.WithString("prompt", mcp.Required()),
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithNumber("depth"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			target := req.GetString("group", "")
			if err := auth.Authorize(id, "delegate_group", auth.AuthzTarget{TargetFolder: target}); err != nil {
				return toolErr(err.Error())
			}
			if db.PutMessage == nil || gated.EnqueueMessageCheck == nil {
				return toolErr("delegate_group not configured")
			}
			prompt := req.GetString("prompt", "")
			chatJid := req.GetString("chatJid", "")
			depth := req.GetInt("depth", 0)
			if depth >= 1 {
				return toolErr(fmt.Sprintf("delegation depth %d exceeds limit 1", depth))
			}

			slog.Info("delegating to child", "sourceGroup", folder, "child", target, "depth", depth)
			if err := db.PutMessage(core.Message{
				ID:            core.MsgID("delegate"),
				ChatJID:       "local:" + target,
				Sender:        "delegate",
				Content:       prompt,
				Timestamp:     time.Now(),
				ForwardedFrom: chatJid,
			}); err != nil {
				return toolErr(err.Error())
			}
			gated.EnqueueMessageCheck("local:" + target)
			return toolJSON(map[string]any{"queued": true})
		})

	granted("list_routes", "Return the routing table rows this group can see. Use for a raw route dump; prefer inspect_routing when you also want JID→folder resolution or errored-chat context.", nil,
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.ListRoutes == nil {
				return toolErr("list_routes not configured")
			}
			return toolJSON(map[string]any{"routes": db.ListRoutes(folder, id.Tier == 0)})
		})

	granted("set_routes",
		"Bulk-overwrite the full routing table for this folder subtree. Use only for wholesale reconfiguration where you've already read the current set. Prefer add_route/delete_route for targeted edits — this clobbers everything else. "+
			"Each route: seq (int), match ('key=glob' pairs; keys: platform, room, chat_jid, sender, verb), target (folder path, or folder:/daemon:/builtin: prefix).",
		[]mcp.ToolOption{mcp.WithString("routes", mcp.Required())},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.SetRoutes == nil {
				return toolErr("set_routes not configured")
			}
			if err := auth.Authorize(id, "set_routes", auth.AuthzTarget{RouteTarget: id.Folder}); err != nil {
				return toolErr(err.Error())
			}
			var routes []core.Route
			if err := json.Unmarshal([]byte(req.GetString("routes", "")), &routes); err != nil {
				return toolErr("invalid routes json: " + err.Error())
			}
			for _, r := range routes {
				if !routeTargetWithin(r.Target, id.Folder) {
					return toolErr("route target outside own folder: " + r.Target)
				}
			}
			if err := db.SetRoutes(id.Folder, routes); err != nil {
				return toolErr(err.Error())
			}
			slog.Info("routes set", "folder", id.Folder, "count", len(routes))
			return toolJSON(map[string]any{"updated": true, "count": len(routes)})
		})

	granted("add_route",
		"Append one routing rule. Use for targeted routing changes (route one chat, one platform pattern) — preferred over set_routes for everything except full rewrites. "+
			"Fields: seq (int), match ('key=glob' pairs; keys: platform, room, chat_jid, sender, verb), target (folder path, or folder:/daemon:/builtin: prefix).",
		[]mcp.ToolOption{mcp.WithString("route", mcp.Required())},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.AddRoute == nil {
				return toolErr("add_route not configured")
			}
			var route core.Route
			if err := json.Unmarshal([]byte(req.GetString("route", "")), &route); err != nil {
				return toolErr("invalid route json")
			}
			if route.Target == "" {
				return toolErr("route.target required")
			}
			if err := auth.Authorize(id, "add_route", auth.AuthzTarget{RouteTarget: route.Target}); err != nil {
				return toolErr(err.Error())
			}
			rid, err := db.AddRoute(route)
			if err != nil {
				return toolErr(err.Error())
			}
			slog.Info("route added", "id", rid, "target", route.Target, "match", route.Match)
			return toolJSON(map[string]any{"id": rid})
		})

	granted("delete_route", "Remove one routing rule by id. Use after list_routes/inspect_routing to surgically drop a rule. Not for bulk clear (set_routes with empty array).",
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
		"Create a scheduled prompt that fires against a target chat. Use when the user asks for reminders, recurring checks, or deferred work. `cron` accepts a 5-field cron expression, an integer millisecond interval, or an RFC3339 timestamp for a one-shot. Not for immediate execution (send_message/inject_message).",
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
		{"pause_task", "Mark a scheduled task paused so it stops firing but is preserved. Use when suspending a task temporarily. Not for permanent removal (cancel_task).", func(tid string) error {
			return db.UpdateTaskStatus(tid, core.TaskPaused)
		}},
		{"resume_task", "Re-activate a paused task so it resumes firing on its schedule. Use to undo pause_task. No effect on already-active or cancelled tasks.", func(tid string) error {
			return db.UpdateTaskStatus(tid, core.TaskActive)
		}},
		{"cancel_task", "Permanently delete a scheduled task. Use when the task is no longer wanted. Not for temporary suspension (pause_task) — this cannot be undone.", func(tid string) error {
			return db.DeleteTask(tid)
		}},
	}
	if db.GetTask != nil {
		for _, op := range taskOps {
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
					slog.Info("task op via mcp", "op", op.name, "taskId", taskID, "sourceGroup", folder)
					return toolJSON(map[string]any{"ok": true})
				})
		}
	}

	granted("list_tasks", "Return scheduled tasks visible to this group. Use for a plain task dump; prefer inspect_tasks when you also want task_run_logs or per-task history.", nil,
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.ListTasks == nil {
				return toolErr("list_tasks not configured")
			}
			return toolJSON(db.ListTasks(folder, id.Tier == 0))
		})

	if id.Tier <= 1 {
		srv.AddTool(mcp.NewTool("get_grants",
			mcp.WithDescription("Read the MCP grant rule list for a folder. Use to audit what a group is permitted to do before adjusting. Tier 0-1 only. Pair with set_grants to change."),
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
			mcp.WithDescription("Overwrite the full grant rule list for a folder. Use when changing what tools/targets a group may invoke; read first with get_grants. Tier 0-1 only. Takes effect on next MCP session."),
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
		inspectMessages := func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
				return toolErr("inspect_messages: " + err.Error())
			}
			oldest := ""
			if len(msgs) > 0 {
				oldest = msgs[0].Timestamp.Format(time.RFC3339)
			}
			return toolJSON(map[string]any{
				"messages": router.FormatMessages(msgs),
				"count":    len(msgs),
				"oldest":   oldest,
				"source":   "local-db",
			})
		}
		srv.AddTool(mcp.NewTool("inspect_messages",
			mcp.WithDescription("Return rows from the local messages.db for one chat_jid, including outbound/bot rows and errored entries. Use for routing/delivery audits or to verify what the store recorded. Not for conversation context before replying (fetch_history) — this shows DB truth, not platform history."),
			mcp.WithString("chat_jid", mcp.Required()),
			mcp.WithNumber("limit"),
			mcp.WithString("before"),
		), inspectMessages)
		// Back-compat alias; remove after next agent release.
		srv.AddTool(mcp.NewTool("get_history",
			mcp.WithDescription("DEPRECATED alias for inspect_messages. Do not use in new code — pick inspect_messages for DB audit or fetch_history for conversation context."),
			mcp.WithString("chat_jid", mcp.Required()),
			mcp.WithNumber("limit"),
			mcp.WithString("before"),
		), inspectMessages)
	}

	if gated.FetchPlatformHistory != nil {
		srv.AddTool(mcp.NewTool("fetch_history",
			mcp.WithDescription("Pull authoritative conversation history from the channel adapter and cache it. Use to reconstruct context before replying, especially on first contact or after a reset_session. Falls back to local cache if the adapter is down. Not for DB/routing audits (inspect_messages)."),
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
			h, err := gated.FetchPlatformHistory(jid, before, limitVal)
			if err != nil {
				return toolErr("fetch_history: " + err.Error())
			}
			oldest := ""
			if len(h.Messages) > 0 {
				oldest = h.Messages[0].Timestamp.Format(time.RFC3339)
			}
			return toolJSON(map[string]any{
				"messages": router.FormatMessages(h.Messages),
				"count":    len(h.Messages),
				"oldest":   oldest,
				"source":   h.Source,
				"cap":      h.Cap,
			})
		})
	}

	if id.Tier == 0 {
		granted("set_web_host", "Bind a hostname to a group folder in vhosts.json so proxyd serves that folder at that host. Use when exposing a group's web/ via a custom domain. Tier 0 only.",
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

	srv.AddTool(mcp.NewTool("get_work",
		mcp.WithDescription("Read this group's work.md — current work, blockers, next steps. Use at the start of a turn to recover what was in-flight. Returns empty content when the file doesn't exist."),
	), func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if gated.GroupsDir == "" {
			return toolErr("get_work not configured")
		}
		data, err := os.ReadFile(filepath.Join(gated.GroupsDir, folder, "work.md"))
		if err != nil {
			if os.IsNotExist(err) {
				return toolJSON(map[string]any{"content": "", "exists": false})
			}
			return toolErr(err.Error())
		}
		return toolJSON(map[string]any{"content": string(data), "exists": true})
	})

	if id.Tier <= 2 {
		srv.AddTool(mcp.NewTool("set_work",
			mcp.WithDescription("Overwrite this group's work.md with a fresh snapshot of current work, blockers, and next steps. Use at turn end to checkpoint state for the next session. This replaces the file — read with get_work first if merging."),
			mcp.WithString("content", mcp.Required()),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if gated.GroupsDir == "" {
				return toolErr("set_work not configured")
			}
			content := req.GetString("content", "")
			groupDir := filepath.Join(gated.GroupsDir, folder)
			if err := os.MkdirAll(groupDir, 0o755); err != nil {
				return toolErr(err.Error())
			}
			p := filepath.Join(groupDir, "work.md")
			tmp := p + ".tmp"
			if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
				return toolErr(err.Error())
			}
			if err := os.Rename(tmp, p); err != nil {
				os.Remove(tmp)
				return toolErr(err.Error())
			}
			slog.Info("set_work", "folder", folder, "bytes", len(content))
			return toolOK()
		})
	}

	registerInspect(srv, db, id, folder)

	if id.Tier <= 1 {
		granted("get_web_host", "Return the hostname currently bound to a folder (or this folder by default). Use to verify vhost wiring before pointing users at a URL. Tier 0-1; non-root can only query own folder.",
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
