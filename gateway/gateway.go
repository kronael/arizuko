package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/onvos/arizuko/auth"
	"github.com/onvos/arizuko/container"
	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/diary"
	"github.com/onvos/arizuko/grants"
	"github.com/onvos/arizuko/groupfolder"
	"github.com/onvos/arizuko/ipc"
	"github.com/onvos/arizuko/queue"
	"github.com/onvos/arizuko/router"
	"github.com/onvos/arizuko/store"
)

type Gateway struct {
	cfg     *core.Config
	store   *store.Store
	queue   *queue.GroupQueue
	folders *groupfolder.Resolver

	mu       sync.RWMutex
	channels []core.Channel
	groups   map[string]core.Group
	gatedFns ipc.GatedFns
	storeFns ipc.StoreFns

	// lastTimestamp is persisted and queried as RFC3339Nano throughout to
	// preserve nanosecond precision; seconds-only formats risk missed messages.
	lastTimestamp time.Time

	impulse *impulseGate
}

func New(cfg *core.Config, s *store.Store) *Gateway {
	return &Gateway{
		cfg:   cfg,
		store: s,
		queue: queue.New(cfg.MaxContainers, cfg.DataDir),
		folders: &groupfolder.Resolver{
			GroupsDir: cfg.GroupsDir,
			DataDir:   cfg.DataDir,
		},
		groups:  make(map[string]core.Group),
		impulse: newImpulseGate(defaultImpulseCfg()),
	}
}

func (g *Gateway) AddChannel(ch core.Channel) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.channels = append(g.channels, ch)
}

func (g *Gateway) RemoveChannel(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, ch := range g.channels {
		if ch.Name() == name {
			g.channels = append(g.channels[:i], g.channels[i+1:]...)
			return
		}
	}
}

func (g *Gateway) Run(ctx context.Context) error {
	if err := container.EnsureRunning(); err != nil {
		return fmt.Errorf("runtime check failed: %w", err)
	}
	container.CleanupOrphans(g.cfg.Image)

	g.loadState()

	// Register LocalChannel for agent-to-agent communication
	g.AddChannel(NewLocalChannel(g.store))

	g.gatedFns = ipc.GatedFns{
		SendMessage: func(jid, text string) (string, error) {
			return g.sendMessageReply(jid, text, "", "")
		},
		SendReply: func(jid, text, replyTo string) (string, error) {
			return g.sendMessageReply(jid, text, replyTo, "")
		},
		SendDocument:     g.sendDocument,
		ClearSession:     g.clearSession,
		GroupsDir:        g.cfg.GroupsDir,
		HostGroupsDir:    g.cfg.HostGroupsDir,
		InjectMessage:    g.injectMessage,
		RegisterGroup:    g.registerGroupIPC,
		GetGroups:        g.getGroups,
		DelegateToChild:  g.delegateToChild,
		DelegateToParent: g.delegateToParent,
		SpawnGroup: func(parentJID, childJID string) (core.Group, error) {
			g.mu.RLock()
			parent, ok := g.groups[parentJID]
			g.mu.RUnlock()
			if !ok {
				return core.Group{}, fmt.Errorf("parent group not found: %s", parentJID)
			}
			return g.spawnFromPrototype(parentJID, parent.Folder, childJID)
		},
	}
	g.storeFns = ipc.StoreFns{
		CreateTask: g.store.CreateTask,
		GetTask:    g.store.GetTask,
		UpdateTaskStatus: func(id, status string) error {
			return g.store.UpdateTask(id, store.TaskPatch{Status: &status})
		},
		DeleteTask:     g.store.DeleteTask,
		ListTasks:      g.store.ListTasks,
		GetRoutes:      g.store.GetRoutes,
		SetRoutes:      g.store.SetRoutes,
		AddRoute:       g.store.AddRoute,
		DeleteRoute:    g.store.DeleteRoute,
		GetRoute:       g.store.GetRoute,
		GetGrants:      g.store.GetGrants,
		SetGrants:      g.store.SetGrants,
		StoreOutbound:  g.store.StoreOutbound,
		GetLastReplyID: g.store.GetLastReplyID,
		SetLastReplyID: g.store.SetLastReplyID,
	}
	g.queue.SetProcessMessagesFn(g.processGroupMessages)
	g.queue.SetNotifyErrorFn(func(jid string, err error) {
		msg := fmt.Sprintf("⚠️ Agent error: %v\n\nSend another message to retry.", err)
		g.sendMessage(jid, msg)
	})

	slog.Info("connecting channels", "count", len(g.channels))
	for _, ch := range g.channels {
		slog.Info("connecting channel", "channel", ch.Name())
		if err := ch.Connect(ctx); err != nil {
			slog.Error("channel connect failed",
				"channel", ch.Name(), "err", err)
			continue
		}
		slog.Info("channel connected", "channel", ch.Name())
	}
	slog.Info("all channels connected")

	g.recoverPendingMessages()

	slog.Info("arizuko running",
		"name", g.cfg.Name,
		"groups", len(g.groups),
		"image", g.cfg.Image,
	)

	g.messageLoop(ctx)
	return nil
}

