package ipc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	grantslib "github.com/kronael/arizuko/grants"
	"github.com/kronael/arizuko/mountsec"
	"github.com/kronael/arizuko/router"
	"github.com/robfig/cron/v3"
	"golang.org/x/sys/unix"
)

type GatedFns struct {
	SendMessage         func(jid, text string) (string, error)
	SendReply           func(jid, text, replyToId string) (string, error)
	SendDocument        func(jid, path, filename, caption string) error
	SendVoice           func(jid, text, voice, folder string) (string, error)
	Post                func(jid, content string, mediaPaths []string) (string, error)
	Like                func(jid, targetID, reaction string) error
	Delete              func(jid, targetID string) error
	Forward             func(sourceMsgID, targetJID, comment string) (string, error)
	Quote               func(jid, sourceMsgID, comment string) (string, error)
	Repost              func(jid, sourceMsgID string) (string, error)
	Dislike             func(jid, targetID string) error
	Edit                func(jid, targetID, content string) error
	ClearSession        func(folder string)
	InjectMessage       func(jid, content, sender, senderName string) (string, error)
	RegisterGroup       func(jid string, group core.Group) error
	SetupGroup          func(folder string) error
	GetGroups           func() map[string]core.Group
	EnqueueMessageCheck func(jid string)
	SpawnGroup          func(parentFolder, childJID string) (core.Group, error)
	UpdateGroupConfig   func(folder string, cfg core.GroupConfig) error
	FetchPlatformHistory func(jid string, before time.Time, limit int) (PlatformHistory, error)
	CreateInvite         func(targetGlob, issuedBySub string, maxUses int, expiresAt *time.Time) (InviteInfo, error)
	SubmitTurn           func(folder string, t TurnResult) error
	AcceptURLBase        string // base URL where /invite/<token> is served (e.g. https://app.example.com)
	GroupsDir            string
	WebDir               string
}

// TurnResult is the agent-submitted turn payload. The MCP `submit_turn`
// method (hidden from tools/list) deserialises into this and calls
// GatedFns.SubmitTurn. Idempotency is enforced by gated on (folder, TurnID).
type TurnResult struct {
	TurnID    string `json:"turn_id"`
	SessionID string `json:"session_id,omitempty"`
	Status    string `json:"status"` // success | error
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
}

// InviteInfo mirrors store.Invite for the ipc layer (ipc must not import store).
type InviteInfo struct {
	Token       string     `json:"token"`
	TargetGlob  string     `json:"target_glob"`
	IssuedBySub string     `json:"issued_by_sub"`
	IssuedAt    time.Time  `json:"issued_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	MaxUses     int        `json:"max_uses"`
	UsedCount   int        `json:"used_count"`
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
	MessagesByThread    func(jid, topic string, before time.Time, limit int) ([]core.Message, error)
	JIDRoutedToFolder   func(jid, folder string) bool
	ErroredChats        func(folder string, isRoot bool) []ErroredChat
	TaskRunLogs         func(taskID string, limit int) []TaskRunLog
	RecentSessions      func(folder string, n int) []core.SessionRecord
	GetSession          func(folder, topic string) (string, bool)
	GetIdentityForSub   func(sub string) (Identity, []string, bool)
	SetWebRoute         func(pathPrefix, access, redirectTo, folder string) error
	DelWebRoute         func(pathPrefix, folder string) (bool, error)
	ListWebRoutes       func(folder string) []WebRoute
}

// WebRoute mirrors store.WebRoute for the ipc layer.
type WebRoute struct {
	PathPrefix string `json:"path_prefix"`
	Access     string `json:"access"`
	RedirectTo string `json:"redirect_to,omitempty"`
	Folder     string `json:"folder"`
	CreatedAt  string `json:"created_at"`
}

// Identity mirrors store.Identity; ipc must not import store.
type Identity struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
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

	srv := buildMCPServer(gated, db, folder, rules)

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
				serveConn(ctx, c, srv, gated, folder)
			}(conn)
		}
	}()

	return func() {
		cancel()
		ln.Close()
		os.Remove(sockPath)
	}, nil
}

func serveConn(ctx context.Context, c net.Conn, srv *server.MCPServer, gated GatedFns, folder string) {
	r := bufio.NewReader(c)
	var writeMu sync.Mutex
	writeJSON := func(v any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		c.Write(append(b, '\n'))
	}
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		raw := bytes.TrimRight(line, "\r\n")
		if len(raw) == 0 {
			continue
		}

		var head struct {
			Method string `json:"method"`
			ID     any    `json:"id"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			writeJSON(map[string]any{
				"jsonrpc": "2.0", "id": nil,
				"error": map[string]any{"code": -32700, "message": "parse error"},
			})
			continue
		}

		if head.Method == "submit_turn" {
			handleSubmitTurn(raw, head.ID, gated, folder, writeJSON)
			continue
		}

		resp := srv.HandleMessage(ctx, raw)
		if resp != nil {
			writeJSON(resp)
		}
	}
}

