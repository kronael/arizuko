package routd

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

// turnMCP is the per-turn binding the in-process fns close over: the
// turn_context routd stores, narrowed to what the MCP fns read.
type turnMCP struct {
	folder   string
	topic    string
	chatJID  string
	turnID   string
	trigger  string
	elevated bool // operator /root turn: the socket authorizes every call
}

// buildRoutdFns wires the write-side agent tools to routd's Deliverer;
// execution-plane tools stay nil and ipc.ServeMCP registers only the non-nil.
func (s *Server) buildRoutdFns(t turnMCP) ipc.RoutdFns {
	requireTurn := func(turnID string) error {
		if turnID != t.turnID {
			return fmt.Errorf("turn_id does not match this socket")
		}
		return nil
	}
	return ipc.RoutdFns{
		AcceptURLBase:        s.webHost,
		WebHost:              s.webHost,
		HostingDomain:        s.hostingDomain,
		VhostAliases:         s.vhostAliases,
		EngagementTTL:        s.engagementT,
		GroupsDir:            s.groupsDir,
		Audit:                s.audit,
		FetchPlatformHistory: s.fetchPlatformHistory,
		SendMessage: func(jid, text, msgID string) (string, string, error) {
			return s.mcpDeliver(t.turnID, jid, text, "", msgID, false)
		},
		SendReply: func(jid, text, replyTo, msgID string) (string, string, error) {
			return s.mcpDeliver(t.turnID, jid, text, replyTo, msgID, true)
		},
		SendDocument: func(jid, path, filename, caption, replyTo, _, msgID string) (string, string, error) {
			return s.mcpAppendDoc(t.turnID, jid, path, filename, caption, replyTo, "", msgID)
		},
		SendVoice: func(jid, text, voice, folder, _ string) (string, string, error) {
			return s.mcpSendVoice(t.turnID, jid, text, voice, folder, "")
		},
		Like: func(jid, target, reaction string) error {
			return s.socialTool(t, "message.like", jid, target, reactionParams(reaction),
				func() error { return s.deliver.React(jid, target, reaction) })
		},
		Dislike: func(jid, target string) error {
			return s.socialTool(t, "message.dislike", jid, target, nil,
				func() error { return s.deliver.Dislike(jid, target) })
		},
		Delete: func(jid, target string) error {
			return s.socialTool(t, "message.delete", jid, target, nil,
				func() error { return s.deliver.Delete(jid, target) })
		},
		Edit: func(jid, target, content string) error {
			return s.socialTool(t, "message.edit", jid, target, nil,
				func() error { return s.deliver.Edit(jid, target, content, "") })
		},
		Pin: func(jid, target string) error {
			return s.socialTool(t, "message.pin", jid, target, nil,
				func() error { return s.deliver.Pin(jid, target) })
		},
		Unpin: func(jid, target string, all bool) error {
			return s.socialTool(t, "message.unpin", jid, target, unpinParams(all),
				func() error { return s.deliver.Unpin(jid, target, all) })
		},
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
			// The child gets the parent's tail — the transcript, else its ambient
			// window. loop is nil in pure REST tests.
			if s.loop != nil {
				s.loop.inheritParentSession(folder, parent, child, childUUID)
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
			})
			// No explicit enqueue: the loop polls new messages by timestamp.
			return id, err
		},
		SubmitTurn: func(folder string, result ipc.TurnResult) (ipc.TurnAck, error) {
			if err := requireTurn(result.TurnID); err != nil {
				return ipc.TurnAck{}, err
			}
			return s.mcpSubmitTurn(folder, result)
		},
		CheckTurn: func(_ string, turnID string) (ipc.TurnCheck, error) {
			if err := requireTurn(turnID); err != nil {
				return ipc.TurnCheck{}, err
			}
			return s.mcpCheckTurn(turnID)
		},
		SubmitStatus: func(_ string, status ipc.TurnStatus) error {
			if err := requireTurn(status.TurnID); err != nil {
				return err
			}
			return s.submitStatus(status)
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
		RevokeInvite: func(ref string) error {
			oc := s.onbodClient()
			if oc == nil {
				return fmt.Errorf("invites unavailable: onbod not wired (ONBOD_URL unset)")
			}
			// onbod's DELETE is ref-only, so confirm the ref is among THIS
			// folder's invites: an agent must not revoke another folder's.
			owned, err := oc.ListInvites("agent:" + t.folder)
			if err != nil {
				return err
			}
			found := false
			for _, inv := range owned {
				if inv.Ref == ref {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("invite not found or not owned by this folder")
			}
			return oc.RevokeInviteByRef(ref)
		},
	}
}

