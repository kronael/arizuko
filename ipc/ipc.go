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
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/mountsec"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/router"
	"github.com/kronael/arizuko/store"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"golang.org/x/sys/unix"
)

type RoutdFns struct {
	// msgID, in the three sends below, is the id the bot row will carry AND the
	// idempotency key the adapter dedups on — one value, because routd's retry
	// sweep re-sends a pending row under its OWN id (routd/loop.go). A key
	// minted inside the send would never match it, so the adapter opened a fresh
	// claim and posted the message a second time (BUGS L106). The caller mints
	// it before the send, so a failed send records the same id it tried.
	SendMessage          func(jid, text, msgID string) (platformID, threadRoot string, err error)
	SendReply            func(jid, text, replyToId, msgID string) (platformID, threadRoot string, err error)
	SendDocument         func(jid, path, filename, caption, replyTo, threadID, msgID string) (platformID, threadRoot string, err error)
	SendVoice            func(jid, text, voice, folder, threadID string) (platformID, threadRoot string, err error)
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
	EnqueueMessageCheck  func(jid string)
	FetchPlatformHistory func(jid string, before time.Time, limit int) (PlatformHistory, error)
	CreateInvite         func(targetGlob, issuedBySub string, maxUses int, expiresAt *time.Time) (InviteInfo, error)
	ListInvites          func(issuedBy string) ([]InviteInfo, error)
	RevokeInvite         func(ref string) error
	SubmitTurn           func(folder string, t TurnResult) (TurnAck, error)
	CheckTurn            func(folder, turnID string) (TurnCheck, error)
	// SubmitStatus declares the task summary or updates progress without ending
	// the turn. Nil = method unconfigured.
	SubmitStatus func(folder string, status TurnStatus) error
	// Per-group ambient controls (spec 5/F-topic-lineage).
	SetGroupOpen          func(folder string, open bool) error
	SetGroupObserveWindow func(folder string, msgs, chars int) error
	GroupObserveWindow    func(folder string) (msgs, chars int)
	// observe_group cross-folder subscriptions (spec 5/F-topic-lineage).
	AddGroupWatcher    func(observer, source string) error
	RemoveGroupWatcher func(observer, source string) error
	AcceptURLBase      string // base URL where /invite/<token> is served (e.g. https://app.example.com)
	GroupsDir          string

	// Web-presence discovery (spec 5/V-web-vhosts get_web_presence). WebHost is the
	// instance's canonical host (WEB_HOST); HostingDomain derives per-world
	// hosts as <folder>.<HostingDomain>; VhostAliases maps a host label that
	// differs from the world to its folder (host→folder). All read-only.
	WebHost       string
	HostingDomain string
	VhostAliases  map[string]string

	// Slack assistant-pane controls (spec 8/D-slack-agent-pane). Both stage values on the
	// owning adapter; values fire after the next outbound into the pane.
	// Adapters without pane semantics return chanlib.ErrUnsupported.
	PaneSetPrompts func(jid string, prompts []core.PanePrompt) error
	PaneSetTitle   func(jid, title string) error

	// EngagementTTL is the window applied at write-time when a bot
	// outbound bumps engaged_until. Spec 5/G-engagement. Zero disables the bump.
	EngagementTTL time.Duration

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
	Context     string    `json:"context,omitempty"`
	// TargetFolder is the folder the token delivers to. Empty on a token
	// minted before the mint recorded it (spec 5/W § Resolution).
	TargetFolder string `json:"target_folder,omitempty"`
}

// TurnResult is the agent-submitted turn payload. The MCP `submit_turn`
// method (hidden from tools/list) deserialises into this and calls
// RoutdFns.SubmitTurn. Idempotency is enforced by routd on (folder, TurnID).
type TurnResult struct {
	TurnID    string `json:"turn_id"`
	SessionID string `json:"session_id,omitempty"`
	Status    string `json:"status"` // success | error
	Error     string `json:"error,omitempty"`
	// TimedOut reports harness time or turn-limit exhaustion.
	TimedOut bool `json:"timed_out,omitempty"`

	// Models carries per-model token usage + pre-computed cost, as the
	// harness reports it. Optional, and nil/empty when the agent does not
	// report. routd logs it and stores nothing: the model gateway is the
	// ledger (spec 6/39).
	Models map[string]ModelUsage `json:"models,omitempty"`
}

// TurnAck acknowledges a submission, optionally holding the turn open for a nudge.
type TurnAck struct {
	OK    bool   `json:"ok"`
	Nudge string `json:"nudge,omitempty"`
}

