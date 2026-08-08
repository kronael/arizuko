package routd

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/router"
)

// steer consumes the latest message of a routed chat BEFORE it reaches a turn.
// It applies, in order: sticky navigation (@group / #topic), routd-serviceable
// slash commands, and @child delegation / the external-route prefix layer.
// Returns true when the message was consumed — the caller then advances the
// cursor and skips the turn.
func (l *Loop) steer(chatJID string, last core.Message, folder string) bool {
	if l.handleStickyCommand(chatJID, last) {
		return true
	}
	if l.handleCommand(chatJID, last, folder) {
		return true
	}
	return l.tryExternalRoute(chatJID, last, folder)
}

// ack sends a steering acknowledgement to the chat (routing reset, topic
// changed, …) via the Deliverer. No-op without a Deliverer (pure REST tests).
func (l *Loop) ack(chatJID, text string) {
	if l.deliver != nil {
		_, _ = l.deliver.Send(chatJID, text, "", "", "", "ack-"+core.MsgID(""))
	}
}

func isStickyCommand(content string) bool {
	t := strings.TrimSpace(content)
	if len(t) == 0 || (t[0] != '@' && t[0] != '#') {
		return false
	}
	return !strings.ContainsAny(t, " \n")
}

// handleStickyCommand pins (or resets) the chat's @group / #topic navigation.
// A bare @ or # resets; @<known-folder> / #<topic> pins. An @ to an unknown
// folder is NOT consumed — passed through to the agent (a bare @ at message
// start has too many meanings to swallow).
func (l *Loop) handleStickyCommand(chatJID string, msg core.Message) bool {
	if msg.BotMsg || strings.HasPrefix(msg.Sender, "timed-") {
		return false
	}
	content := strings.TrimSpace(msg.Content)
	if !isStickyCommand(content) {
		return false
	}
	name := content[1:]
	switch content[0] {
	case '@':
		if name == "" {
			_ = l.db.SetStickyGroup(chatJID, "")
			l.ack(chatJID, "routing reset to default")
			return true
		}
		if !l.db.GroupExists(name) {
			slog.Debug("@-prefix: unknown group, passing to agent", "chat_jid", chatJID, "name", name)
			return false
		}
		_ = l.db.SetStickyGroup(chatJID, name)
		l.ack(chatJID, "routing → "+name)
		return true
	case '#':
		_ = l.db.SetStickyTopic(chatJID, name)
		if name == "" {
			l.ack(chatJID, "topic reset to default")
		} else {
			l.ack(chatJID, "topic → "+name)
		}
		return true
	}
	return false
}

// Slash-command output text is fixed — operators rely on the exact responses.
// /invite and /gate need onbod-owned tables (invites + onboarding_gates); routd
// federates them to onbod over HTTP (the OnbodClient). nil client (ONBOD_URL
// unset) → they report the federation gap.

func cmdText(raw string) string {
	t := strings.TrimSpace(raw)
	if strings.HasPrefix(t, "[") {
		if i := strings.Index(t, "]"); i >= 0 {
			t = strings.TrimSpace(t[i+1:])
		}
	}
	if strings.HasPrefix(t, "@") {
		if i := strings.IndexByte(t, ' '); i >= 0 {
			t = strings.TrimSpace(t[i+1:])
		} else {
			t = ""
		}
	}
	return t
}

// lookupCommand parses a leading slash command, normalizing the Slack \ alias
// and the Telegram @botname suffix. Returns the head ("/new") and its arg.
func lookupCommand(raw string) (head, arg string) {
	t := cmdText(raw)
	if strings.HasPrefix(t, "\\") {
		t = "/" + t[1:]
	}
	if !strings.HasPrefix(t, "/") {
		return "", ""
	}
	h, a, _ := strings.Cut(t, " ")
	h, _, _ = strings.Cut(strings.ToLower(h), "@")
	return h, a
}

func (l *Loop) handleCommand(chatJID string, msg core.Message, folder string) bool {
	if msg.BotMsg || strings.HasPrefix(msg.Sender, "timed-") {
		return false
	}
	head, arg := lookupCommand(msg.Content)
	switch head {
	case "/new":
		l.cmdNew(chatJID, folder, arg)
	case "/chatid":
		l.ack(chatJID, chatJID)
	case "/ping":
		l.cmdPing(chatJID, folder)
	case "/stop":
		l.cmdStop(chatJID, folder)
	case "/status":
		l.cmdStatus(chatJID, folder, msg.Sender)
	case "/root":
		l.cmdRoot(chatJID, folder, arg, msg.Sender)
	case "/invite":
		l.cmdInvite(chatJID, folder, arg, msg.Sender)
	case "/gate":
		l.cmdGate(chatJID, folder, arg, msg.Sender)
	case "/approve":
		l.cmdResolveHold(chatJID, folder, arg, msg.Sender, PendingApproved)
	case "/reject":
		l.cmdResolveHold(chatJID, folder, arg, msg.Sender, PendingRejected)
	default:
		return false
	}
	return true
}