func (g *Gateway) Shutdown() {
	g.queue.Shutdown()
	for _, ch := range g.channels {
		if err := ch.Disconnect(); err != nil {
			slog.Error("channel disconnect failed",
				"channel", ch.Name(), "err", err)
		}
	}
	g.saveState()
	slog.Info("gateway shut down")
}

func (g *Gateway) loadState() {
	raw := g.store.GetState("last_timestamp")
	if raw != "" {
		g.lastTimestamp, _ = time.Parse(time.RFC3339Nano, raw)
	}

	raw = g.store.GetState("last_agent_timestamp")
	if raw != "" {
		var m map[string]string
		if json.Unmarshal([]byte(raw), &m) == nil {
			for k, v := range m {
				if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
					g.store.SetAgentCursor(k, t)
				}
			}
		}
		g.store.SetState("last_agent_timestamp", "")
	}

	g.groups = g.store.AllGroups()
	if g.groups == nil {
		g.groups = make(map[string]core.Group)
	}

	slog.Info("state loaded", "groups", len(g.groups))
}

func (g *Gateway) saveState() {
	g.mu.RLock()
	defer g.mu.RUnlock()
	g.store.SetState("last_timestamp",
		g.lastTimestamp.Format(time.RFC3339Nano))
}

func (g *Gateway) messageLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		g.pollOnce()

		select {
		case <-ctx.Done():
			return
		case <-time.After(g.cfg.PollInterval):
		}
	}
}

func (g *Gateway) pollOnce() {
	jids := g.groupJIDs()
	g.mu.RLock()
	since := g.lastTimestamp
	g.mu.RUnlock()

	if g.cfg.OnboardingEnabled {
		jids = append(jids, g.store.UnroutedChatJIDs(since)...)
	}
	jids = append(jids, g.store.ActiveWebJIDs(since)...)

	if len(jids) == 0 {
		return
	}

	msgs, hi, err := g.store.NewMessages(jids, since, g.cfg.Name)
	if err != nil {
		slog.Error("error in message loop", "err", err)
		time.Sleep(5 * time.Second)
		return
	}
	if len(msgs) == 0 {
		return
	}

	g.mu.Lock()
	g.lastTimestamp = hi
	g.mu.Unlock()

	byChat := make(map[string][]core.Message)
	for _, m := range msgs {
		byChat[m.ChatJID] = append(byChat[m.ChatJID], m)
	}

	for chatJid, chatMsgs := range byChat {
		g.mu.RLock()
		group, ok := g.groupForJid(chatJid)
		g.mu.RUnlock()
		if !ok {
			if g.cfg.OnboardingEnabled {
				last := chatMsgs[len(chatMsgs)-1]
				ch := g.findChannel(chatJid)
				if err := g.store.InsertOnboarding(chatJid, last.Sender, channelName(ch)); err != nil {
					slog.Warn("insert onboarding", "jid", chatJid, "err", err)
				}
			}
			slog.Debug("poll: no group for jid", "jid", chatJid)
			continue
		}

		routes := g.store.GetRoutes(chatJid)

		last := chatMsgs[len(chatMsgs)-1]

		// Handle sticky routing commands (@, @name, #, #topic)
		if g.handleStickyCommand(chatJid, last) {
			slog.Debug("poll: handled sticky command", "jid", chatJid)
			continue
		}

		if g.handleCommand(last, group) {
			slog.Debug("poll: handled command", "jid", chatJid, "sender", last.Sender)
			continue
		}

		if pr := findPrefixRoute(routes, last); pr != nil {
			if g.handlePrefixRoute(pr, last, group, chatJid) {
				slog.Debug("poll: routed via prefix", "jid", chatJid, "match", pr.Match)
				continue
			}
		}

		routingTarget := g.resolveTarget(last, routes, group.Folder)

		if routingTarget != "" {
			if router.IsAuthorizedRoutingTarget(group.Folder, routingTarget) {
				slog.Debug("poll: delegating to child",
					"jid", chatJid, "target", routingTarget)
				g.delegateToChild(routingTarget, last.Content, chatJid, 0, nil)
				continue
			}
		}

		if !g.impulse.accept(chatJid, chatMsgs) {
			slog.Debug("poll: impulse hold", "jid", chatJid)
			continue
		}

		if g.queue.SendMessage(chatJid, last.Content) {
			slog.Debug("poll: enqueued message", "jid", chatJid)
			g.store.ClearChatErrored(chatJid)
			continue
		}

		slog.Debug("poll: enqueue check", "jid", chatJid)
		g.queue.EnqueueMessageCheck(chatJid)
	}

	// Flush JIDs that have pending weight exceeding max_hold.
	for _, jid := range g.impulse.flush() {
		slog.Debug("poll: impulse timeout flush", "jid", jid)
		g.queue.EnqueueMessageCheck(jid)
	}

	g.saveState()
}