// onbodClient reaches the onbod federation handle; nil → onbod not wired.
func (s *Server) onbodClient() OnbodClient {
	if s.loop == nil {
		return nil
	}
	return s.loop.onbod
}

// toInviteInfo maps a routd-local Invite to the ipc layer's InviteInfo so the
// MCP tools never import store.
func toInviteInfo(inv Invite) ipc.InviteInfo {
	return ipc.InviteInfo{
		Ref:         inv.Ref,
		Token:       inv.Token,
		TargetGlob:  inv.TargetGlob,
		IssuedBySub: inv.IssuedBySub,
		IssuedAt:    inv.IssuedAt,
		ExpiresAt:   inv.ExpiresAt,
		MaxUses:     inv.MaxUses,
		UsedCount:   inv.UsedCount,
	}
}

// registerGroup persists the group, adds a room-matched default route (rolling
// the group row back on a failed insert so a route-less orphan is never left),
// then git-inits the group dir. The jid supplies the route's room literal.
func (s *Server) registerGroup(jid string, g core.Group) error {
	// Whether the folder was ALREADY there decides what a rollback may undo:
	// PutGroup upserts, and the purge is a subtree delete that takes the
	// folder's children with it (BUGS L245).
	existed := s.db.GroupExists(g.Folder)
	if err := s.db.PutGroup(g); err != nil {
		return err
	}
	match := "room=" + core.JidRoom(jid)
	if _, err := s.db.AddRoute(core.Route{Seq: 0, Match: match, Target: g.Folder}); err != nil {
		if !existed {
			// A rollback that ALSO fails leaves the orphan this rollback exists
			// to prevent, and the next register sees existed==true; the caller
			// gets both facts (BUGS L343).
			if rerr := s.rollbackGroup(g.Folder); rerr != nil {
				return fmt.Errorf("add route for %s: %w; the group row is still there: %w",
					g.Folder, err, rerr)
			}
		}
		return fmt.Errorf("add route for %s: %w", g.Folder, err)
	}
	ensureGroupGitRepo(filepath.Join(s.groupsDir, g.Folder))
	return nil
}

func (s *Server) rollbackGroup(folder string) error {
	if s.groupsDir == "" {
		return fmt.Errorf("group directory is not configured")
	}
	dataDir := filepath.Dir(s.groupsDir)
	lock, err := container.LockPackages(dataDir)
	if err != nil {
		return err
	}
	// The rollback owns selections until registered identities disappear.
	defer lock.Close() //nolint:errcheck
	registered, err := s.db.store().GroupFolders(0)
	if err != nil {
		return err
	}
	folders := []string{folder}
	for _, registered := range registered {
		if registered != folder && groupfolder.Contains(folder, registered) {
			folders = append(folders, registered)
		}
	}
	if err := container.WithdrawGroupPackages(dataDir, folders); err != nil {
		return err
	}
	_, _, err = s.db.DeleteGroup(folder)
	return err
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
	if err := os.WriteFile(gitignore, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		slog.Warn("group .gitignore not written; the agent's commits pick up its own runtime files",
			"path", gitignore, "err", err)
	}
}

