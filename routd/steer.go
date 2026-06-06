package routd

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/router"
)

// steer consumes the latest message of a routed chat BEFORE it reaches a turn
// (mirrors gated pollOnce's pre-enqueue layer). It applies, in order: sticky
// navigation (@group / #topic), routd-serviceable slash commands, and @child
// delegation / the external-route prefix layer. Returns true when the message
// was consumed — the caller then advances the cursor and skips the turn.
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
		_, _ = l.deliver.Send(chatJID, text, "", "", "ack-"+core.MsgID(""))
	}
}

// --- sticky navigation (@group / #topic) ---

func isStickyCommand(content string) bool {
	t := strings.TrimSpace(content)
	if len(t) == 0 || (t[0] != '@' && t[0] != '#') {
		return false
	}
	return !strings.ContainsAny(t, " \n")
}

// handleStickyCommand pins (or resets) the chat's @group / #topic navigation.
// A bare @ or # resets; @<known-folder> / #<topic> pins. An @ to an unknown
// folder is NOT consumed — passed through to the agent (gated rationale: bare
// @ at message start has too many meanings to swallow).
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

// --- slash commands (port of gateway/commands.go) ---
//
// Output text matches gated verbatim (operators rely on the exact responses).
// routd owns messages/routes/sessions/sticky/chats and reaches containers via
// its queue + tasks via the sibling messages.db, so /new /chatid /ping /status
// /stop /root /approve /reject port in full. /invite and /gate need onbod-owned
// tables (invites + onboarding_gates) routd cannot reach in-process — they
// dispatch a "needs federation" notice rather than silently dropping.

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
		l.cmdStatus(chatJID, folder)
	case "/root":
		l.cmdRoot(chatJID, folder, arg)
	case "/invite":
		l.cmdInvite(chatJID, folder, arg)
	case "/gate":
		l.cmdGate(chatJID, folder, arg)
	case "/approve", "/reject":
		l.ack(chatJID, "HITL not configured")
	default:
		return false
	}
	return true
}

// cmdPing reports the resolved folder, its session prefix, the count of live
// runs in routd's queue, and the registered-group count (port of
// gateway.cmdPing; "active containers" is routd's in-flight run count — the
// container itself runs in runed, but the queue's active count is the
// equivalent live-run gauge).
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

// cmdStop kills the resolved folder's live run (port of gateway.cmdStop). The
// container is owned by runed in the split, so routd asks runed to map the
// folder to its live spawn and kill it (POST /v1/runs/stop) rather than its own
// queue (which launches no containers here). Response text is gated-verbatim.
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

// cmdStatus reports instance-wide counts, root-group only (port of
// gateway.cmdStatus). Channels come from the channel registry (held by the
// Server); errored chats + active tasks from routd's chats table + the timed
// sibling DB.
func (l *Loop) cmdStatus(chatJID, folder string) {
	if auth.Resolve(folder).Tier != 0 {
		l.ack(chatJID, "Permission denied: root only.")
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

// cmdRoot delegates a message up to the world-root group (port of
// gateway.cmdRoot). Tier>1 is denied; a bare /root usage-prompts; an already-
// root or missing-root group short-circuits.
func (l *Loop) cmdRoot(chatJID, folder, arg string) {
	if auth.Resolve(folder).Tier > 1 {
		l.ack(chatJID, "Permission denied.")
		return
	}
	if arg == "" {
		l.ack(chatJID, "Usage: /root <message>")
		return
	}
	rootFolder := strings.SplitN(folder, "/", 2)[0]
	if rootFolder == folder {
		l.ack(chatJID, "Already in root group.")
		return
	}
	if !l.db.GroupExists(rootFolder) {
		l.ack(chatJID, "Root group not found.")
		return
	}
	if err := l.delegateViaMessage(rootFolder, arg, chatJID, 0); err != nil {
		slog.Warn("cmdRoot: delegate failed", "jid", chatJID, "target", rootFolder, "err", err)
	}
}

// cmdInvite is dispatched but cannot be fully wired in routd: onboarding
// invites live in onbod's tables (store.CreateInvite against messages.db),
// which routd does not own and reaches only read-only. It validates the
// tier-0 gate + arg shape exactly as gated, then reports the federation gap
// instead of minting a (storage-less) token.
func (l *Loop) cmdInvite(chatJID, folder, arg string) {
	if auth.Resolve(folder).Tier != 0 {
		l.ack(chatJID, "Permission denied: root group only.")
		return
	}
	if arg != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(arg)); err != nil || n < 1 {
			l.ack(chatJID, "Usage: /invite [max_uses]")
			return
		}
	}
	l.ack(chatJID, "Invites are managed by onbod; run `arizuko invite` or ask onbod (routd cannot mint invites).")
}

