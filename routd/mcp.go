package routd

import (
	"fmt"
	"os"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/grants"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// mcp.go stands up the per-turn agent MCP socket IN-PROCESS, the same way
// gated does via ipc.ServeMCP — but wired to routd's own DB + Deliverer
// instead of federating over HTTP to runed. runTurn calls ServeTurnMCP per
// turn before dispatch (the routd half of the socket-ownership flip; runed
// still creates its own socket until Phase 3b). The tools that need the
// execution plane (spawn/setup/voice/tasks/ACL/identity) are deferred to
// runed/authd and left nil (see bugs.md).

// turnMCP is the per-turn binding the in-process fns close over: which folder/
// topic/chat/turn the socket serves and what triggered it. It mirrors the
// turn_context routd stores, narrowed to what the MCP fns read.
type turnMCP struct {
	folder  string
	topic   string
	chatJID string
	turnID  string
	trigger string
}

// buildGatedFns wires the write-side agent tools (reply/send/social/group
// controls/route-tokens/submit_turn) to routd's appendAndDeliver + Deliverer,
// porting gated's wireFns one tool at a time. Tools whose backing capability
// lives in the execution plane (runed) or auth authority (authd) stay nil;
// ipc.ServeMCP registers only the non-nil ones.
func (s *Server) buildGatedFns(t turnMCP) ipc.GatedFns {
	return ipc.GatedFns{
		EngagementTTL: s.engagementT,
		GroupsDir:     s.groupsDir,
		WebDir:        s.webDir,
		SendMessage: func(jid, text string) (string, error) {
			return s.mcpDeliver(t.turnID, jid, text, "")
		},
		SendReply: func(jid, text, replyTo string) (string, error) {
			return s.mcpDeliver(t.turnID, jid, text, replyTo)
		},
		SendDocument: func(jid, path, filename, caption, replyTo, threadID string) error {
			return s.mcpAppendDoc(t.turnID, jid, path, filename, caption, replyTo)
		},
		Like: func(jid, target, reaction string) error {
			return s.deliver.React(jid, target, reaction)
		},
		Dislike: func(jid, target string) error { return s.deliver.Dislike(jid, target) },
		Delete:  func(jid, target string) error { return s.deliver.Delete(jid, target) },
		Edit: func(jid, target, content string) error {
			return s.deliver.Edit(jid, target, content)
		},
		Pin:   func(jid, target string) error { return s.deliver.Pin(jid, target) },
		Unpin: func(jid, target string, all bool) error { return s.deliver.Unpin(jid, target, all) },
		Post: func(jid, content string, media []string) (string, error) {
			return s.deliver.Post(jid, content, media)
		},
		Forward: func(src, target, comment string) (string, error) {
			return s.deliver.Forward(src, target, comment)
		},
		Quote: func(jid, src, comment string) (string, error) {
			return s.deliver.Quote(jid, src, comment)
		},
		Repost:         func(jid, src string) (string, error) { return s.deliver.Repost(jid, src) },
		PaneSetPrompts: func(jid string, p []core.PanePrompt) error { return s.deliver.SetSuggestions(jid, p) },
		PaneSetTitle:   func(jid, title string) error { return s.deliver.SetName(jid, title) },
		ClearSession:   func(folder string) { _ = s.db.DeleteSession(folder, t.topic) },
		ForkTopic: func(folder, parent, child string, force bool) error {
			return s.db.ForkTopic(folder, parent, child, randHex(16), force)
		},
		SetGroupOpen:          s.db.SetGroupOpen,
		SetGroupObserveWindow: s.db.SetGroupObserveWindow,
		GroupObserveWindow:    s.db.GroupObserveWindow,
		AddGroupWatcher:       s.db.AddGroupWatcher,
		RemoveGroupWatcher:    s.db.RemoveGroupWatcher,
		GetGroups:             s.db.AllGroups,
		RegisterGroup:         func(_ string, g core.Group) error { return s.db.PutGroup(g) },
		InjectMessage: func(jid, content, sender, senderName string) (string, error) {
			id := "in-" + randHex(8)
			err := s.db.PutMessage(core.Message{
				ID: id, ChatJID: jid, Sender: sender, Name: senderName,
				Content: content, Timestamp: time.Now().UTC(), Verb: "message",
				Status: core.MessageStatusSent,
			})
			// No explicit EnqueueMessageCheck: routd's loop polls new messages
			// by timestamp, so the injected row is picked up on the next tick.
			return id, err
		},
		IssueRouteToken:  s.mcpIssueRouteToken,
		ListRouteTokens:  s.mcpListRouteTokens,
		RevokeRouteToken: s.mcpRevokeRouteToken,
		SubmitTurn:       s.mcpSubmitTurn,
	}
}

// mcpDeliver is the in-process reply/send: it ONLY delivers via the Deliverer,
// matching gated's SendReply/SendMessage = deliver-only. The ipc tool layer's
// recordOutbound persists the bot row + threads (SetLastReply) + bumps
// engagement, so this must NOT do any of that or the row double-persists.
// Returns the platform id. returnTarget redirects delegated turns to the origin.
func (s *Server) mcpDeliver(turnID, jid, text, replyTo string) (string, error) {
	if s.deliver == nil {
		return "", nil
	}
	topic := ""
	if tc, ok := s.db.GetTurnContext(turnID); ok {
		jid = returnTarget(tc, jid)
		topic = tc.Topic
	}
	return s.deliver.Send(jid, text, replyTo, topic, "mcp-"+randHex(8))
}

// mcpAppendDoc is the in-process send_file: deliver-only (internalSend's
// recordOutbound persists the row), mirroring mcpDeliver for documents.
func (s *Server) mcpAppendDoc(turnID, jid, path, name, caption, replyTo string) error {
	if s.deliver == nil {
		return nil
	}
	if tc, ok := s.db.GetTurnContext(turnID); ok {
		jid = returnTarget(tc, jid)
	}
	_, err := s.deliver.Document(jid, path, name, caption, replyTo, "mcp-"+randHex(8))
	return err
}

// mcpIssueRouteToken maps the ipc.GatedFns five-arg signature onto routd's
// IssueRouteToken(jid, owner), constructing the jid the same way the REST
// handlers (handleTokenChat/handleTokenHook) do.
func (s *Server) mcpIssueRouteToken(kind, ownerFolder, targetFolder, sourceLabel, jidSuffix string) (ipc.RouteTokenInfo, error) {
	if targetFolder == "" {
		return ipc.RouteTokenInfo{}, fmt.Errorf("target_folder required")
	}
	if ownerFolder == "" {
		ownerFolder = targetFolder
	}
	// Validate the agent-supplied segments before they enter the JID — the REST
	// handlers (handleTokenChat/Hook) gate on segRe; the MCP path must too, or an
	// agent can inject `/` / `..` and corrupt the stored route-token JID.
	if sourceLabel != "" && !segRe.MatchString(sourceLabel) {
		return ipc.RouteTokenInfo{}, fmt.Errorf("source_label must match %s", segRe.String())
	}
	if jidSuffix != "" && !segRe.MatchString(jidSuffix) {
		return ipc.RouteTokenInfo{}, fmt.Errorf("jid_suffix must match %s", segRe.String())
	}
	var jid, urlPrefix string
	switch kind {
	case "chat":
		jid = "web:" + targetFolder
		urlPrefix = "/chat/"
	case "hook":
		if sourceLabel == "" {
			return ipc.RouteTokenInfo{}, fmt.Errorf("source_label required for webhook")
		}
		jid = "hook:" + targetFolder + "/" + sourceLabel
		urlPrefix = "/hook/"
	default:
		return ipc.RouteTokenInfo{}, fmt.Errorf("kind must be chat|hook")
	}
	if jidSuffix != "" {
		jid += "/" + jidSuffix
	}
	token, created, err := s.db.IssueRouteToken(jid, ownerFolder)
	if err != nil {
		return ipc.RouteTokenInfo{}, err
	}
	info := ipc.RouteTokenInfo{RawToken: token, JID: jid, OwnerFolder: ownerFolder}
	info.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if s.webHost != "" {
		info.URL = s.webHost + urlPrefix + token
		if kind == "chat" {
			info.URL += "/"
		}
	}
	return info, nil
}

func (s *Server) mcpListRouteTokens(ownerFolder string) []ipc.RouteTokenInfo {
	rows, err := s.db.ListRouteTokens(ownerFolder)
	if err != nil {
		return nil
	}
	out := make([]ipc.RouteTokenInfo, len(rows))
	for i, r := range rows {
		out[i] = ipc.RouteTokenInfo{JID: r.JID, OwnerFolder: r.OwnerFolder}
		out[i].CreatedAt, _ = time.Parse(time.RFC3339Nano, r.CreatedAt)
	}
	return out
}

func (s *Server) mcpRevokeRouteToken(jid, ownerFolder string) (bool, error) {
	n, err := s.db.RevokeRouteTokens(jid, ownerFolder)
	return n > 0, err
}

// mcpSubmitTurn maps the ipc.TurnResult agent payload onto routd's apiv1
// shape and records it through the shared recordTurnResult writer (the same
// path the REST /result twin uses).
func (s *Server) mcpSubmitTurn(_ string, t ipc.TurnResult) error {
	req := apiv1.TurnResult{
		TurnID: t.TurnID, SessionID: t.SessionID, Status: t.Status,
		Result: t.Result, Error: t.Error, CallerSub: t.CallerSub,
	}
	if len(t.Models) > 0 {
		req.Models = make(map[string]apiv1.ModelCost, len(t.Models))
		for m, u := range t.Models {
			req.Models[m] = apiv1.ModelCost{Input: u.Input, Output: u.Output, CostCents: u.CostCents}
		}
	}
	_, err := s.recordTurnResult(t.TurnID, req)
	return err
}

// beforeStr renders an ipc time bound as the RFC3339Nano string routd's read
// methods expect; a zero time is the empty string (no bound → now).
func beforeStr(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// buildStoreFns wires the read/manage agent tools to routd.DB. Every backing
// method already exists on the DB (reads.go + db.go). Tools whose state lives
// elsewhere are deferred nil: tasks → timed, ACL/Authorize/identity/audit →
// authd, session_log → runed (see bugs.md).
func (s *Server) buildStoreFns(t turnMCP) ipc.StoreFns {
	return ipc.StoreFns{
		ListRoutes: func(_ string, _ bool) []core.Route {
			r, _ := s.db.Routes()
			return r
		},
		SetRoutes:           func(folder string, r []core.Route) error { _, e := s.db.SetRoutes(folder, r); return e },
		AddRoute:            s.db.AddRoute,
		DeleteRoute:         s.db.DeleteRoute,
		GetRoute:            s.db.GetRoute,
		DefaultFolderForJID: s.db.DefaultFolderForJID,
		JIDRoutedToFolder:   s.db.JIDRoutedToFolder,
		MessagesBefore: func(jid string, before time.Time, limit int) ([]core.Message, error) {
			return s.db.MessagesBefore(jid, beforeStr(before), limit)
		},
		MessagesByThread: func(jid, topic string, before time.Time, limit int) ([]core.Message, error) {
			return s.db.MessagesByThread(jid, topic, beforeStr(before), limit)
		},
		FindMessages: func(q, scope, sender, since string, limit int) ([]ipc.FoundMessage, error) {
			rows, err := s.db.FindMessages(q, scope, sender, since, limit)
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
		ErroredChats: func(folder string, isRoot bool) []ipc.ErroredChat {
			rows, err := s.db.ErroredChats(folder, isRoot)
			if err != nil {
				return nil
			}
			out := make([]ipc.ErroredChat, len(rows))
			for i, r := range rows {
				out[i] = ipc.ErroredChat{ChatJID: r.ChatJID, Count: r.Count, LastAt: r.LastAt, RoutedTo: r.RoutedTo}
			}
			return out
		},
		PutMessage:     s.db.PutMessage,
		GetLastReplyID: s.db.LastReplyID,
		SetLastReply:   s.db.SetLastReply,
		SetEngagement: func(jid, topic, folder string, until time.Time) error {
			return s.db.SetEngagement(jid, topic, folder, time.Until(until))
		},
		BumpEngagement: func(jid, topic, folder string, until time.Time) error {
			return s.db.SetEngagement(jid, topic, folder, time.Until(until))
		},
		EngagedFolder:   func(jid, topic string) string { f, _ := s.db.Engaged(jid, topic); return f },
		LogExternalCost: s.db.LogExternalCost,
		SetWebRoute: func(pathPrefix, access, redirectTo, folder string) error {
			return s.db.PutWebRoute(WebRouteRow{PathPrefix: pathPrefix, Access: access, RedirectTo: redirectTo, Folder: folder})
		},
		DelWebRoute:   s.db.DeleteWebRoute,
		WebRouteOwner: s.db.WebRouteOwner,
		ListWebRoutes: func(folder string) []ipc.WebRoute {
			rows, err := s.db.WebRoutes(folder)
			if err != nil {
				return nil
			}
			out := make([]ipc.WebRoute, len(rows))
			for i, r := range rows {
				out[i] = ipc.WebRoute{
					PathPrefix: r.PathPrefix, Access: r.Access, RedirectTo: r.RedirectTo,
					Folder: r.Folder, CreatedAt: r.CreatedAt,
				}
			}
			return out
		},
		CurrentTriggerSender: func(_ string) string { return t.trigger },
		CurrentTopic:         func(_ string) string { return t.topic },
	}
}

// ServeTurnMCP binds the per-turn agent MCP socket in-process: it derives the
// folder's tier-default grant rules, then stands up ipc.ServeMCP wired to
// routd's own DB + Deliverer. expectedUID gates peers (1000 = ant `node` user,
// or the dev host uid). Returns the stop func (removes the socket). Called
// per-turn from runTurn before dispatch.
//
// Operator ACL overlay is DEFERRED: routd has no acl table, so callerSub is ""
// (full operator access) and StoreFns.Authorize stays nil. The row-based ACL
// gate moves to authd (see bugs.md).
// deriveFolderGrants is the single grant-rule renderer for a folder: tier
// defaults (grants.DeriveRules) keyed on the folder's tier + world. Used by both
// ServeTurnMCP (the in-process MCP tool firewall) and dispatchRun (the rules
// shipped to runed for buildMounts/egress) so the two can't drift. ACL overlay
// is deferred (routd has no acl table — see bugs.md).
func deriveFolderGrants(rs grants.RouteSource, folder string) []string {
	tier := auth.Resolve(folder).Tier
	return grants.DeriveRules(rs, folder, tier, auth.WorldOf(folder))
}

func (s *Server) ServeTurnMCP(t turnMCP, ipcDir string) (func(), error) {
	rules := deriveFolderGrants(s.db, t.folder)
	// routd binds the socket BEFORE runed spawns the container, so the per-folder
	// ipc dir may not exist yet (gated relied on the runner's buildMounts mkdir).
	// ServeMCP only os.Removes the stale sock + Listens — never mkdirs — so create
	// the parent here or net.Listen fails on a fresh folder's first turn.
	if err := os.MkdirAll(ipcDir, 0o755); err != nil {
		return nil, err
	}
	sockPath := groupfolder.IpcSocket(ipcDir)
	expectedUID := 1000
	if uid := os.Getuid(); uid > 0 && uid != 1000 {
		expectedUID = uid
	}
	return ipc.ServeMCP(sockPath, s.buildGatedFns(t), s.buildStoreFns(t), t.folder, rules, expectedUID, "")
}