// mcpDeliver is the in-process reply/send: it ONLY delivers. The ipc tool
// layer's recordOutbound persists the row, threads and bumps engagement, so
// this must NOT, or the row double-persists. returnTarget redirects a delegated
// turn to the origin; threaded marks the REPLY verb. It returns the thread root
// because the ipc layer writes the row and cannot see it — without it an MCP
// reply that rooted a thread recorded no thread (BUGS L166).
func (s *Server) mcpDeliver(turnID, jid, text, replyTo, msgID string, threaded bool) (string, string, error) {
	if s.deliver == nil {
		return "", "", nil
	}
	topic, threadRoot := "", ""
	if tc, ok := s.db.GetTurnContext(turnID); ok {
		place, err := placeTurnDelivery(s.db, s.deliver, tc, jid, "", threaded)
		if err != nil {
			return "", "", err
		}
		jid, topic, threadRoot = place.jid, place.threadID, place.threadRoot
	}
	pid, err := s.deliver.Send(jid, text, replyTo, topic, threadRoot, msgID)
	if err != nil {
		slog.Warn("mcp deliver failed", "turn_id", turnID, "jid", jid, "err", err)
	}
	if topic != "" {
		return pid, topic, err
	}
	return pid, threadRoot, err
}

// mcpAppendDoc is the in-process send_file: deliver-only (recordOutbound
// persists the row, keyed on the platform id so a later human reply
// re-promotes). An empty threadID falls back to the turn's topic.
func (s *Server) mcpAppendDoc(turnID, jid, path, name, caption, replyTo, threadID, msgID string) (string, string, error) {
	if s.deliver == nil {
		return "", "", nil
	}
	place := deliveryPlace{jid: jid, threadID: threadID}
	if tc, ok := s.db.GetTurnContext(turnID); ok {
		var err error
		place, err = placeTurnDelivery(s.db, s.deliver, tc, jid, threadID, false)
		if err != nil {
			return "", "", err
		}
	}
	pid, err := s.deliver.Document(place.jid, path, name, caption, replyTo, place.threadID, msgID, place.threadRoot)
	if err != nil {
		slog.Warn("mcp deliver document failed", "turn_id", turnID, "jid", jid, "err", err)
	}
	return pid, place.topic(), err
}

// mcpSendVoice is the in-process send_voice: synthesize via TTS (tts.go) and
// deliver as a voice note. Deliver-only, and a delegated turn is redirected to
// its origin, as in mcpDeliver.
func (s *Server) mcpSendVoice(turnID, jid, text, voice, folder, threadID string) (string, string, error) {
	place := deliveryPlace{jid: jid, threadID: threadID}
	if tc, ok := s.db.GetTurnContext(turnID); ok {
		var err error
		place, err = placeTurnDelivery(s.db, s.deliver, tc, jid, threadID, false)
		if err != nil {
			return "", "", err
		}
	}
	pid, err := s.sendVoice(place.jid, text, voice, folder, place.threadID, place.threadRoot)
	if err != nil {
		slog.Warn("mcp send voice failed", "turn_id", turnID, "jid", jid, "err", err)
	}
	return pid, place.topic(), err
}

// mcpCheckTurn hands out at most one delivery nudge across all turn-end readers.
func (s *Server) mcpCheckTurn(turnID string) (ipc.TurnCheck, error) {
	unlock := lockTurn(turnID)
	defer unlock()
	tc, ok := s.db.GetTurnContext(turnID)
	if !ok {
		return ipc.TurnCheck{}, fmt.Errorf("no turn context for turn_id")
	}
	return s.checkTurn(tc)
}

// checkTurn's caller holds lockTurn until it has acted on the decision.
func (s *Server) checkTurn(tc TurnContext) (ipc.TurnCheck, error) {
	jid := returnTarget(tc, tc.ChatJID)
	_, statusRow := s.db.LastStatusRow(jid, tc.TurnID)
	_, summary, err := readTaskSummary(s.db, jid, tc.TurnID)
	if err != nil {
		return ipc.TurnCheck{}, err
	}
	if summary {
		statusRow = true
	}
	delivered, err := s.turnDelivered(tc)
	if err != nil {
		return ipc.TurnCheck{}, err
	}
	check := ipc.TurnCheck{
		Delivered: delivered,
		Owed:      tc.Addressed || statusRow,
	}
	if !check.Delivered && check.Owed && tc.Nudged == 0 && tc.State == "running" && !tc.RunReturned {
		check.Nudge = turnNudge(tc, statusRow)
		if err := s.db.IncrementNudged(tc.TurnID); err != nil {
			return ipc.TurnCheck{}, err
		}
	}
	return check, nil
}

