package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/onvos/arizuko/auth"
	"github.com/onvos/arizuko/chanlib"
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
	gatedFns ipc.GatedFns
	storeFns ipc.StoreFns

	// lastTimestamp is persisted and queried as RFC3339Nano throughout to
	// preserve nanosecond precision; seconds-only formats risk missed messages.
	lastTimestamp time.Time

	impulse *impulseGate
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

func New(cfg *core.Config, s *store.Store) *Gateway {
	return &Gateway{
		cfg:   cfg,
		store: s,
		queue: queue.New(cfg.MaxContainers, cfg.IpcDir),
		folders: &groupfolder.Resolver{
			GroupsDir: cfg.GroupsDir,
			IpcDir:    cfg.IpcDir,
		},
		impulse: newImpulseGate(),
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
	container.CleanupOrphans(g.cfg.Name, g.cfg.Image)

	g.loadState()

	g.AddChannel(NewLocalChannel(g.store))

	g.gatedFns = ipc.GatedFns{
		SendMessage: func(jid, text string) (string, error) {
			return g.sendMessageReply(jid, text, "", "")
		},
		SendReply: func(jid, text, replyTo string) (string, error) {
			return g.sendMessageReply(jid, text, replyTo, "")
		},
		SendDocument:  g.sendDocument,
		ClearSession:  g.clearSession,
		GroupsDir:     g.cfg.GroupsDir,
		HostGroupsDir: g.cfg.HostGroupsDir,
		WebDir:        g.cfg.WebDir,
		InjectMessage: g.injectMessage,
		RegisterGroup: g.registerGroupIPC,
		SetupGroup: func(folder string) error {
			return container.SetupGroup(g.cfg, folder, "")
		},
		GetGroups:           g.getGroups,
		EnqueueMessageCheck: g.queue.EnqueueMessageCheck,
		SpawnGroup: func(parentFolder, childJID string) (core.Group, error) {
			if _, ok := g.store.GroupByFolder(parentFolder); !ok {
				return core.Group{}, fmt.Errorf("parent group not found: %s", parentFolder)
			}
			return g.spawnFromPrototype(parentFolder, childJID)
		},
	}
	g.storeFns = ipc.StoreFns{
		CreateTask: g.store.CreateTask,
		GetTask:    g.store.GetTask,
		UpdateTaskStatus: func(id, status string) error {
			return g.store.UpdateTask(id, store.TaskPatch{Status: &status})
		},
		DeleteTask:          g.store.DeleteTask,
		ListTasks:           g.store.ListTasks,
		ListRoutes:          g.store.ListRoutes,
		SetRoutes:           g.store.SetRoutes,
		AddRoute:            g.store.AddRoute,
		DeleteRoute:         g.store.DeleteRoute,
		GetRoute:            g.store.GetRoute,
		DefaultFolderForJID: g.store.DefaultFolderForJID,
		GetGrants:           g.store.GetGrants,
		SetGrants:           g.store.SetGrants,
		PutMessage:          g.store.PutMessage,
		GetLastReplyID:      g.store.GetLastReplyID,
		SetLastReplyID:      g.store.SetLastReplyID,
		MessagesBefore:      g.store.MessagesBefore,
		JIDRoutedToFolder:   g.store.JIDRoutedToFolder,
	}
	g.queue.SetProcessMessagesFn(g.processGroupMessages)
	g.queue.SetHasPendingFn(func(jid string) bool {
		if g.store.IsChatErrored(jid) {
			return false
		}
		return g.store.HasPendingMessages(jid, g.cfg.Name)
	})
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
	g.checkMigrationVersion()

	groups := g.store.AllGroups()
	slog.Info("arizuko running",
		"name", g.cfg.Name,
		"groups", len(groups),
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

	groups := g.store.AllGroups()
	routes := g.store.AllRoutes()
	slog.Info("state loaded", "groups", len(groups), "routes", len(routes))
}

func (g *Gateway) saveState() {
	g.mu.RLock()
	defer g.mu.RUnlock()
	g.store.SetState("last_timestamp",
		g.lastTimestamp.Format(time.RFC3339Nano))
}

func (g *Gateway) checkMigrationVersion() {
	// Try the container-side mount first (/srv/app/arizuko is the canonical
	// compose mount point), then fall back to HostAppDir for local dev.
	var latest int
	for _, base := range []string{"/srv/app/arizuko", g.cfg.HostAppDir} {
		latest = container.MigrationVersion(
			filepath.Join(base, "ant", "skills", "self", "MIGRATION_VERSION"))
		if latest > 0 {
			break
		}
	}
	if latest == 0 {
		return
	}
	groups := g.store.AllGroups()
	for _, gr := range groups {
		if !groupfolder.IsRoot(gr.Folder) || gr.Parent != "" {
			continue
		}
		agent := container.MigrationVersion(
			filepath.Join(g.cfg.GroupsDir, gr.Folder, ".claude", "skills", "self", "MIGRATION_VERSION"))
		if agent >= latest {
			continue
		}
		slog.Info("auto-migrate: version behind, triggering /migrate",
			"group", gr.Folder, "agent", agent, "latest", latest)
		prompt := fmt.Sprintf(
			"System update available: v%d → v%d. Run /migrate now.",
			agent, latest)
		g.store.PutMessage(core.Message{
			ID:        core.MsgID("auto-migrate-" + gr.Folder),
			ChatJID:   "local:" + gr.Folder,
			Sender:    "system",
			Content:   prompt,
			Timestamp: time.Now(),
		})
		g.queue.EnqueueMessageCheck("local:" + gr.Folder)

		// Notify child groups directly — don't rely on root agent.
		note := fmt.Sprintf("System update: skills v%d → v%d applied. "+
			"New capabilities may be available — check /self for details.", agent, latest)
		for _, child := range groups {
			if child.Parent != gr.Folder {
				continue
			}
			g.store.PutMessage(core.Message{
				ID:        core.MsgID("auto-migrate-notify-" + child.Folder),
				ChatJID:   "local:" + child.Folder,
				Sender:    "system",
				Content:   note,
				Timestamp: time.Now(),
			})
			slog.Info("auto-migrate: notified child group",
				"child", child.Folder, "parent", gr.Folder)
		}
	}
}

func (g *Gateway) messageLoop(ctx context.Context) {
	for {
		g.pollOnce()
		select {
		case <-ctx.Done():
			return
		case <-time.After(g.cfg.PollInterval):
		}
	}
}

func (g *Gateway) pollOnce() {
	g.mu.RLock()
	since := g.lastTimestamp
	g.mu.RUnlock()

	msgs, hi, err := g.store.NewMessages(nil, since, g.cfg.Name)
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

	routes := g.store.AllRoutes()

	for chatJid, chatMsgs := range byChat {
		group, ok := g.groupForJid(chatJid)
		if !ok {
			if g.cfg.OnboardingEnabled && onboardingAllowed(chatJid, g.cfg.OnboardingPlatforms) {
				if err := g.store.InsertOnboarding(chatJid); err != nil {
					slog.Warn("insert onboarding", "jid", chatJid, "err", err)
				}
			}
			slog.Debug("poll: no group for jid", "jid", chatJid)
			continue
		}

		last := chatMsgs[len(chatMsgs)-1]

		if g.handleStickyCommand(chatJid, last) {
			slog.Debug("poll: handled sticky command", "jid", chatJid)
			continue
		}

		if g.handleCommand(last, group) {
			slog.Debug("poll: handled command", "jid", chatJid, "sender", last.Sender)
			continue
		}

		if g.tryExternalRoute(routes, last, group, chatJid, "poll") {
			g.advanceAgentCursor(chatJid, chatMsgs)
			continue
		}

		if g.cfg.ImpulseEnabled {
			impCfg := ParseImpulseCfg(g.store.GetImpulseConfigJSON(last))
			if !g.impulse.accept(chatJid, chatMsgs, impCfg) {
				slog.Debug("poll: impulse hold", "jid", chatJid)
				continue
			}
		}

		texts := make([]string, len(chatMsgs))
		for i, m := range chatMsgs {
			texts[i] = m.Content
		}
		if g.queue.SendMessages(chatJid, texts) {
			slog.Info("poll: steered messages into running container",
				"jid", chatJid, "count", len(texts))
			g.store.SetLastReplyID(chatJid, g.effectiveTopic(chatJid, last.Topic), last.ID)
			g.store.ClearChatErrored(chatJid)
			g.advanceAgentCursor(chatJid, chatMsgs)
			continue
		}

		slog.Debug("poll: enqueue check", "jid", chatJid)
		g.queue.EnqueueMessageCheck(chatJid)
	}

	if g.cfg.ImpulseEnabled {
		for _, jid := range g.impulse.flush(func(jid string) ImpulseCfg {
			return ParseImpulseCfg(g.store.GetImpulseConfigJSON(core.Message{ChatJID: jid, Verb: "message"}))
		}) {
			slog.Debug("poll: impulse timeout flush", "jid", jid)
			g.queue.EnqueueMessageCheck(jid)
		}
	}

	g.saveState()
}

func (g *Gateway) processGroupMessages(chatJid string) (bool, error) {
	group, ok := g.groupForJid(chatJid)
	if !ok {
		return false, fmt.Errorf("group not registered: %s", chatJid)
	}

	agentTs := g.getAgentCursor(chatJid)

	msgs, err := g.store.MessagesSince(chatJid, agentTs, g.cfg.Name)
	if err != nil {
		return false, fmt.Errorf("query messages: %w", err)
	}
	if len(msgs) == 0 {
		return true, nil
	}

	routes := g.store.AllRoutes()

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
		return g.processWebTopics(group, chatJid, msgs)
	}

	senderBatches := groupBySender(msgs)
	for _, batch := range senderBatches {
		last := batch[len(batch)-1]

		if g.tryExternalRoute(routes, last, group, chatJid, "process") {
			continue
		}

		if !g.processSenderBatch(group, chatJid, batch, agentTs) {
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
	var batches [][]core.Message
	senderIdx := make(map[string]int)

	for _, m := range msgs {
		idx, seen := senderIdx[m.Sender]
		if !seen {
			idx = len(batches)
			senderIdx[m.Sender] = idx
			batches = append(batches, nil)
		}
		batches[idx] = append(batches[idx], m)
	}
	return batches
}

// tryExternalRoute runs the in-code prefix layer (inline @agent / #topic),
// then the data-driven routing layer (routes table). Returns true if the
// message was absorbed, meaning the caller should skip further processing.
func (g *Gateway) tryExternalRoute(
	routes []core.Route, msg core.Message, group core.Group, chatJid, phase string,
) bool {
	if g.handlePrefixLayer(msg, group, chatJid) {
		slog.Debug(phase+": routed via prefix layer",
			"jid", chatJid, "sender", msg.Sender)
		return true
	}

	target := g.resolveTarget(msg, routes, group.Folder)
	if target != "" && router.IsAuthorizedRoutingTarget(group.Folder, target) {
		slog.Debug(phase+": delegating to child",
			"jid", chatJid, "sender", msg.Sender, "target", target)
		g.delegateViaMessage(target, msg.Content, chatJid, 0)
		return true
	}
	return false
}

// logAgentError writes the error to slog and to the group's recovery diary.
func (g *Gateway) logAgentError(group core.Group, key, value, errStr string) {
	slog.Error("agent error", "group", group.Folder, key, value, "err", errStr)
	if gp, err := g.folders.GroupPath(group.Folder); err == nil {
		diary.WriteRecovery(gp, "error", errStr)
	}
}

func (g *Gateway) processSenderBatch(
	group core.Group, chatJid string, msgs []core.Message, agentTs time.Time,
) bool {
	last := msgs[len(msgs)-1]

	for i := range msgs {
		g.enrichAttachments(&msgs[i], group.Folder)
	}

	// Determine delivery target: forwarded_from overrides chatJid (delegation return path)
	deliverTo := chatJid
	deliverCh := g.findChannelByName(last.Source)
	if deliverCh == nil {
		deliverCh = g.findChannelForJID(chatJid)
	}
	if last.ForwardedFrom != "" {
		deliverTo = last.ForwardedFrom
		deliverCh = g.findChannelForJID(deliverTo)
	}

	g.emitSystemEvents(group, chatJid)
	sysMsgs := g.store.FlushSysMsgs(group.Folder)
	observed := g.store.ObservedMessagesSince(group.Folder, chatJid, agentTs.Format(time.RFC3339Nano))
	prompt := sysMsgs + router.ClockXml(g.cfg.Timezone) + "\n" + router.FormatMessages(msgs, observed)

	if deliverCh != nil {
		slog.Info("typing start", "jid", deliverTo, "channel", deliverCh.Name())
		deliverCh.Typing(deliverTo, true)
	} else {
		slog.Warn("typing skip: no channel", "jid", deliverTo)
	}

	isolated := strings.HasPrefix(last.Sender, "timed-isolated")
	topic := g.effectiveTopic(chatJid, last.Topic)
	onOutput, hadOutput := g.makeOutputCallback(deliverCh, deliverTo, topic, last.ID, group.Folder)
	out := g.runAgentWithOpts(group, prompt, chatJid, last.Sender,
		onOutput, isolated, nil, topic, last.ID, len(msgs))

	if deliverCh != nil {
		slog.Info("typing stop", "jid", deliverTo, "channel", deliverCh.Name())
		deliverCh.Typing(deliverTo, false)
	}

	if out.Error != "" {
		g.logAgentError(group, "sender", last.Sender, out.Error)
		if *hadOutput {
			return true
		}
		g.store.SetAgentCursor(chatJid, agentTs)
		g.sendMessage(deliverTo, "Failed: agent error, will retry on next message.")
		g.store.MarkChatErrored(chatJid)
		return false
	}

	if !*hadOutput {
		slog.Warn("agent completed with no output delivered",
			"jid", deliverTo, "group", group.Folder, "sender", last.Sender)
	}
	return true
}

func (g *Gateway) processWebTopics(
	group core.Group, chatJid string, msgs []core.Message,
) (bool, error) {
	ch := g.findChannelForJID(chatJid)
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
		prompt := sysMsgs + router.ClockXml(g.cfg.Timezone) + "\n" + router.FormatMessages(topicMsgs)

		if ch != nil {
			ch.Typing(chatJid, true)
		}

		effectiveTopic := g.effectiveTopic(chatJid, topic)
		onOutput, hadOutput := g.makeOutputCallback(ch, chatJid, effectiveTopic, last.ID, group.Folder)
		out := g.runAgentWithOpts(group, prompt, chatJid, last.Sender,
			onOutput, false, nil, effectiveTopic, last.ID, len(topicMsgs))

		if ch != nil {
			ch.Typing(chatJid, false)
		}

		if out.Error != "" {
			g.logAgentError(group, "topic", topic, out.Error)
			if *hadOutput {
				g.advanceAgentCursor(chatJid, topicMsgs)
				return true, nil
			}
			g.sendMessage(chatJid, "Failed: agent error, will retry on next message.")
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

func (g *Gateway) makeOutputCallback(ch core.Channel, chatJid, topic, firstMsgID, groupFolder string) (func(string, string), *bool) {
	var hadOutput bool
	replyTo := firstMsgID

	sendOnce := func(text, replyToID, threadID string) (string, error) {
		if !g.canSendToJID(chatJid) {
			return "", nil
		}
		// Late-bind the channel: the adapter may have registered AFTER
		// this callback was built (startup race, adapter restart,
		// health-flap). Without this, a nil capture persists for the
		// lifetime of the run and every send silently fails with
		// "no channel for jid" even once the channel is live.
		c := ch
		if c == nil {
			c = g.findChannelForJID(chatJid)
		}
		if c == nil {
			return "", fmt.Errorf("no channel for jid %s", chatJid)
		}
		return c.Send(chatJid, text, replyToID, threadID)
	}

	return func(text, _ string) {
		if text == "" {
			return
		}
		hadOutput = true
		if !g.canSendToGroup(groupFolder) {
			slog.Debug("send suppressed by SEND_DISABLED_GROUPS", "group", groupFolder)
			return
		}
		stripped, statuses := router.ExtractStatusBlocks(router.StripThinkBlocks(text))
		for _, s := range statuses {
			sentID, err := sendOnce("⏳ "+s, "", "")
			if err != nil {
				slog.Error("send status failed",
					"jid", chatJid, "group", groupFolder, "err", err)
			}
			g.store.PutMessage(core.Message{
				ID:        core.MsgID("out"),
				ChatJID:   chatJid,
				Sender:    groupFolder,
				Content:   s,
				Timestamp: time.Now(),
				FromMe:    true,
				BotMsg:    true,
				RoutedTo:  chatJid,
				Topic:     topic,
				ReplyToID: sentID,
			})
		}
		if clean := router.FormatOutbound(stripped); clean != "" {
			sentID, err := sendOnce(clean, replyTo, topic)
			if err != nil {
				slog.Error("send reply failed",
					"jid", chatJid, "group", groupFolder, "err", err)
			}
			if sentID != "" {
				replyTo = sentID
				g.store.SetLastReplyID(chatJid, topic, sentID)
			}
			g.store.PutMessage(core.Message{
				ID:        core.MsgID("out"),
				ChatJID:   chatJid,
				Sender:    groupFolder,
				Content:   clean,
				Timestamp: time.Now(),
				FromMe:    true,
				BotMsg:    true,
				ReplyToID: sentID,
				RoutedTo:  chatJid,
				Topic:     topic,
			})
		}
	}, &hadOutput
}

const sessionIdleExpiry = 2 * 24 * time.Hour

func (g *Gateway) sessionIdleExpired(chatJid string) bool {
	cursor := g.store.GetAgentCursor(chatJid)
	if cursor.IsZero() {
		return false
	}
	return time.Since(cursor) > sessionIdleExpiry
}

func (g *Gateway) runAgentWithOpts(
	group core.Group, prompt, chatJid, sender string,
	onOutput func(string, string), isolated bool,
	rules []string, topic string, msgID string, msgCount int,
) container.Output {
	var sessionID string
	var logRowID int64
	if !isolated {
		sessionID, _ = g.store.GetSession(group.Folder, topic)
		if sessionID != "" && g.sessionIdleExpired(chatJid) {
			slog.Info("session: resetting on idle expiry",
				"group", group.Folder, "jid", chatJid,
				"threshold", sessionIdleExpiry)
			g.store.DeleteSession(group.Folder, topic)
			sessionID = ""
		}
		logRowID, _ = g.store.RecordSession(group.Folder, sessionID, time.Now())
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
	if taskID := strings.TrimPrefix(sender, "timed-isolated:"); taskID != sender {
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
		Prompt:     prompt,
		SessionID:  sessionID,
		ChatJID:    chatJid,
		Folder:     group.Folder,
		Topic:      topic,
		GroupPath:  groupPath,
		Name:       cname,
		Config:     group.Config,
		SlinkToken: group.SlinkToken,
		Channel:    channelName(g.findChannelForJID(chatJid)),
		MessageID:  msgID,
		Sender:     sender,
		OnOutput:   onOutput,
		Grants:     rules,
		GatedFns:   g.gatedFns,
		StoreFns:   g.storeFns,
	}

	out := container.Run(g.cfg, g.folders, input)

	if isolated {
		return out
	}

	result := "ok"
	if out.Error != "" {
		result = "error"
		if strings.Contains(out.Error, "timed out") {
			result = "timeout"
		}
	}
	effectiveSID := out.NewSessionID
	if effectiveSID == "" {
		effectiveSID = sessionID
	}
	if logRowID > 0 {
		if err := g.store.EndSession(logRowID, effectiveSID, result, out.Error, msgCount); err != nil {
			slog.Warn("end session log failed", "group", group.Folder, "err", err)
		}
	}

	if out.NewSessionID != "" {
		g.store.SetSession(group.Folder, topic, out.NewSessionID)
	} else if out.Error != "" && !out.HadOutput {
		g.store.DeleteSession(group.Folder, topic)
	}

	return out
}

// findChannelByName returns the channel registered under name, or nil.
func (g *Gateway) findChannelByName(name string) core.Channel {
	if name == "" {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, ch := range g.channels {
		if ch.Name() == name {
			return ch
		}
	}
	return nil
}

// findChannelForJID picks an outbound channel for jid by looking up the
// most recent inbound message's source. Falls back to a prefix match if
// no source is recorded (e.g. local: / web: JIDs that bypass adapters).
func (g *Gateway) findChannelForJID(jid string) core.Channel {
	if ch := g.findChannelByName(g.store.LatestSource(jid)); ch != nil {
		return ch
	}
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

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func (g *Gateway) canSendToJID(jid string) bool {
	if len(g.cfg.SendDisabledChannels) == 0 {
		return true
	}
	prefix, _, _ := strings.Cut(jid, ":")
	return !containsFold(g.cfg.SendDisabledChannels, prefix)
}

func (g *Gateway) canSendToGroup(folder string) bool {
	return !containsFold(g.cfg.SendDisabledGroups, folder)
}

func (g *Gateway) sendMessageReply(jid, text, replyTo, threadID string) (string, error) {
	if !g.canSendToJID(jid) {
		slog.Debug("send suppressed by SEND_DISABLED_CHANNELS", "jid", jid)
		return "", nil
	}
	ch := g.findChannelForJID(jid)
	if ch == nil {
		return "", fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Send(jid, text, replyTo, threadID)
}

func (g *Gateway) sendDocument(jid, path, name, caption string) error {
	ch := g.findChannelForJID(jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.SendFile(jid, path, name, caption)
}

func (g *Gateway) clearSession(folder string) {
	g.store.DeleteSession(folder, "")
}

func (g *Gateway) injectMessage(jid, content, sender, senderName string) (string, error) {
	id := core.MsgID("inject")
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

func (g *Gateway) enrichAttachments(msg *core.Message, folder string) {
	if !g.cfg.MediaEnabled || msg.Attachments == "" {
		return
	}
	var atts []chanlib.InboundAttachment
	if err := json.Unmarshal([]byte(msg.Attachments), &atts); err != nil || len(atts) == 0 {
		return
	}

	groupPath, err := g.folders.GroupPath(folder)
	if err != nil {
		slog.Warn("enrich: group path", "folder", folder, "err", err)
		return
	}
	day := time.Now().Format("20060102")
	mediaDir := groupfolder.GroupMediaDir(groupPath, day)
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		slog.Warn("enrich: mkdir", "dir", mediaDir, "err", err)
		return
	}

	langs := readWhisperLanguages(groupPath)
	extra := ""
	for i, att := range atts {
		ext := extFromMime(att.Mime, att.Filename)
		fname := sanitizeFilename(att.Filename)
		if fname == "" {
			fname = fmt.Sprintf("%s-%d%s", msg.ID, i, ext)
		}
		dest := filepath.Join(mediaDir, fname)
		if _, err := os.Stat(dest); err == nil {
			fname = fmt.Sprintf("%s-%d%s", msg.ID, i, ext)
			dest = filepath.Join(mediaDir, fname)
		}

		if att.URL == "" {
			if att.Data == "" {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(att.Data)
			if err != nil {
				slog.Warn("enrich: base64 decode", "err", err)
				continue
			}
			if err := os.WriteFile(dest, raw, 0o644); err != nil {
				slog.Warn("enrich: write base64", "dest", dest, "err", err)
				continue
			}
		} else {
			if err := downloadFile(att.URL, dest, g.cfg.ChannelSecret, g.cfg.MediaMaxBytes); err != nil {
				slog.Warn("enrich: download failed", "url", att.URL, "err", err)
				continue
			}
		}

		displayName := att.Filename
		if displayName == "" {
			displayName = fname
		}
		transcript := ""
		if g.cfg.VoiceEnabled && g.cfg.WhisperURL != "" {
			if strings.HasPrefix(att.Mime, "audio/") {
				transcript = whisperTranscribe(g.cfg.WhisperURL, g.cfg.WhisperModel, dest, langs)
			} else if strings.HasPrefix(att.Mime, "video/") {
				if audioPath := extractVideoAudio(dest); audioPath != "" {
					transcript = whisperTranscribe(g.cfg.WhisperURL, g.cfg.WhisperModel, audioPath, langs)
					os.Remove(audioPath)
				}
			}
		}
		containerPath := "/home/node/media/" + day + "/" + fname
		if transcript != "" {
			extra += fmt.Sprintf("\n<attachment path=%q mime=%q filename=%q transcript=%q/>",
				containerPath, att.Mime, displayName, transcript)
		} else {
			extra += fmt.Sprintf("\n<attachment path=%q mime=%q filename=%q/>",
				containerPath, att.Mime, displayName)
		}
	}

	if extra == "" {
		return
	}

	msg.Content += extra
	msg.Attachments = ""
	if err := g.store.EnrichMessage(msg.ID, msg.Content); err != nil {
		slog.Warn("enrich: store update failed", "id", msg.ID, "err", err)
	}
}

func downloadFile(url, dest, secret string, maxBytes int64) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	src := io.Reader(resp.Body)
	if maxBytes > 0 {
		src = io.LimitReader(resp.Body, maxBytes)
	}
	_, cpErr := io.Copy(f, src)
	if closeErr := f.Close(); cpErr == nil {
		cpErr = closeErr
	}
	if cpErr != nil {
		os.Remove(dest)
	}
	return cpErr
}

func whisperTranscribe(baseURL, model, path string, langs []string) string {
	if len(langs) == 0 {
		langs = []string{""}
	}
	var results []string
	for _, lang := range langs {
		if t := transcribeOnce(baseURL, model, path, lang); t != "" {
			results = append(results, t)
		}
	}
	return strings.Join(results, "\n")
}

func transcribeOnce(baseURL, model, path, lang string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	url := baseURL + "/v1/audio/transcriptions"
	req, err := http.NewRequest("POST", url, f)
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "audio/ogg")
	q := req.URL.Query()
	q.Set("model", model)
	if lang != "" {
		q.Set("language", lang)
	}
	req.URL.RawQuery = q.Encode()
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return ""
	}
	defer resp.Body.Close()
	var out struct {
		Text string `json:"text"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return strings.TrimSpace(out.Text)
}

func readWhisperLanguages(groupPath string) []string {
	data, err := os.ReadFile(filepath.Join(groupPath, ".whisper-language"))
	if err != nil {
		return nil
	}
	var langs []string
	for _, line := range strings.Split(string(data), "\n") {
		if l := strings.TrimSpace(line); l != "" {
			langs = append(langs, l)
		}
	}
	return langs
}

func extractVideoAudio(videoPath string) string {
	audioPath := strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + "-audio.mp3"
	cmd := exec.Command("ffmpeg", "-y", "-i", videoPath, "-vn", "-acodec", "libmp3lame", "-q:a", "4", audioPath)
	if err := cmd.Run(); err != nil {
		return ""
	}
	return audioPath
}

func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	if name == "." || name == "/" {
		return ""
	}
	// Strip characters unsafe for filesystems
	var b strings.Builder
	for _, r := range name {
		if r == '/' || r == '\\' || r == '\x00' {
			continue
		}
		b.WriteRune(r)
	}
	s := b.String()
	if len(s) > 200 {
		ext := filepath.Ext(s)
		s = s[:200-len(ext)] + ext
	}
	return s
}

// preferredExts pins canonical extensions so agents see file types Claude's
// Read tool recognizes. Without this, `mime.ExtensionsByType("image/jpeg")`
// picks `.jfif` or `.jpe` depending on the host's /etc/mime.types, and Claude
// can't natively load those.
var preferredExts = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
	"audio/ogg":  ".ogg",
	"audio/mpeg": ".mp3",
	"audio/mp4":  ".m4a",
	"video/mp4":  ".mp4",
}

func extFromMime(mimeType, filename string) string {
	if filename != "" {
		if ext := filepath.Ext(filename); ext != "" {
			return strings.ToLower(ext)
		}
	}
	if ext, ok := preferredExts[mimeType]; ok {
		return ext
	}
	exts, _ := mime.ExtensionsByType(mimeType)
	if len(exts) > 0 {
		return exts[0]
	}
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return "." + strings.TrimPrefix(mimeType, "image/")
	case strings.HasPrefix(mimeType, "video/"):
		return "." + strings.TrimPrefix(mimeType, "video/")
	case strings.HasPrefix(mimeType, "audio/"):
		return ".mp3"
	}
	return ".bin"
}

func (g *Gateway) registerGroupIPC(jid string, group core.Group) error {
	if err := g.store.PutGroup(group); err != nil {
		return err
	}
	// Default route: match the jid's post-colon room literal (or the full
	// string when no platform prefix), target the group folder.
	match := "room=" + core.JidRoom(jid)
	g.store.AddRoute(core.Route{Seq: 0, Match: match, Target: group.Folder})
	ensureGroupGitRepo(filepath.Join(g.cfg.GroupsDir, group.Folder))
	return nil
}

func ensureGroupGitRepo(groupDir string) {
	gitDir := filepath.Join(groupDir, ".git")
	if _, err := os.Stat(gitDir); err == nil {
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
	return g.store.AllGroups()
}

type escalationMetadata struct {
	WorkerFolder string
	OriginJID    string
	ReplyTo      string
}

var escAttrRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

func parseEscalationOrigin(prompt string) *escalationMetadata {
	start := strings.Index(prompt, "<escalation_origin")
	if start == -1 {
		return nil
	}
	end := strings.Index(prompt[start:], "/>")
	if end == -1 {
		return nil
	}
	tag := prompt[start : start+end+2]

	attrs := map[string]string{}
	for _, m := range escAttrRe.FindAllStringSubmatch(tag, -1) {
		attrs[m[1]] = m[2]
	}

	meta := escalationMetadata{
		WorkerFolder: attrs["folder"],
		OriginJID:    attrs["jid"],
		ReplyTo:      attrs["reply_to"],
	}
	if meta.WorkerFolder == "" || meta.OriginJID == "" {
		return nil
	}
	return &meta
}

func (g *Gateway) groupForJid(jid string) (core.Group, bool) {
	for _, prefix := range []string{"local:", "web:"} {
		if folder, ok := strings.CutPrefix(jid, prefix); ok {
			return g.store.GroupByFolder(folder)
		}
	}
	if folder := g.store.DefaultFolderForJID(jid); folder != "" {
		return g.store.GroupByFolder(folder)
	}
	return core.Group{}, false
}

func (g *Gateway) resolveTarget(msg core.Message, routes []core.Route, selfFolder string) string {
	if msg.ReplyToID != "" {
		routedTo := g.store.RoutedToByMessageID(msg.ReplyToID)
		if routedTo != "" {
			if routedTo != selfFolder {
				return routedTo
			}
			return ""
		}
	}

	stickyGroup := g.store.GetStickyGroup(msg.ChatJID)
	if stickyGroup != "" {
		if stickyGroup != selfFolder {
			return stickyGroup
		}
		return ""
	}

	if len(routes) == 0 {
		return ""
	}
	t := router.ResolveRoute(msg, routes)
	if t != "" && t != selfFolder {
		return t
	}
	return ""
}

func isStickyCommand(content string) bool {
	t := strings.TrimSpace(content)
	if len(t) == 0 || (t[0] != '@' && t[0] != '#') {
		return false
	}
	return !strings.ContainsAny(t, " \n")
}

func (g *Gateway) handleStickyCommand(chatJid string, msg core.Message) bool {
	if msg.BotMsg || strings.HasPrefix(msg.Sender, "timed-") {
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
		_, ok := g.store.GroupByFolder(name)

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

func (g *Gateway) effectiveTopic(chatJid, msgTopic string) string {
	stickyTopic := g.store.GetStickyTopic(chatJid)
	if stickyTopic != "" {
		return stickyTopic
	}
	return msgTopic
}

// Navigation prefixes must sit at the very start of the message
// (optional leading whitespace). Mid-content @mentions and #tags are
// references, not nav commands — e.g. forwarded tweets containing
// "@handle" must not be misrouted as child-group delegations.
var rePrefixAt = regexp.MustCompile(`^\s*@(\w[\w-]*)`)
var rePrefixHash = regexp.MustCompile(`^\s*#(\w[\w-]*)`)

func parsePrefix(text string) (name, rest string, ok bool) {
	for _, re := range []*regexp.Regexp{rePrefixAt, rePrefixHash} {
		// Submatch index 2..3 is the captured name (without leading
		// whitespace or the @/# sigil). Whole-match 0..1 is consumed
		// wholesale when computing the remainder.
		m := re.FindStringSubmatchIndex(text)
		if m == nil {
			continue
		}
		name = text[m[2]:m[3]]
		rest = strings.TrimSpace(text[m[1]:])
		return name, rest, true
	}
	return "", "", false
}

// handlePrefixLayer runs the inline prefix navigation layer. Parses an
// inline @name or #name token from msg.Content, delegates to child
// or enqueues a topic-scoped message. Does not read the routes table.
// Returns true if the prefix was consumed.
func (g *Gateway) handlePrefixLayer(
	msg core.Message, group core.Group, chatJid string,
) bool {
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
		childFolder := group.Folder + "/" + name
		if _, exists := g.store.GroupByFolder(childFolder); !exists {
			// Defence in depth: even though the regex is anchored to
			// start-of-message, if a nav prefix references an unknown
			// child we must NOT swallow the message — fall through so
			// the agent still gets to reply.
			slog.Warn("@prefix: child group not found", "child", childFolder)
			return false
		}
		g.delegateViaMessage(childFolder, stripped, chatJid, 0)
		return true
	}
	// hash prefix
	topic := "#" + name
	g.store.PutMessage(core.Message{
		ID:        core.MsgID("topic"),
		ChatJID:   chatJid,
		Sender:    msg.Sender,
		Name:      msg.Name,
		Content:   stripped,
		Topic:     topic,
		Timestamp: time.Now(),
	})
	g.queue.EnqueueMessageCheck(chatJid)
	return true
}

func (g *Gateway) advanceAgentCursor(chatJid string, msgs []core.Message) {
	if len(msgs) == 0 {
		return
	}
	g.store.SetAgentCursor(chatJid, msgs[len(msgs)-1].Timestamp)
}

func (g *Gateway) getAgentCursor(chatJid string) time.Time {
	return g.store.GetAgentCursor(chatJid)
}

func previousSessionXML(sessions []core.SessionRecord) string {
	if len(sessions) == 0 {
		return ""
	}
	s := sessions[0]
	result, ended := s.Result, ""
	if result == "" {
		result = "unknown"
	}
	if s.EndedAt != nil {
		ended = s.EndedAt.Format("2006-01-02T15:04:05Z07:00")
	}
	sid := s.SessionID
	if len(sid) > 8 {
		sid = sid[:8]
	}
	return fmt.Sprintf(
		`<previous_session id=%q started=%q ended=%q msgs="%d" result=%q/>`,
		sid, s.StartedAt.Format("2006-01-02T15:04:05Z07:00"), ended, s.MsgCount, result)
}

func (g *Gateway) emitSystemEvents(group core.Group, chatJid string) {
	folder := group.Folder
	today := time.Now().Format("2006-01-02")

	cursor := g.getAgentCursor(chatJid)
	if !cursor.IsZero() && cursor.Format("2006-01-02") != today {
		g.store.EnqueueSysMsg(folder, "gateway", "new_day",
			fmt.Sprintf("Date changed to %s", today))
	}

	if id, ok := g.store.GetSession(folder, ""); !ok || id == "" {
		body := previousSessionXML(g.store.RecentSessions(folder, 1))
		g.store.EnqueueSysMsg(folder, "gateway", "new_session", body)
	}
}

// delegateViaMessage writes a delegation message to the target group and
// triggers the queue. The child agent's output will be routed back to
// forwardedFrom (the return address carried via the message).
func (g *Gateway) delegateViaMessage(
	targetFolder, prompt, originJid string, depth int,
) error {
	if depth > 1 {
		return fmt.Errorf("delegation depth exceeded")
	}

	// For escalation, override return address to go back to the child
	fwdFrom := originJid
	if esc := parseEscalationOrigin(prompt); esc != nil && esc.WorkerFolder != "" {
		fwdFrom = "local:" + esc.WorkerFolder
	}

	if _, found := g.store.GroupByFolder(targetFolder); !found {
		sep := strings.LastIndex(targetFolder, "/")
		if sep > 0 {
			parentFolder := targetFolder[:sep]
			if _, parentOK := g.store.GroupByFolder(parentFolder); parentOK {
				spawned, err := g.spawnFromPrototype(parentFolder, originJid)
				if err == nil {
					return g.delegateViaMessage(spawned.Folder, prompt, originJid, depth+1)
				}
			}
		}
		return fmt.Errorf("delegate target not found: %s", targetFolder)
	}

	g.store.PutMessage(core.Message{
		ID:            core.MsgID("delegate"),
		ChatJID:       "local:" + targetFolder,
		Sender:        "delegate",
		Content:       prompt,
		Timestamp:     time.Now(),
		ForwardedFrom: fwdFrom,
	})
	g.queue.EnqueueMessageCheck("local:" + targetFolder)
	return nil
}

func (g *Gateway) recoverPendingMessages() {
	jids := make(map[string]struct{})
	for _, r := range g.store.AllRoutes() {
		room := extractRoom(r.Match)
		if room == "" {
			continue
		}
		for _, pfx := range []string{
			"telegram:", "discord:", "mastodon:", "bluesky:",
			"whatsapp:", "reddit:", "email:",
		} {
			jid := pfx + room
			if _, ok := g.groupForJid(jid); ok {
				jids[jid] = struct{}{}
			}
		}
	}
	for _, gr := range g.store.AllGroups() {
		jids["local:"+gr.Folder] = struct{}{}
	}
	for jid := range jids {
		if g.store.IsChatErrored(jid) {
			continue
		}
		if !g.store.HasPendingMessages(jid, g.cfg.Name) {
			continue
		}
		slog.Info("recovering pending messages", "jid", jid)
		g.queue.EnqueueMessageCheck(jid)
	}
}

func extractRoom(match string) string {
	for _, f := range strings.Fields(match) {
		k, v, ok := strings.Cut(f, "=")
		if ok && k == "room" {
			return v
		}
	}
	return ""
}

func (g *Gateway) groupList() []core.Group {
	groups := g.store.AllGroups()
	out := make([]core.Group, 0, len(groups))
	for _, gr := range groups {
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

func onboardingAllowed(jid string, platforms []string) bool {
	if len(platforms) == 0 {
		return true
	}
	p, _, _ := strings.Cut(jid, ":")
	for _, allowed := range platforms {
		if allowed == p {
			return true
		}
	}
	return false
}