func handleSubmitTurn(raw []byte, id any, gated GatedFns, folder string, write func(any)) {
	var req struct {
		Params TurnResult `json:"params"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		write(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32602, "message": "invalid params: " + err.Error()},
		})
		return
	}
	if req.Params.TurnID == "" {
		write(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32602, "message": "turn_id required"},
		})
		return
	}
	if gated.SubmitTurn == nil {
		write(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32603, "message": "submit_turn not configured"},
		})
		return
	}
	if err := gated.SubmitTurn(folder, req.Params); err != nil {
		write(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32603, "message": err.Error()},
		})
		return
	}
	slog.Debug("submit_turn ok", "folder", folder,
		"turn_id", req.Params.TurnID, "session", req.Params.SessionID,
		"status", req.Params.Status)
	write(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"result": map[string]any{"ok": true},
	})
}

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

// toolMaybeUnsupported renders a *chanlib.UnsupportedError with its platform
// hint so the agent learns the alternative; falls back to a plain tool error.
func toolMaybeUnsupported(err error) (*mcp.CallToolResult, error) {
	var ue *chanlib.UnsupportedError
	if errors.As(err, &ue) {
		msg := fmt.Sprintf("unsupported: %s on %s", ue.Tool, ue.Platform)
		if ue.Hint != "" {
			msg += "\nhint: " + ue.Hint
		}
		return mcp.NewToolResultError(msg), nil
	}
	return toolErr(err.Error())
}

func toolJSON(v any) (*mcp.CallToolResult, error) {
	data, _ := json.Marshal(v)
	return mcp.NewToolResultText(string(data)), nil
}

func toolOK() (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText("ok"), nil
}

func recordOutbound(db StoreFns, jid, text, platformID, folder string) {
	if platformID != "" && db.SetLastReplyID != nil {
		db.SetLastReplyID(jid, "", platformID)
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
			Status:    core.MessageStatusSent,
		})
	}
}

func internalSend(gated GatedFns, db StoreFns, folder, jid, text string, files []internalSendFile) error {
	if len(files) == 0 {
		if gated.SendMessage == nil {
			return fmt.Errorf("send not configured")
		}
		platformID, err := gated.SendMessage(jid, text)
		if err != nil {
			return err
		}
		recordOutbound(db, jid, text, platformID, folder)
		return nil
	}
	if gated.SendDocument == nil {
		return fmt.Errorf("send_file not configured")
	}
	for _, f := range files {
		if err := gated.SendDocument(jid, f.LocalPath, f.Filename, text); err != nil {
			return err
		}
		recordOutbound(db, jid, text, "", folder)
	}
	return nil
}

type internalSendFile struct {
	LocalPath string
	Filename  string
}

func validHostname(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == ':':
		default:
			return false
		}
	}
	return true
}

// authorizeJID prevents a sub-folder agent from dispatching to a JID owned
// by a sibling. db.DefaultFolderForJID may be nil in tests.
func authorizeJID(id auth.Identity, action, jid string, db StoreFns) error {
	target := ""
	if db.DefaultFolderForJID != nil {
		target = db.DefaultFolderForJID(jid)
	}
	if err := auth.Authorize(id, action, auth.AuthzTarget{TargetFolder: target}); err != nil {
		if target == "" {
			return fmt.Errorf("forbidden: chat %s has no route in this instance", jid)
		}
		return fmt.Errorf("forbidden: chat %s belongs to folder %s, not in subtree of %s",
			jid, target, id.Folder)
	}
	return nil
}

func routeTargetWithin(target, owner string) bool {
	switch {
	case strings.HasPrefix(target, "folder:"):
		target = strings.TrimPrefix(target, "folder:")
	case strings.HasPrefix(target, "daemon:"), strings.HasPrefix(target, "builtin:"):
		return false
	}
	return target == owner || strings.HasPrefix(target, owner+"/")
}

// isSelfDefault: seq=0 routes pointing at the owner's own folder must not be deleted.
func isSelfDefault(r core.Route, owner string) bool {
	target := strings.TrimPrefix(r.Target, "folder:")
	return r.Seq == 0 && target == owner
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

func parseBefore(req mcp.CallToolRequest) (time.Time, error) {
	s := req.GetString("before", "")
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid before timestamp: %w", err)
	}
	return t, nil
}

func buildMCPServer(gated GatedFns, db StoreFns, folder string, rules []string) *server.MCPServer {
	id := auth.Resolve(folder)
	srv := server.NewMCPServer("arizuko", "1.0")

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

	registerRaw("send", "Deliver a new top-level message to a chat. Use for the normal reply to the user's last message or for a proactive notification. Not for threaded replies to a specific earlier message (`reply`) or file delivery (`send_file` — its caption replaces this call).",
		[]mcp.ToolOption{
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("text", mcp.Required()),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chatJid", "")
			if !grantslib.CheckAction(rules, "send", map[string]string{"jid": jid}) {
				return toolErr("send: not permitted")
			}
			if err := authorizeJID(id, "send", jid, db); err != nil {
				return toolErr(err.Error())
			}
			text := req.GetString("text", "")
			snippet := text
			if len(snippet) > 60 {
				snippet = snippet[:60]
			}
			slog.Info("send", "folder", folder, "jid", jid, "text", snippet)
			if err := internalSend(gated, db, folder, jid, text, nil); err != nil {
				return toolErr(err.Error())
			}
			return toolOK()
		})

	registerRaw("reply", "Deliver a message threaded to a specific earlier message (quote/reply UI on the platform). Use when disambiguating which message you're answering in an active chat, or when replyToId is known. Defaults to the last outbound reply id if omitted. Not for fresh top-level messages (`send`).",
		[]mcp.ToolOption{
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("text", mcp.Required()),
			mcp.WithString("replyToId"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chatJid", "")
			if !grantslib.CheckAction(rules, "reply", map[string]string{"jid": jid}) {
				return toolErr("reply: not permitted")
			}
			if err := authorizeJID(id, "reply", jid, db); err != nil {
				return toolErr(err.Error())
			}
			if gated.SendReply == nil {
				return toolErr("reply not configured")
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
			recordOutbound(db, jid, text, platformID, folder)
			return toolOK()
		})

	registerRaw("send_file", "Deliver a file from the group workspace (~/) to a chat. Works on every platform whose channel registered the tool — don't second-guess by platform name (telegram, discord, whatsapp all supported). Use when the user asked for a file (image, doc, CSV, audio) or when output would exceed a chat-reasonable length. `caption` IS the accompanying message — never follow with `send`. Not for inline text the user can read in-chat.",
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
			if err := authorizeJID(id, "send_file", jid, db); err != nil {
				return toolErr(err.Error())
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
			if err := internalSend(gated, db, folder, jid, caption,
				[]internalSendFile{{LocalPath: localPath, Filename: name}},
			); err != nil {
				return toolErr(err.Error())
			}
			return toolOK()
		})

	registerRaw("send_voice", "Deliver `text` as a synthesized voice message on the platform — push-to-talk on Telegram/WhatsApp, audio attachment on Discord. Use when the user sent voice and expects voice back, or when the persona is voice-first. Not for music/file delivery (use `send_file`). `voice` defaults to the persona's voice from SOUL.md frontmatter or the instance default; pass an explicit voice name to override.",
		[]mcp.ToolOption{
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("text", mcp.Required()),
			mcp.WithString("voice", mcp.Description("Optional voice name (backend-specific, e.g. 'af_bella' for Kokoro). Omit to use SOUL.md frontmatter or instance default.")),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chatJid", "")
			if !grantslib.CheckAction(rules, "send_voice", map[string]string{"jid": jid}) {
				return toolErr("send_voice: not permitted")
			}
			if err := authorizeJID(id, "send_voice", jid, db); err != nil {
				return toolErr(err.Error())
			}
			if gated.SendVoice == nil {
				return toolErr("send_voice not configured")
			}
			text := strings.TrimSpace(req.GetString("text", ""))
			if text == "" {
				return toolErr("send_voice: text is empty")
			}
			if len(text) > 5000 {
				return toolErr("send_voice: text too long (max 5000 chars)")
			}
			voice := req.GetString("voice", "")
			snippet := text
			if len(snippet) > 60 {
				snippet = snippet[:60]
			}
			slog.Info("send_voice", "folder", folder, "jid", jid, "voice", voice, "text", snippet)
			platformID, err := gated.SendVoice(jid, text, voice, folder)
			if err != nil {
				return toolMaybeUnsupported(err)
			}
			recordOutbound(db, jid, text, platformID, folder)
			return toolJSON(map[string]any{"ok": true, "id": platformID})
		})

	registerRaw("post", "Create a new top-level post on a platform (mastodon toot, bluesky post, discord channel message, reddit submission). Use for broadcast/announcement content that isn't replying to anyone. Not for replies (`reply`), direct messages (`send`), or file delivery (`send_file`). Tier 0-2 only.",
		[]mcp.ToolOption{
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("content", mcp.Required()),
			mcp.WithArray("media"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chatJid", "")
			if !grantslib.CheckAction(rules, "post", map[string]string{"jid": jid}) {
				return toolErr("post: not permitted")
			}
			if err := authorizeJID(id, "post", jid, db); err != nil {
				return toolErr(err.Error())
			}
			if gated.Post == nil {
				return toolErr("post not configured")
			}
			content := req.GetString("content", "")
			var mediaPaths []string
			if raw := req.GetStringSlice("media", nil); len(raw) > 0 {
				for _, fp := range raw {
					rel, err := workspaceRel(fp)
					if err != nil {
						return toolErr(err.Error())
					}
					localPath := filepath.Join(gated.GroupsDir, folder, rel)
					groupRoot := filepath.Join(gated.GroupsDir, folder)
					if _, err := mountsec.ValidateFilePath(localPath, groupRoot); err != nil {
						return toolErr("media path outside group dir")
					}
					mediaPaths = append(mediaPaths, localPath)
				}
			}
			slog.Info("post", "folder", folder, "jid", jid, "media", len(mediaPaths))
			platformID, err := gated.Post(jid, content, mediaPaths)
			if err != nil {
				return toolMaybeUnsupported(err)
			}
			recordOutbound(db, jid, content, platformID, folder)
			return toolJSON(map[string]any{"ok": true, "id": platformID})
		})

	type socialAct struct {
		name     string
		desc     string
		args     []string // first arg is grant-check jid (unless jidArg overrides)
		jidArg   string   // arg name used for grant-check; defaults to args[0]
		optional map[string]bool
		call     func(a map[string]string) (string, error) // id may be ""
		idOut    bool                                       // true → JSON {ok,id}; false → "ok"
	}
	regSocial := func(s socialAct) {
		opts := make([]mcp.ToolOption, 0, len(s.args))
		for _, a := range s.args {
			if s.optional[a] {
				opts = append(opts, mcp.WithString(a))
			} else {
				opts = append(opts, mcp.WithString(a, mcp.Required()))
			}
		}
		jidArg := s.jidArg
		if jidArg == "" {
			jidArg = s.args[0]
		}
		registerRaw(s.name, s.desc, opts,
			func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				vals := make(map[string]string, len(s.args))
				for _, a := range s.args {
					vals[a] = req.GetString(a, "")
				}
				jid := vals[jidArg]
				if !grantslib.CheckAction(rules, s.name, map[string]string{"jid": jid}) {
					return toolErr(s.name + ": not permitted")
				}
				if err := authorizeJID(id, s.name, jid, db); err != nil {
					return toolErr(err.Error())
				}
				slog.Info(s.name, "folder", folder, "jid", jid)
				id, err := s.call(vals)
				if err != nil {
					return toolMaybeUnsupported(err)
				}
				if s.idOut {
					return toolJSON(map[string]any{"ok": true, "id": id})
				}
				return toolOK()
			})
	}

	regSocial(socialAct{
		name: "like",
		desc: "Like an existing message (unicode emoji on discord, favourite on mastodon, like on bluesky). Use when acknowledging or endorsing a specific earlier message without sending text. Not for textual responses (`reply`) or new posts (`post`). Platform decides what reaction strings are valid; unsupported platforms return an error.",
		args: []string{"chatJid", "targetId", "reaction"},
		call: func(a map[string]string) (string, error) {
			if gated.Like == nil {
				return "", errors.New("like not configured")
			}
			return "", gated.Like(a["chatJid"], a["targetId"], a["reaction"])
		},
	})

	regSocial(socialAct{
		name: "delete",
		desc: "Delete a post/message previously created by this agent (platform enforces authorship). Use to retract an incorrect or superseded post. Not for editing (no edit tool — delete and re-post) or for hiding inbound messages. Tier 0-2 only.",
		args: []string{"chatJid", "targetId"},
		call: func(a map[string]string) (string, error) {
			if gated.Delete == nil {
				return "", errors.New("delete not configured")
			}
			return "", gated.Delete(a["chatJid"], a["targetId"])
		},
	})

	regSocial(socialAct{
		name:     "forward",
		desc:     "Redeliver an existing message to a different chat with provenance preserved (Telegram forward, WhatsApp forward, email Fwd:). Use to relay an inbound message to another chat without paraphrasing. Not for replying within the same chat (`reply`) or amplifying on a public feed (`repost` / `quote`).",
		args:     []string{"sourceMsgId", "targetJid", "comment"},
		jidArg:   "targetJid",
		optional: map[string]bool{"comment": true},
		idOut:    true,
		call: func(a map[string]string) (string, error) {
			if gated.Forward == nil {
				return "", errors.New("forward not configured")
			}
			return gated.Forward(a["sourceMsgId"], a["targetJid"], a["comment"])
		},
	})

	regSocial(socialAct{
		name:  "quote",
		desc:  "Republish a message on your own feed with added commentary (Bluesky quote, X quote-tweet). Native only — Mastodon has no quote primitive and returns unsupported with a hint to use `post(content=..., source_url=...)`. Not for in-chat threaded replies (`reply`) or simple amplification (`repost`).",
		args:  []string{"chatJid", "sourceMsgId", "comment"},
		idOut: true,
		call: func(a map[string]string) (string, error) {
			if gated.Quote == nil {
				return "", errors.New("quote not configured")
			}
			return gated.Quote(a["chatJid"], a["sourceMsgId"], a["comment"])
		},
	})

	regSocial(socialAct{
		name:  "repost",
		desc:  "Amplify a message on your own feed without added text (Mastodon boost, Bluesky repost, X retweet). Use to endorse-and-share. Not for commentary (`quote`) or sending a copy to a different chat (`forward`).",
		args:  []string{"chatJid", "sourceMsgId"},
		idOut: true,
		call: func(a map[string]string) (string, error) {
			if gated.Repost == nil {
				return "", errors.New("repost not configured")
			}
			return gated.Repost(a["chatJid"], a["sourceMsgId"])
		},
	})

	regSocial(socialAct{
		name: "dislike",
		desc: "Endorse-negative on a message (Discord 👎 reaction). Native only — Mastodon, Bluesky, and most platforms have no native downvote and return unsupported with a hint.",
		args: []string{"chatJid", "targetId"},
		call: func(a map[string]string) (string, error) {
			if gated.Dislike == nil {
				return "", errors.New("dislike not configured")
			}
			return "", gated.Dislike(a["chatJid"], a["targetId"])
		},
	})

	regSocial(socialAct{
		name: "edit",
		desc: "Modify a message previously sent by this agent in-place (Discord, Mastodon, Bluesky, Telegram own bot messages). Preserves the platform message id. Not for retract-and-resend (use `delete` then `post`/`send`). Email is unsupported.",
		args: []string{"chatJid", "targetId", "content"},
		call: func(a map[string]string) (string, error) {
			if gated.Edit == nil {
				return "", errors.New("edit not configured")
			}
			return "", gated.Edit(a["chatJid"], a["targetId"], a["content"])
		},
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

	granted("inject_message", "Write a synthetic inbound message into the store as if received from chat, triggering the normal agent loop. Use for programmatic prompts, tests, or scheduling one-off runs from tool code. Not for clearing context (reset_session) or sending output to users (`send`).",
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
			fwdFrom := folder
			if err := db.PutMessage(core.Message{
				ID:            core.MsgID("escalate"),
				ChatJID:       parent,
				Sender:        "escalate",
				Content:       wrapped,
				Timestamp:     time.Now(),
				ForwardedFrom: fwdFrom,
			}); err != nil {
				return toolErr(err.Error())
			}
			gated.EnqueueMessageCheck(parent)
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
				ChatJID:       target,
				Sender:        "delegate",
				Content:       prompt,
				Timestamp:     time.Now(),
				ForwardedFrom: chatJid,
			}); err != nil {
				return toolErr(err.Error())
			}
			gated.EnqueueMessageCheck(target)
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
			if db.ListRoutes != nil {
				hadDefault := false
				for _, r := range db.ListRoutes(id.Folder, false) {
					if isSelfDefault(r, id.Folder) {
						hadDefault = true
						break
					}
				}
				if hadDefault {
					keepsDefault := false
					for _, r := range routes {
						if isSelfDefault(r, id.Folder) {
							keepsDefault = true
							break
						}
					}
					if !keepsDefault {
						return toolErr("cannot delete own default route")
					}
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
			if isSelfDefault(route, id.Folder) {
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
		"Create a scheduled prompt that fires against a target chat. Use when the user asks for reminders, recurring checks, or deferred work. `cron` accepts a 5-field cron expression, an integer millisecond interval, or an RFC3339 timestamp for a one-shot. Not for immediate execution (`send`/inject_message).",
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

			targetFolder := ""
			if db.DefaultFolderForJID != nil {
				targetFolder = db.DefaultFolderForJID(targetJid)
			}
			if targetFolder == "" {
				return toolErr("target group not registered")
			}

			if err := auth.Authorize(id, "schedule_task", auth.AuthzTarget{TaskOwner: targetFolder}); err != nil {
				return toolErr(err.Error())
			}

			var nextRun *time.Time
			var cronStore string
			if cronExpr != "" {
				if ms, err := strconv.ParseInt(cronExpr, 10, 64); err == nil && ms > 0 {
					t := time.Now().Add(time.Duration(ms) * time.Millisecond)
					nextRun = &t
					cronStore = cronExpr
				} else if t, err := time.Parse(time.RFC3339, cronExpr); err == nil {
					nextRun = &t
					cronStore = "" // empty cron → timed marks completed after one firing
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

			prompt := req.GetString("prompt", "")
			if db.ListTasks != nil && cronStore != "" {
				for _, t := range db.ListTasks(targetFolder, false) {
					if t.Status == core.TaskActive && t.Cron == cronStore && t.Prompt == prompt {
						slog.Info("schedule_task: returning existing task (dedup)",
							"taskId", t.ID, "folder", targetFolder)
						return toolJSON(map[string]any{"taskId": t.ID})
					}
				}
			}
			taskID := fmt.Sprintf("task-%d-%s", time.Now().UnixMilli(), uuid.New().String()[:8])
			task := core.Task{
				ID: taskID, Owner: targetFolder, ChatJID: targetJid,
				Prompt: prompt, Cron: cronStore,
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

	registerRaw("invite_create",
		"Issue an invite token granting access to a path glob. The recipient accepts the token via /invite/<token> and gets a user_groups row matching target_glob. Use to onboard new collaborators to a world or sub-folder you own. The agent's authority must cover target_glob — you can't issue access you don't have. Tier 0-1 only.",
		[]mcp.ToolOption{
			mcp.WithString("target_glob", mcp.Required()),
			mcp.WithNumber("max_uses"),
			mcp.WithString("expires_at"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if gated.CreateInvite == nil {
				return toolErr("invite_create not configured")
			}
			targetGlob := req.GetString("target_glob", "")
			if targetGlob == "" {
				return toolErr("target_glob required")
			}
			if err := auth.Authorize(id, "invite_create", auth.AuthzTarget{TargetFolder: targetGlob}); err != nil {
				return toolErr(err.Error())
			}
			maxUses := req.GetInt("max_uses", 1)
			if maxUses < 1 {
				maxUses = 1
			}
			var expiresAt *time.Time
			if exp := req.GetString("expires_at", ""); exp != "" {
				t, err := time.Parse(time.RFC3339, exp)
				if err != nil {
					return toolErr("invalid expires_at: " + err.Error())
				}
				expiresAt = &t
			}
			inv, err := gated.CreateInvite(targetGlob, "agent:"+folder, maxUses, expiresAt)
			if err != nil {
				return toolErr(err.Error())
			}
			out := map[string]any{
				"token":       inv.Token,
				"target_glob": inv.TargetGlob,
				"max_uses":    inv.MaxUses,
			}
			if inv.ExpiresAt != nil {
				out["expires_at"] = inv.ExpiresAt.Format(time.RFC3339)
			}
			if gated.AcceptURLBase != "" {
				out["accept_url"] = strings.TrimRight(gated.AcceptURLBase, "/") + "/invite/" + inv.Token
			}
			slog.Info("invite_create", "folder", folder, "target_glob", targetGlob, "max_uses", maxUses)
			return toolJSON(out)
		})

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
			before, err := parseBefore(req)
			if err != nil {
				return toolErr(err.Error())
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
			mcp.WithDescription("Return rows from the local messages.db for one chat_jid, including outbound/bot rows and errored entries. Use for routing/delivery audits or to verify what the store recorded. Not for conversation context before replying (fetch_history), and not for one-thread-only history when the chat fans out into topics (get_thread) — this shows DB truth for the whole chat."),
			mcp.WithString("chat_jid", mcp.Required()),
			mcp.WithNumber("limit"),
			mcp.WithString("before"),
		), inspectMessages)
		srv.AddTool(mcp.NewTool("get_history",
			mcp.WithDescription("DEPRECATED alias for inspect_messages. Do not use in new code — pick inspect_messages for whole-chat DB audit, get_thread for a single (chat_jid, topic) slice, or fetch_history for platform-truth context."),
			mcp.WithString("chat_jid", mcp.Required()),
			mcp.WithNumber("limit"),
			mcp.WithString("before"),
		), inspectMessages)
	}

	if db.MessagesByThread != nil {
		srv.AddTool(mcp.NewTool("get_thread",
			mcp.WithDescription("Return rows from the local messages.db scoped to one thread (chat_jid + topic). Use when a chat fans out into per-topic conversations (Telegram forum topics, web-chat topics) and you want history for a single thread, not the whole chat. Not for whole-chat history (inspect_messages) or platform-truth context (fetch_history)."),
			mcp.WithString("chat_jid", mcp.Required()),
			mcp.WithString("topic", mcp.Required()),
			mcp.WithNumber("limit"),
			mcp.WithString("before"),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chat_jid", "")
			if jid == "" {
				return toolErr("chat_jid required")
			}
			topic := req.GetString("topic", "")
			if topic == "" {
				return toolErr("topic required")
			}
			if id.Tier > 0 && db.JIDRoutedToFolder != nil && !db.JIDRoutedToFolder(jid, folder) {
				return toolErr("access_denied: jid not routed to your group")
			}
			limitVal := req.GetInt("limit", 50)
			if limitVal <= 0 || limitVal > 100 {
				limitVal = 50
			}
			before, err := parseBefore(req)
			if err != nil {
				return toolErr(err.Error())
			}
			msgs, err := db.MessagesByThread(jid, topic, before, limitVal)
			if err != nil {
				return toolErr("get_thread: " + err.Error())
			}
			oldest := ""
			if len(msgs) > 0 {
				oldest = msgs[len(msgs)-1].Timestamp.Format(time.RFC3339)
			}
			return toolJSON(map[string]any{
				"messages": router.FormatMessages(msgs),
				"count":    len(msgs),
				"oldest":   oldest,
				"source":   "local-db",
			})
		})
	}

	if gated.FetchPlatformHistory != nil {
		srv.AddTool(mcp.NewTool("fetch_history",
			mcp.WithDescription("Pull authoritative conversation history from the channel adapter and cache it. Use to reconstruct context before replying, especially on first contact or after a reset_session. Falls back to local cache if the adapter is down. Not for DB/routing audits (inspect_messages) or single-thread slices (get_thread)."),
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
			before, err := parseBefore(req)
			if err != nil {
				return toolErr(err.Error())
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
				if !validHostname(hostname) {
					return toolErr("invalid hostname")
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

	granted("set_web_route",
		"Upsert a web route: control whether a URL path is public, auth-gated, denied, or redirected. "+
			"`path` must start with `/`. `access` is one of public|auth|deny|redirect. "+
			"When access=redirect, `redirect_to` (URL) is required. Route is scoped to this folder.",
		[]mcp.ToolOption{
			mcp.WithString("path", mcp.Required()),
			mcp.WithString("access", mcp.Required()),
			mcp.WithString("redirect_to"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.SetWebRoute == nil {
				return toolErr("set_web_route not configured")
			}
			p := req.GetString("path", "")
			if p == "" || p[0] != '/' {
				return toolErr("path must start with /")
			}
			access := req.GetString("access", "")
			switch access {
			case "public", "auth", "deny", "redirect":
			default:
				return toolErr("access must be one of: public, auth, deny, redirect")
			}
			redirectTo := req.GetString("redirect_to", "")
			if access == "redirect" && redirectTo == "" {
				return toolErr("redirect_to required when access=redirect")
			}
			if err := db.SetWebRoute(p, access, redirectTo, folder); err != nil {
				return toolErr(err.Error())
			}
			slog.Info("set_web_route", "folder", folder, "path", p, "access", access)
			return toolOK()
		})

	granted("del_web_route",
		"Delete a web route by path. Only routes owned by this folder may be deleted (operators can delete any).",
		[]mcp.ToolOption{mcp.WithString("path", mcp.Required())},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.DelWebRoute == nil {
				return toolErr("del_web_route not configured")
			}
			p := req.GetString("path", "")
			if p == "" {
				return toolErr("path required")
			}
			scopedFolder := folder
			if id.Tier == 0 {
				scopedFolder = ""
			}
			ok, err := db.DelWebRoute(p, scopedFolder)
			if err != nil {
				return toolErr(err.Error())
			}
			if !ok {
				return toolErr("route not found or not owned by this folder")
			}
			slog.Info("del_web_route", "folder", folder, "path", p)
			return toolOK()
		})

	granted("list_web_routes",
		"List all web routes owned by this folder. Returns a JSON array of {path_prefix, access, redirect_to, folder, created_at}.",
		nil,
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.ListWebRoutes == nil {
				return toolErr("list_web_routes not configured")
			}
			return toolJSON(db.ListWebRoutes(folder))
		})

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