// mcpSubmitTurn maps the ipc.TurnResult agent payload onto routd's apiv1
// shape and records it through recordTurnResult (turns.go).
func (s *Server) mcpSubmitTurn(_ string, t ipc.TurnResult) (ipc.TurnAck, error) {
	req := apiv1.TurnResult{
		TurnID: t.TurnID, SessionID: t.SessionID, Status: t.Status,
		Error: t.Error, TimedOut: t.TimedOut,
	}
	if len(t.Models) > 0 {
		req.Models = make(map[string]apiv1.ModelCost, len(t.Models))
		for m, u := range t.Models {
			req.Models[m] = apiv1.ModelCost{Input: u.Input, Output: u.Output, CostCents: u.CostCents}
		}
	}
	return s.recordTurnResult(t.TurnID, req)
}

// submitStatus hands a mid-turn declaration to the progress renderer
// without ending the turn. Serialized per turn_id like the other turn writes;
// a closed turn is a no-op.
func (s *Server) submitStatus(status ipc.TurnStatus) error {
	unlock := lockTurn(status.TurnID)
	defer unlock()
	tc, ok := s.db.GetTurnContext(status.TurnID)
	if !ok {
		return fmt.Errorf("no turn context for turn_id")
	}
	if s.callbackClosed(tc) {
		return nil
	}
	jid := returnTarget(tc, tc.ChatJID)
	if s.deliver == nil || s.mutedGroup(tc.Folder) || !s.deliver.HasCapability(jid, "edit") {
		slog.Debug("progress update dropped: no editable message here",
			"turn_id", tc.TurnID, "jid", jid)
		return nil
	}
	if status.Kind == ipc.StatusSummary {
		return s.declareSummary(tc, jid, status.Text)
	}
	if status.Kind == ipc.StatusProgress && summaryThreadEnabled(s.db, s.deliver, tc, jid) {
		_, ok, err := readTaskSummary(s.db, jid, tc.TurnID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("declare the overall task summary before progress")
		}
	}
	return s.updateProgress(tc, jid, status.Text)
}

// fetchPlatformHistory backs the fetch_history MCP tool: proxy to the owning
// adapter, else source:"cache"/"cache-only" from the local DB. The rows are
// rendered, never stored: `messages` is the inbound log the poll loop
// dispatches from, and a platform row there is a user message with no bot flag
// — sloth fed 630 of its own posts back to its agent as <observed>.
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
		out.Messages = append(out.Messages, core.Message{
			ID: m.ID, ChatJID: m.ChatJID, Sender: m.Sender, Name: m.SenderName, Content: m.Content,
			Timestamp: time.Unix(m.Timestamp, 0).UTC(), ReplyToID: m.ReplyTo,
		})
	}
	return out, nil
}

// beforeStr renders an ipc time bound as the stored-timestamp string routd's
// read methods expect; a zero time is the empty string (no bound → now).
func beforeStr(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return core.TS(t)
}

// scopeRoutes applies store.Store.ListRoutes's folder-visibility filter
// (target equals the folder or nests under `folder#`/`folder/`; root sees every
// row) to an unscoped reader, since routd.DB's `routes` has no scoped query of
// its own (BUGS A3).
func scopeRoutes(all func() ([]core.Route, error)) func(folder string, isRoot bool) ([]core.Route, error) {
	return func(folder string, isRoot bool) ([]core.Route, error) {
		routes, err := all()
		if err != nil {
			return nil, err
		}
		if isRoot || folder == "" {
			return routes, nil
		}
		out := make([]core.Route, 0, len(routes))
		for _, r := range routes {
			if r.Target == folder || strings.HasPrefix(r.Target, folder+"#") || strings.HasPrefix(r.Target, folder+"/") {
				out = append(out, r)
			}
		}
		return out, nil
	}
}