func (g *Gateway) processGroupMessages(chatJid string) (bool, error) {
	g.mu.RLock()
	group, ok := g.groupForJid(chatJid)
	g.mu.RUnlock()
	if !ok {
		return false, fmt.Errorf("group not registered: %s", chatJid)
	}

	agentTs := g.store.GetAgentCursor(chatJid)

	ch := g.findChannel(chatJid)

	msgs, err := g.store.MessagesSince(chatJid, agentTs, g.cfg.Name)
	if err != nil {
		return false, fmt.Errorf("query messages: %w", err)
	}
	if len(msgs) == 0 {
		return true, nil
	}

	routes := g.store.GetRoutes(chatJid)

	all := msgs
	n := 0
	for _, m := range msgs {
		if !isGatewayCommand(m.Content) {
			msgs[n] = m
			n++
		}
	}
	msgs = msgs[:n]
	if len(msgs) == 0 {
		g.advanceAgentCursor(chatJid, all)
		return true, nil
	}

	if strings.HasPrefix(chatJid, "web:") {
		ok, err := g.processWebTopics(group, chatJid, ch, msgs)
		if ok || err != nil {
			return ok, err
		}
		g.advanceAgentCursor(chatJid, all)
		return true, nil
	}

	senderBatches := groupBySender(msgs)
	for _, batch := range senderBatches {
		last := batch[len(batch)-1]

		if pr := findPrefixRoute(routes, last); pr != nil {
			if g.handlePrefixRoute(pr, last, group, chatJid) {
				slog.Debug("process: routed via prefix", "jid", chatJid, "sender", last.Sender, "match", pr.Match)
				continue
			}
		}

		routingTarget := g.resolveTarget(last, routes, group.Folder)

		if routingTarget != "" {
			if router.IsAuthorizedRoutingTarget(group.Folder, routingTarget) {
				slog.Debug("process: delegating to child",
					"jid", chatJid, "sender", last.Sender, "target", routingTarget)
				g.delegateToChild(routingTarget, last.Content, chatJid, 0, nil)
				continue
			}
		}

		if !g.processSenderBatch(group, chatJid, ch, batch, agentTs) {
			g.advanceAgentCursor(chatJid, msgs)
			return false, fmt.Errorf("sender batch failed: %s", last.Sender)
		}
	}

	slog.Debug("process: completed all sender batches",
		"jid", chatJid, "group", group.Folder, "batches", len(senderBatches))
	g.advanceAgentCursor(chatJid, msgs)
	g.store.ClearChatErrored(chatJid)
	return true, nil
}

func groupBySender(msgs []core.Message) [][]core.Message {
	if len(msgs) == 0 {
		return nil
	}
	type batch struct {
		sender   string
		messages []core.Message
	}
	var batches []batch
	senderIdx := make(map[string]int)

	for _, m := range msgs {
		idx, seen := senderIdx[m.Sender]
		if !seen {
			idx = len(batches)
			senderIdx[m.Sender] = idx
			batches = append(batches, batch{sender: m.Sender})
		}
		batches[idx].messages = append(batches[idx].messages, m)
	}

	result := make([][]core.Message, len(batches))
	for i, b := range batches {
		result[i] = b.messages
	}
	return result
}

