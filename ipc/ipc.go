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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	grantslib "github.com/kronael/arizuko/grants"
	"github.com/kronael/arizuko/mountsec"
	"github.com/kronael/arizuko/router"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/robfig/cron/v3"
	"golang.org/x/sys/unix"
)

type GatedFns struct {
	SendMessage          func(jid, text string) (string, error)
	SendReply            func(jid, text, replyToId string) (string, error)
	SendDocument         func(jid, path, filename, caption, replyTo, threadID string) (string, error)
	SendVoice            func(jid, text, voice, folder, threadID string) (string, error)
	Post                 func(jid, content string, mediaPaths []string) (string, error)
	Like                 func(jid, targetID, reaction string) error
	Delete               func(jid, targetID string) error
	Forward              func(sourceMsgID, targetJID, comment string) (string, error)
	Quote                func(jid, sourceMsgID, comment string) (string, error)
	Repost               func(jid, sourceMsgID string) (string, error)
	Dislike              func(jid, targetID string) error
	Edit                 func(jid, targetID, content string) error
	Pin                  func(jid, targetID string) error
	Unpin                func(jid, targetID string, all bool) error
	ClearSession         func(folder string)
	ForkTopic            func(folder, parent, child string, force bool) error
	InjectMessage        func(jid, content, sender, senderName string) (string, error)
	RegisterGroup        func(jid string, group core.Group) error
	SetupGroup           func(folder string) error
	GetGroups            func() map[string]core.Group
	EnqueueMessageCheck  func(jid string)
	SpawnGroup           func(parentFolder, childJID string) (core.Group, error)
	FetchPlatformHistory func(jid string, before time.Time, limit int) (PlatformHistory, error)
	CreateInvite         func(targetGlob, issuedBySub string, maxUses int, expiresAt *time.Time) (InviteInfo, error)
	ListInvites          func(issuedBy string) ([]InviteInfo, error)
	RevokeInvite         func(token string) error
	GrantACL             func(principal, scope, action, effect string) error
	RevokeACL            func(principal, scope, action, effect string) error
	SubmitTurn           func(folder string, t TurnResult) error
	// SubmitStatus delivers a mid-turn progress notice immediately as an interim
	// "⏳ ..." message without ending the turn. Nil = method unconfigured.
	SubmitStatus func(folder, turnID, text string) error
	// Per-group ambient controls (spec 6/F).
	SetGroupOpen          func(folder string, open bool) error
	SetGroupObserveWindow func(folder string, msgs, chars int) error
	GroupObserveWindow    func(folder string) (msgs, chars int)
	// observe_group cross-folder subscriptions (spec 5/F).
	AddGroupWatcher    func(observer, source string) error
	RemoveGroupWatcher func(observer, source string) error
	AcceptURLBase      string // base URL where /invite/<token> is served (e.g. https://app.example.com)
	GroupsDir          string
	WebDir             string

	// Web-presence discovery (spec 5/V get_web_presence). WebHost is the
	// instance's canonical host (WEB_HOST); HostingDomain derives per-world
	// hosts as <folder>.<HostingDomain>; VhostAliases maps a host label that
	// differs from the world to its folder (host→folder). All read-only.
	WebHost       string
	HostingDomain string
	VhostAliases  map[string]string

	// Slack assistant-pane controls (spec 6/D). Both stage values on the
	// owning adapter; values fire after the next outbound into the pane.
	// Adapters without pane semantics return chanlib.ErrUnsupported.
	PaneSetPrompts func(jid string, prompts []core.PanePrompt) error
	PaneSetTitle   func(jid, title string) error

	// EngagementTTL is the window applied at write-time when a bot
	// outbound bumps engaged_until. Spec 5/G. Zero disables the bump.
	EngagementTTL time.Duration

	// Route tokens (spec 5/W). Shared writer for chat-link / webhook
	// issuance from both MCP and REST surfaces. IssueRouteToken returns
	// the raw token exactly once.
	IssueRouteToken  func(kind, ownerFolder, targetFolder, sourceLabel, jidSuffix string) (RouteTokenInfo, error)
	ListRouteTokens  func(ownerFolder string) []RouteTokenInfo
	RevokeRouteToken func(jid, ownerFolder string) (bool, error)
	// Audit receives system events for mutating MCP tool calls. Nil = no-op.
	Audit *audit.Audit
}