// buildStoreFns wires the read/manage agent tools to routd.DB. Read tools whose
// state lives elsewhere federate over HTTP (identity → authd, session_log →
// runed); graceful-absent → empty.
func (s *Server) buildStoreFns(t turnMCP) ipc.StoreFns {
	return ipc.StoreFns{
		// An elevated /root turn is the operator acting directly — holding their
		// own call for their own approval is a deadlock (spec 5/19).
		CheckHold: s.holdGate(t),
		// The task write tools live on the scheduled_tasks resreg seam (spec 5/16).
		ListTasks: s.db.Tasks,
		GetTask:   s.db.GetTask,
		TaskRunLogs: func(taskID string, limit int) []ipc.TaskRunLog {
			return s.db.TaskRunLogs(taskID, limit)
		},
		GetSession:        s.db.GetSession,
		RecentSessions:    s.recentSessions,
		GetIdentityForSub: s.resolveIdentity,
		// The route CRUD tools live on the routes resreg seam. s.db.Routes() is
		// the WHOLE table, so scope it and propagate the error: unscoped let
		// inspect see other groups' routes and hid a DB error as an empty list
		// (BUGS A3).
		ListRoutes:          scopeRoutes(func() ([]core.Route, error) { return s.db.Routes() }),
		DefaultFolderForJID: s.db.DefaultFolderForJID,
		RouteTokenTarget:    s.db.RouteTokenTarget,
		JIDRoutedToFolder: func(jid, folder string) bool {
			if t.chatJID != "" && jid == t.chatJID && folder == t.folder {
				return true
			}
			return s.db.JIDRoutedToFolder(jid, folder)
		},
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
		PutOutbound:    s.db.PutOutbound,
		GetLastReplyID: s.db.LastReplyID,
		SetLastReply:   s.db.SetLastReply,
		SetEngagement:  s.db.store().BumpEngagement,
		BumpEngagement: func(jid, topic, folder string, until time.Time) error {
			return s.db.store().RenewEngagement(t.turnID, jid, topic, folder, until)
		},
		EngagedFolder:        func(jid, topic string) string { f, _ := s.db.Engaged(jid, topic); return f },
		CurrentTriggerSender: func(_ string) string { return t.trigger },
		CurrentTopic:         func(_ string) string { return t.topic },
		CurrentTurnID:        func(_ string) string { return t.turnID },
		// ResolveConnectorSecrets narrows to the connector's declared secrets=
		// list, so CallConnectorTool both injects them AND scrubs their values
		// from the result (a nil resolver leaves it unscrubbed).
		Connectors: s.connectors,
		ExtTools:   s.extTools,
		// The trigger sender resolves the triggering user's BYOA secrets, not
		// folder scope only.
		ResolveConnectorSecrets: func(folder, tool string, required []string) (map[string]string, error) {
			callerSub := turnCallerSub(t.trigger)
			res, scopes, err := s.db.ConnectorSecrets(folder, callerSub, required)
			// secret_use_log (spec 5/13 §Audit): one row per resolved key — THAT a
			// broker secret was read, never the value. A log failure must not fail
			// the tool call.
			status := "ok"
			if err != nil {
				status = "err"
			}
			st := s.db.store()
			for _, k := range required {
				scope := scopes[k]
				if scope == "" {
					scope = store.ScopeMissing
				}
				if lerr := st.LogSecretUse(store.SecretUseRow{
					SpawnID: t.turnID, CallerSub: callerSub, Folder: folder,
					Tool: tool, Key: k, Scope: string(scope), Status: status,
				}); lerr != nil {
					slog.Warn("secret_use_log write failed", "key", k, "err", lerr)
				}
			}
			return res, err
		},
		// The reactive half of spec 5/15: called at most once, only on a 401.
		// No secret_use_log row — see RefreshConnectorSecrets.
		RefreshConnectorSecrets: func(folder, _ string, required []string) (map[string]string, error) {
			return s.db.RefreshConnectorSecrets(folder, turnCallerSub(t.trigger), required)
		},
		// Authorize is the per-call row-ACL check (magnitude + containment in
		// one); elevated turns allow-all (turnAuthorize). The hand-authored gates
		// pass the ACTUAL target as the scope so a delegated row scoped to a
		// subtree bounds containment.
		Authorize: s.turnAuthorize(t),
		// Visible is the tools/list view: a tool shows iff the turn holds it
		// (turnVisible).
		Visible: s.turnVisible(t),
		// Spec 5/I-tool-call-logging layer A: every hand-authored mutation and authz denial lands
		// one audit_log row in routd.db; left nil, the agent MCP surface was the
		// one path /dash/audit/ could not see (BUGS F70).
		LogIPCAudit: s.db.store().LogIPCAudit,
	}
}