// cmdPing reports the resolved folder, its session prefix, the count of live
// runs in routd's queue, and the registered-group count. "active containers" is
// routd's in-flight run count (the container runs in runed, but the queue's
// active count is the equivalent live-run gauge).
func (l *Loop) cmdPing(chatJID, folder string) {
	sessID, _ := l.db.GetSession(folder, "")
	nGroups := len(l.db.AllGroups())
	active := l.q.ActiveCount()
	sess := "none"
	if sessID != "" {
		sess = sessID[:min(8, len(sessID))]
	}
	l.ack(chatJID, fmt.Sprintf(
		"pong\ngroup: %s\nsession: %s\nactive containers: %d\nregistered groups: %d",
		folder, sess, active, nGroups))
}

// cmdStop kills the resolved folder's live run. The container is owned by runed,
// so routd asks runed to map the folder to its live spawn and kill it (POST
// /v1/runs/stop).
func (l *Loop) cmdStop(chatJID, folder string) {
	stopper, ok := l.runner.(RunStopper)
	if !ok {
		l.ack(chatJID, "No active container for this chat.")
		return
	}
	res, err := stopper.StopFolder(context.Background(), folder)
	if err != nil {
		slog.Warn("cmdStop: runed stop failed", "jid", chatJID, "folder", folder, "err", err)
		l.ack(chatJID, "No active container for this chat.")
		return
	}
	if res.Killed {
		l.ack(chatJID, "Container stopped.")
	} else {
		l.ack(chatJID, "No active container for this chat.")
	}
}

// cmdStatus reports instance-wide counts. It is operator-only — instance-wide
// visibility is a `**` (operator) privilege, not a folder position (a top-level
// world is a tenant like any other). Channels come from the channel registry (held
// by the Server); errored chats + active tasks from routd.db.
func (l *Loop) cmdStatus(chatJID, folder, sender string) {
	if !l.db.IsOperator(sender) {
		l.ack(chatJID, "Permission denied: operator only.")
		return
	}
	nChannels := 0
	if l.srv != nil && l.srv.reg != nil {
		nChannels = len(l.srv.reg.All())
	}
	nGroups := len(l.db.AllGroups())
	active := l.q.ActiveCount()
	errored := l.db.CountErroredChats()
	tasks := l.db.CountActiveTasks()
	l.ack(chatJID, fmt.Sprintf(
		"status\nchannels: %d\ngroups: %d\nactive containers: %d\nerrored chats: %d\nactive tasks: %d",
		nChannels, nGroups, active, errored, tasks))
}

// cmdRoot raises the caller's message to a root-privileged turn — the ONLY path
// to root. It is gated on the caller carrying the operator `**` grant, NOT on
// folder shape: a bare top-level world is a tenant, so a folder-shape test is
// the wrong gate. A non-operator gets a clear denial (no silent downgrade). On
// success the arg is re-injected in-place and its message id is registered in
// pendingElevation, so runTurn spawns it with Elevated=true.
func (l *Loop) cmdRoot(chatJID, folder, arg, sender string) {
	if !l.db.IsOperator(sender) {
		l.ack(chatJID, "Permission denied: /root requires an operator grant (**).")
		return
	}
	if arg == "" {
		l.ack(chatJID, "Usage: /root <message>")
		return
	}
	// Re-inject the instruction as a fresh inbound in THIS chat (routes to the
	// same folder) and mark it elevated. The turn runs root-privileged in place;
	// the reply lands back here, no cross-folder delegation. Elevation is one-shot
	// (this message only) — the transient, operator-gated root the model wants.
	msg := core.Message{
		ID:        core.MsgID("root-" + folder),
		ChatJID:   chatJID,
		Sender:    sender,
		Content:   arg,
		Timestamp: time.Now().UTC(),
	}
	l.pendingElevation.Store(msg.ID, struct{}{})
	if err := l.db.PutMessage(msg); err != nil {
		l.pendingElevation.Delete(msg.ID)
		slog.Warn("cmdRoot: put message", "jid", chatJID, "folder", folder, "err", err)
		l.ack(chatJID, "Failed to raise root turn.")
		return
	}
	l.Enqueue(chatJID)
}