func (g *Gateway) processSenderBatch(
	group core.Group, chatJid string, ch core.Channel, msgs []core.Message, agentTs time.Time,
) bool {
	last := msgs[len(msgs)-1]

	g.emitSystemEvents(group, chatJid)
	sysMsgs := g.store.FlushSysMsgs(group.Folder)
	prompt := sysMsgs + router.ClockXml(g.cfg.Timezone) + "\n"
	prompt += router.FormatMessages(msgs)

	if ch != nil {
		ch.Typing(chatJid, true)
	}

	isolated := strings.HasPrefix(last.Sender, "scheduler-isolated")
	topic := g.effectiveTopic(chatJid, last.Topic)
	onOutput, hadOutput := g.makeOutputCallback(chatJid, topic, last.ID, group.Folder)
	out := g.runAgentWithOpts(group, prompt, chatJid, last.Sender,
		onOutput, isolated, nil, topic, last.ID)

	if ch != nil {
		ch.Typing(chatJid, false)
	}

	if out.Error != "" {
		slog.Error("agent error",
			"group", group.Folder, "sender", last.Sender, "err", out.Error)
		if gp, err := g.folders.GroupPath(group.Folder); err == nil {
			diary.WriteRecovery(gp, "error", out.Error)
		}
		if *hadOutput {
			return true
		}
		g.store.SetAgentCursor(chatJid, agentTs)
		g.sendMessage(chatJid,
			"Failed: agent error, will retry on next message.")
		g.store.MarkChatErrored(chatJid)
		return false
	}

	if !*hadOutput {
		slog.Warn("agent completed with no output delivered",
			"jid", chatJid, "group", group.Folder, "sender", last.Sender)
	}
	return true
}

func (g *Gateway) processWebTopics(
	group core.Group, chatJid string, ch core.Channel, msgs []core.Message,
) (bool, error) {
	byTopic := make(map[string][]core.Message)
	var topicOrder []string
	for _, m := range msgs {
		if _, seen := byTopic[m.Topic]; !seen {
			topicOrder = append(topicOrder, m.Topic)
		}
		byTopic[m.Topic] = append(byTopic[m.Topic], m)
	}

	if len(topicOrder) == 0 {
		return false, nil
	}

	for _, topic := range topicOrder {
		topicMsgs := byTopic[topic]
		last := topicMsgs[len(topicMsgs)-1]

		sysMsgs := g.store.FlushSysMsgs(group.Folder)
		prompt := sysMsgs + router.ClockXml(g.cfg.Timezone) + "\n"
		prompt += router.FormatMessages(topicMsgs)

		if ch != nil {
			ch.Typing(chatJid, true)
		}

		effectiveTopic := g.effectiveTopic(chatJid, topic)
		onOutput, hadOutput := g.makeOutputCallback(chatJid, effectiveTopic, last.ID, group.Folder)
		out := g.runAgentWithOpts(group, prompt, chatJid, last.Sender,
			onOutput, false, nil, effectiveTopic, last.ID)

		if ch != nil {
			ch.Typing(chatJid, false)
		}

		if out.Error != "" {
			slog.Error("agent error",
				"group", group.Folder, "topic", topic, "err", out.Error)
			if gp, err := g.folders.GroupPath(group.Folder); err == nil {
				diary.WriteRecovery(gp, "error", out.Error)
			}
			if *hadOutput {
				g.advanceAgentCursor(chatJid, topicMsgs)
				return true, nil
			}
			g.sendMessage(chatJid,
				"Failed: agent error, will retry on next message.")
			g.store.MarkChatErrored(chatJid)
			return false, fmt.Errorf("agent: %s", out.Error)
		}

		if !*hadOutput {
			slog.Warn("agent completed with no output delivered",
				"jid", chatJid, "group", group.Folder, "topic", topic)
		}
		g.advanceAgentCursor(chatJid, topicMsgs)
	}

	g.store.ClearChatErrored(chatJid)
	return true, nil
}

func (g *Gateway) makeOutputCallback(chatJid, topic, firstMsgID, groupFolder string) (func(string, string), *bool) {
	var hadOutput bool
	lastSentID := g.store.GetLastReplyID(chatJid, topic)
	if lastSentID == "" {
		lastSentID = firstMsgID
	}
	return func(text, _ string) {
		if text == "" {
			return
		}
		hadOutput = true
		stripped, statuses := router.ExtractStatusBlocks(text)
		for _, s := range statuses {
			if sentID, _ := g.sendMessageReply(chatJid, s, "", ""); sentID != "" {
				g.store.StoreOutbound(core.OutboundEntry{
					ChatJID:       chatJid,
					Content:       s,
					Source:        "agent",
					GroupFolder:   groupFolder,
					PlatformMsgID: sentID,
				})
			}
		}
		if clean := router.FormatOutbound(stripped); clean != "" {
			if sentID, _ := g.sendMessageReply(chatJid, clean, lastSentID, topic); sentID != "" {
				lastSentID = sentID
				g.store.SetLastReplyID(chatJid, topic, sentID)
				g.store.PutMessage(core.Message{
					ID:        sentID,
					ChatJID:   chatJid,
					Sender:    g.cfg.Name,
					Name:      g.cfg.Name,
					Content:   clean,
					Timestamp: time.Now(),
					BotMsg:    true,
					Topic:     topic,
					RoutedTo:  groupFolder,
				})
					g.store.StoreOutbound(core.OutboundEntry{
					ChatJID:       chatJid,
					Content:       clean,
					Source:        "agent",
					GroupFolder:   groupFolder,
					ReplyToID:     lastSentID,
					PlatformMsgID: sentID,
				})
			}
		}
	}, &hadOutput
}

