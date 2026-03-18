package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/onvos/arizuko/auth"
	"github.com/onvos/arizuko/container"
	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/diary"
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

	lastTimestamp time.Time
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
		groups: make(map[string]core.Group),
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

	g.gatedFns = ipc.GatedFns{
		SendMessage:      g.sendMessage,
		SendDocument:     g.sendDocument,
		ClearSession:     g.clearSession,
		GroupsDir:        g.cfg.GroupsDir,
		HostGroupsDir:    g.cfg.HostGroupsDir,
		InjectMessage:    g.injectMessage,
		RegisterGroup:    g.registerGroupIPC,
		GetGroups:        g.getGroups,
		DelegateToChild:  g.delegateToChild,
		DelegateToParent: g.delegateToParent,
	}
	g.storeFns = ipc.StoreFns{
		CreateTask: g.store.CreateTask,
		GetTask:    g.store.GetTask,
		UpdateTaskStatus: func(id, status string) error {
			return g.store.UpdateTask(id, store.TaskPatch{Status: &status})
		},
		DeleteTask:  g.store.DeleteTask,
		ListTasks:   g.store.ListTasks,
		GetRoutes:   g.store.GetRoutes,
		SetRoutes:   g.store.SetRoutes,
		AddRoute:    g.store.AddRoute,
		DeleteRoute: g.store.DeleteRoute,
		GetRoute:    g.store.GetRoute,
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

	if g.cfg.WebPort > 0 {
		go g.serveWeb(ctx)
	}

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

	if len(jids) == 0 {
		return
	}

	msgs, hi, err := g.store.NewMessages(jids, since, g.cfg.Name)
	if err != nil {
		slog.Error("error in message loop", "err", err)
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
			continue
		}

		routes := g.store.GetRoutes(chatJid)
		if routesNeedTrigger(routes) && !g.checkTrigger(chatMsgs) {
			continue
		}

		last := chatMsgs[len(chatMsgs)-1]
		if g.handleCommand(last, group) {
			continue
		}

		routingTarget := resolveTarget(last, routes, group.Folder)

		if routingTarget != "" {
			if router.IsAuthorizedRoutingTarget(group.Folder, routingTarget) {
				g.delegateToChild(routingTarget, last.Content, chatJid, 0)
				continue
			}
		}

		if g.queue.SendMessage(chatJid, last.Content) {
			g.store.ClearChatErrored(chatJid)
			continue
		}

		g.queue.EnqueueMessageCheck(chatJid)
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
		return false, nil
	}

	routes := g.store.GetRoutes(chatJid)
	if routesNeedTrigger(routes) && !g.checkTrigger(msgs) {
		return false, nil
	}

	// Filter out gateway commands — they are handled by the message loop.
	// Advance cursor past command-only batches to avoid double-processing.
	agentMsgs := make([]core.Message, 0, len(msgs))
	for _, m := range msgs {
		if !isGatewayCommand(m.Content) {
			agentMsgs = append(agentMsgs, m)
		}
	}
	if len(agentMsgs) == 0 {
		g.advanceAgentCursor(chatJid, msgs)
		return true, nil
	}

	last := agentMsgs[len(agentMsgs)-1]
	if g.handleCommand(last, group) {
		g.advanceAgentCursor(chatJid, msgs)
		return true, nil
	}

	msgs = agentMsgs

	routingTarget := resolveTarget(last, routes, group.Folder)

	if routingTarget != "" {
		if router.IsAuthorizedRoutingTarget(group.Folder, routingTarget) {
			g.delegateToChild(routingTarget, last.Content, chatJid, 0)
			g.advanceAgentCursor(chatJid, msgs)
			return true, nil
		}
	}

	g.emitSystemEvents(group, chatJid)

	sysMsgs := g.store.FlushSysMsgs(group.Folder)

	var userCtx string
	if last.Sender != "" {
		if gp, err := g.folders.GroupPath(group.Folder); err == nil {
			userCtx = router.UserContextXml(last.Sender, gp)
		}
	}

	prompt := sysMsgs + router.ClockXml(g.cfg.Timezone) + "\n"
	if userCtx != "" {
		prompt += userCtx + "\n"
	}
	prompt += router.FormatMessages(msgs)

	if ch != nil {
		ch.Typing(chatJid, true)
	}

	var hadOutput bool
	savedTs := agentTs

	out := g.runAgentWithOpts(group, prompt, chatJid,
		func(text, status string) {
			if text != "" {
				hadOutput = true
				stripped, statuses := router.ExtractStatusBlocks(text)
				for _, s := range statuses {
					g.sendMessage(chatJid, s)
				}
				clean := router.FormatOutbound(stripped)
				if clean != "" {
					g.sendMessage(chatJid, clean)
				}
			}
		}, false, last.ID)

	if ch != nil {
		ch.Typing(chatJid, false)
	}

	if out.Error != "" {
		slog.Error("agent error",
			"group", group.Folder, "err", out.Error)
		if gp, err := g.folders.GroupPath(group.Folder); err == nil {
			diary.WriteRecovery(gp, "error", out.Error)
		}
		if hadOutput {
			g.advanceAgentCursor(chatJid, msgs)
		} else {
			g.store.SetAgentCursor(chatJid, savedTs)
			g.sendMessage(chatJid,
				"Failed: agent error, will retry on next message.")
			g.store.MarkChatErrored(chatJid)
		}
		return false, fmt.Errorf("agent: %s", out.Error)
	}

	g.advanceAgentCursor(chatJid, msgs)
	g.store.ClearChatErrored(chatJid)
	return true, nil
}