// cmdInvite mints an invite for the caller's folder subtree. It is operator-only
// (`**`), then validates the arg shape and calls onbod's POST /v1/invites. nil
// onbod client (ONBOD_URL unset) → the federation-gap notice. The minted invite
// targets the folder + "/" so the redeemer picks a username under it.
func (l *Loop) cmdInvite(chatJID, folder, arg, sender string) {
	if !l.db.IsOperator(sender) {
		l.ack(chatJID, "Permission denied: operator only.")
		return
	}
	maxUses := 1
	if arg != "" {
		n, err := strconv.Atoi(strings.TrimSpace(arg))
		if err != nil || n < 1 {
			l.ack(chatJID, "Usage: /invite [max_uses]")
			return
		}
		maxUses = n
	}
	if l.onbod == nil {
		l.ack(chatJID, "Invites are managed by onbod; run `arizuko invite` (ONBOD_URL not wired).")
		return
	}
	token, err := l.onbod.CreateInvite(folder+"/", maxUses)
	if err != nil {
		slog.Warn("cmdInvite: onbod create failed", "jid", chatJID, "folder", folder, "err", err)
		l.ack(chatJID, "Failed to create invite.")
		return
	}
	l.ack(chatJID, "Invite link token: "+token)
}

// cmdGate manages the onboarding gates. It is operator-only (`**`), then
// validates the subcommand shape and calls onbod's /v1/gates endpoints. nil
// onbod client → the federation-gap notice.
func (l *Loop) cmdGate(chatJID, folder, arg, sender string) {
	if !l.db.IsOperator(sender) {
		l.ack(chatJID, "Permission denied: operator only.")
		return
	}
	parts := strings.Fields(arg)
	action := ""
	if len(parts) > 0 {
		action = parts[0]
	}
	// Validate the subcommand shape BEFORE touching onbod, so an unknown
	// subcommand always gets the usage line (even with no onbod wired).
	switch action {
	case "", "list", "add", "rm", "enable", "disable":
	default:
		l.ack(chatJID, "Usage: /gate [list|add|rm|enable|disable]")
		return
	}
	if l.onbod == nil {
		l.ack(chatJID, "Gates are managed by onbod (ONBOD_URL not wired).")
		return
	}
	switch action {
	case "", "list":
		gates, err := l.onbod.ListGates()
		if err != nil {
			l.ack(chatJID, "Failed to list gates.")
			return
		}
		if len(gates) == 0 {
			l.ack(chatJID, "no gates")
			return
		}
		var b strings.Builder
		for _, g := range gates {
			en := "enabled"
			if !g.Enabled {
				en = "disabled"
			}
			fmt.Fprintf(&b, "%s %d/day %s\n", g.Gate, g.LimitPerDay, en)
		}
		l.ack(chatJID, strings.TrimRight(b.String(), "\n"))
	case "add":
		if len(parts) < 3 {
			l.ack(chatJID, "Usage: /gate add <spec> <N>")
			return
		}
		n, err := strconv.Atoi(strings.TrimSuffix(parts[2], "/day"))
		if err != nil || n < 1 {
			l.ack(chatJID, "Usage: /gate add <spec> <N>")
			return
		}
		if err := l.onbod.PutGate(parts[1], n); err != nil {
			l.ack(chatJID, "Failed to add gate.")
			return
		}
		l.ack(chatJID, fmt.Sprintf("gate added: %s %d/day", parts[1], n))
	case "rm":
		if len(parts) < 2 {
			l.ack(chatJID, "Usage: /gate rm <spec>")
			return
		}
		if err := l.onbod.DeleteGate(parts[1]); err != nil {
			l.ack(chatJID, "Failed to remove gate.")
			return
		}
		l.ack(chatJID, "gate removed: "+parts[1])
	case "enable", "disable":
		if len(parts) < 2 {
			l.ack(chatJID, "Usage: /gate "+action+" <spec>")
			return
		}
		if err := l.onbod.EnableGate(parts[1], action == "enable"); err != nil {
			l.ack(chatJID, "Failed to "+action+" gate.")
			return
		}
		l.ack(chatJID, "gate "+action+"d: "+parts[1])
	}
}