// TurnCheck is the shared delivery decision returned to a harness Stop hook.
type TurnCheck struct {
	Delivered bool   `json:"delivered"`
	Owed      bool   `json:"owed"`
	Nudge     string `json:"nudge"`
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
// Ref is the non-secret handle (store.TokenRef) every read surface uses; Token
// is the live bearer and is populated ONLY on the create path.
type InviteInfo struct {
	Ref         string     `json:"ref"`
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
	GetTask             func(id string) (core.Task, bool)
	ListTasks           func(folder string, isRoot bool) []core.Task
	ListRoutes          func(folder string, isRoot bool) ([]core.Route, error)
	DefaultFolderForJID func(jid string) string
	// RouteTokenTarget returns the folder a web:/hook: token was MINTED to
	// deliver to, or "" when no token records one. The JID grammar cannot
	// answer it — see authorizeJID. May be nil in tests.
	RouteTokenTarget func(jid string) string
	PutMessage       func(m core.Message) error
	// PutOutbound appends a row that still owes a delivery, with the status
	// deciding which sweep owns it. It is a door of its own because a delivery
	// status is not a field of a message: `sent` is the absence of a delivery
	// item, so an already-delivered row goes through PutMessage.
	PutOutbound    func(m core.Message, status string) error
	GetLastReplyID func(jid, topic string) string
	// SetLastReply upserts last_reply_id and engaged_folder for (jid,
	// topic). Always called on outbound; BumpEngagement is the separate
	// timed-aware bump. Spec 5/G-engagement.
	SetLastReply func(jid, topic, replyID, folder string) error
	// BumpEngagement renews the active turn through store.RenewEngagement.
	// Explicit disengage takes precedence over that turn's later answers.
	BumpEngagement      func(jid, topic, folder string, until time.Time) error
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

	// Spec 5/G-engagement engagement primitives. SetEngagement is used by the
	// engage/disengage MCP tools and by api.handleMessage (verb=mention).
	// EngagedFolder powers the routing-miss fallback and the MCP authz
	// check (caller folder must own the conversation).
	SetEngagement func(jid, topic, folder string, until time.Time) error
	EngagedFolder func(jid, topic string) string

	// CurrentTriggerSender returns the sender of the inbound that
	// triggered the active turn for `folder`, or "" if none. MCP
	// recordOutbound consults this to honour the spec 5/G-engagement timed-* skip
	// for agent-side send/reply outbounds — same policy as the
	// gateway-side bump sites.
	CurrentTriggerSender func(folder string) string

	// CurrentTopic returns the effective topic of the active turn for
	// `folder`, or "" if none. The reply/send fallback reads it so an
	// explicit reply with no replyToId resolves GetLastReplyID under the
	// active topic (the key the gateway seeds via SetLastReply), threading
	// in-thread replies instead of posting to the channel root.
	CurrentTopic func(folder string) string

	// CurrentTurnID returns the active turn's id for `folder`, or "" if none.
	// recordOutbound stamps it on the bot row so TurnHasBotReply sees the
	// reply — otherwise recordTurnResult re-delivers the agent's prose result
	// as a duplicate (e.g. "Replied with …") on top of the real reply.
	CurrentTurnID func(folder string) string

	// Connectors is the configured MCP HTTP connector set. buildMCPServer
	// discovers each one's catalog with this folder's broker-resolved
	// credential and registers the result through the broker chain.
	// Empty/nil disables the connector path. Spec 5/13.
	Connectors []ConnectorSpec

	// ExtTools is the REST-descriptor tool catalog loaded from [[ext]]
	// TOML blocks (built-in providers + operator connectors.toml).
	// Empty/nil leaves the ext path off.
	ExtTools []ExtTool

	// ResolveConnectorSecrets returns the folder/user-scoped secret values a
	// connector call needs, narrowed to the connector's declared `required` names.
	// CallConnectorTool expands `{secret:KEY}` into the subprocess env and scrubs
	// those values from the result. Nil → no injection. A non-nil error is a
	// surrogate-OAuth "reconnect" signal (spec 5/15): a required credential's
	// refresh_token was revoked.
	//
	// `tool` is passed rather than inferred because the resolver is the audit seam:
	// secret_use_log answers "who used which credential, from where", and the tool
	// half of that is knowable only here at the call site.
	ResolveConnectorSecrets func(folder, tool string, required []string) (map[string]string, error)

	// RefreshConnectorSecrets force-refreshes the surrogate-OAuth rows behind
	// `required` and returns the re-resolved values — same shape as
	// ResolveConnectorSecrets, one implementation behind both. CallExtTool calls it
	// at most once, only on a 401. Nil disables the retry.
	RefreshConnectorSecrets func(folder, tool string, required []string) (map[string]string, error)

	// Authorize is the SOLE per-call authz check (5/33-paths-roles): may this turn call action
	// (e.g. "mcp:send") with params against `folder` as the SCOPE — the caller's own
	// folder for magnitude, or the ACTUAL target folder for a management tool's
	// containment (a delegated row scoped to a subtree covers it). Used by ServeMCP
	// when callerSub != "". Nil means no row-based check (full
	// operator access).
	//
	// It takes no principal. The turn's principals — the folder it was routed to and
	// the sender that triggered it — are bound by the injector (routd.turnAuthorize),
	// so ipc cannot supply a third and the two notions of "who" in this file cannot
	// stack.
	Authorize func(folder, action string, params map[string]string) bool

	// Visible is the tools/list visibility view (auth.EffectiveActions): does the
	// caller hold `name` at any scope? Injected by routd per turn. Nil → every
	// registerRaw/granted tool is advertised (the call-time Authorize still gates).
	Visible func(name string) bool

	// CheckHold answers "may this call proceed?" (spec 5/19). It runs at the single
	// tools/call interception, so hot tools, resreg facade tools and timed-triggered
	// turns are all covered by construction.
	//
	// It BLOCKS: a held call suspends this connection until a human answers, and an
	// approval returns held=false so the call runs here, in the turn that asked, with
	// the arguments the human saw. `refusal` is what the agent is told when the call
	// does not run — empty means the hold itself could not be recorded, which ipc
	// reports as an error rather than a verdict.
	//
	// It runs BEFORE authz, which is safe: approval never substitutes for
	// authorization. An approved call re-enters the normal path and every in-handler
	// grant and JID gate still runs (BUGS J2).
	CheckHold func(tool string, args map[string]any) (refusal string, held bool)

	// LogIPCAudit records one audit_log row (surface=mcp, action=mcp.tool.invoke)
	// through audit.EmitTo — the name is legacy, the destination is not. The
	// ipc_audit table is retired and unwritten; a comment claiming otherwise cost
	// a review (BUGS F34). Nil = no-op.
	LogIPCAudit func(folder, sub, tool, params, outcome string) error
}

// FoundMessage mirrors the ranked index's hit for the ipc layer (ipc must not
// import store). One hit from `find_messages`. Content is the snippet — the
// matched fragment with «» around each hit — not the full message body. Rank is
// BM25, and lower is the better match.
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
	// ID is the run's position, which is its identity: a fixed-width instant
	// and a suffix. It is what the reads order by.
	ID         string    `json:"id"`
	TaskID     string    `json:"task_id"`
	RunAt      time.Time `json:"run_at"`
	DurationMS int64     `json:"duration_ms"`
	Status     string    `json:"status"`
	Result     string    `json:"result,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// maxMCPConns bounds concurrent in-flight MCP connections per group.
const maxMCPConns = 8

// ServeMCP binds the group MCP socket (0660, chowned to expectedUID), bounds
// accept fan-out, and verifies each peer via SO_PEERCRED. expectedUID <= 0
// disables the check (dev/tests). callerSub, when non-empty, enables row-based
// grants checks via db.Authorize for every tool call.
//
// postBuild runs on the built *server.MCPServer before the accept loop starts. It
// is the layering seam for resreg-driven tool registration: ipc must not import
// store/resreg, so routd passes a closure that calls resreg.MCPTools. Empty in
// every non-routd caller.
func ServeMCP(sockPath string, routd RoutdFns, db StoreFns, folder string, isRoot bool, expectedUID int, callerSub string, postBuild ...func(*server.MCPServer)) (func(), error) {
	// A stale socket that will not unlink shows up as the Listen below refusing
	// the address, which is the error the caller gets.
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	// The mode and the owner ARE the socket's reach: net.Listen leaves it at the
	// umask, where the agent's uid cannot connect. Neither is fatal — a socket
	// whose group the agent is in connects on the group bit alone — but a failure
	// is the difference between a live socket and a group with no tool calls.
	if err := os.Chmod(sockPath, 0o660); err != nil {
		slog.Error("mcp socket chmod failed; the group may get no tool calls",
			"folder", folder, "sock", sockPath, "err", err)
	}
	if expectedUID > 0 {
		if err := os.Chown(sockPath, expectedUID, -1); err != nil {
			slog.Error("mcp socket chown failed; the group may get no tool calls",
				"folder", folder, "sock", sockPath, "uid", expectedUID, "err", err)
		}
	}
	slog.Info("mcp server listening",
		"folder", folder, "sock", sockPath, "peer_uid", expectedUID)

	srv := buildMCPServer(routd, db, folder, isRoot, callerSub)
	for _, pb := range postBuild {
		if pb != nil {
			pb(srv)
		}
	}

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
				_ = conn.Close()
				continue
			}
			go func(c net.Conn) {
				defer func() { <-sem; _ = c.Close() }()
				if expectedUID > 0 {
					cred, err := peerCred(c)
					if err != nil {
						slog.Error("mcp peer cred read failed; connection rejected, group gets no tool calls",
							"folder", folder, "err", err)
						return
					}
					if int(cred.Uid) != expectedUID {
						slog.Error("mcp peer uid mismatch; connection rejected, group gets no tool calls",
							"folder", folder,
							"want", expectedUID, "got", cred.Uid,
							"pid", cred.Pid)
						return
					}
				}
				serveConn(ctx, c, srv, routd, db, folder)
			}(conn)
		}
	}()

	return func() {
		cancel()
		_ = ln.Close()
		_ = os.Remove(sockPath)
	}, nil
}

// holdEligible reports whether a tools/call may create a pending-action row. The
// hold gate runs before per-call authz, so these two cheap preconditions stand in
// for it. Without them `hold:mcp:*` let a raw caller mint unlimited rows and
// approval notices for tools that could never execute, and probe which forbidden
// tools carry a hold rule (BUGS J2).
func holdEligible(srv *server.MCPServer, db StoreFns, tool string) bool {
	if srv.GetTool(tool) == nil {
		return false
	}
	return db.Visible == nil || db.Visible(tool)
}

// rpcEncodeFailure renders the JSON-RPC error frame writeJSON sends in place of
// a response that will not marshal. It is assembled from bytes rather than
// marshalled as a whole: writeJSON reaches it only because a marshal has already
// failed, and a replacement that can fail the same way leaves the caller with
// nothing to read.
//
// The id is the only part from outside, and it is what tells the caller which
// request the frame answers. It arrives as JSON off the wire, so it re-marshals;
// a request that carried none gets null, the id JSON-RPC gives a response the
// server cannot attribute. The Go value that would not encode stays in the log —
// putting it in the frame means marshalling a second value here, and the agent
// can act on neither.
func rpcEncodeFailure(id any) []byte {
	raw, err := json.Marshal(id)
	if err != nil {
		raw = []byte("null")
	}
	return []byte(`{"jsonrpc":"2.0","id":` + string(raw) + `,"error":{"code":-32603,` +
		`"message":"internal error: the response could not be encoded; the call may already have taken effect"}}`)
}

func serveConn(ctx context.Context, c net.Conn, srv *server.MCPServer, routd RoutdFns, db StoreFns, folder string) {
	r := bufio.NewReader(c)
	var writeMu sync.Mutex
	writeJSON := func(id, v any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		b, err := json.Marshal(v)
		if err != nil {
			// The operator needs the Go value that would not encode; the agent
			// needs a frame to read, or it waits out its deadline for a response
			// that is never coming.
			slog.Error("mcp response marshal — sending an error frame instead",
				"folder", folder, "err", err)
			b = rpcEncodeFailure(id)
		}
		if _, werr := c.Write(append(b, '\n')); werr != nil {
			// Same cost as a frame that would not marshal: no response reaches the
			// agent and it waits out its deadline.
			slog.Error("mcp response write failed; the agent gets no frame for this call",
				"folder", folder, "err", werr)
		}
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
			writeJSON(nil, map[string]any{
				"jsonrpc": "2.0", "id": nil,
				"error": map[string]any{"code": -32700, "message": "parse error"},
			})
			continue
		}
		// reply carries this request's id alongside the response, so a response
		// that will not marshal is replaced by an error frame the caller can
		// still match to the call it is waiting on.
		reply := func(v any) { writeJSON(head.ID, v) }

		if head.Method == "submit_turn" {
			handleSubmitTurn(raw, head.ID, routd, folder, reply)
			continue
		}
		if head.Method == "submit_status" {
			handleSubmitStatus(raw, head.ID, routd, folder, reply)
			continue
		}
		if head.Method == "check_turn" {
			handleCheckTurn(raw, head.ID, routd, folder, reply)
			continue
		}

		// mcp_tool span (spec 5/O-observability): every tools/call funnels through
		// HandleMessage. Parse the tool name for the span attr; non-tool methods
		// (initialize/tools/list) get no span — zero overhead when traces off.
		mctx := ctx
		var endSpan func(error)
		if head.Method == "tools/call" {
			var p struct {
				Params struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				} `json:"params"`
			}
			_ = json.Unmarshal(raw, &p)
			// Spec 5/19: the hold gate sits here, before the call reaches
			// HandleMessage, so no tool can route around it.
			if db.CheckHold != nil && holdEligible(srv, db, p.Params.Name) {
				if refusal, held := db.CheckHold(p.Params.Name, p.Params.Arguments); held {
					reply(holdResponse(head.ID, refusal, p.Params.Name))
					continue
				}
			}
			mctx, endSpan = obs.StartSpan(ctx, "mcp_tool",
				"tool", p.Params.Name, "folder", folder)
		}
		resp := srv.HandleMessage(mctx, raw)
		if endSpan != nil {
			endSpan(nil)
		}
		if resp != nil {
			reply(resp)
		}
	}
}

func handleCheckTurn(raw []byte, id any, routd RoutdFns, folder string, write func(any)) {
	var req struct {
		Params struct {
			TurnID string `json:"turn_id"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.Params.TurnID == "" {
		write(map[string]any{"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32602, "message": "turn_id required"}})
		return
	}
	if routd.CheckTurn == nil {
		write(map[string]any{"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32603, "message": "check_turn not configured"}})
		return
	}
	check, err := routd.CheckTurn(folder, req.Params.TurnID)
	if err != nil {
		write(map[string]any{"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32603, "message": err.Error()}})
		return
	}
	write(map[string]any{"jsonrpc": "2.0", "id": id, "result": check})
}

func handleSubmitTurn(raw []byte, id any, routd RoutdFns, folder string, write func(any)) {
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
	if routd.SubmitTurn == nil {
		write(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32603, "message": "submit_turn not configured"},
		})
		return
	}
	ack, err := routd.SubmitTurn(folder, req.Params)
	if err != nil {
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
		"result": ack,
	})
}

type StatusKind string

const (
	StatusSummary  StatusKind = "summary"
	StatusProgress StatusKind = "progress"
)

type TurnStatus struct {
	TurnID string     `json:"turn_id"`
	Text   string     `json:"text"`
	Kind   StatusKind `json:"kind,omitempty"`
}

// handleSubmitStatus is hidden from tools/list, like submit_turn.
func handleSubmitStatus(raw []byte, id any, routd RoutdFns, folder string, write func(any)) {
	var req struct {
		Params TurnStatus `json:"params"`
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
	if req.Params.Kind != "" && req.Params.Kind != StatusSummary && req.Params.Kind != StatusProgress {
		write(map[string]any{"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32602, "message": "kind must be summary or progress"}})
		return
	}
	if routd.SubmitStatus == nil {
		write(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32603, "message": "submit_status not configured"},
		})
		return
	}
	if err := routd.SubmitStatus(folder, req.Params); err != nil {
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

// recordOutbound appends the durable row for a DELIVERED outbound message.
func recordOutbound(routd RoutdFns, db StoreFns, jid, text, platformID, folder, msgID string) {
	putOutboundRow(routd, db, jid, text, platformID, folder, "", activeTopic(db, folder), msgID, core.MessageStatusSent)
}

// recordOutboundReply records the thread resolved by the delivery path.
func recordOutboundReply(routd RoutdFns, db StoreFns, jid, text, platformID, folder, threadRoot, msgID string) {
	putOutboundRow(routd, db, jid, text, platformID, folder, "", threadRoot, msgID, core.MessageStatusSent)
}

// recordOutboundFailure appends the durable row for an outbound message the
// platform did NOT accept, so a failed send leaves evidence, and returns the
// error the tool hands back to the agent.
//
// A transient failure lands 'pending' and routd's sweep re-sends it. A permanent
// refusal lands 'failed'. retryable=false forces the dead letter for a file send,
// which the text-only sweep would repeat as a captionless, fileless message.
//
// threadRoot is the thread the send MEANT to root, known before the send: a
// pending reply without it cannot be put back where it belonged, so the retry
// landed in the parent channel instead (BUGS L169 link 2).
//
// The status decided here is also what the agent needs, so it goes into the
// returned error rather than into a second classifier at the call site: one
// declaration, two readers. A bare "failed" leaves the retry with two owners —
// a `reply` answered 502, the sweep delivered the row 52 s later, and the agent,
// told only that it failed, re-sent the same text six seconds after that, so the
// channel got both copies (BUGS L165).
func recordOutboundFailure(routd RoutdFns, db StoreFns, jid, text, folder, replyToID, threadRoot, msgID string, retryable bool, err error) error {
	status := core.MessageStatusPending
	if !retryable || errors.Is(err, chanlib.ErrPermanent) {
		status = core.MessageStatusFailed
	}
	slog.Error("outbound send failed: recording undelivered row",
		"folder", folder, "jid", jid, "status", status, "err", err)
	putOutboundRow(routd, db, jid, text, "", folder, replyToID, threadRoot, msgID, status)
	if status == core.MessageStatusPending {
		return fmt.Errorf("%w (queued: the router re-sends this message — do NOT send it again)", err)
	}
	return fmt.Errorf("%w (not delivered, and no retry follows — the message is dead-lettered)", err)
}

// putOutboundRow is the ONE writer of an MCP-originated outbound row. Both
// outcomes go through it, so the delivered and undelivered shapes cannot drift.
//
// A verb whose Deliverer takes no idempotency key — voice, post and the social
// verbs — passes msgID="" and the row mints its own id: there is nothing for it
// to have to match.
func putOutboundRow(routd RoutdFns, db StoreFns, jid, text, platformID, folder, replyToID, threadRoot, msgID, status string) {
	// Key last-reply + engagement under the active turn's topic — same
	// key the gateway seeds (SetLastReply at turn start). "" would chain
	// the next in-thread reply off the wrong (root) key.
	turnTopic := activeTopic(db, folder)
	topic := threadRoot
	if platformID != "" && db.SetLastReply != nil {
		_ = db.SetLastReply(jid, topic, platformID, folder)
	}
	// Spec 5/G-engagement — engagement bump shares ONE policy across the three write sites.
	// timed-* triggers skip the bump; the trigger sender for the active turn is
	// exposed via StoreFns.CurrentTriggerSender so MCP honours the same skip
	// without plumbing per-call context through the socket.
	if platformID != "" && db.BumpEngagement != nil && routd.EngagementTTL > 0 {
		triggerSender := ""
		if db.CurrentTriggerSender != nil {
			triggerSender = db.CurrentTriggerSender(folder)
		}
		if !strings.HasPrefix(triggerSender, "timed-") {
			_ = db.BumpEngagement(jid, turnTopic, folder, time.Now().Add(routd.EngagementTTL))
		}
	}
	turnID := ""
	if db.CurrentTurnID != nil {
		turnID = db.CurrentTurnID(folder)
	}
	if msgID == "" {
		msgID = core.MsgID("mcp")
	}
	if db.PutOutbound != nil {
		err := db.PutOutbound(core.Message{
			ID:        msgID,
			ChatJID:   jid,
			Sender:    folder,
			Content:   text,
			Timestamp: time.Now(),
			FromMe:    true,
			BotMsg:    true,
			TurnID:    turnID,
			// An undelivered row carries the reply target so the sweep re-sends
			// it into the same thread. A delivered row leaves it empty: its own
			// platform id belongs in platform_id.
			ReplyToID: replyToID,
			// Store the sent message's own platform id in platform_id: a later human
			// reply carries this id in reply_to, and the reply-to-bot → verb=mention
			// promotion (spec 5/L) matches it. reply-tool sends were dropping it, so
			// threads rooted on a tool reply never re-promoted.
			PlatformID: platformID,
			// The folder the turn ran in, which is what `routed_to` names and what
			// the folder feed is partitioned by. The chat's jid is already the
			// row's ChatJID, and a jid here makes a folder-scoped read miss the
			// reply it is looking for.
			RoutedTo: folder,
			Topic:    topic,
		}, status)
		if err != nil {
			slog.Error("outbound row not stored", "folder", folder, "jid", jid,
				"status", status, "err", err)
		}
	}
}

// internalSend delivers one MCP send and records it. Both outcomes write a
// durable row, and a failure returns the error that names which of them owns the
// retry. It returns the last platform-assigned message id it recorded,
// so send and send_file can hand it back for edit/delete/branch targeting.
func internalSend(routd RoutdFns, db StoreFns, folder, jid, text string, files []internalSendFile) (string, error) {
	if len(files) == 0 {
		if routd.SendMessage == nil {
			return "", fmt.Errorf("send not configured")
		}
		msgID := core.MsgID("mcp")
		platformID, thread, err := routd.SendMessage(jid, text, msgID)
		if err != nil {
			return "", recordOutboundFailure(routd, db, jid, text, folder, "", thread, msgID, true, err)
		}
		recordOutboundReply(routd, db, jid, text, platformID, folder, thread, msgID)
		return platformID, nil
	}
	if routd.SendDocument == nil {
		return "", fmt.Errorf("send_file not configured")
	}
	var lastID string
	for _, f := range files {
		// One id per file: each file is its own delivery and its own row.
		msgID := core.MsgID("mcp")
		platformID, thread, err := routd.SendDocument(jid, f.LocalPath, f.Filename, text, f.ReplyTo, f.ThreadID, msgID)
		if err != nil {
			return "", recordOutboundFailure(routd, db, jid, text, folder, f.ReplyTo, thread, msgID, false, err)
		}
		recordOutboundReply(routd, db, jid, text, platformID, folder, thread, msgID)
		lastID = platformID
	}
	return lastID, nil
}

type internalSendFile struct {
	LocalPath string
	Filename  string
	ReplyTo   string
	ThreadID  string
}

// WebPresence is a folder's public web presence (spec 5/V-web-vhosts get_web_presence):
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

// authorizeJID is the MESSAGING containment (5/33-paths-roles: kept as route-ownership, separate
// from the acl magnitude gate). It prevents a sub-folder agent from dispatching to a
// JID owned by a sibling: the jid's routing target must be the caller's folder or a
// descendant (root bypasses). db.DefaultFolderForJID may be nil in tests.
func authorizeJID(id auth.Identity, action, jid string, db StoreFns) error {
	target := ""
	if db.DefaultFolderForJID != nil {
		target = db.DefaultFolderForJID(jid)
	}
	// No route row: fall back to the token's own binding so an agent can reply to
	// its own web-chat surface. Read the folder the MINT recorded, never the JID
	// grammar — `web:T/S` is byte-identical to a plain token at a folder named
	// `T/S`, so the grammar hands containment to a descendant (BUGS L4).
	if target == "" && db.RouteTokenTarget != nil {
		target = db.RouteTokenTarget(jid)
	}
	if target == "" {
		if folder, ok := strings.CutPrefix(jid, "web:"); ok {
			target = folder
		}
	}
	// Self-or-descendant containment on the target chat's folder (root bypasses).
	if id.IsRoot || (target != "" && (target == id.Folder || strings.HasPrefix(target, id.Folder+"/"))) {
		return nil
	}
	// The default resolution forces verb="message", so a sub-folder that handles
	// this chat only under a verb-scoped route (e.g. mention-only) resolves to an
	// ancestor and is wrongly denied. Authorize when id.Folder (or a descendant)
	// is a route target for jid ignoring verb — matching the subtree-containment
	// rule. Bug: mention-only sub-folder can't reply.
	if db.JIDRoutableToFolder != nil && db.JIDRoutableToFolder(jid, id.Folder) {
		return nil
	}
	if target == "" {
		return fmt.Errorf("forbidden: chat %s has no route in this instance", jid)
	}
	return fmt.Errorf("forbidden: chat %s belongs to folder %s, not in subtree of %s",
		jid, target, id.Folder)
}

func workspaceRel(fp string) (string, error) {
	prefix := core.ContainerHome + "/"
	if after, ok := strings.CutPrefix(fp, prefix); ok {
		return after, nil
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

// refuseText checks the delivery arguments, without rewriting the agent's words.
func refuseText(text string, allowEmpty bool) error {
	if !allowEmpty && strings.TrimSpace(text) == "" {
		return fmt.Errorf("delivery text is empty")
	}
	for _, literal := range []string{"$WEB_HOST", "/home/node/", "/var/lib/www/"} {
		if strings.Contains(text, literal) {
			return fmt.Errorf("delivery text contains %s; use the public URL instead", literal)
		}
	}
	return nil
}

// buildMCPServer wires one group's tool surface.
//
// callerSub is the socket's own principal — `folder:<folder>` for a turn. It is the
// AUDIT actor and the resreg identity, and it switches row-based authz on (empty =
// dev/tests, no check). It is NOT an authorization input: the principals win from
// db.Authorize's injected closure, which holds the turn's route AND its attested
// sender (routd.turnAuthorize). Two notions of "who" used to live in this file — the
// socket's folder and the trigger's sender — and passing both to the gate is exactly
// the stacking that builds a confused deputy, so only one reaches it.
func buildMCPServer(routd RoutdFns, db StoreFns, folder string, isRoot bool, callerSub string) *server.MCPServer {
	identity := auth.Resolve(folder)
	identity.IsRoot = identity.IsRoot || isRoot
	srv := server.NewMCPServer("arizuko", "1.0")

	authorizeCall := func(name string, params map[string]string) bool {
		if callerSub == "" || db.Authorize == nil {
			return true
		}
		return db.Authorize(folder, "mcp:"+name, params)
	}

	// visible is the tools/list gate: a management/messaging tool shows iff the caller
	// holds it (auth.EffectiveActions via db.Visible). Nil Visible (container/tests) or
	// a root turn advertises everything; the call-time authorizeCall still gates.
	visible := func(name string) bool {
		return isRoot || db.Visible == nil || db.Visible(name)
	}

	registerRaw := func(name, desc string, opts []mcp.ToolOption, h server.ToolHandlerFunc) {
		if !visible(name) {
			return
		}
		all := append([]mcp.ToolOption{mcp.WithDescription(desc)}, opts...)
		tool := mcp.NewTool(name, all...)
		// mcp.NewTool seeds InputSchema.Type, and Tool.MarshalJSON refuses a tool
		// that carries it beside a raw schema. tools/list marshals as ONE
		// document, so one such tool costs the turn every other tool with it.
		// Clearing Type is what mcp-go's own WithInputSchema does; doing it at the
		// single registration point holds for every raw-schema caller (BUGS L222).
		if tool.RawInputSchema != nil {
			tool.InputSchema.Type = ""
		}
		srv.AddTool(tool, h)
	}
	emitAuthzDenied := func(tool, actorSub string) {
		actor := actorSub
		if actor == "" {
			actor = "agent:" + folder
		}
		if db.LogIPCAudit != nil {
			_ = db.LogIPCAudit(folder, actor, tool, "{}", "authz_denied")
		}
		if routd.Audit != nil {
			routd.Audit.EmitSystem(audit.SystemEvent{
				ActorSub: actor,
				Tool:     tool,
				Folder:   folder,
				Outcome:  audit.Outcome{Status: "authz_denied"},
			})
		}
	}

	granted := func(name, desc string, opts []mcp.ToolOption, h server.ToolHandlerFunc) {
		registerRaw(name, desc, opts, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			// Magnitude: the caller must hold mcp:<name> (role:member floor or a
			// delegated grant). The per-target containment (self-or-descendant) is the
			// handler's authzStructural, which re-runs auth.Authorize on the arg target.
			if !authorizeCall(name, nil) {
				emitAuthzDenied(name, callerSub)
				return toolErr(name + ": not permitted")
			}
			return h(ctx, req)
		})
	}

	// gateJID is the two-gate prologue the messaging tools share: magnitude
	// (mcp:<tool> held, with the target jid bound) then JID containment. They
	// cannot use granted(), which passes a nil param map — so the prologue was
	// hand-copied at eight sites and none emitted the denial granted() emits
	// (BUGS F70). nil = the call may proceed.
	gateJID := func(tool, jid string) error {
		if !authorizeCall(tool, map[string]string{"jid": jid}) {
			emitAuthzDenied(tool, callerSub)
			return fmt.Errorf("%s: not permitted", tool)
		}
		if err := authorizeJID(identity, tool, jid, db); err != nil {
			emitAuthzDenied(tool, callerSub)
			return err
		}
		return nil
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
		if routd.Audit == nil {
			return
		}
		outcome := audit.Outcome{Status: "ok"}
		if err != nil {
			outcome = audit.Outcome{Status: "error", Detail: err.Error()}
		}
		routd.Audit.EmitSystem(audit.SystemEvent{
			ActorSub: actor,
			Tool:     tool,
			Folder:   targetFolder,
			Params:   params,
			Outcome:  outcome,
		})
	}

	// authzStructural is the per-target containment for the hand-authored management
	// tools (5/33-paths-roles: one evaluator). It re-runs auth.Authorize with the ACTUAL target as
	// the scope, so a delegated row scoped to a subtree covers exactly that subtree.
	// An empty target = the caller's own folder (magnitude-only tools). Root/elevated
	// pass via db.Authorize's allow-all; a nil Authorize (container/tests) is open.
	authzStructural := func(action, target string) error {
		if target == "" {
			target = folder
		}
		if callerSub == "" || db.Authorize == nil || db.Authorize(target, "mcp:"+action, nil) {
			return nil
		}
		emitAuthzDenied(action, callerSub)
		return fmt.Errorf("forbidden: %s on %s is outside %s's granted scope", action, target, folder)
	}

	// MCP connectors. Each configured endpoint is discovered with THIS folder's
	// credential — the catalog is per folder because the credential is — then each
	// remote tool registers as a broker tool that re-resolves per call.
	for i := range db.Connectors {
		for _, tool := range discoverConnector(&db.Connectors[i], db, folder) {
			tool := tool // capture
			// Skip announcement when the turn lacks the grant. A default grant of
			// mcp:* covers every tool, so this only hides tools an explicit deny covers.
			if db.Authorize != nil && !db.Authorize(folder, "mcp:"+tool.LocalName, nil) {
				continue
			}
			opts := []mcp.ToolOption{}
			if len(tool.InputSchema) > 0 {
				opts = append(opts, mcp.WithRawInputSchema(tool.InputSchema))
			}
			granted(tool.LocalName, tool.Description, opts,
				func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					var secrets map[string]string
					if db.ResolveConnectorSecrets != nil && tool.Connector != nil {
						var rerr error
						secrets, rerr = db.ResolveConnectorSecrets(folder, tool.LocalName, tool.Connector.Secrets)
						if rerr != nil {
							return mcp.NewToolResultError(rerr.Error()), nil
						}
					}
					return CallConnectorTool(ctx, tool, req.GetArguments(), secrets)
				})
		}
	}

	// REST ext tools. Each call makes an HTTP request; secrets are
	// resolved per-call and scrubbed from the response.
	for i := range db.ExtTools {
		tool := db.ExtTools[i] // capture
		if db.Authorize != nil && !db.Authorize(folder, tool.Scope, nil) {
			continue
		}
		opts := []mcp.ToolOption{}
		if len(tool.InputSchema) > 0 {
			opts = append(opts, mcp.WithRawInputSchema(tool.InputSchema))
		}
		granted(tool.LocalName, tool.Description, opts,
			func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				needed := tool.Secrets
				var secrets map[string]string
				if db.ResolveConnectorSecrets != nil {
					var rerr error
					secrets, rerr = db.ResolveConnectorSecrets(folder, tool.LocalName, needed)
					if rerr != nil {
						return mcp.NewToolResultError(rerr.Error()), nil
					}
				}
				// The 401 retry hook (spec 5/15). nil when the host wires no
				// refresher, which leaves the single-attempt behaviour.
				var refresh func(context.Context) (map[string]string, error)
				if db.RefreshConnectorSecrets != nil {
					refresh = func(context.Context) (map[string]string, error) {
						return db.RefreshConnectorSecrets(folder, tool.LocalName, needed)
					}
				}
				return CallExtTool(ctx, tool, req.GetArguments(), secrets, refresh)
			})
	}

	registerRaw("send", "Send a notification or a message to an explicit chat. Use `reply` for answers and `send_file` for files. Where supported, sends share the current task's existing thread or the declared summary's thread. An explicit different chat keeps its own destination. Returns the sent message's `id`. Use `<status>` and `TodoWrite` for live progress.",
		[]mcp.ToolOption{
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("text", mcp.Required()),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chatJid", "")
			if err := gateJID("send", jid); err != nil {
				return toolErr(err.Error())
			}
			text := req.GetString("text", "")
			if err := refuseText(text, false); err != nil {
				return toolErr(err.Error())
			}
			snippet := text
			if len(snippet) > 60 {
				snippet = snippet[:60]
			}
			slog.Info("send", "folder", folder, "jid", jid, "text", snippet)
			platformID, err := internalSend(routd, db, folder, jid, text, nil)
			if err != nil {
				return toolErr(err.Error())
			}
			return toolJSON(map[string]any{"ok": true, "id": platformID})
		})

	registerRaw("reply", "Deliver the answer in the current conversation. Existing threads take precedence. Where supported, replies follow the declared task summary's thread. Omit `replyToId` to use the current conversation, or supply an earlier message to quote. A quote outside the destination thread may be omitted. An explicit different chat keeps its own destination. Returns the posted message's `id`. Use `<status>` and `TodoWrite` for live progress.",
		[]mcp.ToolOption{
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("text", mcp.Required()),
			mcp.WithString("replyToId"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chatJid", "")
			if err := gateJID("reply", jid); err != nil {
				return toolErr(err.Error())
			}
			if routd.SendReply == nil {
				return toolErr("reply not configured")
			}
			text := req.GetString("text", "")
			if err := refuseText(text, false); err != nil {
				return toolErr(err.Error())
			}
			replyToID := req.GetString("replyToId", "")
			if replyToID == "" && db.GetLastReplyID != nil {
				// Resolve under the active turn's topic — the same key the
				// gateway seeds via SetLastReply. "" would miss the seed for
				// in-thread turns (topic = thread_ts) and post to root.
				replyToID = db.GetLastReplyID(jid, activeTopic(db, folder))
			}
			msgID := core.MsgID("mcp")
			platformID, threadRoot, err := routd.SendReply(jid, text, replyToID, msgID)
			if err != nil {
				err = recordOutboundFailure(routd, db, jid, text, folder, replyToID, threadRoot, msgID, true, err)
				return toolErr(err.Error())
			}
			recordOutboundReply(routd, db, jid, text, platformID, folder, threadRoot, msgID)
			return toolJSON(map[string]any{"ok": true, "id": platformID})
		})

	registerRaw("send_file", "Deliver a file from the group workspace (~/) to a chat. Works on every platform whose channel registered the tool — don't second-guess by platform name (telegram, discord, whatsapp all supported). Use when the user asked for a file (image, doc, CSV, audio) or when output would exceed a chat-reasonable length. `caption` IS the accompanying message — a 2-4 sentence TL;DR of the file's key findings, so the user understands it without opening it — never follow with `send`. Not for inline text the user can read in-chat.",
		[]mcp.ToolOption{
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("filepath", mcp.Required()),
			mcp.WithString("filename"),
			mcp.WithString("caption", mcp.Description("Message text to accompany the file. This IS the message — do not output separate text.")),
			mcp.WithString("replyToId", mcp.Description("Optional reply target where supported. Omit to use the current task's thread placement. An explicit different chat keeps its own destination.")),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chatJid", "")
			if err := gateJID("send_file", jid); err != nil {
				return toolErr(err.Error())
			}
			fp := req.GetString("filepath", "")
			name := req.GetString("filename", "")
			caption := req.GetString("caption", "")
			if err := refuseText(caption, true); err != nil {
				return toolErr(err.Error())
			}
			replyToID := req.GetString("replyToId", "")
			// Fall back to the active turn's topic so a file sent inside a
			// thread lands in that thread, not the channel root. Same key
			// the gateway seeds via SetLastReply (mirrors the `reply` fix).
			threadID := activeTopic(db, folder)
			rel, err := workspaceRel(fp)
			if err != nil {
				return toolErr(err.Error())
			}
			localPath := filepath.Join(routd.GroupsDir, folder, rel)
			groupRoot := filepath.Join(routd.GroupsDir, folder)
			if _, err := mountsec.ValidateFilePath(localPath, groupRoot); err != nil {
				return toolErr("path outside group dir")
			}
			slog.Info("send_file", "folder", folder, "jid", jid, "path", localPath)
			platformID, err := internalSend(routd, db, folder, jid, caption,
				[]internalSendFile{{LocalPath: localPath, Filename: name, ReplyTo: replyToID, ThreadID: threadID}},
			)
			if err != nil {
				return toolErr(err.Error())
			}
			return toolJSON(map[string]any{"ok": true, "id": platformID})
		})

	registerRaw("send_voice", "Deliver `text` as a synthesized voice message on the platform — push-to-talk on Telegram/WhatsApp, audio attachment on Discord. Use when the user sent voice and expects voice back, or when the persona is voice-first. Not for music/file delivery (use `send_file`). `voice` defaults to the persona's voice from PERSONA.md frontmatter or the instance default; pass an explicit voice name to override.",
		[]mcp.ToolOption{
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("text", mcp.Required()),
			mcp.WithString("voice", mcp.Description("Optional voice name (backend-specific, e.g. 'af_bella' for Kokoro). Omit to use PERSONA.md frontmatter or instance default.")),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chatJid", "")
			if err := gateJID("send_voice", jid); err != nil {
				return toolErr(err.Error())
			}
			if routd.SendVoice == nil {
				return toolErr("send_voice not configured")
			}
			text := strings.TrimSpace(req.GetString("text", ""))
			if err := refuseText(text, false); err != nil {
				return toolErr(err.Error())
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
			platformID, thread, err := routd.SendVoice(jid, text, voice, folder, activeTopic(db, folder))
			if err != nil {
				return toolMaybeUnsupported(err)
			}
			recordOutboundReply(routd, db, jid, text, platformID, folder, thread, "")
			return toolJSON(map[string]any{"ok": true, "id": platformID})
		})

	registerRaw("post", "Create a new top-level post on a platform (bluesky post, discord channel message). Use for broadcast/announcement content that isn't replying to anyone. Not for replies (`reply`), direct messages (`send`), or file delivery (`send_file`). Returns the new post's `id` (for `edit`, `delete`, or as `replyToId` to chain a thread).",
		[]mcp.ToolOption{
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithString("content", mcp.Required()),
			mcp.WithArray("media"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chatJid", "")
			if err := gateJID("post", jid); err != nil {
				return toolErr(err.Error())
			}
			if routd.Post == nil {
				return toolErr("post not configured")
			}
			content := req.GetString("content", "")
			if err := refuseText(content, false); err != nil {
				return toolErr(err.Error())
			}
			var mediaPaths []string
			if raw := req.GetStringSlice("media", nil); len(raw) > 0 {
				for _, fp := range raw {
					rel, err := workspaceRel(fp)
					if err != nil {
						return toolErr(err.Error())
					}
					localPath := filepath.Join(routd.GroupsDir, folder, rel)
					groupRoot := filepath.Join(routd.GroupsDir, folder)
					if _, err := mountsec.ValidateFilePath(localPath, groupRoot); err != nil {
						return toolErr("media path outside group dir")
					}
					mediaPaths = append(mediaPaths, localPath)
				}
			}
			slog.Info("post", "folder", folder, "jid", jid, "media", len(mediaPaths))
			platformID, err := routd.Post(jid, content, mediaPaths)
			if err != nil {
				return toolMaybeUnsupported(err)
			}
			recordOutbound(routd, db, jid, content, platformID, folder, "")
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
		// successful call — for verbs that author new content on the agent's own
		// feed (quote, repost). nil for relay/reaction verbs: forward targets a
		// foreign chat, and like/delete/edit mutate existing content.
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
				if err := gateJID(s.name, jid); err != nil {
					return toolErr(err.Error())
				}
				slog.Info(s.name, "folder", folder, "jid", jid)
				id, err := s.call(vals)
				// A social verb leaves no `messages` row unless it authors new
				// content (see `record` above), so without this the mutation
				// verbs — like/dislike/delete/edit/forward — left NO trace at
				// all: an agent could delete a message and nothing recorded it.
				emitSys(s.name, folder, callerSub, auditParams(vals), err)
				if err != nil {
					return toolMaybeUnsupported(err)
				}
				if s.record != nil {
					recordOutbound(routd, db, jid, s.record(vals), id, folder, "")
				}
				if s.idOut {
					return toolJSON(map[string]any{"ok": true, "id": id})
				}
				return toolOK()
			})
	}

	regSocial(socialAct{
		name: "like",
		desc: "Add a reaction to an existing message (unicode emoji on discord/slack/telegram, like on bluesky). `targetId` MUST be the platform-native id: the `platform_id=` attribute of the message XML, never `id=` (an internal DB id no platform accepts). Prefer a reaction over a limp text 'ok'/'done' when its meaning is unambiguous (👍 ack, ✅ done, 👀 seen, ❤️ warmth); when nuance or a question is present, react AND `reply` — a reaction alone reads as dismissal. Mirror the user's own emoji register and never inject enthusiasm they haven't shown; in DMs react sparingly, never to your own messages, never 👍 on something that deserves a word. Not for new posts (`post`). Platform decides what reaction strings are valid; unsupported platforms return an error — do not retry.",
		args: []string{"chatJid", "targetId", "reaction"},
		call: func(a map[string]string) (string, error) {
			if routd.Like == nil {
				return "", errors.New("like not configured")
			}
			return "", routd.Like(a["chatJid"], a["targetId"], a["reaction"])
		},
	})

	regSocial(socialAct{
		name: "delete",
		desc: "Delete a post/message previously created by this agent (platform enforces authorship; a user's message errors — do not retry). `targetId` is the `platform_id=` attribute of the message XML, never `id=`. Use to retract an incorrect or superseded post. Not for changing text (`edit`) or for hiding inbound messages.",
		args: []string{"chatJid", "targetId"},
		call: func(a map[string]string) (string, error) {
			if routd.Delete == nil {
				return "", errors.New("delete not configured")
			}
			return "", routd.Delete(a["chatJid"], a["targetId"])
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
			if routd.Forward == nil {
				return "", errors.New("forward not configured")
			}
			return routd.Forward(a["sourceMsgId"], a["targetJid"], a["comment"])
		},
	})

	regSocial(socialAct{
		name:   "quote",
		desc:   "Republish a message on your own feed with added commentary (Bluesky quote, X quote-tweet). Native only — Discord has no quote primitive and returns unsupported with a hint to use `post(content=..., source_url=...)`. Not for in-chat threaded replies (`reply`) or simple amplification (`repost`).",
		args:   []string{"chatJid", "sourceMsgId", "comment"},
		idOut:  true,
		record: func(a map[string]string) string { return a["comment"] },
		call: func(a map[string]string) (string, error) {
			if routd.Quote == nil {
				return "", errors.New("quote not configured")
			}
			return routd.Quote(a["chatJid"], a["sourceMsgId"], a["comment"])
		},
	})

	regSocial(socialAct{
		name:   "repost",
		desc:   "Amplify a message on your own feed without added text (Bluesky repost, X retweet). Use to endorse-and-share. Not for commentary (`quote`) or sending a copy to a different chat (`forward`).",
		args:   []string{"chatJid", "sourceMsgId"},
		idOut:  true,
		record: func(a map[string]string) string { return "" },
		call: func(a map[string]string) (string, error) {
			if routd.Repost == nil {
				return "", errors.New("repost not configured")
			}
			return routd.Repost(a["chatJid"], a["sourceMsgId"])
		},
	})

	regSocial(socialAct{
		name: "dislike",
		desc: "Endorse-negative on a message (Discord 👎 reaction). `targetId` is the `platform_id=` attribute of the message XML, never `id=`. Native only — Bluesky and most platforms have no native downvote and return unsupported with a hint.",
		args: []string{"chatJid", "targetId"},
		call: func(a map[string]string) (string, error) {
			if routd.Dislike == nil {
				return "", errors.New("dislike not configured")
			}
			return "", routd.Dislike(a["chatJid"], a["targetId"])
		},
	})

	regSocial(socialAct{
		name: "edit",
		desc: "Modify a message previously sent by this agent in-place (Discord, Slack, Bluesky, Telegram own bot messages) — corrections, a live status. `targetId` is the `platform_id=` attribute of the message XML or the `id` that `reply`/`send` returned, never the XML `id=`. Preserves the platform message id. Platform windows apply (Telegram ≤48h, WhatsApp ~15m); past the window the adapter returns unsupported — do not retry. Not for retract-and-resend (use `delete` then `post`/`send`). Email is unsupported.",
		args: []string{"chatJid", "targetId", "content"},
		call: func(a map[string]string) (string, error) {
			if err := refuseText(a["content"], false); err != nil {
				return "", err
			}
			if routd.Edit == nil {
				return "", errors.New("edit not configured")
			}
			return "", routd.Edit(a["chatJid"], a["targetId"], a["content"])
		},
	})

	regSocial(socialAct{
		name: "pin_message",
		desc: "Pin a message to a chat/channel (Slack pins.add, Discord channel pin, Telegram pinned message). `targetId` is the `platform_id=` attribute of the message XML, never `id=`. Use to mark a live status surface (deploy progress, standup link) or anchor a reference. Not for editing content (`edit`) or amplifying on a feed (`repost`/`quote`). Bluesky/Email/WhatsApp return unsupported.",
		args: []string{"chatJid", "targetId"},
		call: func(a map[string]string) (string, error) {
			if routd.Pin == nil {
				return "", errors.New("pin not configured")
			}
			return "", routd.Pin(a["chatJid"], a["targetId"])
		},
	})

	regSocial(socialAct{
		name: "unpin_message",
		desc: "Remove the pin on a specific message (Slack pins.remove, Discord channel unpin, Telegram unpinChatMessage). `targetId` is the `platform_id=` attribute of the message XML, never `id=`. Use to retire a status surface or rotate the pinned reference. Not for clearing every pin (use `unpin_all` on Slack/Telegram) or deleting the message itself (`delete`).",
		args: []string{"chatJid", "targetId"},
		call: func(a map[string]string) (string, error) {
			if routd.Unpin == nil {
				return "", errors.New("unpin not configured")
			}
			return "", routd.Unpin(a["chatJid"], a["targetId"], false)
		},
	})

	regSocial(socialAct{
		name: "unpin_all",
		desc: "Clear every pin in a chat/channel (Slack: iterates pins.list+pins.remove; Telegram: unpinAllChatMessages). Use when wholesale resetting a channel's pinned set. Discord has no bulk primitive — call `unpin_message` per id. Not for removing one pin (`unpin_message`).",
		args: []string{"chatJid"},
		call: func(a map[string]string) (string, error) {
			if routd.Unpin == nil {
				return "", errors.New("unpin not configured")
			}
			return "", routd.Unpin(a["chatJid"], "", true)
		},
	})

	if routd.PaneSetPrompts != nil {
		registerRaw("pane_set_prompts",
			"Slack only — stage suggested-prompt buttons shown at the bottom of the assistant pane after your next reply lands. Fire-and-forget; the buttons appear once and persist until your next pane_set_prompts call. Use after a reply when you can anticipate the user's likely follow-ups (e.g. \"dig deeper\", \"summarise\", \"export\"). Not for sending messages (use `send`). 3-4 prompts is the visible cap.",
			[]mcp.ToolOption{
				mcp.WithString("chatJid", mcp.Required()),
				mcp.WithArray("prompts", mcp.Required(), mcp.Description("Array of {title, message}. Title shows on the button; message is sent as user input on click.")),
			},
			func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				jid := req.GetString("chatJid", "")
				if err := gateJID("pane_set_prompts", jid); err != nil {
					return toolErr(err.Error())
				}
				raw := req.GetArguments()["prompts"]
				prompts, err := decodePanePrompts(raw)
				if err != nil {
					return toolErr(err.Error())
				}
				slog.Info("pane_set_prompts", "folder", folder, "jid", jid, "n", len(prompts))
				if err := routd.PaneSetPrompts(jid, prompts); err != nil {
					return toolErr(err.Error())
				}
				return toolOK()
			})
	}
	if routd.PaneSetTitle != nil {
		registerRaw("pane_set_title",
			"Slack only — override the title shown at the top of the assistant pane. Fires after your next reply lands. Use to reflect the active topic (e.g. \"atlas — debugging the build\"). Defaults to \"<assistant> — chat\" when never set.",
			[]mcp.ToolOption{
				mcp.WithString("chatJid", mcp.Required()),
				mcp.WithString("title", mcp.Required()),
			},
			func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				jid := req.GetString("chatJid", "")
				if err := gateJID("pane_set_title", jid); err != nil {
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
				if err := routd.PaneSetTitle(jid, title); err != nil {
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
			if routd.ForkTopic == nil {
				return toolErr("fork_topic not configured")
			}
			parent := req.GetString("parent", "")
			child := req.GetString("child", "")
			if child == "" {
				return toolErr("child required")
			}
			if err := authzStructural("fork_topic", identity.Folder); err != nil {
				return toolErr(err.Error())
			}
			force := req.GetBool("force", false)
			if err := routd.ForkTopic(identity.Folder, parent, child, force); err != nil {
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

	// Engage can claim a fresh conversation. Disengage needs active ownership
	// or a route/source-turn binding (spec 5/G-engagement).
	engagementAuthz := func(action, jid, topic string) error {
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
		if action == "engage" && currentOwner == "" {
			return nil
		}
		return fmt.Errorf("engagement: %q owned by %q, not caller %q", jid, currentOwner, identity.Folder)
	}

	granted("engage",
		"Mark (jid, topic) engaged for the spec 5/G TTL so subsequent inbounds fire even when the route table wouldn't route them here. Use for scheduled / autonomous turns or recovery after a failed reply. Fresh conversations can be claimed; active ones require ownership.",
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
			if err := engagementAuthz("engage", jid, topic); err != nil {
				return toolErr(err.Error())
			}
			if db.SetEngagement == nil || routd.EngagementTTL <= 0 {
				return toolErr("engage not configured")
			}
			// Spec 5/G-engagement §"The primitive": write engaged_folder =
			// caller, so future inbounds steer here. Authz already validated
			// the caller may claim this conversation.
			if err := db.SetEngagement(jid, topic, identity.Folder, time.Now().Add(routd.EngagementTTL)); err != nil {
				return toolErr(err.Error())
			}
			return toolJSON(map[string]any{"ok": true})
		})

	granted("disengage",
		"Clear engagement for (jid, topic). Call before the final answer when the requested task is complete and no follow-up is needed, or immediately for a stop-only request. Use the current chat_id and topic; check success. Final prose does not undo this stop. A fresh mention or direct reply can start a new task. Caller folder must own the conversation.",
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
			if err := engagementAuthz("disengage", jid, topic); err != nil {
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
			if routd.ClearSession == nil {
				return toolErr("reset_session not configured")
			}
			if err := authzStructural("reset_session", gf); err != nil {
				return toolErr(err.Error())
			}
			slog.Info("reset_session", "folder", folder, "targetFolder", gf)
			routd.ClearSession(gf)
			return toolOK()
		})

	// Per-group ambient controls (spec 5/F-topic-lineage). Both default to the calling
	// folder; pass `folder` to edit a descendant. AuthorizeStructural
	// gates cross-folder writes to the caller's own subtree.
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
			if routd.SetGroupObserveWindow == nil || routd.GroupObserveWindow == nil {
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
			if err := authzStructural("set_observe_window", target); err != nil {
				return toolErr(err.Error())
			}
			// Absent args preserve the stored value; explicit -1 clears.
			prevM, prevC := routd.GroupObserveWindow(target)
			if !mOK {
				msgs = prevM
			}
			if !cOK {
				chars = prevC
			}
			if err := routd.SetGroupObserveWindow(target, msgs, chars); err != nil {
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
		"Toggle a group's visibility to its siblings. Defaults to this folder; pass `folder` to flip a descendant (e.g. a parent opening a child for cross-sibling observation). When open=true, sibling folders' ambient observed messages surface in that group's <observed> block (and vice versa) — see spec 5/F. Target must be caller's folder or a descendant.",
		[]mcp.ToolOption{
			mcp.WithBoolean("open", mcp.Required(),
				mcp.Description("true to expose to siblings, false to seal off")),
			mcp.WithString("folder",
				mcp.Description("target group folder; defaults to caller's folder. Must be caller's folder or a descendant.")),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if routd.SetGroupOpen == nil {
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
			if err := authzStructural("set_group_open", target); err != nil {
				return toolErr(err.Error())
			}
			if err := routd.SetGroupOpen(target, open); err != nil {
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
			if routd.AddGroupWatcher == nil {
				return toolErr("observe_group not configured")
			}
			src := req.GetString("source", "")
			if src == "" {
				return toolErr("source required")
			}
			if err := authzStructural("observe_group", src); err != nil {
				return toolErr(err.Error())
			}
			if err := routd.AddGroupWatcher(folder, src); err != nil {
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
			if routd.RemoveGroupWatcher == nil {
				return toolErr("unobserve_group not configured")
			}
			src := req.GetString("source", "")
			if src == "" {
				return toolErr("source required")
			}
			if err := authzStructural("unobserve_group", src); err != nil {
				return toolErr(err.Error())
			}
			if err := routd.RemoveGroupWatcher(folder, src); err != nil {
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
			if routd.InjectMessage == nil {
				return toolErr("inject_message not configured")
			}
			jid := req.GetString("chatJid", "")
			if jid == "" {
				return toolErr("chatJid required")
			}
			// inject_message carries no per-jid containment (the old gate was depth-derived);
			// magnitude on the caller's own folder is the whole check — a grant-holder
			// may inject anywhere, as before.
			if err := authzStructural("inject_message", identity.Folder); err != nil {
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
			mid, err := routd.InjectMessage(jid, req.GetString("content", ""), sender, senderName)
			if err != nil {
				return toolErr(err.Error())
			}
			slog.Info("message injected", "id", mid, "chatJid", jid, "sourceGroup", folder)
			return toolJSON(map[string]any{"injected": true, "id": mid})
		})

	// register_group + refresh_groups moved to the groups resreg seam (spec 5/16,
	// the last agent-face fold): routd owns the shared handler and mounts them on
	// this server via the ServeMCP postBuild seam. register_group is a FORWARDER
	// (its group-row + route + git-init FS side-effects via s.registerGroup can't
	// ride a resreg tx), so its auth + audit live in routd/groups_resource.go.

	granted("escalate_group", "Hand a prompt up to this group's parent folder; the parent responds back through this child. Use when the request exceeds this group's authority or needs operator review. Not for peer/child handoff (delegate_group) or creating a new group (register_group). Depth capped at 1.",
		[]mcp.ToolOption{
			mcp.WithString("prompt", mcp.Required()),
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithNumber("depth"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			// EnqueueMessageCheck is OPTIONAL. routd does not set it — its loop polls
			// new messages by timestamp. Demanding it here made escalate_group and
			// delegate_group return "not configured" on EVERY call in the split routd:
			// registered, listed, and dead (BUGS L65). PutMessage is the real
			// dependency; the enqueue is a latency optimisation.
			if db.PutMessage == nil {
				return toolErr("escalate_group not configured: no PutMessage")
			}
			if err := authzStructural("escalate_group", folder); err != nil {
				return toolErr(err.Error())
			}
			prompt := req.GetString("prompt", "")
			chatJid := req.GetString("chatJid", "")
			depth := req.GetInt("depth", 0)
			if depth >= 1 {
				return toolErr(fmt.Sprintf("delegation depth %d exceeds limit 1", depth))
			}
			parent, _, found := strings.CutLast(folder, "/")
			if !found {
				return toolErr("unauthorized: no parent group")
			}
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
				Sender:        store.EscalateSender,
				Content:       wrapped,
				Timestamp:     time.Now(),
				ForwardedFrom: fwdFrom,
			}); err != nil {
				return toolErr(err.Error())
			}
			if routd.EnqueueMessageCheck != nil {
				routd.EnqueueMessageCheck(parent)
			}
			return toolJSON(map[string]any{"queued": true, "parent": parent})
		})

	granted("delegate_group", "Hand a prompt down to a specific child group for async execution; the child runs in its own session and workspace. Use to offload specialist work to an existing child without blocking this chat. Not for parent handoff (escalate_group) or creating the child (register_group). Depth capped at 1.",
		[]mcp.ToolOption{
			mcp.WithString("group", mcp.Required()),
			mcp.WithString("prompt", mcp.Required()),
			mcp.WithString("chatJid", mcp.Required()),
			mcp.WithNumber("depth"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			target := req.GetString("group", "")
			if err := authzStructural("delegate_group", target); err != nil {
				return toolErr(err.Error())
			}
			if db.PutMessage == nil {
				return toolErr("delegate_group not configured: no PutMessage")
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
				Sender:        store.DelegateSender,
				Content:       prompt,
				Timestamp:     time.Now(),
				ForwardedFrom: chatJid,
			}); err != nil {
				return toolErr(err.Error())
			}
			if routd.EnqueueMessageCheck != nil {
				routd.EnqueueMessageCheck(target)
			}
			return toolJSON(map[string]any{"queued": true})
		})

	// add_route/set_routes/list_routes/delete_route moved to the routes resreg
	// seam (spec 5/16); inspect_routing (inspect.go) still reads db.ListRoutes.

	// list_acl / add_acl / remove_acl are no longer hand-rolled here. They ride
	// resreg's two-face mechanism (spec 5/16): routd owns the shared handler +
	// tx/audit and mounts them on this server via the ServeMCP postBuild seam,
	// with the agent's Gate (scope-containment) + MatchingRules visibility
	// injected. See routd/acl_resource.go.

	registerRaw("invite_create",
		"Issue an invite token granting access to a path glob. The recipient accepts the token via /invite/<token> and gets an `acl` row granting admin on target_glob. The token comes back ONLY here, once — deliver it now; invite_list later shows just the `ref`. Use to onboard new collaborators to a world or sub-folder you own. The agent's authority must cover target_glob — you can't issue access you don't have.",
		[]mcp.ToolOption{
			mcp.WithString("target_glob", mcp.Required()),
			mcp.WithNumber("max_uses"),
			mcp.WithString("expires_at"),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if routd.CreateInvite == nil {
				return toolErr("invite_create not configured")
			}
			targetGlob := req.GetString("target_glob", "")
			if targetGlob == "" {
				return toolErr("target_glob required")
			}
			if err := authzStructural("invite_create", targetGlob); err != nil {
				return toolErr(err.Error())
			}
			maxUses := max(req.GetInt("max_uses", 1), 1)
			var expiresAt *time.Time
			if exp := req.GetString("expires_at", ""); exp != "" {
				t, err := time.Parse(time.RFC3339, exp)
				if err != nil {
					return toolErr("invalid expires_at: " + err.Error())
				}
				expiresAt = &t
			}
			inv, err := routd.CreateInvite(targetGlob, "agent:"+folder, maxUses, expiresAt)
			if err != nil {
				emitSys("invite_create", folder, callerSub,
					map[string]any{"target_glob": targetGlob, "max_uses": maxUses}, err)
				return toolErr(err.Error())
			}
			emitSys("invite_create", folder, callerSub,
				map[string]any{"target_glob": targetGlob, "max_uses": maxUses}, nil)
			out := map[string]any{
				"token":       inv.Token,
				"ref":         inv.Ref,
				"target_glob": inv.TargetGlob,
				"max_uses":    inv.MaxUses,
			}
			if inv.ExpiresAt != nil {
				out["expires_at"] = inv.ExpiresAt.Format(time.RFC3339)
			}
			if routd.AcceptURLBase != "" {
				out["accept_url"] = strings.TrimRight(routd.AcceptURLBase, "/") + "/invite/" + inv.Token
			}
			slog.Info("invite_create", "folder", folder, "target_glob", targetGlob, "max_uses", maxUses)
			return toolJSON(out)
		})

	// invite_list/invite_revoke: the MCP twins of REST GET/DELETE /v1/invites
	// (spec 5/16-mcp-rest-unification). invite_list is read-only + self-filtered (this folder's
	// invites only), so it carries no authzStructural — like list_routes. The
	// folder scope is applied by the routd closure (issued_by="agent:"+folder),
	// not from any caller arg.
	granted("invite_list",
		"List the invites THIS agent has issued (ref, target glob, expiry, use count). Raw tokens are NOT returned — they're shown once at invite_create; `ref` identifies an invite for invite_revoke without being redeemable. Read-only; shows only your own folder's invites, never another folder's. Use to audit outstanding invites before invite_revoke. Not for issuing (invite_create) or revoking (invite_revoke).",
		nil,
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if routd.ListInvites == nil {
				return toolErr("invite_list not configured")
			}
			invs, err := routd.ListInvites("agent:" + folder)
			if err != nil {
				return toolErr(err.Error())
			}
			out := make([]map[string]any, 0, len(invs))
			for _, inv := range invs {
				row := map[string]any{
					"ref":         inv.Ref,
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
		"Revoke an invite YOU issued so it can no longer be redeemed, addressed by the `ref` invite_list returns (not the raw token — that is never readable back). You may only revoke invites issued by your own folder. Not for issuing (invite_create).",
		[]mcp.ToolOption{
			mcp.WithString("ref", mcp.Required()),
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if routd.RevokeInvite == nil {
				return toolErr("invite_revoke not configured")
			}
			ref := req.GetString("ref", "")
			if ref == "" {
				return toolErr("ref required")
			}
			if err := authzStructural("invite_revoke", folder); err != nil {
				return toolErr(err.Error())
			}
			// Ownership (this invite belongs to this folder) is enforced in the
			// routd closure, which lists the folder's invites before DELETE.
			if err := routd.RevokeInvite(ref); err != nil {
				emitSys("invite_revoke", folder, callerSub, map[string]any{"ref": ref}, err)
				return toolErr(err.Error())
			}
			emitSys("invite_revoke", folder, callerSub, map[string]any{"ref": ref}, nil)
			return toolJSON(map[string]any{"ok": true, "ref": ref})
		})

	// add_acl / remove_acl migrated to routd/acl_resource.go (see the comment at
	// the former list_acl site above).

	// issue_chat_link / issue_webhook / list_tokens / revoke_token migrated to
	// routd/route_tokens_resource.go (spec 5/16, agent-MCP fold). Their mint gate
	// cap + owner-scoped revoke ride the resreg postBuild seam now.

	if db.MessagesBefore != nil {
		inspectMessages := func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			jid := req.GetString("chat_jid", "")
			if jid == "" {
				return toolErr("chat_jid required")
			}
			if !identity.IsRoot && db.JIDRoutedToFolder != nil && !db.JIDRoutedToFolder(jid, folder) {
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
			mcp.WithDescription("Return rows from the store for one chat_jid, including outbound/bot rows and errored entries. Use for routing/delivery audits or to verify what the store recorded. Not for conversation context before replying (fetch_history), and not for one-thread-only history when the chat fans out into topics (get_thread) — this shows DB truth for the whole chat."),
			mcp.WithString("chat_jid", mcp.Required()),
			mcp.WithNumber("limit"),
			mcp.WithString("before"),
		), inspectMessages)
	}

	if db.FindMessages != nil {
		srv.AddTool(mcp.NewTool("find_messages",
			mcp.WithDescription("Ranked search over every message this instance has recorded. `query` is words, and ALL of them must appear: `budget review` needs both. \"In double quotes\" matches that exact phrase; a leading `-` excludes (`budget -draft`). There is no OR, no `prefix*` wildcard and no NEAR. Optional `scope` narrows to one chat_jid (contains ':') or a folder subtree (no ':'); `sender` exact-matches the sender; `since` is an RFC3339 lower bound. Returns up to `limit` hits (default 20, max 200), best match first by BM25 rank. Each row carries chat_jid, sender, timestamp, a content snippet with «» around each hit, and the rank. Use to find a past message by content. Not for whole-chat scroll (inspect_messages), single-thread slices (get_thread), or platform-truth fallback (fetch_history)."),
			mcp.WithString("query", mcp.Required(),
				mcp.Description("Words to find, all required; \"quoted\" for an exact phrase; -word to exclude. No OR, no wildcard, no NEAR.")),
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
			// folder. An operator grant bypasses. JIDRoutedToFolder is the same
			// gate inspect_messages uses (spec 5/C).
			filtered := hits[:0]
			if !identity.IsRoot && db.JIDRoutedToFolder != nil {
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
			mcp.WithDescription("Return rows from the store scoped to one thread (chat_jid + topic). Use when a chat fans out into per-topic conversations (Telegram forum topics, web-chat topics) and you want history for a single thread, not the whole chat. Not for whole-chat history (inspect_messages) or platform-truth context (fetch_history)."),
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
			if !identity.IsRoot && db.JIDRoutedToFolder != nil && !db.JIDRoutedToFolder(jid, folder) {
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

	if routd.FetchPlatformHistory != nil {
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
			if !identity.IsRoot && db.JIDRoutedToFolder != nil && !db.JIDRoutedToFolder(jid, folder) {
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
			h, err := routd.FetchPlatformHistory(jid, before, limitVal)
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
		if routd.GroupsDir == "" {
			return toolErr("get_work not configured")
		}
		data, err := os.ReadFile(filepath.Join(routd.GroupsDir, folder, "work.md"))
		if err != nil {
			if os.IsNotExist(err) {
				return toolJSON(map[string]any{"content": "", "exists": false})
			}
			return toolErr(err.Error())
		}
		return toolJSON(map[string]any{"content": string(data), "exists": true})
	})

	// set_work (memory checkpoint) is always-on (5/33-paths-roles: reads + memory need no grant).
	srv.AddTool(mcp.NewTool("set_work",
		mcp.WithDescription("Overwrite this group's work.md with a fresh snapshot of current work, blockers, and next steps. Use at turn end to checkpoint state for the next session. This replaces the file — read with get_work first if merging."),
		mcp.WithString("content", mcp.Required()),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if routd.GroupsDir == "" {
			return toolErr("set_work not configured")
		}
		content := req.GetString("content", "")
		groupDir := filepath.Join(routd.GroupsDir, folder)
		if err := os.MkdirAll(groupDir, 0o755); err != nil {
			return toolErr(err.Error())
		}
		p := filepath.Join(groupDir, "work.md")
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
			return toolErr(err.Error())
		}
		if err := os.Rename(tmp, p); err != nil {
			_ = os.Remove(tmp)
			return toolErr(err.Error())
		}
		slog.Info("set_work", "folder", folder, "bytes", len(content))
		return toolOK()
	})

	// web_routes management tools ride resreg's two-face mechanism (spec 5/16
	// pilot): routd owns the shared handler plus tx/audit and mounts them here via
	// the ServeMCP postBuild seam. get_web_presence stays hand-authored — a
	// read-only presence report, not web_routes CRUD, with no resreg REST twin.
	granted("get_web_presence",
		"Report this folder's public web presence: its canonical hostname (derived <folder>.<HOSTING_DOMAIN> or an operator alias), the /pub/<folder>/ path that always works, and the OAuth /priv base. Use to tell a user where your site is. Read-only.",
		[]mcp.ToolOption{mcp.WithString("folder")},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			target := req.GetString("folder", folder)
			if target == "" {
				target = folder
			}
			// a scoped grant inspects its own folder or descendants; operator any.
			if !identity.IsRoot && target != folder &&
				!groupfolder.Contains(folder, target) {
				return toolErr("get_web_presence: can only query own folder or descendants")
			}
			return toolJSON(WebPresenceFor(target, routd.WebHost, routd.HostingDomain, routd.VhostAliases))
		})

	registerInspect(srv, db, identity, folder)

	return srv
}

// ListTools builds the MCP tool registry for folder and returns the tool schemas.
// visible is the tool-visibility view (a tool shows iff the folder holds it;
// reads are unconditional). Safe to call without a running container — RoutdFns
// and StoreFns are zero-valued.
//
// The result is hot-tier tools PLUS the visible cold-tier facade tools, which the
// live agent socket mounts via routd's resreg postBuild seam, not here.
// addFacadeTools derives them from the SAME resreg specs routd uses, so the
// browser shows exactly the agent's surface.
func ListTools(folder string, visible func(name string) bool) []mcp.Tool {
	srv := buildMCPServer(RoutdFns{}, StoreFns{Visible: visible}, folder, false, "")
	addFacadeTools(srv, visible)
	m := srv.ListTools()
	out := make([]mcp.Tool, 0, len(m))
	for _, st := range m {
		out = append(out, st.Tool)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// addFacadeTools registers the cold-tier facade tools onto srv for DISPLAY only.
// It walks the resreg registry and derives every resource carrying MCP metadata —
// MCPNames set is the agent-facade discriminator, so the pure-REST resource and
// dotted-name ones are skipped. visible mirrors the agent socket's filter exactly.
// The stub caller is never invoked.
func addFacadeTools(srv *server.MCPServer, visible func(name string) bool) {
	stub := func(context.Context, mcp.CallToolRequest) (resreg.Caller, error) {
		return resreg.Caller{}, nil
	}
	for _, res := range resreg.All() {
		if len(res.MCPNames) == 0 {
			continue
		}
		resreg.MCPTools(srv, *res, stub, visible)
	}
}

// holdResponse is what a suspended tool call returns to the agent: a normal tool
// RESULT, not a JSON-RPC error. The call did not fail, it is waiting, and the
// agent must be able to say so and move on rather than retry a "failure".
//
// An EMPTY pendingID is the gate reporting that it blocked the call but could not
// record it. The pending shape would tell the agent to wait for `/approve` on an
// id nobody can resolve (BUGS J3), so that case returns a JSON-RPC error.
// auditParams widens a social verb's string args for the audit sink, capping each
// value so a long `content` cannot bloat every audit_log row.
func auditParams(vals map[string]string) map[string]any {
	out := make(map[string]any, len(vals))
	for k, v := range vals {
		if len(v) > 200 {
			v = v[:200] + "…"
		}
		out[k] = v
	}
	return out
}

func holdResponse(id any, refusal, tool string) map[string]any {
	if refusal == "" {
		return map[string]any{
			"jsonrpc": "2.0", "id": id,
			"error": map[string]any{
				"code": -32603,
				"message": tool + " needs human approval and the hold could not be " +
					"recorded, so the call was blocked. There is nothing to approve — " +
					"tell the user, and an operator must check the router.",
			},
		}
	}
	// The call already has its answer by the time this frame is built — the gate
	// waited for it — so the text NAMES the verdict rather than describing a wait.
	// isError, because the call did not run: a plain result reads as "done".
	return map[string]any{
		"jsonrpc": "2.0", "id": id,
		"result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": refusal}},
			"isError": true,
		},
	}
}