// ServeTurnMCP binds the per-turn agent MCP socket in-process; expectedUID
// gates peers. No grant bundle is computed (5/33-paths-roles): authz is auth.Authorize on
// the acl rows, visibility auth.EffectiveActions over the same rows; both
// elevate under /root.
func (s *Server) ServeTurnMCP(t turnMCP, ipcDir string) (func(), error) {
	authorize := s.turnAuthorize(t)
	callerID := turnIdentity(t.folder, t.elevated)
	// routd binds the socket BEFORE runed spawns the container, and ServeMCP
	// never mkdirs, so the per-folder ipc dir is created here.
	if err := os.MkdirAll(ipcDir, 0o755); err != nil {
		return nil, err
	}
	sockPath := groupfolder.IpcSocket(ipcDir)
	expectedUID := 1000
	if uid := os.Getuid(); uid > 0 && uid != 1000 {
		expectedUID = uid
	}
	// callerSub is the socket's own principal, folder:<path> — the audit actor
	// and the resreg Caller; its non-emptiness switches ipc's row-based authz
	// on. It is NOT an authorization input: the gate's principals come from
	// turnAuthorize (route + attested sender).
	callerSub := turnPrincipal(t.folder)
	visible := s.turnVisible(t)
	// spec 5/16: one postBuild seam per resreg-mounted resource; ServeMCP
	// applies them all.
	webRoutes := s.webRoutesPostBuild(t.folder, callerSub, authorize, visible)
	networkRules := s.networkRulesPostBuild(t.folder, callerSub, authorize, visible)
	scheduledTasks := s.scheduledTasksPostBuild(t.folder, callerSub, authorize, visible, callerID)
	routes := s.routesPostBuild(t.folder, callerSub, authorize, visible)
	acl := s.aclPostBuild(t.folder, callerSub, authorize, visible, callerID)
	routeTokens := s.routeTokensPostBuild(t.folder, callerSub, authorize, visible)
	githubSources := s.githubSourcesPostBuild(t.folder, callerSub, authorize, visible)
	gdriveFiles := s.gdriveFilesPostBuild(t.folder, callerSub, authorize, visible)
	groups := s.groupsPostBuild(t.folder, callerSub, authorize, visible, callerID)
	membership := s.membershipPostBuild(t.folder, callerSub, authorize, visible)
	packages := s.installedPackagesPostBuild(t.folder, callerSub, authorize, visible)
	auditTools := s.auditPostBuild(t.folder, callerSub, authorize, visible)
	deadLetters := s.deadLettersPostBuild(t.folder, callerSub, authorize, visible)
	return ipc.ServeMCP(sockPath, s.buildRoutdFns(t), s.buildStoreFns(t), t.folder, callerID.IsRoot, expectedUID, callerSub, webRoutes, networkRules, scheduledTasks, routes, acl, routeTokens, githubSources, gdriveFiles, groups, membership, packages, auditTools, deadLetters)
}
