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
	"github.com/kronael/arizuko/store"
)

// steer consumes the latest message of a routed chat BEFORE it reaches a turn:
// sticky navigation (@group / #topic), then slash commands, then @child
// delegation / the external-route prefix layer. True means consumed — the
// caller advances the cursor and skips the turn.
func (l *Loop) steer(chatJID string, last core.Message, folder string) bool {
	if l.handleStickyCommand(chatJID, last) {
		return true
	}
	if l.handleCommand(chatJID, last, folder) {
		return true
	}
	return l.tryExternalRoute(chatJID, last, folder)
}

// ack sends a steering acknowledgement to the chat; no-op without a Deliverer.
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
// An @ to an unknown folder is NOT consumed: a bare @ at message start has too
// many meanings to swallow.
func (l *Loop) handleStickyCommand(chatJID string, msg core.Message) bool {
	if msg.BotMsg || strings.HasPrefix(msg.Sender, "timed-") {
		return false
	}
	_, content := typedText(msg.Content)
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
// /invite and /gate federate to onbod over HTTP; a nil client reports the gap.

// typedText splits a message into the envelope gitd put in front of it and the
// text the person typed: the one reading the sticky and prefix handlers take.
// The envelope is the "[github " location header and the fenced diff hunk that
// can follow it. Any other leading [..] is typed text.
func typedText(raw string) (envelope, typed string) {
	t := strings.TrimSpace(raw)
	if !strings.HasPrefix(t, "[github ") {
		return "", t
	}
	i := strings.Index(t, "]")
	if i < 0 {
		return "", t
	}
	typed = strings.TrimSpace(t[i+1:])
	if strings.HasPrefix(typed, "```") {
		if j := strings.Index(typed, "\n```"); j >= 0 {
			typed = strings.TrimSpace(typed[j+len("\n```"):])
		}
	}
	return strings.TrimSpace(strings.TrimSuffix(t, typed)), typed
}

// cmdText is the text a slash command reads: typedText, and for a message with
// no gitd envelope a leading [..] tag is dropped too, so "[tag] /new" is a
// command on every channel.
func cmdText(raw string) string {
	envelope, t := typedText(raw)
	if envelope == "" && strings.HasPrefix(t, "[") {
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

// steerCommand runs one slash command. arg is the text after the head.
type steerCommand func(l *Loop, chatJID, folder, arg, sender string)

// steerCommands is a map, not a switch, so a SECOND reader can ask "is this
// row a command?" without running it: dispatch must find the command in a
// batch before it decides which row to steer (BUGS L336).
var steerCommands = map[string]steerCommand{
	"/new":    func(l *Loop, chat, folder, arg, _ string) { l.cmdNew(chat, folder, arg) },
	"/chatid": func(l *Loop, chat, _, _, _ string) { l.ack(chat, chat) },
	"/ping":   func(l *Loop, chat, folder, _, _ string) { l.cmdPing(chat, folder) },
	"/stop":   func(l *Loop, chat, folder, _, _ string) { l.cmdStop(chat, folder) },
	"/status": func(l *Loop, chat, folder, _, sender string) { l.cmdStatus(chat, folder, sender) },
	"/root":   func(l *Loop, chat, folder, arg, sender string) { l.cmdRoot(chat, folder, arg, sender) },
	"/invite": func(l *Loop, chat, folder, arg, sender string) { l.cmdInvite(chat, folder, arg, sender) },
	"/gate":   func(l *Loop, chat, folder, arg, sender string) { l.cmdGate(chat, folder, arg, sender) },
}

// Verdicts must reach the suspended call without waiting for its agent worker.
var verdictCommands = map[string]steerCommand{
	"/approve": func(l *Loop, chat, folder, arg, sender string) {
		l.cmdResolveHold(chat, folder, arg, sender, PendingApproved, false)
	},
	"/reject": func(l *Loop, chat, folder, arg, sender string) {
		l.cmdResolveHold(chat, folder, arg, sender, PendingRejected, false)
	},
	"/always": func(l *Loop, chat, folder, arg, sender string) {
		l.cmdResolveHold(chat, folder, arg, sender, PendingApproved, true)
	},
}

func commandHandler(head string) steerCommand {
	if handler := verdictCommands[head]; handler != nil {
		return handler
	}
	return steerCommands[head]
}

func (l *Loop) handleVerdictCommand(chatJID string, msg core.Message) bool {
	if !IsSteerCommand(msg) {
		return false
	}
	head, arg := lookupCommand(msg.Content)
	handler := verdictCommands[head]
	if handler == nil {
		return false
	}
	handler(l, chatJID, "", arg, msg.Sender)
	return true
}

// IsSteerCommand reports whether this row is a slash command handleCommand
// would run. PURE — no store, no send — which lets dispatch call it on every
// row of a batch.
func IsSteerCommand(msg core.Message) bool {
	if msg.BotMsg || strings.HasPrefix(msg.Sender, "timed-") {
		return false
	}
	head, _ := lookupCommand(msg.Content)
	return commandHandler(head) != nil
}

func (l *Loop) handleCommand(chatJID string, msg core.Message, folder string) bool {
	if !IsSteerCommand(msg) {
		return false
	}
	head, arg := lookupCommand(msg.Content)
	commandHandler(head)(l, chatJID, folder, arg, msg.Sender)
	return true
}

// cmdPing reports the resolved folder, its session prefix, the queue's in-flight
// run count and the registered-group count.
//
// The count is TURNS, not guests: `queue.ActiveCount()` counts what the queue holds,
// and one turn is one guest only while nothing is queued behind it. It said
// "containers" until the substrate stopped having any (BUGS `L389`).
func (l *Loop) cmdPing(chatJID, folder string) {
	sessID, _ := l.db.GetSession(folder, "")
	nGroups := len(l.db.AllGroups())
	active := l.q.ActiveCount()
	sess := "none"
	if sessID != "" {
		sess = sessID[:min(8, len(sessID))]
	}
	l.ack(chatJID, fmt.Sprintf(
		"pong\ngroup: %s\nsession: %s\nactive turns: %d\nregistered groups: %d",
		folder, sess, active, nGroups))
}

// cmdStop asks runed to map the folder to its live spawn and kill it.
//
// The answers name the TURN. A turn is a KVM guest now, and an operator who reads
// "container" goes looking for one with `docker ps` (BUGS `L389`).
func (l *Loop) cmdStop(chatJID, folder string) {
	stopper, ok := l.runner.(RunStopper)
	if !ok {
		l.ack(chatJID, "No active turn for this chat.")
		return
	}
	res, err := stopper.StopFolder(context.Background(), folder)
	if err != nil {
		slog.Warn("cmdStop: runed stop failed", "jid", chatJID, "folder", folder, "err", err)
		l.ack(chatJID, "No active turn for this chat.")
		return
	}
	if res.Killed {
		l.ack(chatJID, "Turn stopped.")
	} else {
		l.ack(chatJID, "No active turn for this chat.")
	}
}

// denyNonOperator is the ONE gate the operator-only slash commands share and
// the one place their refusal is worded; true means refused. The ack NAMES the
// principal tested: IsOperator reads the sender string verbatim, so an operator
// whose grant row names a different platform id reads a bare "operator only"
// as the platform refusing everybody (BUGS L333, L334). The WARN is the
// operator-side half: a steer denial writes no message row and no audit row,
// so without it the refusal leaves NO trace on the instance.
func (l *Loop) denyNonOperator(chatJID, sender, cmd string) bool {
	if l.db.IsOperator(sender) {
		return false
	}
	slog.Warn("operator command denied: the sender holds no ** grant",
		"cmd", cmd, "chat_jid", chatJID, "sender", sender)
	l.ack(chatJID, fmt.Sprintf(
		"Permission denied: %s requires an operator grant (**); %q has none.", cmd, sender))
	return true
}

// cmdStatus reports instance-wide counts, operator-only: instance-wide
// visibility is a `**` privilege, not a folder position (a top-level world is a
// tenant like any other).
func (l *Loop) cmdStatus(chatJID, folder, sender string) {
	if l.denyNonOperator(chatJID, sender, "/status") {
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
		"status\nchannels: %d\ngroups: %d\nactive turns: %d\nerrored chats: %d\nactive tasks: %d",
		nChannels, nGroups, active, errored, tasks))
}

// cmdRoot raises the caller's message to a root turn — the ONLY path to root,
// gated on the operator `**` grant, NOT on folder shape (a top-level world is
// a tenant). The arg is re-injected in place and registered in
// pendingElevation, so runTurn spawns it Elevated; the reply lands back here.
func (l *Loop) cmdRoot(chatJID, folder, arg, sender string) {
	if l.denyNonOperator(chatJID, sender, "/root") {
		return
	}
	if arg == "" {
		l.ack(chatJID, "Usage: /root <message>")
		return
	}
	// Elevation is one-shot: this message only.
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

// cmdInvite mints an invite via onbod's POST /v1/invites, operator-only. The
// target is the folder + "/" so the redeemer picks a username under it.
func (l *Loop) cmdInvite(chatJID, folder, arg, sender string) {
	if l.denyNonOperator(chatJID, sender, "/invite") {
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

// cmdGate manages the onboarding gates via onbod's /v1/gates, operator-only.
func (l *Loop) cmdGate(chatJID, folder, arg, sender string) {
	if l.denyNonOperator(chatJID, sender, "/gate") {
		return
	}
	parts := strings.Fields(arg)
	action := ""
	if len(parts) > 0 {
		action = parts[0]
	}
	// Validate the shape BEFORE touching onbod, so an unknown subcommand gets
	// the usage line even with no onbod wired.
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

// cmdNew clears the folder's session (root or #topic) AND re-injects trailing
// text as a fresh inbound, so `/new look into X` clears then processes "look
// into X". The consumed /new row advances the cursor, so the follow-up runs on
// a clean session next poll.
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

// Navigation prefixes sit at the very start of the message; mid-content
// @mentions / #tags are references, not nav.
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

// handlePrefixLayer routes an explicit @child / #topic prefix: @child
// delegates to folder/child; #topic re-appends the stripped message under
// topic="#name". An unknown child is NOT swallowed.
func (l *Loop) handlePrefixLayer(chatJID string, msg core.Message, folder string) bool {
	if msg.BotMsg || strings.HasPrefix(msg.Sender, "timed-") {
		return false
	}
	envelope, typed := typedText(msg.Content)
	hasAt := rePrefixAt.MatchString(typed)
	hasHash := rePrefixHash.MatchString(typed)
	if !hasAt && !hasHash {
		return false
	}
	name, stripped, ok := parsePrefix(typed)
	if !ok || name == "" {
		return false
	}
	// The envelope stays with the message, so the turn that takes it still
	// reads where it sits and which id a reply names.
	if envelope != "" {
		stripped = envelope + "\n\n" + stripped
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
// table. "" when the target is self or none applies.
func (l *Loop) resolveTarget(chatJID string, msg core.Message, selfFolder string) string {
	if msg.ReplyToID != "" {
		if routed := l.db.RoutedToByMessageID(chatJID, msg.ReplyToID); routed != "" {
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
// prompt's <escalation_origin/> tag; the child's reply routes back to it.
var escOriginRe = regexp.MustCompile(`<escalation_origin\s[^/]*folder="([^"]+)"[^/]*jid="([^"]+)"[^/]*/>`)

// escalationWorker returns the escalation-origin folder carried in a delegated
// prompt, or "" for a plain delegation.
func escalationWorker(prompt string) string {
	m := escOriginRe.FindStringSubmatch(prompt)
	if m == nil {
		return ""
	}
	return m[1]
}

// delegate writes a delegation message to the target group's folder JID. A
// missing target is reported back into the originating chat — routing consumes
// the message, so a silent drop would swallow it.
func (l *Loop) delegate(targetFolder, prompt, originJID string) {
	if err := l.delegateViaMessage(targetFolder, prompt, originJID); err != nil {
		slog.Warn("delegate failed", "target", targetFolder, "err", err)
		l.ack(originJID, err.Error())
	}
}

// delegateViaMessage writes the delegation row and triggers the target's
// queue. ForwardedFrom is the return address (the escalation worker jid when
// the prompt carries an <escalation_origin/> tag). Groups are never created
// here: an unknown target is an error naming the tool that creates it.
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
		Sender:        store.DelegateSender,
		Content:       prompt,
		Timestamp:     time.Now().UTC(),
		ForwardedFrom: fwdFrom,
	})
	l.Enqueue(targetFolder)
	return nil
}

// cmdResolveHold records a human's verdict on a held tool call (spec 5/19).
// The agent's call is suspended on the row, so the verdict IS the answer: the
// waiter runs or refuses the call inside that turn; there is no out-of-turn
// dispatcher. Who may answer is decided inside resolveHoldInto, the SAME core
// the REST face runs. `standing` is /always: `yes` plus the skip-approval
// rule, one batch. There is no /never: acl rows carry no verdict identity
// (spec 6/26-personal-mode §"The mechanics behind `always`").
func (l *Loop) cmdResolveHold(chatJID, folder, arg, sender, verdict string, standing bool) {
	id, note, _ := strings.Cut(strings.TrimSpace(arg), " ")
	if id == "" {
		l.ack(chatJID, "Usage: /approve <id> [note]  |  /reject <id> [note]  |  /always <id> [note]")
		return
	}
	ctx := context.Background()
	ws := &store.WriteSet{}
	p, err := l.db.resolveHoldInto(ctx, ws, id, verdict, sender, strings.TrimSpace(note), standing,
		fmt.Errorf("pending action %q %w (by someone else)", id, ErrPendingResolved))
	if err != nil {
		l.ack(chatJID, "Failed: "+err.Error())
		return
	}
	if err := l.db.store().Apply(ctx, ws); err != nil {
		l.ack(chatJID, "Failed: "+err.Error())
		return
	}
	switch {
	case verdict == PendingRejected:
		l.ack(chatJID, fmt.Sprintf("⛔ rejected (%s).", id))
	case standing:
		l.ack(chatJID, fmt.Sprintf("✅ ok (%s). %s Undo: revoke the %s%s skip row on the grants page.",
			id, p.RuleText, auth.HoldPrefix, p.Tool))
	default:
		l.ack(chatJID, fmt.Sprintf("✅ ok (%s) — the agent runs it now.", id))
	}
}