// cmdGate is dispatched but cannot be fully wired in routd: the
// onboarding_gates table is onbod-owned (store.ListGates/PutGate/... against
// messages.db) and not reachable from routd in-process. It validates the
// tier-0 gate + subcommand shape exactly as gated, then reports the federation
// gap instead of mutating a table it doesn't own.
func (l *Loop) cmdGate(chatJID, folder, arg string) {
	if auth.Resolve(folder).Tier != 0 {
		l.ack(chatJID, "Permission denied: root only.")
		return
	}
	parts := strings.Fields(arg)
	action := ""
	if len(parts) > 0 {
		action = parts[0]
	}
	switch action {
	case "", "list", "add", "rm", "enable", "disable":
		l.ack(chatJID, "Gates are managed by onbod; use the onbod surface (routd cannot read/mutate onboarding gates).")
	default:
		l.ack(chatJID, "Usage: /gate [list|add|rm|enable|disable]")
	}
}

// cmdNew clears the resolved folder's session (root or #topic) AND reinjects any
// trailing text as a fresh inbound so `/new look into X` clears the session then
// processes "look into X"; a bare `/new` just clears. Mirrors gated cmdNew
// (commands.go § cmdNew). The synthetic inbound is enqueued; the consumed /new
// row advances the cursor, so the followup runs on a clean session next poll.
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

// --- @child delegation / external-route prefix layer ---

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
// Mirrors gated tryExternalRoute (prefix layer + resolveTarget). Returns true
// when the message was delegated/forked (consumed).
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

// delegate writes a delegation message to the target group's folder JID and
// enqueues it. ForwardedFrom carries the origin chat as the return address so
// the child's reply-to-bot routes back. When the target is unknown but its
// parent exists with a prototype/ dir, it is spawned on the fly (port of
// gateway.delegateViaMessage's spawn-on-delegation). Depth-0 entry.
func (l *Loop) delegate(targetFolder, prompt, originJID string) {
	if err := l.delegateViaMessage(targetFolder, prompt, originJID, 0); err != nil {
		slog.Warn("delegate failed", "target", targetFolder, "err", err)
	}
}

// delegateViaMessage writes the delegation row and triggers the target's
// queue. On an unknown target whose parent group exists, it spawns a child
// from the parent's prototype/ dir and recurses once (depth guard). The
// child's reply routes back to forwardedFrom — overridden to the escalation
// worker jid when the prompt carries an <escalation_origin/> tag. Faithful
// port of gateway.delegateViaMessage.
func (l *Loop) delegateViaMessage(targetFolder, prompt, originJID string, depth int) error {
	if depth > 1 {
		return fmt.Errorf("delegation depth exceeded")
	}

	fwdFrom := originJID
	if worker := escalationWorker(prompt); worker != "" {
		fwdFrom = worker
	}

	if !l.db.GroupExists(targetFolder) {
		sep := strings.LastIndex(targetFolder, "/")
		if sep > 0 {
			parentFolder := targetFolder[:sep]
			if l.db.GroupExists(parentFolder) {
				spawned, err := l.spawnFromPrototype(parentFolder, originJID)
				if err == nil {
					return l.delegateViaMessage(spawned.Folder, prompt, originJID, depth+1)
				}
				slog.Warn("delegate: spawn from prototype failed",
					"parent", parentFolder, "target", targetFolder, "err", err)
			}
		}
		return fmt.Errorf("delegate target not found: %s", targetFolder)
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
