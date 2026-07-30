package routd

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/grants"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

// turnMCP is the per-turn binding the in-process fns close over: which folder/
// topic/chat/turn the socket serves and what triggered it. It mirrors the
// turn_context routd stores, narrowed to what the MCP fns read.
type turnMCP struct {
	folder   string
	topic    string
	chatJID  string
	turnID   string
	trigger  string
	elevated bool // operator /root turn: the socket grants `*` (tier-0 grant set)
}

// buildGatedFns wires the write-side agent tools (reply/send/social/group
// controls/route-tokens/submit_turn) to routd's Deliverer; execution-plane
// tools stay nil. ipc.ServeMCP registers only the non-nil ones.
func (s *Server) buildGatedFns(t turnMCP) ipc.GatedFns {
	return ipc.GatedFns{
		AcceptURLBase:        s.webHost,
		WebHost:              s.webHost,
		HostingDomain:        s.hostingDomain,
		VhostAliases:         s.vhostAliases,
		EngagementTTL:        s.engagementT,
		GroupsDir:            s.groupsDir,
		Audit:                s.audit,
		FetchPlatformHistory: s.fetchPlatformHistory,
		SendMessage: func(jid, text string) (string, error) {
			return s.mcpDeliver(t.turnID, jid, text, "", false)
		},
		SendReply: func(jid, text, replyTo string) (string, error) {
			return s.mcpDeliver(t.turnID, jid, text, replyTo, true)
		},
		SendDocument: func(jid, path, filename, caption, replyTo, threadID string) (string, error) {
			return s.mcpAppendDoc(t.turnID, jid, path, filename, caption, replyTo, threadID)
		},
		SendVoice: func(jid, text, voice, folder, threadID string) (string, error) {
			return s.mcpSendVoice(t.turnID, jid, text, voice, folder, threadID)
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
			childUUID := core.NewSessionID()
			if err := s.db.ForkTopic(folder, parent, child, childUUID, force); err != nil {
				return err
			}
			// Copy the parent topic's Claude session jsonl so the child resumes
			// from the parent's tail instead of cold. loop is nil in pure REST
			// tests — skip then.
			if s.loop != nil {
				s.loop.copyParentSession(folder, parent, childUUID)
			}
			return nil
		},
		SetGroupOpen:          s.db.SetGroupOpen,
		SetGroupObserveWindow: s.db.SetGroupObserveWindow,
		GroupObserveWindow:    s.db.GroupObserveWindow,
		AddGroupWatcher:       s.db.AddGroupWatcher,
		RemoveGroupWatcher:    s.db.RemoveGroupWatcher,
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
		SubmitTurn: s.mcpSubmitTurn,
		SubmitStatus: func(_ string, turnID, text string) error {
			return s.mcpSubmitStatus(turnID, text)
		},
		CreateInvite: func(targetGlob, issuedBySub string, maxUses int, expiresAt *time.Time) (ipc.InviteInfo, error) {
			oc := s.onbodClient()
			if oc == nil {
				return ipc.InviteInfo{}, fmt.Errorf("invites unavailable: onbod not wired (ONBOD_URL unset)")
			}
			inv, err := oc.CreateInviteFull(targetGlob, issuedBySub, maxUses, expiresAt)
			if err != nil {
				return ipc.InviteInfo{}, err
			}
			return toInviteInfo(inv), nil
		},
		ListInvites: func(issuedBy string) ([]ipc.InviteInfo, error) {
			oc := s.onbodClient()
			if oc == nil {
				return nil, fmt.Errorf("invites unavailable: onbod not wired (ONBOD_URL unset)")
			}
			invs, err := oc.ListInvites(issuedBy)
			if err != nil {
				return nil, err
			}
			out := make([]ipc.InviteInfo, len(invs))
			for i, inv := range invs {
				out[i] = toInviteInfo(inv)
			}
			return out, nil
		},
		RevokeInvite: func(token string) error {
			oc := s.onbodClient()
			if oc == nil {
				return fmt.Errorf("invites unavailable: onbod not wired (ONBOD_URL unset)")
			}
			// Ownership gate: onbod's DELETE is token-only, so confirm the token
			// is among THIS folder's invites before revoking — an agent must not
			// revoke another folder's invite even though the wire call would.
			owned, err := oc.ListInvites("agent:" + t.folder)
			if err != nil {
				return err
			}
			found := false
			for _, inv := range owned {
				if inv.Token == token {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("invite not found or not owned by this folder")
			}
			return oc.RevokeInvite(token)
		},
	}
}

// onbodClient reaches the onbod federation handle (the same client /invite +
// /gate slash commands use). nil → onbod not wired (ONBOD_URL unset).
func (s *Server) onbodClient() OnbodClient {
	if s.loop == nil {
		return nil
	}
	return s.loop.onbod
}

// toInviteInfo maps a routd-local Invite (decoded from onbod's JSON) to the ipc
// layer's InviteInfo so the MCP tools never import store.
func toInviteInfo(inv Invite) ipc.InviteInfo {
	return ipc.InviteInfo{
		Token:       inv.Token,
		TargetGlob:  inv.TargetGlob,
		IssuedBySub: inv.IssuedBySub,
		IssuedAt:    inv.IssuedAt,
		ExpiresAt:   inv.ExpiresAt,
		MaxUses:     inv.MaxUses,
		UsedCount:   inv.UsedCount,
	}
}

// registerGroup is the manual register_group path (ipc.GatedFns.RegisterGroup).
// registerGroup persists the group, adds a room-matched
// default route (rolling the group row back if the route insert fails so a
// route-less, unreachable, un-respawnable orphan is never left behind), then
// git-init the group dir. The jid supplies the route's room literal.
func (s *Server) registerGroup(jid string, g core.Group) error {
	if err := s.db.PutGroup(g); err != nil {
		return err
	}
	match := "room=" + core.JidRoom(jid)
	if _, err := s.db.AddRoute(core.Route{Seq: 0, Match: match, Target: g.Folder}); err != nil {
		_ = s.db.DeleteGroup(g.Folder)
		return fmt.Errorf("add route for %s: %w", g.Folder, err)
	}
	ensureGroupGitRepo(filepath.Join(s.groupsDir, g.Folder))
	return nil
}

// ensureGroupGitRepo git-inits groupDir + seeds a .gitignore when neither
// exists. Non-fatal: a missing git binary or dir leaves the group untracked.
func ensureGroupGitRepo(groupDir string) {
	if _, err := os.Stat(filepath.Join(groupDir, ".git")); err == nil {
		return
	}
	if err := exec.Command("git", "init", groupDir).Run(); err != nil {
		return // git unavailable or dir missing — non-fatal
	}
	gitignore := filepath.Join(groupDir, ".gitignore")
	if _, err := os.Stat(gitignore); err == nil {
		return
	}
	lines := []string{"diary/", "episodes/", "users/", "logs/", "media/", "tmp/", "*.jl"}
	os.WriteFile(gitignore, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// mcpDeliver is the in-process reply/send: it ONLY delivers via the Deliverer.
// The ipc tool layer's recordOutbound persists the bot row + threads
// (SetLastReply) + bumps engagement, so this must NOT do any of that or the row
// double-persists. Returns the platform id. returnTarget redirects delegated
// turns to the origin. threaded marks the REPLY verb: it may root a new
// platform thread on the trigger message (replyThreadRoot); send never does.
func (s *Server) mcpDeliver(turnID, jid, text, replyTo string, threaded bool) (string, error) {
	if s.deliver == nil {
		return "", nil
	}
	topic, threadRoot := "", ""
	if tc, ok := s.db.GetTurnContext(turnID); ok {
		jid = returnTarget(tc, jid)
		topic = tc.Topic
		if threaded {
			threadRoot = s.replyThreadRoot(tc, jid)
		}
	}
	pid, err := s.deliver.Send(jid, text, replyTo, topic, threadRoot, "mcp-"+randHex(8))
	if err != nil {
		slog.Warn("mcp deliver failed", "turn_id", turnID, "jid", jid, "err", err)
	}
	return pid, err
}

// mcpAppendDoc is the in-process send_file: deliver-only (the ipc tool layer's
// recordOutbound persists the row, keyed on the returned platform id so a later
// human reply re-promotes). An empty threadID falls back to the turn's topic so
// files land in the active thread like text replies (mirrors mcpSendVoice).
func (s *Server) mcpAppendDoc(turnID, jid, path, name, caption, replyTo, threadID string) (string, error) {
	if s.deliver == nil {
		return "", nil
	}
	if tc, ok := s.db.GetTurnContext(turnID); ok {
		jid = returnTarget(tc, jid)
		if threadID == "" {
			threadID = tc.Topic
		}
	}
	pid, err := s.deliver.Document(jid, path, name, caption, replyTo, threadID, "mcp-"+randHex(8))
	if err != nil {
		slog.Warn("mcp deliver document failed", "turn_id", turnID, "jid", jid, "err", err)
	}
	return pid, err
}

// mcpSendVoice is the in-process send_voice: synthesize text → Opus via the
// TTS service (tts.go) and deliver as a voice note. deliver-only (the ipc tool
// layer's recordOutbound persists the bot row). Redirects a delegated turn to
// its origin chat, mirroring mcpDeliver.
func (s *Server) mcpSendVoice(turnID, jid, text, voice, folder, threadID string) (string, error) {
	if tc, ok := s.db.GetTurnContext(turnID); ok {
		jid = returnTarget(tc, jid)
		if threadID == "" {
			threadID = tc.Topic
		}
	}
	pid, err := s.sendVoice(jid, text, voice, folder, threadID)
	if err != nil {
		slog.Warn("mcp send voice failed", "turn_id", turnID, "jid", jid, "err", err)
	}
	return pid, err
}

// Route-token issuance/list/revocation migrated to routd/route_tokens_resource.go
// (spec 5/16, agent-MCP fold). The REST twin stays hand-rolled in tokens_http.go.

// mcpSubmitTurn maps the ipc.TurnResult agent payload onto routd's apiv1
// shape and records it through the shared recordTurnResult writer (the same
// path the REST /result twin uses).
func (s *Server) mcpSubmitTurn(_ string, t ipc.TurnResult) error {
	req := apiv1.TurnResult{
		TurnID: t.TurnID, SessionID: t.SessionID, Status: t.Status,
		Result: t.Result, Error: t.Error, CallerSub: t.CallerSub, TimedOut: t.TimedOut,
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

// mcpSubmitStatus delivers a mid-turn <status> notice immediately as an
// interim "⏳ ..." message, without ending the turn or threading it as the
// reply. Serialized per turn_id like the other turn writes; a closed turn is a
// no-op (a trailing status after the run returned has nothing to add).
func (s *Server) mcpSubmitStatus(turnID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	unlock := lockTurn(turnID)
	defer unlock()
	tc, ok := s.db.GetTurnContext(turnID)
	if !ok {
		return fmt.Errorf("no turn context for turn_id")
	}
	if s.callbackClosed(tc) {
		return nil
	}
	jid := returnTarget(tc, tc.ChatJID)
	// Edit the turn's live ⏳ message in place if one already landed — its id is the
	// last persisted verb=status row (no in-memory map). Only SEND a fresh notice on
	// the first status of the turn or when the adapter can't edit (email/WhatsApp/
	// Reddit → ErrUnsupported). A real edit failure (401/500/timeout) surfaces — it
	// is never silently re-posted as a duplicate (spec 5/24).
	if id := s.db.LastStatusPlatformID(turnID); id != "" && s.deliver != nil {
		err := s.deliver.Edit(jid, id, "⏳ "+text)
		if err == nil {
			return nil
		}
		if !errors.Is(err, chanlib.ErrUnsupported) {
			return err
		}
	}
	s.deliverStatus(tc, jid, text)
	return nil
}

// fetchPlatformHistory backs the fetch_history MCP tool: proxy to the adapter
// owning jid (Deliverer.FetchHistory → GET /v1/history), decode the
// HistoryResponse, cache rows in routd.db (dedup by ID via PutMessage), and
// return the decoded messages. On no-adapter/decode failure it returns
// source:"cache"/"cache-only" with whatever the local DB holds.
func (s *Server) fetchPlatformHistory(jid string, before time.Time, limit int) (ipc.PlatformHistory, error) {
	fallback := func(source string) (ipc.PlatformHistory, error) {
		msgs, err := s.db.MessagesBefore(jid, beforeStr(before), limit)
		if err != nil {
			return ipc.PlatformHistory{}, err
		}
		return ipc.PlatformHistory{Source: source, Messages: msgs}, nil
	}
	if s.deliver == nil {
		return fallback("cache-only")
	}
	raw, err := s.deliver.FetchHistory(jid, before, limit)
	if err != nil {
		slog.Warn("fetch_history: adapter failed, falling back to cache", "jid", jid, "err", err)
		return fallback("cache")
	}
	var resp chanlib.HistoryResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fallback("cache")
	}
	out := ipc.PlatformHistory{Source: resp.Source, Cap: resp.Cap}
	for _, m := range resp.Messages {
		cm := core.Message{
			ID: m.ID, ChatJID: m.ChatJID, Sender: m.Sender, Content: m.Content,
			Timestamp: time.Unix(m.Timestamp, 0).UTC(), ReplyToID: m.ReplyTo,
		}
		if cm.ID != "" {
			_ = s.db.PutMessage(cm)
		}
		out.Messages = append(out.Messages, cm)
	}
	return out, nil
}

// beforeStr renders an ipc time bound as the RFC3339Nano string routd's read
// methods expect; a zero time is the empty string (no bound → now).
func beforeStr(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// buildStoreFns wires the read/manage agent tools to routd.DB. Read tools whose
// state lives elsewhere federate over HTTP (identity → authd, session_log →
// runed's GET /v1/sessions/recent; graceful-absent → empty).
// Deferred nil: LogIPCAudit — routd does not write a SQLite audit_log table;
// it emits audit events via slog/.jl (see buildGatedFns.Audit).
func (s *Server) buildStoreFns(t turnMCP) ipc.StoreFns {
	// Set and Bump engagement are the same write — one closure, two names the
	// tool layer distinguishes (extend vs. start a window) but the store doesn't.
	setEngagement := func(jid, topic, folder string, until time.Time) error {
		return s.db.SetEngagement(jid, topic, folder, time.Until(until))
	}
	return ipc.StoreFns{
		// Task reads for inspect_tasks (the write tools schedule/pause/resume/
		// cancel/list moved to the scheduled_tasks resreg seam — spec 5/16).
		ListTasks: s.db.Tasks,
		GetTask:   s.db.GetTask,
		TaskRunLogs: func(taskID string, limit int) []ipc.TaskRunLog {
			return s.db.TaskRunLogs(taskID, limit)
		},
		// Session reads: current session_id from routd.db; recent session_log rows
		// federated from runed's GET /v1/sessions/recent.
		GetSession:     s.db.GetSession,
		RecentSessions: s.recentSessions,
		// Identity reads: authd's GET /v1/identities/{sub}, snapshotted over HTTP.
		// nil resolver → unclaimed shape.
		GetIdentityForSub: s.resolveIdentity,
		// list_routes/add_route/set_routes/delete_route moved to the routes resreg
		// seam (spec 5/16); ListRoutes stays — inspect_routing still reads it.
		ListRoutes: func(_ string, _ bool) []core.Route {
			r, _ := s.db.Routes()
			return r
		},
		DefaultFolderForJID: s.db.DefaultFolderForJID,
		JIDRoutedToFolder:   s.db.JIDRoutedToFolder,
		JIDRoutableToFolder: s.db.JIDRoutableToFolder,
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
		SetEngagement:  setEngagement,
		BumpEngagement: setEngagement,
		EngagedFolder:   func(jid, topic string) string { f, _ := s.db.Engaged(jid, topic); return f },
		LogExternalCost:      s.db.LogExternalCost,
		CurrentTriggerSender: func(_ string) string { return t.trigger },
		CurrentTopic:         func(_ string) string { return t.topic },
		CurrentTurnID:        func(_ string) string { return t.turnID },
		// MCP connectors: the discovered catalog + the per-call secret resolver.
		// ResolveConnectorSecrets reads folder/user secrets, decrypts via
		// SECRETS_KEY, and narrows to the connector's declared secrets= list — so
		// CallConnectorTool both injects them into the subprocess env AND scrubs
		// their values from the result (a nil resolver leaves it unscrubbed).
		Connectors: s.connectors,
		ExtTools:   s.extTools,
		// Capture the trigger sender so ConnectorSecrets resolves the triggering
		// user's BYOA secrets (FolderSecretsForUser), not folder scope only.
		// Normalize the caller the same way dispatchRun does (spec 5/14
		// resolution chain): timed/system triggers resolve as service:routd, so
		// a stray timed-<id> scope_id can't diverge connector-secret resolution
		// from spawn-time resolution.
		ResolveConnectorSecrets: func(folder string, required []string) (map[string]string, error) {
			callerSub := t.trigger
			if callerSub == "" || strings.HasPrefix(callerSub, "timed-") || strings.HasPrefix(callerSub, "system") {
				callerSub = "service:routd"
			}
			res, err := s.db.ConnectorSecrets(folder, callerSub, required)
			// secret_use_log writer (spec 5/13 §Audit, M2): one row per resolved key —
			// records THAT a broker secret was read, never the value. Closes the gap
			// where store.LogSecretUse had no production caller. Log-failure is
			// non-fatal: an audit miss must not fail the tool call.
			status := "ok"
			if err != nil {
				status = "err"
			}
			st := store.New(s.db.SQL())
			for _, k := range required {
				scope := "missing"
				if _, ok := res[k]; ok {
					scope = "folder"
				}
				if lerr := st.LogSecretUse(store.SecretUseRow{
					SpawnID: t.turnID, CallerSub: callerSub, Folder: folder,
					Key: k, Scope: scope, Status: status,
				}); lerr != nil {
					slog.Warn("secret_use_log write failed", "key", k, "err", lerr)
				}
			}
			return res, err
		},
		// Authorize is the per-call row-ACL check ServeMCP runs when callerSub is
		// set. nil-safe. Elevated (/root) turns allow-all — see turnAuthorize.
		Authorize: s.turnAuthorize(t.elevated),
	}
}

// deriveFolderGrants is the single grant-rule renderer for a folder: tier
// defaults (grants.DeriveRules) keyed on the folder's tier + world, then the
// per-folder operator ACL overlay. Used by both ServeTurnMCP (the in-process MCP
// tool firewall) and dispatchRun (the rules shipped to runed for
// buildMounts/egress) so the two can't drift. Operator overrides live as acl rows
// with principal=folder:<folder> and action=mcp:<tool>; they append onto the
// tier-derived rules so the grants check sees both layers (deny precedence kept
// by the evaluator's last-match-wins). Empty table → tier defaults only.
func deriveFolderGrants(d *DB, folder string) []string {
	tier := auth.Resolve(folder).Tier
	rules := grants.DeriveRules(d, folder, tier, auth.WorldOf(folder))
	// Overlay acl mcp: grants for the agent principal AND its transitive roles
	// (4/R audit finding #4). The old read was exact-match ListACL("folder:"+folder),
	// so a grant held via role membership (acl_membership) was invisible HERE while
	// visible at the live per-call gate (auth.Authorize expands Ancestors) — and
	// toolGrant requires BOTH, so a role-inherited tool was silently unreachable.
	// Reading the same expanded principal set closes that dual-path gap. Neutral
	// today (no folder:<path> holds a role yet); correct once default roles seed.
	st := store.New(d.SQL())
	principal := "folder:" + folder
	principals := append([]string{principal}, st.Ancestors(principal)...)
	for _, r := range st.ACLRowsFor(principals) {
		if !strings.HasPrefix(r.Action, "mcp:") {
			continue
		}
		rule := strings.TrimPrefix(r.Action, "mcp:")
		if r.Params != "" {
			rule += "(" + r.Params + ")"
		}
		if r.Effect == "deny" {
			rule = "!" + rule
		}
		rules = append(rules, rule)
	}
	return rules
}

// ServeTurnMCP binds the per-turn agent MCP socket in-process: it derives the
// folder's tier-default grant rules, then stands up ipc.ServeMCP wired to
// routd's own DB + Deliverer. expectedUID gates peers (1000 = ant `node` user,
// or the dev host uid). Returns the stop func (removes the socket). Called
// per-turn from runTurn before dispatch.
func (s *Server) ServeTurnMCP(t turnMCP, ipcDir string) (func(), error) {
	rules := deriveFolderGrants(s.db, t.folder)
	if t.elevated {
		// Operator /root: regain the tier-0 `*` grant set (grants.DeriveRules
		// case 0). Same override as the shipped Grants in dispatchRun — one
		// elevation, both sinks. The row-ACL half elevates too (turnAuthorize):
		// leaving it on the folder's static tier 403'd every root-only tool.
		rules = []string{"*"}
	}
	authorize := s.turnAuthorize(t.elevated)
	// The structural gates (auth.AuthorizeStructural) run on a SEPARATE tier axis
	// from authorize's row-ACL check; elevate it the same way so /root lifts both
	// halves. tier 0 under /root, else the folder's static tier.
	callerID := turnIdentity(t.folder, t.elevated)
	// routd binds the socket BEFORE runed spawns the container, so the per-folder
	// ipc dir may not exist yet. ServeMCP only os.Removes the stale sock + Listens
	// — never mkdirs — so create the parent here or net.Listen fails on a fresh
	// folder's first turn.
	if err := os.MkdirAll(ipcDir, 0o755); err != nil {
		return nil, err
	}
	sockPath := groupfolder.IpcSocket(ipcDir)
	expectedUID := 1000
	if uid := os.Getuid(); uid > 0 && uid != 1000 {
		expectedUID = uid
	}
	// Per-call row-ACL: pass the canonical in-container agent principal so ipc's
	// authorizeCall runs auth.AuthorizeWith against the overlaid acl rows instead
	// of short-circuiting. The folder agent is principal=folder:<path> (matches the
	// ListACL key in deriveFolderGrants). With no acl rows, Authorize returns false
	// only on an explicit deny; tier-default fallback covers the no-row case.
	callerSub := "folder:" + t.folder
	// spec 5/16: mount the agent's management tools via resreg (shared handler +
	// tx/audit) with the agent's tier-aware Gate + visibility. One postBuild seam
	// per migrated resource; ServeMCP applies them all.
	webRoutes := s.webRoutesPostBuild(t.folder, callerSub, rules, authorize)
	networkRules := s.networkRulesPostBuild(t.folder, callerSub, rules, authorize, callerID)
	scheduledTasks := s.scheduledTasksPostBuild(t.folder, callerSub, rules, authorize, callerID)
	routes := s.routesPostBuild(t.folder, callerSub, rules, authorize, callerID)
	acl := s.aclPostBuild(t.folder, callerSub, rules, authorize, callerID)
	routeTokens := s.routeTokensPostBuild(t.folder, callerSub, rules, authorize)
	groups := s.groupsPostBuild(t.folder, callerSub, rules, authorize, callerID)
	return ipc.ServeMCP(sockPath, s.buildGatedFns(t), s.buildStoreFns(t), t.folder, rules, expectedUID, callerSub, webRoutes, networkRules, scheduledTasks, routes, acl, routeTokens, groups)
}