func (g *Gateway) runAgentWithOpts(
	group core.Group, prompt, chatJid string,
	onOutput func(string, string), isolated bool, msgID ...string,
) container.Output {
	var sessionID string
	if !isolated {
		sessionID = g.store.GetSession(group.Folder)
	}

	groupPath, err := g.folders.GroupPath(group.Folder)
	if err != nil {
		return container.Output{Error: err.Error()}
	}

	ts := time.Now().UnixMilli()
	sanitized := container.SanitizeFolder(group.Folder)
	cname := fmt.Sprintf("arizuko-%s-%d", sanitized, ts)

	g.queue.RegisterProcess(chatJid, cname, group.Folder)

	isRoot := g.cfg.IsRoot(group.Folder)
	container.WriteTasksSnapshot(
		g.folders, group.Folder, isRoot, g.store.ListTasks("", true))
	container.WriteGroupsSnapshot(
		g.folders, group.Folder, isRoot, g.groupList())

	var annotations []string
	if d := diary.Read(groupPath, 2); d != "" {
		annotations = append(annotations, d)
	}

	var mid string
	if len(msgID) > 0 {
		mid = msgID[0]
	}

	input := container.Input{
		Prompt:      prompt,
		SessionID:   sessionID,
		ChatJID:     chatJid,
		Folder:      group.Folder,
		GroupPath:   groupPath,
		Name:        cname,
		Config:      group.Config,
		SlinkToken:  group.SlinkToken,
		Channel:     channelName(g.findChannel(chatJid)),
		MessageID:   mid,
		Annotations: annotations,
		OnOutput:    onOutput,
		GatedFns: g.gatedFns,
		StoreFns: g.storeFns,
	}

	out := container.Run(g.cfg, g.folders, input)

	if isolated {
		return out
	}

	if out.NewSessionID != "" {
		g.store.SetSession(group.Folder, out.NewSessionID)
		if sessionID == "" {
			g.store.RecordSession(group.Folder, out.NewSessionID)
		}
	} else if out.Error != "" && !out.HadOutput {
		g.store.DeleteSession(group.Folder)
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
	ch := g.findChannel(jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Send(jid, text)
}

func (g *Gateway) sendDocument(jid, path, name string) error {
	ch := g.findChannel(jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.SendFile(jid, path, name)
}

func (g *Gateway) clearSession(folder string) {
	g.store.DeleteSession(folder)
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
	return g.store.PutGroup(jid, group)
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

func (g *Gateway) groupByFolder(folder string) (string, core.Group, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.groupByFolderLocked(folder)
}

func (g *Gateway) delegateToParent(parentFolder, prompt, originJid string, depth int) error {
	return g.delegateToFolder("escalate", parentFolder, prompt, originJid, depth)
}

func routesNeedTrigger(routes []core.Route) bool {
	for _, r := range routes {
		if r.Type == "trigger" {
			return true
		}
	}
	return false
}

func (g *Gateway) groupForJid(jid string) (core.Group, bool) {
	if gr, ok := g.groups[jid]; ok {
		return gr, true
	}
	if strings.HasPrefix(jid, "local:") {
		_, gr, ok := g.groupByFolderLocked(jid[6:])
		return gr, ok
	}
	return core.Group{}, false
}

func resolveTarget(msg core.Message, routes []core.Route, selfFolder string) string {
	if len(routes) == 0 {
		return ""
	}
	t := router.ResolveRoute(msg, routes)
	if t != "" && t != selfFolder {
		return t
	}
	return ""
}

func (g *Gateway) checkTrigger(msgs []core.Message) bool {
	for _, m := range msgs {
		if g.cfg.TriggerRE.MatchString(m.Content) {
			return true
		}
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

	if g.store.GetSession(folder) == "" {
		g.store.EnqueueSysMsg(folder, "gateway", "new_session", "")
	}
}

func (g *Gateway) delegateToChild(
	childFolder, prompt, originJid string, depth int,
) error {
	return g.delegateToFolder("delegate", childFolder, prompt, originJid, depth)
}

func (g *Gateway) delegateToFolder(
	label, folder, prompt, originJid string, depth int,
) error {
	if depth > 1 {
		return fmt.Errorf("delegation depth exceeded")
	}

	targetJid, target, found := g.groupByFolder(folder)
	if !found {
		return fmt.Errorf("%s target not found: %s", label, folder)
	}

	g.queue.EnqueueTask(targetJid, fmt.Sprintf("%s-%s-%d",
		label, folder, time.Now().UnixMilli()),
		func() error {
			out := g.runAgentWithOpts(target, prompt, originJid,
				func(text, status string) {
					if text != "" {
						clean := router.FormatOutbound(text)
						if clean != "" {
							g.sendMessage(originJid, clean)
						}
					}
				}, false)
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

func (g *Gateway) serveWeb(ctx context.Context) {
	pubDir := filepath.Join(g.cfg.WebDir, "pub")
	addr := net.JoinHostPort("", strconv.Itoa(g.cfg.WebPort))

	mux := http.NewServeMux()
	auth.RegisterRoutes(mux, g.store, g.cfg)
	mux.Handle("/", http.FileServer(http.Dir(pubDir)))
	var handler http.Handler = mux
	if g.cfg.AuthSecret != "" {
		handler = auth.Middleware([]byte(g.cfg.AuthSecret), mux)
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
	}
	slog.Info("web server starting", "addr", addr, "dir", pubDir)

	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("web server error", "err", err)
	}
}