// cmdNew clears the resolved folder's session (root or #topic) AND reinjects any
// trailing text as a fresh inbound so `/new look into X` clears the session then
// processes "look into X"; a bare `/new` just clears. The synthetic inbound is
// enqueued; the consumed /new row advances the cursor, so the followup runs on a
// clean session next poll.
func (l *Loop) cmdNew(chatJID, folder, arg string) {
	label := "Session cleared."
	followup := ""
	if strings.HasPrefix(strings.TrimSpace(arg), "#") {
		label = "Topic session cleared."
		name, rest, _ := parsePrefix(arg)
		_ = l.db.DeleteSession(folder, "#"+name)
		if rest != "" {
			followup = "#" + name + " " + rest
		}
	} else {
		_ = l.db.DeleteSession(folder, "")
		followup = strings.TrimSpace(arg)
	}
	if followup == "" {
		l.ack(chatJID, label)
		return
	}
	_ = l.db.PutMessage(core.Message{
		ID:        core.MsgID("cmd-new"),
		ChatJID:   chatJID,
		Sender:    "user",
		Content:   followup,
		Timestamp: time.Now().UTC(),
	})
	l.Enqueue(chatJID)
	l.ack(chatJID, label+" Processing your message...")
}

// Navigation prefixes must sit at the very start of the message (optional
// leading whitespace). Mid-content @mentions / #tags are references, not nav.
var rePrefixAt = regexp.MustCompile(`^\s*@(\w[\w-]*)`)
var rePrefixHash = regexp.MustCompile(`^\s*#(\w[\w-]*)`)

func parsePrefix(text string) (name, rest string, ok bool) {
	for _, re := range []*regexp.Regexp{rePrefixAt, rePrefixHash} {
		m := re.FindStringSubmatchIndex(text)
		if m == nil {
			continue
		}
		return text[m[2]:m[3]], strings.TrimSpace(text[m[1]:]), true
	}
	return "", "", false
}

// tryExternalRoute delegates the message to a child group when an explicit
// @child / #topic prefix or a routing rule points outside the current folder.
// Returns true when the message was delegated/forked (consumed).
func (l *Loop) tryExternalRoute(chatJID string, msg core.Message, folder string) bool {
	if l.handlePrefixLayer(chatJID, msg, folder) {
		return true
	}
	target := l.resolveTarget(chatJID, msg, folder)
	if target != "" && router.IsAuthorizedRoutingTarget(folder, target) {
		l.delegate(target, msg.Content, chatJID)
		return true
	}
	return false
}

// handlePrefixLayer routes an explicit @child / #topic prefix. @child
// delegates to folder/child; #topic re-appends the stripped message under
// topic="#name". An unknown child is NOT swallowed (defence in depth).
func (l *Loop) handlePrefixLayer(chatJID string, msg core.Message, folder string) bool {
	if msg.BotMsg || strings.HasPrefix(msg.Sender, "timed-") {
		return false
	}
	hasAt := rePrefixAt.MatchString(msg.Content)
	hasHash := rePrefixHash.MatchString(msg.Content)
	if !hasAt && !hasHash {
		return false
	}
	name, stripped, ok := parsePrefix(msg.Content)
	if !ok || name == "" {
		return false
	}
	if hasAt {
		if strings.Contains(name, "/") {
			slog.Warn("@prefix: name contains slash, rejecting", "name", name)
			return false
		}
		child := folder + "/" + name
		if !l.db.GroupExists(child) {
			slog.Warn("@prefix: child group not found", "child", child)
			return false
		}
		l.delegate(child, stripped, chatJID)
		return true
	}
	_ = l.db.PutMessage(core.Message{
		ID:        core.MsgID("topic"),
		ChatJID:   chatJID,
		Sender:    msg.Sender,
		Name:      msg.Name,
		Content:   stripped,
		Topic:     "#" + name,
		Timestamp: time.Now().UTC(),
	})
	l.Enqueue(chatJID)
	return true
}

// resolveTarget picks a delegation target distinct from selfFolder: a reply to
// a bot row routed elsewhere, then the chat's sticky group, then the route
// table. Returns "" when the target is self or none applies.
func (l *Loop) resolveTarget(chatJID string, msg core.Message, selfFolder string) string {
	if msg.ReplyToID != "" {
		if routed := l.db.RoutedToByMessageID(msg.ReplyToID); routed != "" {
			if routed != selfFolder {
				return routed
			}
			return ""
		}
	}
	if sticky, _ := l.db.StickyState(chatJID); sticky != "" {
		if sticky != selfFolder {
			return sticky
		}
		return ""
	}
	routes, err := l.db.Routes()
	if err != nil || len(routes) == 0 {
		return ""
	}
	rt := router.ResolveRouteTarget(msg, routes)
	if rt.Folder != "" && rt.Folder != selfFolder {
		return rt.Folder
	}
	return ""
}