func (g *Gateway) runAgentWithOpts(
	group core.Group, prompt, chatJid, sender string,
	onOutput func(string, string), isolated bool,
	rules []string, topic string, msgID string,
) container.Output {
	var sessionID string
	if !isolated {
		sessionID, _ = g.store.GetSession(group.Folder, topic)
	}

	if rules == nil {
		id := auth.Resolve(group.Folder)
		rules = grants.DeriveRules(g.store, group.Folder, id.Tier, auth.WorldOf(group.Folder))
		rules = append(rules, g.store.GetGrants(group.Folder)...)
	}

	groupPath, err := g.folders.GroupPath(group.Folder)
	if err != nil {
		return container.Output{Error: err.Error()}
	}

	sanitized := container.SanitizeFolder(group.Folder)
	var cname string
	if taskID := strings.TrimPrefix(sender, "scheduler-isolated:"); taskID != sender {
		cname = fmt.Sprintf("arizuko-%s-task-%s", sanitized, container.SanitizeFolder(taskID))
	} else {
		cname = fmt.Sprintf("arizuko-%s-%d", sanitized, time.Now().UnixMilli())
	}

	g.queue.RegisterProcess(chatJid, cname, group.Folder)

	isRoot := groupfolder.IsRoot(group.Folder)
	container.WriteTasksSnapshot(
		g.folders, group.Folder, isRoot, g.store.ListTasks("", true))
	container.WriteGroupsSnapshot(
		g.folders, group.Folder, isRoot, g.groupList())

	input := container.Input{
		Prompt:    prompt,
		SessionID: sessionID,
		ChatJID:   chatJid,
		Folder:    group.Folder,
		Topic:     topic,
		GroupPath: groupPath,
		Name:      cname,
		Config:    group.Config,
		SlinkToken: group.SlinkToken,
		Channel:   channelName(g.findChannel(chatJid)),
		MessageID: msgID,
		Sender:    sender,
		OnOutput:  onOutput,
		Grants:    rules,
		GatedFns:  g.gatedFns,
		StoreFns:  g.storeFns,
	}

	out := container.Run(g.cfg, g.folders, input)

	if isolated {
		return out
	}

	if out.NewSessionID != "" {
		g.store.SetSession(group.Folder, topic, out.NewSessionID)
		if sessionID == "" {
			g.store.RecordSession(group.Folder, out.NewSessionID)
		}
	} else if out.Error != "" && !out.HadOutput {
		g.store.DeleteSession(group.Folder, topic)
	}

	return out
}

func (g *Gateway) findChannel(jid string) core.Channel {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, ch := range g.channels {
		if ch.Owns(jid) {
			return ch
		}
	}
	return nil
}

func (g *Gateway) sendMessage(jid, text string) error {
	_, err := g.sendMessageReply(jid, text, "", "")
	return err
}