// RouteTokenInfo mirrors store.RouteToken plus the raw token (returned
// only at issue time; List/Revoke return RawToken="").
type RouteTokenInfo struct {
	RawToken    string    `json:"token,omitempty"`
	JID         string    `json:"jid"`
	OwnerFolder string    `json:"owner_folder"`
	URL         string    `json:"url,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
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
	// TimedOut marks a result that is the graceful query-timeout summary
	// rather than a normal completion. routd logs a WARN; outcome stays OK.
	TimedOut bool `json:"timed_out,omitempty"`

	// Models carries per-model token usage + pre-computed cost.
	// Optional; nil/empty when the agent doesn't report (e.g. ant
	// versions before the cost-caps cutover). One cost_log row per
	// model when present. Spec 5/34.
	Models map[string]ModelUsage `json:"models,omitempty"`

	// CallerSub is the user_sub the turn ran on behalf of (empty for
	// channel-scoped turns). Spec 5/34 uses this for per-user spend
	// aggregation; spec 5/5 will replace it with full Caller shape.
	CallerSub string `json:"caller_sub,omitempty"`
}

// ModelUsage is one model's tokens + pre-computed cost for a turn.
// Field names mirror muaddib's `usage = {input, cacheRead, cacheWrite}`
// shape (refs/muaddib/src/agent/session-factory.ts:285); JSON tags are
// snake_case for our JSON-RPC over MCP. CostCents = round(costUSD × 100).
type ModelUsage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
	CostCents  int `json:"cost_cents"`
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
	// SetLastReply upserts last_reply_id and engaged_folder for (jid,
	// topic). Always called on outbound; BumpEngagement is the separate
	// timed-aware bump. Spec 5/G.
	SetLastReply func(jid, topic, replyID, folder string) error
	// BumpEngagement upserts engaged_until + engaged_folder. Callers MUST
	// skip the call when the triggering inbound sender is timed-* (spec
	// 5/G: scheduled / autonomous turns do not extend engagement).
	BumpEngagement      func(jid, topic, folder string, until time.Time) error
	ListACL             func(principal string) []core.ACLRow
	MessagesBefore      func(jid string, before time.Time, limit int) ([]core.Message, error)
	MessagesByThread    func(jid, topic string, before time.Time, limit int) ([]core.Message, error)
	FindMessages        func(query, scope, sender, since string, limit int) ([]FoundMessage, error)
	JIDRoutedToFolder   func(jid, folder string) bool
	JIDRoutableToFolder func(jid, folder string) bool
	ErroredChats        func(folder string, isRoot bool) []ErroredChat
	TaskRunLogs         func(taskID string, limit int) []TaskRunLog
	RecentSessions      func(folder string, n int) []core.SessionRecord
	GetSession          func(folder, topic string) (string, bool)
	GetIdentityForSub   func(sub string) (Identity, []string, bool)
	SetWebRoute         func(pathPrefix, access, redirectTo, folder string) error
	DelWebRoute         func(pathPrefix, folder string) (bool, error)
	ListWebRoutes       func(folder string) []WebRoute
	// WebRouteOwner reports which folder already owns an exact path_prefix
	// row, if any. Used by set_web_route to enforce first-claim ownership
	// of top-level prefixes outside the caller's own slot (spec 5/V §4).
	WebRouteOwner func(pathPrefix string) (folder string, ok bool)

	// Spec 5/G engagement primitives. SetEngagement is used by the
	// engage/disengage MCP tools and by api.handleMessage (verb=mention).
	// EngagedFolder powers the routing-miss fallback and the MCP authz
	// check (caller folder must own the conversation).
	SetEngagement func(jid, topic, folder string, until time.Time) error
	EngagedFolder func(jid, topic string) string

	// CurrentTriggerSender returns the sender of the inbound that
	// triggered the active turn for `folder`, or "" if none. MCP
	// recordOutbound consults this to honour the spec 5/G timed-* skip
	// for agent-side send/reply outbounds — same policy as the
	// gateway-side bump sites.
	CurrentTriggerSender func(folder string) string

	// CurrentTopic returns the effective topic of the active turn for
	// `folder`, or "" if none. The reply/send fallback reads it so an
	// explicit reply with no replyToId resolves GetLastReplyID under the
	// active topic (the key the gateway seeds via SetLastReply), threading
	// in-thread replies instead of posting to the channel root.
	CurrentTopic func(folder string) string

	// LogExternalCost records one cost_log row for a non-Anthropic LLM call
	// (oracle/codex/openai). Spec 5/34.
	LogExternalCost func(folder, provider, model string, inputTok, outputTok, costCents int) error
	// Connectors is the (discovered, namespaced) MCP-subprocess tool
	// catalog, registered through the broker chain at buildMCPServer.
	// Empty/nil disables the connector path. Spec 7/Y M6.
	Connectors []ConnectorTool

	// ResolveConnectorSecrets returns the folder/user-scoped secret values a
	// connector call needs, narrowed to the connector's declared `required`
	// names. buildMCPServer calls it per connector invocation and hands the
	// result to CallConnectorTool — which expands `{secret:KEY}` into the
	// subprocess env AND scrubs those values from the result. Nil → no
	// injection (the connector sees the placeholders literally), matching the
	// pre-injection behaviour. Spec 7/Y.
	ResolveConnectorSecrets func(folder string, required []string) map[string]string

	// Authorize checks whether sub may call action (e.g. "mcp:send") with
	// params in the context of folder. Used by ServeMCP when callerSub != ""
	// (ARIZUKO_LOCAL_SUB). Nil means no row-based check (full operator access).
	Authorize func(sub, folder, action string, params map[string]string) bool

	// LogIPCAudit persists one ipc_audit row. Nil = no-op.
	LogIPCAudit func(folder, sub, tool, params, outcome string) error

	// Egress allowlist (network_rules). AddNetworkRule appends one target for
	// folder; RemoveNetworkRule drops it; ResolveAllowlist returns the inherited
	// set (folder + ancestors + base); ListNetworkRules returns folder's own
	// explicit rows. Spec 5/5 egress_allowlist.
	AddNetworkRule    func(folder, target, by string) error
	RemoveNetworkRule func(folder, target string) error
	ResolveAllowlist  func(folder string) ([]string, error)
	ListNetworkRules  func(folder string) ([]NetworkRule, error)
}

// NetworkRule mirrors store/routd NetworkRule for the ipc layer (ipc must not
// import store). One explicit egress allowlist row.
type NetworkRule struct {
	Folder    string `json:"folder"`
	Target    string `json:"target"`
	CreatedBy string `json:"created_by,omitempty"`
}

// FoundMessage mirrors store.FoundMessage for the ipc layer
// (ipc must not import store). One hit from `find_messages`.
// Content is the FTS5 snippet (matched fragment with «»-highlight),
// not the full message body. Rank is BM25 — lower is better.
type FoundMessage struct {
	ChatJID      string    `json:"chat_jid"`
	Sender       string    `json:"sender"`
	Timestamp    time.Time `json:"timestamp"`
	IsFromMe     bool      `json:"is_from_me"`
	IsBotMessage bool      `json:"is_bot_message"`
	Content      string    `json:"content"`
	Rank         float64   `json:"rank"`
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
// callerSub, when non-empty, enables row-based grants checks via
// db.Authorize for every tool call (ARIZUKO_LOCAL_SUB). Empty = full
// operator access (default behavior).
func ServeMCP(sockPath string, gated GatedFns, db StoreFns, folder string, rules []string, expectedUID int, callerSub string) (func(), error) {
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

	srv := buildMCPServer(gated, db, folder, rules, callerSub)

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
		if head.Method == "submit_status" {
			handleSubmitStatus(raw, head.ID, gated, folder, writeJSON)
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

// handleSubmitStatus is the mid-turn progress hook: it delivers the agent's
// interim <status> text immediately (as a "⏳ ..." notice) without ending the
// turn. Hidden from tools/list, same as submit_turn.
func handleSubmitStatus(raw []byte, id any, gated GatedFns, folder string, write func(any)) {
	var req struct {
		Params struct {
			TurnID string `json:"turn_id"`
			Text   string `json:"text"`
		} `json:"params"`
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
	if gated.SubmitStatus == nil {
		write(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32603, "message": "submit_status not configured"},
		})
		return
	}
	if err := gated.SubmitStatus(folder, req.Params.TurnID, req.Params.Text); err != nil {
		write(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32603, "message": err.Error()},
		})
		return
	}
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

// numArg pulls an integer-valued JSON number from the args map. Returns
// (0, false) when missing or non-numeric. JSON numbers arrive as float64.
func numArg(args map[string]any, key string) (int, bool) {
	v, ok := args[key]
	if !ok || v == nil {
		return 0, false
	}
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	return int(f), true
}

// activeTopic returns the effective topic of the turn currently running
// for folder, or "" when unknown. Used by the reply fallback to seed
// GetLastReplyID under the same (jid, topic) key the gateway wrote.
func activeTopic(db StoreFns, folder string) string {
	if db.CurrentTopic == nil {
		return ""
	}
	return db.CurrentTopic(folder)
}

func recordOutbound(gated GatedFns, db StoreFns, jid, text, platformID, folder string) {
	// Key last-reply + engagement under the active turn's topic — same
	// key the gateway seeds (SetLastReply at turn start). "" would chain
	// the next in-thread reply off the wrong (root) key.
	topic := activeTopic(db, folder)
	if platformID != "" && db.SetLastReply != nil {
		_ = db.SetLastReply(jid, topic, platformID, folder)
	}
	// Spec 5/G — engagement bump shares ONE policy across the three
	// write sites (gateway steered echo, gateway output callback, MCP
	// recordOutbound). timed-* triggers skip the bump. The trigger
	// sender for the active turn is exposed by the gateway via
	// StoreFns.CurrentTriggerSender so MCP can honour the same skip
	// without plumbing per-call context through the socket.
	if platformID != "" && db.BumpEngagement != nil && gated.EngagementTTL > 0 {
		triggerSender := ""
		if db.CurrentTriggerSender != nil {
			triggerSender = db.CurrentTriggerSender(folder)
		}
		if !strings.HasPrefix(triggerSender, "timed-") {
			_ = db.BumpEngagement(jid, topic, folder, time.Now().Add(gated.EngagementTTL))
		}
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
			// Store the sent message's own platform id in platform_id (the
			// contract in store IsBotMessageByID): a later human reply to this
			// message carries this id in reply_to, and the reply-to-bot →
			// verb=mention promotion (spec 6/J) matches it. The gateway
			// turn-result path does the same via MarkMessageDelivered; reply-
			// tool sends were dropping it (platform_id empty) → threads rooted
			// on a tool reply never re-promoted.
			PlatformID: platformID,
			RoutedTo:   jid,
			Topic:      topic,
			Status:     core.MessageStatusSent,
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
		recordOutbound(gated, db, jid, text, platformID, folder)
		return nil
	}
	if gated.SendDocument == nil {
		return fmt.Errorf("send_file not configured")
	}
	for _, f := range files {
		platformID, err := gated.SendDocument(jid, f.LocalPath, f.Filename, text, f.ReplyTo, f.ThreadID)
		if err != nil {
			return err
		}
		recordOutbound(gated, db, jid, text, platformID, folder)
	}
	return nil
}

type internalSendFile struct {
	LocalPath string
	Filename  string
	ReplyTo   string
	ThreadID  string
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

// WebPresence is a folder's public web presence (spec 5/V get_web_presence):
// its derived/aliased canonical hostname plus the always-works /pub path and
// the OAuth /priv base. One renderer, two sinks — the MCP tool and routd's
// REST twin both call WebPresenceFor.
type WebPresence struct {
	Folder         string `json:"folder"`
	HostingDomain  string `json:"hosting_domain"`
	DerivedHost    string `json:"derived_host"`
	AliasHost      string `json:"alias_host,omitempty"`
	CanonicalHost  string `json:"canonical_host"`
	PublicBaseURL  string `json:"public_base_url"`
	PrivateBaseURL string `json:"private_base_url"`
	PubPath        string `json:"pub_path"`
}

// WebPresenceFor computes a folder's web presence from the instance host config.
// derivedHost = <folder>.<hostingDomain> when hostingDomain set, else "".
// aliasHost = the first host in aliases whose world == folder, else "".
// canonicalHost prefers the alias, then the derived host, then webHost.
// pubPath/privateBaseURL are path-based and always work regardless of vhosting.
func WebPresenceFor(folder, webHost, hostingDomain string, aliases map[string]string) WebPresence {
	derived := ""
	if hostingDomain != "" {
		derived = folder + "." + hostingDomain
	}
	alias := ""
	for h, w := range aliases {
		if w == folder {
			alias = h
			break
		}
	}
	canonical := webHost
	if derived != "" {
		canonical = derived
	}
	if alias != "" {
		canonical = alias
	}
	return WebPresence{
		Folder:         folder,
		HostingDomain:  hostingDomain,
		DerivedHost:    derived,
		AliasHost:      alias,
		CanonicalHost:  canonical,
		PublicBaseURL:  "https://" + canonical + "/",
		PrivateBaseURL: "https://" + webHost + "/priv/" + folder + "/",
		PubPath:        "https://" + webHost + "/pub/" + folder + "/",
	}
}

// authorizeJID prevents a sub-folder agent from dispatching to a JID owned
// by a sibling. db.DefaultFolderForJID may be nil in tests.
func authorizeJID(id auth.Identity, action, jid string, db StoreFns) error {
	target := ""
	if db.DefaultFolderForJID != nil {
		target = db.DefaultFolderForJID(jid)
	}
	// Bare web:<folder> is a structural 1:1 binding to <folder> and carries no
	// route row (gateway.folderForJid / the web-strict-1:1 contract). The route
	// table still wins for routed web JIDs (web:X/sub → some group), so fall back
	// to the 1:1 binding only when no route resolved — lets an agent reply to its
	// own web-chat surface.
	if target == "" {
		if folder, ok := strings.CutPrefix(jid, "web:"); ok {
			target = folder
		}
	}
	if err := auth.AuthorizeStructural(id, action, auth.AuthzTarget{TargetFolder: target}); err == nil {
		return nil
	}
	// The default resolution forces verb="message", so a sub-folder that handles
	// this chat only under a verb-scoped route (e.g. mention-only) resolves to an
	// ancestor and is wrongly denied. Authorize when id.Folder (or a descendant)
	// is a route target for jid ignoring verb — matching authorizeOutbound's
	// subtree-containment rule. Bug: mention-only sub-folder can't reply.
	if db.JIDRoutableToFolder != nil && db.JIDRoutableToFolder(jid, id.Folder) {
		return nil
	}
	if target == "" {
		return fmt.Errorf("forbidden: chat %s has no route in this instance", jid)
	}
	return fmt.Errorf("forbidden: chat %s belongs to folder %s, not in subtree of %s",
		jid, target, id.Folder)
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
	prefix := core.ContainerHome + "/"
	if strings.HasPrefix(fp, prefix) {
		return strings.TrimPrefix(fp, prefix), nil
	}
	return "", fmt.Errorf("filepath must be under ~/ (%s)", core.ContainerHome)
}

// decodePanePrompts coerces the MCP `prompts` arg into typed
// PanePrompt slice. The JSON-RPC envelope decodes the array as
// []any of map[string]any; we walk it strictly (title+message
// both required, both strings, non-empty).
func decodePanePrompts(raw any) ([]core.PanePrompt, error) {
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("prompts: expected array, got %T", raw)
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("prompts: empty")
	}
	if len(arr) > 16 {
		return nil, fmt.Errorf("prompts: too many (max 16)")
	}
	out := make([]core.PanePrompt, 0, len(arr))
	for i, v := range arr {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("prompts[%d]: expected object", i)
		}
		title, _ := m["title"].(string)
		msg, _ := m["message"].(string)
		title = strings.TrimSpace(title)
		msg = strings.TrimSpace(msg)
		if title == "" || msg == "" {
			return nil, fmt.Errorf("prompts[%d]: title and message required", i)
		}
		out = append(out, core.PanePrompt{Title: title, Message: msg})
	}
	return out, nil
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

func buildMCPServer(gated GatedFns, db StoreFns, folder string, rules []string, callerSub string) *server.MCPServer {
	identity := auth.Resolve(folder)
	srv := server.NewMCPServer("arizuko", "1.0")

	authorizeCall := func(name string, params map[string]string) bool {
		if callerSub == "" || db.Authorize == nil {
			return true
		}
		return db.Authorize(callerSub, folder, "mcp:"+name, params)
	}

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
	emitAuthzDenied := func(tool, actorSub string) {
		actor := actorSub
		if actor == "" {
			actor = "agent:" + folder
		}
		if db.LogIPCAudit != nil {
			_ = db.LogIPCAudit(folder, actor, tool, "{}", "authz_denied")
		}
		if gated.Audit != nil {
			gated.Audit.EmitSystem(audit.SystemEvent{
				ActorSub: actor,
				Tool:     tool,
				Folder:   folder,
				Outcome:  audit.Outcome{Status: "authz_denied"},
			})
		}
	}

	granted := func(name, desc string, opts []mcp.ToolOption, h server.ToolHandlerFunc) {
		registerRaw(name, desc, opts, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if !grantslib.CheckAction(rules, name, nil) {
				emitAuthzDenied(name, callerSub)
				return toolErr(name + ": not permitted")
			}
			if !authorizeCall(name, nil) {
				emitAuthzDenied(name, callerSub)
				return toolErr(name + ": not permitted")
			}
			return h(ctx, req)
		})
	}

	// actorSub is the session caller; params must already be redacted.
	emitSys := func(tool, targetFolder, actorSub string, params map[string]any, err error) {
		actor := actorSub
		if actor == "" {
			actor = "agent:" + folder
		}
		outcomeStr := "ok"
		if err != nil {
			outcomeStr = "error: " + err.Error()
		}
		if db.LogIPCAudit != nil {
			paramsJSON, _ := json.Marshal(params)
			_ = db.LogIPCAudit(targetFolder, actor, tool, string(paramsJSON), outcomeStr)
		}
		if gated.Audit == nil {
			return
		}
		outcome := audit.Outcome{Status: "ok"}
		if err != nil {
			outcome = audit.Outcome{Status: "error", Detail: err.Error()}
		}
		gated.Audit.EmitSystem(audit.SystemEvent{
			ActorSub: actor,
			Tool:     tool,
			Folder:   targetFolder,
			Params:   params,
			Outcome:  outcome,
		})
	}

	authzStructural := func(action string, target auth.AuthzTarget) error {
		err := auth.AuthorizeStructural(identity, action, target)
		if err != nil {
			emitAuthzDenied(action, callerSub)
		}
		return err
	}

	if db.LogExternalCost != nil {
		registerRaw("log_external_cost",
			"Record one non-Anthropic LLM call against the folder's daily "+
				"budget. Call this AFTER invoking an external model (e.g. "+
				"`codex exec --json` for /oracle). Pass provider, model, "+
				"token counts and the call's USD cost; gateway converts to "+
				"cents and writes a cost_log row. Skipping this hides the "+
				"call from cost-caps (operator-visible drift only via the "+
				"provider's own invoice). Spec 5/34.",
			[]mcp.ToolOption{
				mcp.WithString("provider", mcp.Required(),
					mcp.Description("openai | codex | other")),
				mcp.WithString("model", mcp.Required(),
					mcp.Description("model identifier, e.g. gpt-5, codex-mini")),
				mcp.WithNumber("input_tokens",
					mcp.Description("input token count (0 if unknown)")),
				mcp.WithNumber("output_tokens",
					mcp.Description("output token count (0 if unknown)")),
				mcp.WithNumber("cost_usd", mcp.Required(),
					mcp.Description("USD cost reported by the provider")),
			},
			func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				provider := req.GetString("provider", "")
				model := req.GetString("model", "")
				if provider == "" || model == "" {
					return toolErr("log_external_cost: provider and model required")
				}
				costUSD := req.GetFloat("cost_usd", 0)
				cents := int(costUSD*100 + 0.5)
				inputTok := int(req.GetFloat("input_tokens", 0))
				outputTok := int(req.GetFloat("output_tokens", 0))
				if err := db.LogExternalCost(folder, provider, model, inputTok, outputTok, cents); err != nil {
					return toolErr("log_external_cost: " + err.Error())
				}
				out, _ := json.Marshal(map[string]any{"ok": true, "cents": cents})
				return mcp.NewToolResultText(string(out)), nil
			})
	}

	// MCP connectors. The handler spawns the subprocess per call, proxies
	// tools/call, scrubs the result, tears the subprocess down.
	for i := range db.Connectors {
		tool := db.Connectors[i] // capture
		opts := []mcp.ToolOption{}
		if len(tool.InputSchema) > 0 {
			opts = append(opts, mcp.WithRawInputSchema(tool.InputSchema))
		}
		granted(tool.LocalName, tool.Description, opts,
			func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				var secrets map[string]string
				if db.ResolveConnectorSecrets != nil && tool.Connector != nil {
					secrets = db.ResolveConnectorSecrets(folder, tool.Connector.Secrets)
				}
				return CallConnectorTool(ctx, tool, req.GetArguments(), secrets)
			})
	}

	registerRaw("send", "A fresh top-level message that is NOT a reply to the current conversation. Use ONLY when you explicitly need that — a proactive/unprompted notification, or a message to a different chat. For responding to the user (the normal case) use `reply`, which threads. Not for threaded replies (`reply`) or file delivery (`send_file` — its caption replaces this call).",
		[]mcp.ToolOption{
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("text", mcp.Required()),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chatJid", "")
			if !grantslib.CheckAction(rules, "send", map[string]string{"jid": jid}) {
				return toolErr("send: not permitted")
			}
			if !authorizeCall("send", map[string]string{"jid": jid}) {
				return toolErr("send: not permitted")
			}
			if err := authorizeJID(identity, "send", jid, db); err != nil {
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

	registerRaw("reply", "THE DEFAULT way to respond — use this for virtually every answer to the user. Delivers your message into the thread of the conversation you're answering: on Slack/Discord it lands in a thread off the triggering message (started for you if one doesn't exist yet), keeping the channel clean. Omit replyToId to thread to the current conversation automatically, or pass it to target a specific earlier message. Only reach for `send` when you deliberately need a fresh top-level message in a channel that is NOT a reply.",
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
			if !authorizeCall("reply", map[string]string{"jid": jid}) {
				return toolErr("reply: not permitted")
			}
			if err := authorizeJID(identity, "reply", jid, db); err != nil {
				return toolErr(err.Error())
			}
			if gated.SendReply == nil {
				return toolErr("reply not configured")
			}
			text := req.GetString("text", "")
			replyToID := req.GetString("replyToId", "")
			if replyToID == "" && db.GetLastReplyID != nil {
				// Resolve under the active turn's topic — the same key the
				// gateway seeds via SetLastReply. "" would miss the seed for
				// in-thread turns (topic = thread_ts) and post to root.
				replyToID = db.GetLastReplyID(jid, activeTopic(db, folder))
			}
			platformID, err := gated.SendReply(jid, text, replyToID)
			if err != nil {
				return toolErr(err.Error())
			}
			recordOutbound(gated, db, jid, text, platformID, folder)
			return toolOK()
		})

	registerRaw("send_file", "Deliver a file from the group workspace (~/) to a chat. Works on every platform whose channel registered the tool — don't second-guess by platform name (telegram, discord, whatsapp all supported). Use when the user asked for a file (image, doc, CSV, audio) or when output would exceed a chat-reasonable length. `caption` IS the accompanying message — never follow with `send`. Not for inline text the user can read in-chat.",
		[]mcp.ToolOption{
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("filepath", mcp.Required()),
			mcp.WithString("filename"),
			mcp.WithString("caption", mcp.Description("Message text to accompany the file. This IS the message — do not output separate text.")),
			mcp.WithString("replyToId", mcp.Description("Post the file into a thread (Slack thread_ts). Omit to post at channel root.")),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chatJid", "")
			if !grantslib.CheckAction(rules, "send_file", map[string]string{"jid": jid}) {
				return toolErr("send_file: not permitted")
			}
			if !authorizeCall("send_file", map[string]string{"jid": jid}) {
				return toolErr("send_file: not permitted")
			}
			if err := authorizeJID(identity, "send_file", jid, db); err != nil {
				return toolErr(err.Error())
			}
			fp := req.GetString("filepath", "")
			name := req.GetString("filename", "")
			caption := req.GetString("caption", "")
			replyToID := req.GetString("replyToId", "")
			// Fall back to the active turn's topic so a file sent inside a
			// thread lands in that thread, not the channel root. Same key
			// the gateway seeds via SetLastReply (mirrors the `reply` fix).
			threadID := activeTopic(db, folder)
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
				[]internalSendFile{{LocalPath: localPath, Filename: name, ReplyTo: replyToID, ThreadID: threadID}},
			); err != nil {
				return toolErr(err.Error())
			}
			return toolOK()
		})

	registerRaw("send_voice", "Deliver `text` as a synthesized voice message on the platform — push-to-talk on Telegram/WhatsApp, audio attachment on Discord. Use when the user sent voice and expects voice back, or when the persona is voice-first. Not for music/file delivery (use `send_file`). `voice` defaults to the persona's voice from PERSONA.md frontmatter or the instance default; pass an explicit voice name to override.",
		[]mcp.ToolOption{
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("text", mcp.Required()),
			mcp.WithString("voice", mcp.Description("Optional voice name (backend-specific, e.g. 'af_bella' for Kokoro). Omit to use PERSONA.md frontmatter or instance default.")),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chatJid", "")
			if !grantslib.CheckAction(rules, "send_voice", map[string]string{"jid": jid}) {
				return toolErr("send_voice: not permitted")
			}
			if !authorizeCall("send_voice", map[string]string{"jid": jid}) {
				return toolErr("send_voice: not permitted")
			}
			if err := authorizeJID(identity, "send_voice", jid, db); err != nil {
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
			// Thread the voice reply to the active turn's topic so it lands
			// in the originating thread, not the channel root (bug-1 mirror).
			platformID, err := gated.SendVoice(jid, text, voice, folder, activeTopic(db, folder))
			if err != nil {
				return toolMaybeUnsupported(err)
			}
			recordOutbound(gated, db, jid, text, platformID, folder)
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
			if !authorizeCall("post", map[string]string{"jid": jid}) {
				return toolErr("post: not permitted")
			}
			if err := authorizeJID(identity, "post", jid, db); err != nil {
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
			recordOutbound(gated, db, jid, content, platformID, folder)
			return toolJSON(map[string]any{"ok": true, "id": platformID})
		})

	type socialAct struct {
		name     string
		desc     string
		args     []string // first arg is grant-check jid (unless jidArg overrides)
		jidArg   string   // arg name used for grant-check; defaults to args[0]
		optional map[string]bool
		call     func(a map[string]string) (string, error) // id may be ""
		idOut    bool                                      // true → JSON {ok,id}; false → "ok"
		// record, when set, returns the text logged via recordOutbound after a
		// successful call — for verbs that author new content on the agent's
		// own feed (quote, repost) so the agent later sees its own IDs and
		// threading/engagement track, same as post. nil for relay/reaction
		// verbs: forward targets a foreign chat (recording under the active
		// turn's key would mis-thread it); like/delete/edit mutate existing
		// content rather than authoring new content.
		record func(a map[string]string) string
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
				if !authorizeCall(s.name, map[string]string{"jid": jid}) {
					return toolErr(s.name + ": not permitted")
				}
				if err := authorizeJID(identity, s.name, jid, db); err != nil {
					return toolErr(err.Error())
				}
				slog.Info(s.name, "folder", folder, "jid", jid)
				id, err := s.call(vals)
				if err != nil {
					return toolMaybeUnsupported(err)
				}
				if s.record != nil {
					recordOutbound(gated, db, jid, s.record(vals), id, folder)
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
		name:   "quote",
		desc:   "Republish a message on your own feed with added commentary (Bluesky quote, X quote-tweet). Native only — Mastodon has no quote primitive and returns unsupported with a hint to use `post(content=..., source_url=...)`. Not for in-chat threaded replies (`reply`) or simple amplification (`repost`).",
		args:   []string{"chatJid", "sourceMsgId", "comment"},
		idOut:  true,
		record: func(a map[string]string) string { return a["comment"] },
		call: func(a map[string]string) (string, error) {
			if gated.Quote == nil {
				return "", errors.New("quote not configured")
			}
			return gated.Quote(a["chatJid"], a["sourceMsgId"], a["comment"])
		},
	})

	regSocial(socialAct{
		name:   "repost",
		desc:   "Amplify a message on your own feed without added text (Mastodon boost, Bluesky repost, X retweet). Use to endorse-and-share. Not for commentary (`quote`) or sending a copy to a different chat (`forward`).",
		args:   []string{"chatJid", "sourceMsgId"},
		idOut:  true,
		record: func(a map[string]string) string { return "" },
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

	regSocial(socialAct{
		name: "pin_message",
		desc: "Pin a message to a chat/channel (Slack pins.add, Discord channel pin, Telegram pinned message). Use to mark a live status surface (deploy progress, standup link) or anchor a reference. Not for editing content (`edit`) or amplifying on a feed (`repost`/`quote`). Mastodon/Bluesky/Reddit/Email/WhatsApp return unsupported.",
		args: []string{"chatJid", "targetId"},
		call: func(a map[string]string) (string, error) {
			if gated.Pin == nil {
				return "", errors.New("pin not configured")
			}
			return "", gated.Pin(a["chatJid"], a["targetId"])
		},
	})

	regSocial(socialAct{
		name: "unpin_message",
		desc: "Remove the pin on a specific message (Slack pins.remove, Discord channel unpin, Telegram unpinChatMessage). Use to retire a status surface or rotate the pinned reference. Not for clearing every pin (use `unpin_all` on Slack/Telegram) or deleting the message itself (`delete`).",
		args: []string{"chatJid", "targetId"},
		call: func(a map[string]string) (string, error) {
			if gated.Unpin == nil {
				return "", errors.New("unpin not configured")
			}
			return "", gated.Unpin(a["chatJid"], a["targetId"], false)
		},
	})

	regSocial(socialAct{
		name: "unpin_all",
		desc: "Clear every pin in a chat/channel (Slack: iterates pins.list+pins.remove; Telegram: unpinAllChatMessages). Use when wholesale resetting a channel's pinned set. Discord has no bulk primitive — call `unpin_message` per id. Not for removing one pin (`unpin_message`).",
		args: []string{"chatJid"},
		call: func(a map[string]string) (string, error) {
			if gated.Unpin == nil {
				return "", errors.New("unpin not configured")
			}
			return "", gated.Unpin(a["chatJid"], "", true)
		},
	})

	if gated.PaneSetPrompts != nil {
		registerRaw("pane_set_prompts",
			"Slack only — stage suggested-prompt buttons shown at the bottom of the assistant pane after your next reply lands. Fire-and-forget; the buttons appear once and persist until your next pane_set_prompts call. Use after a reply when you can anticipate the user's likely follow-ups (e.g. \"dig deeper\", \"summarise\", \"export\"). Not for sending messages (use `send`). 3-4 prompts is the visible cap.",
			[]mcp.ToolOption{
				mcp.WithString("chatJid", mcp.Required()),
				mcp.WithArray("prompts", mcp.Required(), mcp.Description("Array of {title, message}. Title shows on the button; message is sent as user input on click.")),
			},
			func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				jid := req.GetString("chatJid", "")
				if !grantslib.CheckAction(rules, "pane_set_prompts", map[string]string{"jid": jid}) {
					return toolErr("pane_set_prompts: not permitted")
				}
				if !authorizeCall("pane_set_prompts", map[string]string{"jid": jid}) {
					return toolErr("pane_set_prompts: not permitted")
				}
				if err := authorizeJID(identity, "pane_set_prompts", jid, db); err != nil {
					return toolErr(err.Error())
				}
				raw := req.GetArguments()["prompts"]
				prompts, err := decodePanePrompts(raw)
				if err != nil {
					return toolErr(err.Error())
				}
				slog.Info("pane_set_prompts", "folder", folder, "jid", jid, "n", len(prompts))
				if err := gated.PaneSetPrompts(jid, prompts); err != nil {
					return toolErr(err.Error())
				}
				return toolOK()
			})
	}
	if gated.PaneSetTitle != nil {
		registerRaw("pane_set_title",
			"Slack only — override the title shown at the top of the assistant pane. Fires after your next reply lands. Use to reflect the active topic (e.g. \"atlas — debugging the build\"). Defaults to \"<assistant> — chat\" when never set.",
			[]mcp.ToolOption{
				mcp.WithString("chatJid", mcp.Required()),
				mcp.WithString("title", mcp.Required()),
			},
			func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				jid := req.GetString("chatJid", "")
				if !grantslib.CheckAction(rules, "pane_set_title", map[string]string{"jid": jid}) {
					return toolErr("pane_set_title: not permitted")
				}
				if !authorizeCall("pane_set_title", map[string]string{"jid": jid}) {
					return toolErr("pane_set_title: not permitted")
				}
				if err := authorizeJID(identity, "pane_set_title", jid, db); err != nil {
					return toolErr(err.Error())
				}
				title := strings.TrimSpace(req.GetString("title", ""))
				if title == "" {
					return toolErr("pane_set_title: title is empty")
				}
				if len(title) > 256 {
					return toolErr("pane_set_title: title too long (max 256 chars)")
				}
				slog.Info("pane_set_title", "folder", folder, "jid", jid, "title", title)
				if err := gated.PaneSetTitle(jid, title); err != nil {
					return toolErr(err.Error())
				}
				return toolOK()
			})
	}

	granted("fork_topic", "Branch a topic from another's current state. Child gets a fresh session_id; the parent's Claude Code session jsonl is copied so the child resumes natively from parent's tail. Use when starting a focused side-conversation that needs the parent's recent state but should not pollute the parent's session. Pass force=true to overwrite an existing child topic.",
		[]mcp.ToolOption{
			mcp.WithString("parent", mcp.Required()),
			mcp.WithString("child", mcp.Required()),
			mcp.WithBoolean("force"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if gated.ForkTopic == nil {
				return toolErr("fork_topic not configured")
			}
			parent := req.GetString("parent", "")
			child := req.GetString("child", "")
			if child == "" {
				return toolErr("child required")
			}
			if err := authzStructural("fork_topic", auth.AuthzTarget{TargetFolder: identity.Folder}); err != nil {
				return toolErr(err.Error())
			}
			force := req.GetBool("force", false)
			if err := gated.ForkTopic(identity.Folder, parent, child, force); err != nil {
				if errors.Is(err, core.ErrTopicExists) {
					return toolErr("topic_exists")
				}
				return toolErr(err.Error())
			}
			slog.Info("topic forked", "folder", identity.Folder, "parent", parent, "child", child, "force", force)
			return toolJSON(map[string]any{
				"folder":       identity.Folder,
				"parent_topic": parent,
				"child_topic":  child,
			})
		})

	// Spec 5/G engagement primitives. Both MCP sockets are per-folder,
	// so the agent passes (jid, topic) explicitly. Authz: caller folder
	// must own the conversation — either the last bot reply in
	// (jid, topic) was routed to this folder, or the jid's default
	// route resolves here. No cross-folder engage/disengage.
	// Authz arms (spec 5/G — oracle round 2 fix 5):
	//   1. EngagedFolder match — caller already owns the active engagement.
	//   2. JIDRoutedToFolder — caller is the default route target.
	//   3. No current engagement — fresh autonomous turn can claim the
	//      chat (escape hatch; scheduled jobs bootstrapping a conversation
	//      otherwise have no path to engage). Stealing an active
	//      engagement still requires arm 1 or 2.
	engagementAuthz := func(jid, topic string) error {
		if identity.Folder == "" {
			return fmt.Errorf("engagement: caller folder unresolved")
		}
		currentOwner := ""
		if db.EngagedFolder != nil {
			currentOwner = db.EngagedFolder(jid, topic)
		}
		if currentOwner != "" && currentOwner == identity.Folder {
			return nil
		}
		if db.JIDRoutedToFolder != nil && db.JIDRoutedToFolder(jid, identity.Folder) {
			return nil
		}
		if currentOwner == "" {
			return nil
		}
		return fmt.Errorf("engagement: %q owned by %q, not caller %q", jid, currentOwner, identity.Folder)
	}

	granted("engage",
		"Mark (jid, topic) engaged for the spec 5/G TTL so subsequent inbounds fire even when the route table wouldn't route them here. Use for scheduled / autonomous turns or recovery after a failed reply. Caller folder must already own the conversation (last reply here OR default route here).",
		[]mcp.ToolOption{
			mcp.WithString("jid", mcp.Required()),
			mcp.WithString("topic"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("jid", "")
			topic := req.GetString("topic", "")
			if jid == "" {
				return toolErr("engage: jid required")
			}
			if err := engagementAuthz(jid, topic); err != nil {
				return toolErr(err.Error())
			}
			if db.SetEngagement == nil || gated.EngagementTTL <= 0 {
				return toolErr("engage not configured")
			}
			// Spec 5/G fix 5: write engaged_folder = caller, so
			// future inbounds steer here. Authz already validated
			// the caller may claim this conversation.
			if err := db.SetEngagement(jid, topic, identity.Folder, time.Now().Add(gated.EngagementTTL)); err != nil {
				return toolErr(err.Error())
			}
			return toolJSON(map[string]any{"ok": true})
		})

	granted("disengage",
		"Clear engagement for (jid, topic). Subsequent inbounds need a fresh mention to re-fire. Use when the bot is done with a conversation or when a corrective fork is closing. Caller folder must own the conversation.",
		[]mcp.ToolOption{
			mcp.WithString("jid", mcp.Required()),
			mcp.WithString("topic"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("jid", "")
			topic := req.GetString("topic", "")
			if jid == "" {
				return toolErr("disengage: jid required")
			}
			if err := engagementAuthz(jid, topic); err != nil {
				return toolErr(err.Error())
			}
			if db.SetEngagement == nil {
				return toolErr("disengage not configured")
			}
			if err := db.SetEngagement(jid, topic, identity.Folder, time.Time{}); err != nil {
				return toolErr(err.Error())
			}
			return toolJSON(map[string]any{"ok": true})
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
			if err := authzStructural("reset_session", auth.AuthzTarget{TargetFolder: gf}); err != nil {
				return toolErr(err.Error())
			}
			slog.Info("reset_session", "folder", folder, "targetFolder", gf)
			gated.ClearSession(gf)
			return toolOK()
		})

	// Per-group ambient controls (spec 6/F). Both default to the calling
	// folder; pass `folder` to edit a descendant. AuthorizeStructural
	// gates cross-folder writes to own subtree (tier 0/1).
	granted("set_observe_window",
		"Override a group's ambient observe-window caps (messages and/or chars). Defaults to this folder; pass `folder` to edit a descendant. Per-group caps win over instance env defaults; per-route caps still win over both. Pass -1 to clear an override. Omit messages|chars to leave unchanged.",
		[]mcp.ToolOption{
			mcp.WithNumber("messages",
				mcp.Description("max ambient messages surfaced per turn; -1 clears override")),
			mcp.WithNumber("chars",
				mcp.Description("max ambient chars surfaced per turn; -1 clears override")),
			mcp.WithString("folder",
				mcp.Description("target group folder; defaults to caller's folder. Must be caller's folder or a descendant.")),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if gated.SetGroupObserveWindow == nil || gated.GroupObserveWindow == nil {
				return toolErr("set_observe_window not configured")
			}
			args := req.GetArguments()
			msgs, mOK := numArg(args, "messages")
			chars, cOK := numArg(args, "chars")
			if !mOK && !cOK {
				return toolErr("set_observe_window: at least one of messages|chars required")
			}
			target := folder
			if f, _ := args["folder"].(string); f != "" {
				target = f
			}
			if err := authzStructural("set_observe_window", auth.AuthzTarget{TargetFolder: target}); err != nil {
				return toolErr(err.Error())
			}
			// Absent args preserve the stored value; explicit -1 clears.
			prevM, prevC := gated.GroupObserveWindow(target)
			if !mOK {
				msgs = prevM
			}
			if !cOK {
				chars = prevC
			}
			if err := gated.SetGroupObserveWindow(target, msgs, chars); err != nil {
				emitSys("set_observe_window", target, callerSub,
					map[string]any{"messages": msgs, "chars": chars}, err)
				return toolErr(err.Error())
			}
			emitSys("set_observe_window", target, callerSub,
				map[string]any{"messages": msgs, "chars": chars}, nil)
			slog.Info("set_observe_window", "folder", target, "caller", folder, "messages", msgs, "chars", chars)
			return toolJSON(map[string]any{"ok": true, "folder": target, "messages": msgs, "chars": chars})
		})

	granted("set_group_open",
		"Toggle a group's visibility to its siblings. Defaults to this folder; pass `folder` to flip a descendant (e.g. a parent opening a child for cross-sibling observation). When open=true, sibling folders' ambient observed messages surface in that group's <observed> block (and vice versa) — see spec 6/F. Tier 0-1; target must be caller's folder or a descendant.",
		[]mcp.ToolOption{
			mcp.WithBoolean("open", mcp.Required(),
				mcp.Description("true to expose to siblings, false to seal off")),
			mcp.WithString("folder",
				mcp.Description("target group folder; defaults to caller's folder. Must be caller's folder or a descendant.")),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if gated.SetGroupOpen == nil {
				return toolErr("set_group_open not configured")
			}
			args := req.GetArguments()
			rawOpen, ok := args["open"]
			if !ok {
				return toolErr("set_group_open: open required")
			}
			open, ok := rawOpen.(bool)
			if !ok {
				return toolErr("set_group_open: open must be bool")
			}
			target := folder
			if f, _ := args["folder"].(string); f != "" {
				target = f
			}
			if err := authzStructural("set_group_open", auth.AuthzTarget{TargetFolder: target}); err != nil {
				return toolErr(err.Error())
			}
			if err := gated.SetGroupOpen(target, open); err != nil {
				emitSys("set_group_open", target, callerSub, map[string]any{"open": open}, err)
				return toolErr(err.Error())
			}
			emitSys("set_group_open", target, callerSub, map[string]any{"open": open}, nil)
			slog.Info("set_group_open", "folder", target, "caller", folder, "open", open)
			return toolJSON(map[string]any{"ok": true, "folder": target, "open": open})
		})

	granted("observe_group",
		"Subscribe this folder to receive another folder's inbound messages as <observed> context on future turns. "+
			"The observer sees what arrives at source without becoming its active agent. "+
			"Use to let a parent monitor a child, a sibling watch a sibling, or a root agent aggregate context. "+
			"Not for taking over routing (add_route) or ambient sibling visibility (set_group_open). Spec: specs/5/F.",
		[]mcp.ToolOption{
			mcp.WithString("source", mcp.Required(),
				mcp.Description("folder to observe, e.g. corp/sales")),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if gated.AddGroupWatcher == nil {
				return toolErr("observe_group not configured")
			}
			src := req.GetString("source", "")
			if src == "" {
				return toolErr("source required")
			}
			if err := authzStructural("observe_group", auth.AuthzTarget{TargetFolder: src}); err != nil {
				return toolErr(err.Error())
			}
			if err := gated.AddGroupWatcher(folder, src); err != nil {
				emitSys("observe_group", folder, callerSub, map[string]any{"source": src}, err)
				return toolErr(err.Error())
			}
			emitSys("observe_group", folder, callerSub, map[string]any{"source": src}, nil)
			slog.Info("observe_group", "observer", folder, "source", src)
			return toolJSON(map[string]any{"ok": true, "observer": folder, "source": src})
		})

	granted("unobserve_group",
		"Cancel an observe_group subscription. After this call, source's messages no longer surface in this folder's <observed> context.",
		[]mcp.ToolOption{
			mcp.WithString("source", mcp.Required(),
				mcp.Description("folder to stop observing")),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if gated.RemoveGroupWatcher == nil {
				return toolErr("unobserve_group not configured")
			}
			src := req.GetString("source", "")
			if src == "" {
				return toolErr("source required")
			}
			if err := authzStructural("unobserve_group", auth.AuthzTarget{TargetFolder: src}); err != nil {
				return toolErr(err.Error())
			}
			if err := gated.RemoveGroupWatcher(folder, src); err != nil {
				emitSys("unobserve_group", folder, callerSub, map[string]any{"source": src}, err)
				return toolErr(err.Error())
			}
			emitSys("unobserve_group", folder, callerSub, map[string]any{"source": src}, nil)
			slog.Info("unobserve_group", "observer", folder, "source", src)
			return toolJSON(map[string]any{"ok": true, "observer": folder, "source": src})
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
			if jid == "" {
				return toolErr("chatJid required")
			}
			if err := authzStructural("inject_message", auth.AuthzTarget{TargetFolder: jid}); err != nil {
				return toolErr(err.Error())
			}
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
			mcp.WithBoolean("fromPrototype"),
			mcp.WithString("folder"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if gated.RegisterGroup == nil {
				return toolErr("register_group not configured")
			}
			jid := req.GetString("jid", "")

			if req.GetBool("fromPrototype", false) {
				if gated.SpawnGroup == nil {
					return toolErr("register_group: fromPrototype not configured")
				}
				child, err := gated.SpawnGroup(folder, jid)
				if err != nil {
					emitSys("register_group", "", callerSub,
						map[string]any{"jid": jid, "fromPrototype": true}, err)
					return toolErr(err.Error())
				}
				if gated.SetupGroup != nil {
					if err := gated.SetupGroup(child.Folder); err != nil {
						slog.Warn("register_group: seed group dir", "folder", child.Folder, "err", err)
					}
				}
				emitSys("register_group", child.Folder, callerSub,
					map[string]any{"jid": jid, "fromPrototype": true}, nil)
				slog.Info("group registered from prototype", "jid", jid, "folder", child.Folder, "sourceGroup", folder)
				return toolJSON(map[string]any{"registered": true, "folder": child.Folder, "jid": jid})
			}

			gfld := req.GetString("folder", "")
			if gfld == "" {
				return toolErr("folder required when fromPrototype is false")
			}
			if err := authzStructural("register_group", auth.AuthzTarget{TargetFolder: gfld}); err != nil {
				return toolErr(err.Error())
			}
			groups := gated.GetGroups()
			if pg, ok := groups[folder]; ok {
				if err := auth.CheckSpawnAllowed(pg, groups); err != nil {
					return toolErr(err.Error())
				}
			}
			gr := core.Group{
				Folder:  gfld,
				AddedAt: time.Now(),
			}
			if err := gated.RegisterGroup(jid, gr); err != nil {
				emitSys("register_group", gfld, callerSub,
					map[string]any{"jid": jid, "fromPrototype": false}, err)
				return toolErr(err.Error())
			}
			if gated.SetupGroup != nil {
				if err := gated.SetupGroup(gfld); err != nil {
					slog.Warn("register_group: seed group dir", "folder", gfld, "err", err)
				}
			}
			emitSys("register_group", gfld, callerSub,
				map[string]any{"jid": jid, "fromPrototype": false}, nil)
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
			if err := authzStructural("escalate_group", auth.AuthzTarget{TargetFolder: folder}); err != nil {
				return toolErr(err.Error())
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
				// Active-topic key so an escalation from inside a thread
				// embeds the thread's reply-id (parent's reply-back threads),
				// not the channel-root id. Same fix family as the reply
				// fallback. "" would miss the gateway's (jid, topic) seed.
				replyTo = db.GetLastReplyID(chatJid, activeTopic(db, folder))
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

	if identity.Tier <= 2 {
		srv.AddTool(mcp.NewTool("refresh_groups",
			mcp.WithDescription("Return folder for every registered group. Use to discover delegation targets or audit the group tree. Not for routing details (inspect_routing) or per-group tasks (inspect_tasks)."),
		), func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if gated.GetGroups == nil {
				return toolErr("refresh_groups not configured")
			}
			groups := gated.GetGroups()
			type groupInfo struct {
				Folder string `json:"folder"`
			}
			out := make([]groupInfo, 0, len(groups))
			for _, g := range groups {
				out = append(out, groupInfo{Folder: g.Folder})
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
			if err := authzStructural("delegate_group", auth.AuthzTarget{TargetFolder: target}); err != nil {
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

	granted("list_routes", "Return the routing table rows this group can see, each annotated with mode (trigger/observe), fires_turn, triggers_on, a plain explain, and shadowed_by (earlier rule that intercepts it). Prefer inspect_routing when you also want JID→folder resolution or errored-chat context.", nil,
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.ListRoutes == nil {
				return toolErr("list_routes not configured")
			}
			return toolJSON(map[string]any{"routes": router.Describe(db.ListRoutes(folder, identity.Tier == 0))})
		})

	granted("set_routes",
		"Bulk-overwrite the full routing table for this folder subtree. Use only for wholesale reconfiguration where you've already read the current set. Prefer add_route/delete_route for targeted edits — this clobbers everything else. "+
			"Each route: seq (int), match ('key=glob' pairs; keys: platform, room, chat_jid, sender, verb), target (folder path, or folder:/daemon:/builtin: prefix). A bare target fires a turn on every match; append #observe to ingest silently with no turn (e.g. atlas/general#observe). Mention-only channel = a verb=mention trigger row stacked above a #observe catch-all; lower seq wins (first match).",
		[]mcp.ToolOption{mcp.WithString("routes", mcp.Required())},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.SetRoutes == nil {
				return toolErr("set_routes not configured")
			}
			if err := authzStructural("set_routes", auth.AuthzTarget{RouteTarget: identity.Folder}); err != nil {
				return toolErr(err.Error())
			}
			var routes []core.Route
			if err := json.Unmarshal([]byte(req.GetString("routes", "")), &routes); err != nil {
				return toolErr("invalid routes json: " + err.Error())
			}
			for _, r := range routes {
				if !routeTargetWithin(r.Target, identity.Folder) {
					return toolErr("route target outside own folder: " + r.Target)
				}
			}
			if db.ListRoutes != nil {
				hadDefault := false
				for _, r := range db.ListRoutes(identity.Folder, false) {
					if isSelfDefault(r, identity.Folder) {
						hadDefault = true
						break
					}
				}
				if hadDefault {
					keepsDefault := false
					for _, r := range routes {
						if isSelfDefault(r, identity.Folder) {
							keepsDefault = true
							break
						}
					}
					if !keepsDefault {
						return toolErr("cannot delete own default route")
					}
				}
			}
			if err := db.SetRoutes(identity.Folder, routes); err != nil {
				emitSys("set_routes", identity.Folder, callerSub,
					map[string]any{"count": len(routes)}, err)
				return toolErr(err.Error())
			}
			emitSys("set_routes", identity.Folder, callerSub,
				map[string]any{"count": len(routes)}, nil)
			slog.Info("routes set", "folder", identity.Folder, "count", len(routes))
			return toolJSON(map[string]any{"updated": true, "count": len(routes)})
		})

	granted("add_route",
		"Append one routing rule. Use for targeted routing changes (route one chat, one platform pattern) — preferred over set_routes for everything except full rewrites. "+
			"Fields: seq (int), match ('key=glob' pairs; keys: platform, room, chat_jid, sender, verb), target (folder path, or folder:/daemon:/builtin: prefix). A bare target fires a turn on every match; append #observe to ingest silently with no turn (e.g. atlas/general#observe). Mention-only channel = a verb=mention trigger row stacked above a #observe catch-all; lower seq wins (first match).",
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
			if err := authzStructural("add_route", auth.AuthzTarget{RouteTarget: route.Target}); err != nil {
				return toolErr(err.Error())
			}
			rid, err := db.AddRoute(route)
			if err != nil {
				emitSys("add_route", identity.Folder, callerSub,
					map[string]any{"target": route.Target}, err)
				return toolErr(err.Error())
			}
			emitSys("add_route", identity.Folder, callerSub,
				map[string]any{"target": route.Target, "id": rid}, nil)
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
			if isSelfDefault(route, identity.Folder) {
				return toolErr("cannot delete own default route")
			}
			if err := authzStructural("delete_route", auth.AuthzTarget{RouteTarget: route.Target}); err != nil {
				return toolErr(err.Error())
			}
			if err := db.DeleteRoute(rid); err != nil {
				emitSys("delete_route", identity.Folder, callerSub,
					map[string]any{"id": rid}, err)
				return toolErr(err.Error())
			}
			emitSys("delete_route", identity.Folder, callerSub,
				map[string]any{"id": rid, "target": route.Target}, nil)
			slog.Info("route deleted", "id", rid)
			return toolJSON(map[string]any{"deleted": true, "id": rid})
		})

	granted("network_allow",
		"Open egress to `host` for `folder` and every descendant by appending an allowlist rule. "+
			"Use when an agent needs to reach a host the default-deny proxy blocks (e.g. a vendor API). "+
			"`host` is a bare domain (no scheme/port), e.g. 'example.com'. A rule at a parent folder cascades to all children.",
		[]mcp.ToolOption{mcp.WithString("folder", mcp.Required()), mcp.WithString("host", mcp.Required())},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.AddNetworkRule == nil {
				return toolErr("network_allow not configured")
			}
			target := req.GetString("folder", "")
			host := req.GetString("host", "")
			if !validHostname(host) {
				return toolErr("host must be a bare hostname, e.g. example.com (no scheme or path)")
			}
			if err := authzStructural("network_allow", auth.AuthzTarget{TargetFolder: target}); err != nil {
				return toolErr(err.Error())
			}
			if err := db.AddNetworkRule(target, host, callerSub); err != nil {
				emitSys("network_allow", target, callerSub, map[string]any{"host": host}, err)
				return toolErr(err.Error())
			}
			emitSys("network_allow", target, callerSub, map[string]any{"host": host}, nil)
			slog.Info("egress allowed", "folder", target, "host", host)
			return toolJSON(map[string]any{"allowed": true, "folder": target, "host": host})
		})

	granted("network_deny",
		"Remove an egress allowlist rule: close access to `host` for `folder`. "+
			"Only drops a rule set on `folder` itself; a host inherited from an ancestor must be removed at that ancestor.",
		[]mcp.ToolOption{mcp.WithString("folder", mcp.Required()), mcp.WithString("host", mcp.Required())},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.RemoveNetworkRule == nil {
				return toolErr("network_deny not configured")
			}
			target := req.GetString("folder", "")
			host := req.GetString("host", "")
			if host == "" {
				return toolErr("host required")
			}
			if err := authzStructural("network_deny", auth.AuthzTarget{TargetFolder: target}); err != nil {
				return toolErr(err.Error())
			}
			if err := db.RemoveNetworkRule(target, host); err != nil {
				emitSys("network_deny", target, callerSub, map[string]any{"host": host}, err)
				return toolErr(err.Error())
			}
			emitSys("network_deny", target, callerSub, map[string]any{"host": host}, nil)
			slog.Info("egress denied", "folder", target, "host", host)
			return toolJSON(map[string]any{"denied": true, "folder": target, "host": host})
		})

	granted("network_list",
		"List the egress allowlist for `folder`: `resolved` is every host the folder can reach "+
			"(its own rules plus those inherited from ancestors and the instance base); `own` is only the rules set on `folder` itself.",
		[]mcp.ToolOption{mcp.WithString("folder", mcp.Required())},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.ResolveAllowlist == nil || db.ListNetworkRules == nil {
				return toolErr("network_list not configured")
			}
			target := req.GetString("folder", "")
			if err := authzStructural("network_list", auth.AuthzTarget{TargetFolder: target}); err != nil {
				return toolErr(err.Error())
			}
			resolved, err := db.ResolveAllowlist(target)
			if err != nil {
				return toolErr(err.Error())
			}
			own, err := db.ListNetworkRules(target)
			if err != nil {
				return toolErr(err.Error())
			}
			return toolJSON(map[string]any{"folder": target, "resolved": resolved, "own": own})
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

			if err := authzStructural("schedule_task", auth.AuthzTarget{TaskOwner: targetFolder}); err != nil {
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
				emitSys("schedule_task", targetFolder, callerSub,
					map[string]any{"task_id": taskID, "target_jid": targetJid}, err)
				return toolErr(err.Error())
			}
			emitSys("schedule_task", targetFolder, callerSub,
				map[string]any{"task_id": taskID, "target_jid": targetJid}, nil)
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
					if err := authzStructural(op.name, auth.AuthzTarget{TaskOwner: task.Owner}); err != nil {
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
			return toolJSON(db.ListTasks(folder, identity.Tier == 0))
		})

	if identity.Tier <= 1 {
		// Unified ACL inspection. Reads acl rows scoped to folder; subsumes
		// the legacy get_grants/set_grants surface. Writes go through
		// dashd or `arizuko grant`. Tier 0-1 only — AuthorizeStructural
		// denies tier 2 (grant-management group), so registering it there
		// only yields a tool that always errors.
		srv.AddTool(mcp.NewTool("list_acl",
			mcp.WithDescription("List acl rows for a folder. Returns rows where scope matches the folder. Audit what's permitted before changing. Tier 0-1 only."),
			mcp.WithString("folder", mcp.Required()),
		), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if db.ListACL == nil {
				return toolErr("list_acl not configured")
			}
			gf := req.GetString("folder", "")
			if gf == "" {
				return toolErr("folder required")
			}
			if err := authzStructural("list_acl", auth.AuthzTarget{TargetFolder: gf}); err != nil {
				return toolErr(err.Error())
			}
			rows := db.ListACL("")
			out := make([]map[string]any, 0, len(rows))
			for _, r := range rows {
				if r.Scope != gf {
					continue
				}
				out = append(out, map[string]any{
					"principal": r.Principal, "action": r.Action,
					"scope": r.Scope, "effect": r.Effect,
					"params": r.Params, "predicate": r.Predicate,
				})
			}
			return toolJSON(map[string]any{"folder": gf, "acl": out})
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
			if err := authzStructural("invite_create", auth.AuthzTarget{TargetFolder: targetGlob}); err != nil {
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
				emitSys("invite_create", folder, callerSub,
					map[string]any{"target_glob": targetGlob, "max_uses": maxUses}, err)
				return toolErr(err.Error())
			}
			emitSys("invite_create", folder, callerSub,
				map[string]any{"target_glob": targetGlob, "max_uses": maxUses}, nil)
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

	// invite_list/invite_revoke: the MCP twins of REST GET/DELETE /v1/invites
	// (spec 5/5). invite_list is read-only + self-filtered (this folder's
	// invites only), so it carries no authzStructural — like list_routes. The
	// folder scope is applied by the routd closure (issued_by="agent:"+folder),
	// not from any caller arg.
	granted("invite_list",
		"List the invite tokens THIS agent has issued (token, target glob, expiry, use count). Read-only; shows only your own folder's invites, never another folder's. Use to audit outstanding invites before invite_revoke. Not for issuing (invite_create) or revoking (invite_revoke).",
		nil,
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if gated.ListInvites == nil {
				return toolErr("invite_list not configured")
			}
			invs, err := gated.ListInvites("agent:" + folder)
			if err != nil {
				return toolErr(err.Error())
			}
			out := make([]map[string]any, 0, len(invs))
			for _, inv := range invs {
				row := map[string]any{
					"token":       inv.Token,
					"target_glob": inv.TargetGlob,
					"max_uses":    inv.MaxUses,
					"used_count":  inv.UsedCount,
				}
				if inv.ExpiresAt != nil {
					row["expires_at"] = inv.ExpiresAt.Format(time.RFC3339)
				}
				out = append(out, row)
			}
			return toolJSON(map[string]any{"invites": out})
		})

	registerRaw("invite_revoke",
		"Revoke an invite token YOU issued so it can no longer be redeemed. You may only revoke invites issued by your own folder — revoking another folder's token is rejected. Use invite_list to find your tokens. Not for issuing (invite_create). Tier 0-1 only.",
		[]mcp.ToolOption{
			mcp.WithString("token", mcp.Required()),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if gated.RevokeInvite == nil {
				return toolErr("invite_revoke not configured")
			}
			token := req.GetString("token", "")
			if token == "" {
				return toolErr("token required")
			}
			if err := authzStructural("invite_revoke", auth.AuthzTarget{TargetFolder: folder}); err != nil {
				return toolErr(err.Error())
			}
			// Ownership (this token belongs to this folder) is enforced in the
			// routd closure, which lists the folder's invites before DELETE.
			if err := gated.RevokeInvite(token); err != nil {
				emitSys("invite_revoke", folder, callerSub, map[string]any{"token": token}, err)
				return toolErr(err.Error())
			}
			emitSys("invite_revoke", folder, callerSub, map[string]any{"token": token}, nil)
			return toolJSON(map[string]any{"ok": true, "token": token})
		})

	// add_acl/remove_acl: the MCP write-face of REST POST/DELETE /v1/acl.
	// Both funnel through gated.GrantACL/RevokeACL — one writer, MCP and REST
	// produce identical rows (spec 5/5). authzStructural binds scope to the
	// caller's authority; scope "**" (operator role) is tier-0 only.
	granted("add_acl",
		"Grant a principal access to a folder scope (an acl row); scope '**' grants the operator role. You can only grant within your own authority. Not for routes (add_route) or invites (invite_create).",
		[]mcp.ToolOption{
			mcp.WithString("principal", mcp.Required()),
			mcp.WithString("scope", mcp.Required()),
			mcp.WithString("action"),
			mcp.WithString("effect"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if gated.GrantACL == nil {
				return toolErr("add_acl not configured")
			}
			principal := req.GetString("principal", "")
			scope := req.GetString("scope", "")
			if principal == "" || scope == "" {
				return toolErr("principal and scope required")
			}
			if err := authzStructural("add_acl", auth.AuthzTarget{TargetFolder: scope}); err != nil {
				return toolErr(err.Error())
			}
			err := gated.GrantACL(principal, scope, req.GetString("action", ""), req.GetString("effect", ""))
			emitSys("add_acl", folder, callerSub,
				map[string]any{"principal": principal, "scope": scope}, err)
			if err != nil {
				return toolErr(err.Error())
			}
			return toolJSON(map[string]any{"ok": true, "principal": principal, "scope": scope})
		})

	granted("remove_acl",
		"Revoke a principal's access to a folder scope (drop an acl row); scope '**' revokes the operator role. You can only revoke within your own authority. Not for routes (delete_route) or invites.",
		[]mcp.ToolOption{
			mcp.WithString("principal", mcp.Required()),
			mcp.WithString("scope", mcp.Required()),
			mcp.WithString("action"),
			mcp.WithString("effect"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if gated.RevokeACL == nil {
				return toolErr("remove_acl not configured")
			}
			principal := req.GetString("principal", "")
			scope := req.GetString("scope", "")
			if principal == "" || scope == "" {
				return toolErr("principal and scope required")
			}
			if err := authzStructural("remove_acl", auth.AuthzTarget{TargetFolder: scope}); err != nil {
				return toolErr(err.Error())
			}
			err := gated.RevokeACL(principal, scope, req.GetString("action", ""), req.GetString("effect", ""))
			emitSys("remove_acl", folder, callerSub,
				map[string]any{"principal": principal, "scope": scope}, err)
			if err != nil {
				return toolErr(err.Error())
			}
			return toolJSON(map[string]any{"ok": true, "principal": principal, "scope": scope})
		})

	// Route-token issuance. Spec 5/W. Two distinct intents (chat link
	// vs webhook) → two distinct tools per `mcp_tool_naming`. Both
	// funnel through gated.IssueRouteToken — one writer, MCP/REST
	// produce identical rows. Owner_folder is bound from the agent's
	// session folder (mint-on-behalf-of bookkeeping); tier table:
	//   tier 0 → any folder, tier 1-2 → self+descendants, tier 3+ → no mint.
	authorizeMint := func(targetFolder string) error {
		t := targetFolder
		if t == "" {
			t = folder
		}
		if identity.Tier >= 3 {
			return fmt.Errorf("unauthorized: tier %d cannot issue route tokens", identity.Tier)
		}
		if identity.Tier <= 2 && t != folder && !strings.HasPrefix(t, folder+"/") {
			return fmt.Errorf("unauthorized: can only mint for self+descendants")
		}
		return nil
	}

	if gated.IssueRouteToken != nil {
		registerRaw("issue_chat_link",
			"Mint a route token that serves the anonymous web chat widget at "+
				"/chat/<token>/. Returns {token, url, jid}; the token is shown "+
				"once. Inbound messages append at jid=web:<target_folder>[/<jid_suffix>]. "+
				"Use when you want a public, password-less chat surface for the "+
				"folder — paste the URL into a website, share with a visitor. "+
				"target_folder defaults to your own folder. Spec 5/W.",
			[]mcp.ToolOption{
				mcp.WithString("target_folder",
					mcp.Description("Folder the token routes to. Defaults to your own folder. Tier 0 = any; tier 1 = self+descendants; tier 2 = self only.")),
				mcp.WithString("jid_suffix",
					mcp.Description("Optional path appended to the JID (web:<folder>/<suffix>) — useful to partition multiple chat surfaces under one folder.")),
			},
			func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				target := strings.TrimSpace(req.GetString("target_folder", folder))
				if err := authorizeMint(target); err != nil {
					emitAuthzDenied("issue_chat_link", callerSub)
					return toolErr(err.Error())
				}
				suffix := strings.TrimSpace(req.GetString("jid_suffix", ""))
				info, err := gated.IssueRouteToken("chat", folder, target, "", suffix)
				if err != nil {
					emitSys("issue_chat_link", target, callerSub,
						map[string]any{"target_folder": target}, err)
					return toolErr(err.Error())
				}
				emitSys("issue_chat_link", target, callerSub,
					map[string]any{"target_folder": target, "jid": info.JID}, nil)
				slog.Info("issue_chat_link", "folder", folder, "target", target, "jid", info.JID)
				return toolJSON(map[string]any{
					"token": info.RawToken, "jid": info.JID, "url": info.URL,
				})
			})

		registerRaw("issue_webhook",
			"Mint a route token for an inbound webhook surface at /hook/<token>. "+
				"POSTs append a message at jid=hook:<target_folder>/<source_label>[/<jid_suffix>], "+
				"sender=<source_label>; the agent sees it like any other inbound. "+
				"Returns {token, url, jid} once. Use to register an external "+
				"system (GitHub, Linear, Stripe, …) as a fire-and-forget event "+
				"source for the folder. Spec 5/W.",
			[]mcp.ToolOption{
				mcp.WithString("source_label", mcp.Required(),
					mcp.Description("Short identifier of the upstream system (e.g. github, linear, stripe). Becomes the JID's source segment and the inbound sender field.")),
				mcp.WithString("target_folder",
					mcp.Description("Folder the token routes to. Defaults to your own folder. Tier rules match issue_chat_link.")),
				mcp.WithString("jid_suffix",
					mcp.Description("Optional path appended to the JID — partition multiple webhooks under one source_label.")),
			},
			func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				src := strings.TrimSpace(req.GetString("source_label", ""))
				if src == "" {
					return toolErr("source_label required")
				}
				target := strings.TrimSpace(req.GetString("target_folder", folder))
				if err := authorizeMint(target); err != nil {
					emitAuthzDenied("issue_webhook", callerSub)
					return toolErr(err.Error())
				}
				suffix := strings.TrimSpace(req.GetString("jid_suffix", ""))
				info, err := gated.IssueRouteToken("hook", folder, target, src, suffix)
				if err != nil {
					emitSys("issue_webhook", target, callerSub,
						map[string]any{"target_folder": target, "source_label": src}, err)
					return toolErr(err.Error())
				}
				emitSys("issue_webhook", target, callerSub,
					map[string]any{"target_folder": target, "source_label": src, "jid": info.JID}, nil)
				slog.Info("issue_webhook", "folder", folder, "target", target, "source", src, "jid", info.JID)
				return toolJSON(map[string]any{
					"token": info.RawToken, "jid": info.JID, "url": info.URL,
				})
			})
	}

	if gated.ListRouteTokens != nil {
		registerRaw("list_tokens",
			"List route tokens (chat links + webhooks) owned by your folder. "+
				"Returns rows with {jid, owner_folder, created_at}. Raw tokens "+
				"are NOT returned — they're shown once at issue time. Spec 5/W.",
			nil,
			func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				rows := gated.ListRouteTokens(folder)
				return toolJSON(map[string]any{"tokens": rows})
			})
	}

	if gated.RevokeRouteToken != nil {
		registerRaw("revoke_token",
			"Revoke a route token by JID. Caller must own the token "+
				"(owner_folder = your folder). After revocation the URL "+
				"returns 404 immediately — no grace period. Spec 5/W.",
			[]mcp.ToolOption{
				mcp.WithString("jid", mcp.Required(),
					mcp.Description("JID of the token to revoke (e.g. web:acme or hook:acme/github).")),
			},
			func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				jid := strings.TrimSpace(req.GetString("jid", ""))
				if jid == "" {
					return toolErr("jid required")
				}
				deleted, err := gated.RevokeRouteToken(jid, folder)
				if err != nil {
					emitSys("revoke_token", folder, callerSub,
						map[string]any{"jid": jid}, err)
					return toolErr(err.Error())
				}
				emitSys("revoke_token", folder, callerSub,
					map[string]any{"jid": jid, "deleted": deleted}, nil)
				slog.Info("revoke_token", "folder", folder, "jid", jid, "deleted", deleted)
				return toolJSON(map[string]any{"deleted": deleted})
			})
	}

	if db.MessagesBefore != nil {
		inspectMessages := func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chat_jid", "")
			if jid == "" {
				return toolErr("chat_jid required")
			}
			if identity.Tier > 0 && db.JIDRoutedToFolder != nil && !db.JIDRoutedToFolder(jid, folder) {
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
	}

	if db.FindMessages != nil {
		srv.AddTool(mcp.NewTool("find_messages",
			mcp.WithDescription("Full-text search over local messages.db (SQLite FTS5). `query` accepts FTS5 syntax: bare token, \"exact phrase\", `a OR b`, `a NOT b`, `prefix*`, `NEAR(a b, 5)`. Optional `scope` narrows to one chat_jid (contains ':') or a folder subtree (no ':'); `sender` exact-matches the sender column; `since` is an RFC3339 lower bound. Returns up to `limit` hits (default 20, max 200) ordered by BM25 rank. Each row carries chat_jid, sender, timestamp, content snippet (with «»-highlight) and rank. Use to find a past message by content. Not for whole-chat scroll (inspect_messages), single-thread slices (get_thread), or platform-truth fallback (fetch_history)."),
			mcp.WithString("query", mcp.Required(),
				mcp.Description("FTS5 query — bare token, phrase, OR/NOT, prefix*, NEAR(...).")),
			mcp.WithString("scope",
				mcp.Description("Optional: chat_jid (contains ':') or folder subtree (no ':').")),
			mcp.WithString("sender",
				mcp.Description("Optional exact sender match.")),
			mcp.WithString("since",
				mcp.Description("Optional RFC3339 timestamp lower bound.")),
			mcp.WithNumber("limit",
				mcp.Description("Max rows to return (default 20, max 200).")),
		), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query := strings.TrimSpace(req.GetString("query", ""))
			if query == "" {
				return toolErr("query required")
			}
			scope := strings.TrimSpace(req.GetString("scope", ""))
			sender := strings.TrimSpace(req.GetString("sender", ""))
			since := strings.TrimSpace(req.GetString("since", ""))
			limitVal := req.GetInt("limit", 20)
			if limitVal <= 0 || limitVal > 200 {
				limitVal = 20
			}
			hits, err := db.FindMessages(query, scope, sender, since, limitVal)
			if err != nil {
				return toolErr("find_messages: " + err.Error())
			}
			// Post-fetch ACL: drop rows whose chat_jid isn't routed to caller's
			// folder. Tier-0 (operator) bypasses. JIDRoutedToFolder is the same
			// gate inspect_messages uses (spec 5/C).
			filtered := hits[:0]
			if identity.Tier > 0 && db.JIDRoutedToFolder != nil {
				for _, h := range hits {
					if db.JIDRoutedToFolder(h.ChatJID, folder) {
						filtered = append(filtered, h)
					}
				}
			} else {
				filtered = hits
			}
			actor := callerSub
			if actor == "" {
				actor = "agent:" + folder
			}
			audit.Emit(ctx, audit.Event{
				Category: audit.CategoryAccess,
				Action:   "find_messages",
				Actor:    actor,
				ActorSub: callerSub,
				Surface:  audit.SurfaceMCP,
				Folder:   folder,
				ParamsSummary: map[string]any{
					"query":        query,
					"scope":        scope,
					"sender":       sender,
					"since":        since,
					"limit":        limitVal,
					"result_count": len(filtered),
				},
			})
			return toolJSON(map[string]any{
				"messages": filtered,
				"count":    len(filtered),
				"source":   "local-db",
			})
		})
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
			if identity.Tier > 0 && db.JIDRoutedToFolder != nil && !db.JIDRoutedToFolder(jid, folder) {
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
			if identity.Tier > 0 && db.JIDRoutedToFolder != nil && !db.JIDRoutedToFolder(jid, folder) {
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

	if identity.Tier <= 2 {
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
			"When access=redirect, `redirect_to` is required and must point into this folder's own "+
			"slot (/pub/<folder>/... or /priv/<folder>/...) — no external URLs or other folders. "+
			"Route is scoped to this folder.",
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
			if access == "redirect" {
				if redirectTo == "" {
					return toolErr("redirect_to required when access=redirect")
				}
				// Self-slot constraint: redirect_to must point into this
				// folder's own /pub/<folder>/ or /priv/<folder>/ slot. No
				// external URLs, no other folders' slots — prevents
				// open-redirect and cross-folder impersonation.
				if !strings.HasPrefix(redirectTo, "/pub/"+folder+"/") &&
					!strings.HasPrefix(redirectTo, "/priv/"+folder+"/") {
					return toolErr("redirect_to must point into this folder's own slot: /pub/" +
						folder + "/... or /priv/" + folder + "/...")
				}
			}
			// Path-claim enforcement (spec 5/V §4): a path inside the caller's
			// own /pub/<folder>/ or /priv/<folder>/ slot is always allowed. A
			// top-level / other prefix is allowed only if unclaimed — reject if
			// an existing row already owns it for a different folder.
			inOwnSlot := strings.HasPrefix(p, "/pub/"+folder+"/") ||
				strings.HasPrefix(p, "/priv/"+folder+"/")
			if !inOwnSlot && db.WebRouteOwner != nil {
				if owner, ok := db.WebRouteOwner(p); ok && owner != folder {
					return toolErr("path prefix already claimed by folder: " + owner)
				}
			}
			if err := db.SetWebRoute(p, access, redirectTo, folder); err != nil {
				emitSys("set_web_route", folder, callerSub,
					map[string]any{"path": p, "access": access}, err)
				return toolErr(err.Error())
			}
			emitSys("set_web_route", folder, callerSub,
				map[string]any{"path": p, "access": access}, nil)
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
			if identity.Tier == 0 {
				scopedFolder = ""
			}
			ok, err := db.DelWebRoute(p, scopedFolder)
			if err != nil {
				emitSys("del_web_route", folder, callerSub, map[string]any{"path": p}, err)
				return toolErr(err.Error())
			}
			if !ok {
				return toolErr("route not found or not owned by this folder")
			}
			emitSys("del_web_route", folder, callerSub, map[string]any{"path": p}, nil)
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

	granted("get_web_presence",
		"Report this folder's public web presence: its canonical hostname (derived <folder>.<HOSTING_DOMAIN> or an operator alias), the /pub/<folder>/ path that always works, and the OAuth /priv base. Use to tell a user where your site is. Read-only.",
		[]mcp.ToolOption{mcp.WithString("folder")},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			target := req.GetString("folder", folder)
			if target == "" {
				target = folder
			}
			// tier-1+ may only inspect own folder or descendants; tier-0 any.
			if identity.Tier > 0 && target != folder &&
				!strings.HasPrefix(target, folder+"/") {
				return toolErr("get_web_presence: can only query own folder or descendants")
			}
			return toolJSON(WebPresenceFor(target, gated.WebHost, gated.HostingDomain, gated.VhostAliases))
		})

	registerInspect(srv, db, identity, folder)

	return srv
}

// ListTools builds the MCP tool registry for folder with the given grant rules
// and returns the tool schemas. Safe to call without a running container —
// GatedFns and StoreFns are zero-valued (handlers are never invoked).
// Used by dashd and /v1/tools to render the tool browser without duplication.
func ListTools(folder string, rules []string) []mcp.Tool {
	srv := buildMCPServer(GatedFns{}, StoreFns{}, folder, rules, "")
	m := srv.ListTools()
	out := make([]mcp.Tool, 0, len(m))
	for _, st := range m {
		out = append(out, st.Tool)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