// escOriginRe extracts the escalation origin (folder + jid) from a delegated
// prompt's <escalation_origin/> tag. When present, the child's reply routes back
// to that worker jid instead of the original chat.
var escOriginRe = regexp.MustCompile(`<escalation_origin\s[^/]*folder="([^"]+)"[^/]*jid="([^"]+)"[^/]*/>`)

// escalationWorker returns the escalation-origin return address carried in a
// delegated prompt, or "" when the prompt is a plain delegation. Returns m[1],
// the folder capture group (not jid).
func escalationWorker(prompt string) string {
	m := escOriginRe.FindStringSubmatch(prompt)
	if m == nil {
		return ""
	}
	return m[1]
}

// delegate writes a delegation message to the target group's folder JID and
// enqueues it. A missing target is reported back into the originating chat —
// routing consumes the message, so a silent drop would swallow it.
func (l *Loop) delegate(targetFolder, prompt, originJID string) {
	if err := l.delegateViaMessage(targetFolder, prompt, originJID); err != nil {
		slog.Warn("delegate failed", "target", targetFolder, "err", err)
		l.ack(originJID, err.Error())
	}
}

// delegateViaMessage writes the delegation row and triggers the target's queue.
// ForwardedFrom carries the origin chat as the return address so the child's
// reply-to-bot routes back — overridden to the escalation worker jid when the
// prompt carries an <escalation_origin/> tag. Groups are never created here: an
// unknown target is an error naming the folder and the tool that creates it.
func (l *Loop) delegateViaMessage(targetFolder, prompt, originJID string) error {
	fwdFrom := originJID
	if worker := escalationWorker(prompt); worker != "" {
		fwdFrom = worker
	}

	if !l.db.GroupExists(targetFolder) {
		return fmt.Errorf("no group %q — create it first with register_group(folder=%q, jid=<chat jid>), then retry", targetFolder, targetFolder)
	}

	_ = l.db.PutMessage(core.Message{
		ID:            core.MsgID("delegate"),
		ChatJID:       targetFolder,
		Sender:        "delegate",
		Content:       prompt,
		Timestamp:     time.Now().UTC(),
		ForwardedFrom: fwdFrom,
	})
	l.Enqueue(targetFolder)
	return nil
}

// cmdResolveHold records an operator's verdict on a held tool call and, on
// approval, enqueues the held call's chat so the agent re-issues the call in
// its OWN next turn (spec 5/19). There is no out-of-turn dispatcher: the agent
// MCP server is per-turn, so a second path would have to re-implement ipc's
// grant and audit discipline — the dual-path CLAUDE.md bans.
//
// Gated on IsOperator, the same `**` test as /root. A button press and a typed
// command converge here, and the verdict itself funnels through resolveHoldTx
// — the SAME core the REST face runs — so an approval always commits together
// with the resolution message that triggers the re-issue.
func (l *Loop) cmdResolveHold(chatJID, folder, arg, sender, verdict string) {
	if !l.db.IsOperator(sender) {
		l.ack(chatJID, "Permission denied: approving a held call requires an operator grant (**).")
		return
	}
	id, note, _ := strings.Cut(strings.TrimSpace(arg), " ")
	if id == "" {
		l.ack(chatJID, "Usage: /approve <id> [note]  |  /reject <id> [note]")
		return
	}
	tx, err := l.db.SQL().Begin()
	if err != nil {
		l.ack(chatJID, "Failed: "+err.Error())
		return
	}
	defer tx.Rollback()
	p, enqueueJID, err := resolveHoldTx(tx, id, verdict, sender, strings.TrimSpace(note))
	if err != nil {
		l.ack(chatJID, "Failed: "+err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		l.ack(chatJID, "Failed: "+err.Error())
		return
	}
	if verdict == PendingRejected {
		l.ack(chatJID, fmt.Sprintf("rejected %s (%s)", id, p.Tool))
		return
	}
	l.ack(chatJID, fmt.Sprintf("approved %s (%s) — the agent will re-issue it", id, p.Tool))
	l.Enqueue(enqueueJID)
}