func (g *Gateway) sendMessageReply(jid, text, replyTo, threadID string) (string, error) {
	ch := g.findChannel(jid)
	if ch == nil {
		return "", fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Send(jid, text, replyTo, threadID)
}

func (g *Gateway) sendDocument(jid, path, name string) error {
	ch := g.findChannel(jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.SendFile(jid, path, name)
}

func (g *Gateway) clearSession(folder string) {
	g.store.DeleteSession(folder, "")
}

func (g *Gateway) injectMessage(jid, content, sender, senderName string) (string, error) {
	id := fmt.Sprintf("inject-%d", time.Now().UnixNano())
	msg := core.Message{
		ID:        id,
		ChatJID:   jid,
		Sender:    sender,
		Name:      senderName,
		Content:   content,
		Timestamp: time.Now(),
	}
	if err := g.store.PutMessage(msg); err != nil {
		return "", err
	}
	g.store.ClearChatErrored(jid)
	return id, nil
}

func (g *Gateway) registerGroupIPC(jid string, group core.Group) error {
	g.mu.Lock()
	g.groups[jid] = group
	g.mu.Unlock()
	if err := g.store.PutGroup(jid, group); err != nil {
		return err
	}
	if auth.Resolve(group.Folder).Tier <= 2 {
		g.store.InsertPrefixRoutes(jid, group.Folder)
	}
	ensureGroupGitRepo(filepath.Join(g.cfg.GroupsDir, group.Folder))
	return nil
}

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

func (g *Gateway) getGroups() map[string]core.Group {
	g.mu.RLock()
	defer g.mu.RUnlock()
	cp := make(map[string]core.Group, len(g.groups))
	for k, v := range g.groups {
		cp[k] = v
	}
	return cp
}

func (g *Gateway) groupByFolderLocked(folder string) (string, core.Group, bool) {
	for jid, gr := range g.groups {
		if gr.Folder == folder {
			return jid, gr, true
		}
	}
	return "", core.Group{}, false
}

type escalationMetadata struct {
	WorkerFolder string
	OriginJID    string
	ReplyTo      string
}

// parseEscalationOrigin extracts metadata from <escalation_origin> XML tag.
// Format: <escalation_origin folder="world/support" jid="telegram:123" reply_to="567"/>
func parseEscalationOrigin(prompt string) *escalationMetadata {
	// Simple XML parsing - look for escalation_origin tag
	start := strings.Index(prompt, "<escalation_origin")
	if start == -1 {
		return nil
	}
	end := strings.Index(prompt[start:], "/>")
	if end == -1 {
		return nil
	}
	tag := prompt[start : start+end+2]

	var meta escalationMetadata
	// Extract folder="..."
	if idx := strings.Index(tag, `folder="`); idx != -1 {
		rest := tag[idx+8:]
		if endQuote := strings.Index(rest, `"`); endQuote != -1 {
			meta.WorkerFolder = rest[:endQuote]
		}
	}
	// Extract jid="..."
	if idx := strings.Index(tag, `jid="`); idx != -1 {
		rest := tag[idx+5:]
		if endQuote := strings.Index(rest, `"`); endQuote != -1 {
			meta.OriginJID = rest[:endQuote]
		}
	}
	// Extract reply_to="..."
	if idx := strings.Index(tag, `reply_to="`); idx != -1 {
		rest := tag[idx+10:]
		if endQuote := strings.Index(rest, `"`); endQuote != -1 {
			meta.ReplyTo = rest[:endQuote]
		}
	}

	if meta.WorkerFolder == "" || meta.OriginJID == "" {
		return nil
	}
	return &meta
}

func (g *Gateway) delegateToParent(parentFolder, prompt, originJid string, depth int, rules []string) error {
	return g.delegateToFolder("escalate", parentFolder, prompt, originJid, depth, rules)
}

func (g *Gateway) groupForJid(jid string) (core.Group, bool) {
	if gr, ok := g.groups[jid]; ok {
		return gr, true
	}
	if strings.HasPrefix(jid, "local:") {
		_, gr, ok := g.groupByFolderLocked(jid[6:])
		return gr, ok
	}
	if strings.HasPrefix(jid, "web:") {
		_, gr, ok := g.groupByFolderLocked(jid[4:])
		return gr, ok
	}
	return core.Group{}, false
}

func (g *Gateway) resolveTarget(msg core.Message, routes []core.Route, selfFolder string) string {
	// 1. Check reply-chain routing first (takes precedence over sticky)
	if msg.ReplyToID != "" {
		routedTo := g.store.RoutedToByMessageID(msg.ReplyToID)
		if routedTo != "" {
			// Reply chain found - either route there or stay in current group
			if routedTo != selfFolder {
				return routedTo
			}
			// Reply chain points to self - stay in current group (no other routing)
			return ""
		}
	}

	// 2. Check sticky group routing
	stickyGroup := g.store.GetStickyGroup(msg.ChatJID)
	if stickyGroup != "" {
		if stickyGroup != selfFolder {
			return stickyGroup
		}
		// Sticky points to self - stay in current group (no other routing)
		return ""
	}

	// 3. Fall back to standard route resolution
	if len(routes) == 0 {
		return ""
	}
	t := router.ResolveRoute(msg, routes)
	if t != "" && t != selfFolder {
		return t
	}
	return ""
}

// isStickyCommand returns true if content is exactly @, @name, #, or #topic.
func isStickyCommand(content string) bool {
	t := strings.TrimSpace(content)
	if len(t) == 0 {
		return false
	}
	first := t[0]
	if first != '@' && first != '#' {
		return false
	}
	// Must be only @ or # followed by non-space chars, no spaces
	return !strings.Contains(t, " ")
}

func (g *Gateway) handleStickyCommand(chatJid string, msg core.Message) bool {
	if msg.BotMsg || strings.HasPrefix(msg.Sender, "scheduler-") {
		return false
	}

	content := strings.TrimSpace(msg.Content)
	if !isStickyCommand(content) {
		return false
	}

	switch {
	case content == "@":
		g.store.SetStickyGroup(chatJid, "")
		g.sendMessage(chatJid, "routing reset to default")
		return true

	case strings.HasPrefix(content, "@"):
		name := content[1:]
		g.mu.RLock()
		_, _, ok := g.groupByFolderLocked(name)
		g.mu.RUnlock()

		if ok {
			g.store.SetStickyGroup(chatJid, name)
			g.sendMessage(chatJid, fmt.Sprintf("routing → %s", name))
		} else {
			g.sendMessage(chatJid, fmt.Sprintf("Failed: group %q not found", name))
		}
		return true

	case content == "#":
		g.store.SetStickyTopic(chatJid, "")
		g.sendMessage(chatJid, "topic reset to default")
		return true

	case strings.HasPrefix(content, "#"):
		topic := content[1:]
		g.store.SetStickyTopic(chatJid, topic)
		g.sendMessage(chatJid, fmt.Sprintf("topic → %s", topic))
		return true
	}

	return false
}

// effectiveTopic returns the sticky topic if set, otherwise the message topic.
func (g *Gateway) effectiveTopic(chatJid, msgTopic string) string {
	stickyTopic := g.store.GetStickyTopic(chatJid)
	if stickyTopic != "" {
		return stickyTopic
	}
	return msgTopic
}

var rePrefixAt = regexp.MustCompile(`@(\w[\w-]*)`)
var rePrefixHash = regexp.MustCompile(`#(\w[\w-]*)`)

func parsePrefix(text string) (name, rest string, ok bool) {
	for _, re := range []*regexp.Regexp{rePrefixAt, rePrefixHash} {
		if m := re.FindStringIndex(text); m != nil {
			full := text[m[0]:m[1]]
			name = full[1:] // strip @ or #
			rest = strings.TrimSpace(strings.Join(strings.Fields(text[:m[0]]+" "+text[m[1]:]), " "))
			return name, rest, true
		}
	}
	return "", "", false
}

func findPrefixRoute(routes []core.Route, msg core.Message) *core.Route {
	hasAt := rePrefixAt.MatchString(msg.Content)
	hasHash := rePrefixHash.MatchString(msg.Content)
	for i := range routes {
		r := &routes[i]
		if r.Type != "prefix" || r.Match == "" {
			continue
		}
		if r.Match == "@" && hasAt {
			return r
		}
		if r.Match == "#" && hasHash {
			return r
		}
	}
	return nil
}

func (g *Gateway) handlePrefixRoute(
	r *core.Route, msg core.Message, group core.Group, chatJid string,
) bool {
	name, stripped, ok := parsePrefix(msg.Content)
	if !ok || name == "" {
		return false
	}
	switch r.Match {
	case "@":
		if strings.Contains(name, "/") {
			slog.Warn("@prefix: name contains slash, rejecting", "name", name)
			return false
		}
		childFolder := r.Target + "/" + name
		g.mu.RLock()
		_, _, exists := g.groupByFolderLocked(childFolder)
		g.mu.RUnlock()
		if !exists {
			slog.Warn("@prefix: child group not found", "child", childFolder)
			return true
		}
		g.delegateToFolder("route", childFolder, stripped, chatJid, 0, nil)
		return true
	case "#":
		topic := "#" + name
		g.queue.EnqueueTask(chatJid,
			fmt.Sprintf("topic-%s-%s-%d", group.Folder, name, time.Now().UnixMilli()),
			func() error {
				out := g.runAgentWithOpts(group, stripped, chatJid, "",
					func(text, status string) {
						if text != "" {
							clean := router.FormatOutbound(text)
							if clean != "" {
								g.sendMessage(chatJid, clean)
							}
						}
					}, false, nil, topic, "")
				if out.Error != "" {
					return fmt.Errorf("topic agent: %s", out.Error)
				}
				return nil
			},
		)
		return true
	}
	return false
}

func (g *Gateway) advanceAgentCursor(chatJid string, msgs []core.Message) {
	if len(msgs) == 0 {
		return
	}
	g.store.SetAgentCursor(chatJid, msgs[len(msgs)-1].Timestamp)
}

func (g *Gateway) emitSystemEvents(group core.Group, chatJid string) {
	folder := group.Folder
	today := time.Now().Format("2006-01-02")

	cursor := g.store.GetAgentCursor(chatJid)
	if !cursor.IsZero() && cursor.Format("2006-01-02") != today {
		g.store.EnqueueSysMsg(folder, "gateway", "new_day",
			fmt.Sprintf("Date changed to %s", today))
	}

	if id, ok := g.store.GetSession(folder, ""); !ok || id == "" {
		g.store.EnqueueSysMsg(folder, "gateway", "new_session", "")
	}
}

func (g *Gateway) delegateToChild(
	childFolder, prompt, originJid string, depth int, rules []string,
) error {
	return g.delegateToFolder("delegate", childFolder, prompt, originJid, depth, rules)
}

func (g *Gateway) delegateToFolder(
	label, folder, prompt, originJid string, depth int, rules []string,
) error {
	if depth > 1 {
		return fmt.Errorf("delegation depth exceeded")
	}

	// Parse escalation metadata if this is an escalation
	var escalation *escalationMetadata
	if label == "escalate" {
		escalation = parseEscalationOrigin(prompt)
		if escalation != nil {
			// Route response to worker's local JID instead of original user
			originJid = "local:" + escalation.WorkerFolder
		}
	}

	g.mu.RLock()
	targetJid, target, found := g.groupByFolderLocked(folder)
	g.mu.RUnlock()
	if !found {
		// attempt prototype spawn when target has a parent prefix
		sep := strings.LastIndex(folder, "/")
		if sep > 0 {
			parentFolder := folder[:sep]
			g.mu.RLock()
			parentJID, _, parentOK := g.groupByFolderLocked(parentFolder)
			g.mu.RUnlock()
			if parentOK {
				spawned, err := g.spawnFromPrototype(parentJID, parentFolder, originJid)
				if err == nil {
					return g.delegateToFolder(label, spawned.Folder, prompt, originJid, depth+1, rules)
				}
			}
		}
		return fmt.Errorf("%s target not found: %s", label, folder)
	}

	g.queue.EnqueueTask(targetJid, fmt.Sprintf("%s-%s-%d",
		label, folder, time.Now().UnixMilli()),
		func() error {
			out := g.runAgentWithOpts(target, prompt, originJid, "",
				func(text, status string) {
					if text != "" {
						clean := router.FormatOutbound(text)
						if clean != "" {
							// Wrap response with escalation metadata if applicable
							if escalation != nil {
								clean = fmt.Sprintf("<escalation_response origin_jid=%q origin_msg_id=%q>\n%s\n</escalation_response>",
									escalation.OriginJID, escalation.ReplyTo, clean)
							}
							if sentID, _ := g.sendMessageReply(originJid, clean, "", ""); sentID != "" {
								g.store.StoreOutbound(core.OutboundEntry{
									ChatJID:       originJid,
									Content:       clean,
									Source:        "agent",
									GroupFolder:   folder,
									PlatformMsgID: sentID,
								})
							}
						}
					}
				}, false, rules, "", "")
			if out.Error != "" {
				return fmt.Errorf("%s agent: %s", label, out.Error)
			}
			return nil
		},
	)
	return nil
}

func (g *Gateway) groupJIDs() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	jids := make([]string, 0, len(g.groups))
	for jid := range g.groups {
		jids = append(jids, jid)
	}
	return jids
}

func (g *Gateway) recoverPendingMessages() {
	for _, jid := range g.groupJIDs() {
		agentTs := g.store.GetAgentCursor(jid)
		msgs, err := g.store.MessagesSince(jid, agentTs, g.cfg.Name)
		if err != nil {
			slog.Error("recovery query failed", "jid", jid, "err", err)
			continue
		}
		if len(msgs) > 0 && !g.store.IsChatErrored(jid) {
			slog.Info("recovering pending messages",
				"jid", jid, "count", len(msgs))
			g.queue.EnqueueMessageCheck(jid)
		}
	}
}

func (g *Gateway) groupList() []core.Group {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]core.Group, 0, len(g.groups))
	for _, gr := range g.groups {
		out = append(out, gr)
	}
	return out
}

func channelName(ch core.Channel) string {
	if ch == nil {
		return ""
	}
	return ch.Name()
}
