package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
		SendMessage: g.sendMessage,
		SendReply: func(jid, text, replyTo string) error {
			_, err := g.sendMessageReply(jid, text, replyTo)
			return err
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
		DeleteTask:  g.store.DeleteTask,
		ListTasks:   g.store.ListTasks,
		GetRoutes:   g.store.GetRoutes,
		SetRoutes:   g.store.SetRoutes,
		AddRoute:    g.store.AddRoute,
		DeleteRoute: g.store.DeleteRoute,
		GetRoute:    g.store.GetRoute,
		GetGrants:   g.store.GetGrants,
		SetGrants:   g.store.SetGrants,
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
			if g.cfg.OnboardingEnabled {
				last := chatMsgs[len(chatMsgs)-1]
				ch := g.findChannel(chatJid)
				if err := g.store.InsertOnboarding(chatJid, last.Sender, channelName(ch)); err != nil {
					slog.Warn("insert onboarding", "jid", chatJid, "err", err)
				}
			}
			continue
		}

		routes := g.store.GetRoutes(chatJid)

		last := chatMsgs[len(chatMsgs)-1]
		if g.handleCommand(last, group) {
			continue
		}

		if pr := findPrefixRoute(routes, last); pr != nil {
			if g.handlePrefixRoute(pr, last, group, chatJid) {
				continue
			}
		}

		routingTarget := resolveTarget(last, routes, group.Folder)

		if routingTarget != "" {
			if router.IsAuthorizedRoutingTarget(group.Folder, routingTarget) {
				g.delegateToChild(routingTarget, last.Content, chatJid, 0, nil)
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

	last := msgs[len(msgs)-1]

	if pr := findPrefixRoute(routes, last); pr != nil {
		if g.handlePrefixRoute(pr, last, group, chatJid) {
			g.advanceAgentCursor(chatJid, msgs)
			return true, nil
		}
	}

	routingTarget := resolveTarget(last, routes, group.Folder)

	if routingTarget != "" {
		if router.IsAuthorizedRoutingTarget(group.Folder, routingTarget) {
			g.delegateToChild(routingTarget, last.Content, chatJid, 0, nil)
			g.advanceAgentCursor(chatJid, msgs)
			return true, nil
		}
	}

	g.emitSystemEvents(group, chatJid)

	sysMsgs := g.store.FlushSysMsgs(group.Folder)

	prompt := sysMsgs + router.ClockXml(g.cfg.Timezone) + "\n"
	prompt += router.FormatMessages(msgs)

	if ch != nil {
		ch.Typing(chatJid, true)
	}

	var hadOutput bool
	savedTs := agentTs

	isolated := last.Sender == "scheduler-isolated"
	lastSentID := last.ID
	out := g.runAgentWithOpts(group, prompt, chatJid, last.Sender,
		func(text, status string) {
			if text != "" {
				hadOutput = true
				stripped, statuses := router.ExtractStatusBlocks(text)
				for _, s := range statuses {
					g.sendMessage(chatJid, s)
				}
				clean := router.FormatOutbound(stripped)
				if clean != "" {
					if sentID, _ := g.sendMessageReply(chatJid, clean, lastSentID); sentID != "" {
						lastSentID = sentID
					}
				}
			}
		}, isolated, nil, "", last.ID)

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
			// Output was already delivered and cursor advanced; treat as
			// success so the circuit breaker does not fire and a duplicate
			// error notification is not sent.
			g.advanceAgentCursor(chatJid, msgs)
			return true, nil
		}
		g.store.SetAgentCursor(chatJid, savedTs)
		g.sendMessage(chatJid,
			"Failed: agent error, will retry on next message.")
		g.store.MarkChatErrored(chatJid)
		return false, fmt.Errorf("agent: %s", out.Error)
	}

	g.advanceAgentCursor(chatJid, msgs)
	g.store.ClearChatErrored(chatJid)
	return true, nil
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

	ts := time.Now().UnixMilli()
	sanitized := container.SanitizeFolder(group.Folder)
	cname := fmt.Sprintf("arizuko-%s-%d", sanitized, ts)

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
	_, err := g.sendMessageReply(jid, text, "")
	return err
}

func (g *Gateway) sendMessageReply(jid, text, replyTo string) (string, error) {
	ch := g.findChannel(jid)
	if ch == nil {
		return "", fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Send(jid, text, replyTo)
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

// ensureGroupGitRepo initialises a git repo in groupDir if one does not exist,
// and writes a .gitignore that excludes runtime-only directories.
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
	entries, _ := os.ReadDir(groupDir)
	runtime := map[string]bool{"diary": true, "episodes": true, "users": true, "logs": true, "media": true, "tmp": true}
	for _, e := range entries {
		if e.IsDir() && !runtime[e.Name()] {
			lines = append(lines, e.Name()+"/")
		}
	}
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

// parsePrefix parses "@name rest" or "#name rest".
// Returns the name (without the symbol) and the remaining text.
func parsePrefix(text string) (name, rest string, ok bool) {
	t := strings.TrimSpace(text)
	if len(t) < 2 || (t[0] != '@' && t[0] != '#') {
		return "", "", false
	}
	sym := t[1:]
	i := strings.IndexByte(sym, ' ')
	if i == -1 {
		return sym, "", true
	}
	return sym[:i], strings.TrimSpace(sym[i+1:]), true
}

// findPrefixRoute returns the first prefix-type route matching msg, or nil.
func findPrefixRoute(routes []core.Route, msg core.Message) *core.Route {
	for i := range routes {
		r := &routes[i]
		if r.Type == "prefix" && r.Match != "" &&
			strings.HasPrefix(strings.TrimSpace(msg.Content), r.Match) {
			return r
		}
	}
	return nil
}

// handlePrefixRoute dispatches a message that matched a prefix route.
// Returns true if the message was consumed.
func (g *Gateway) handlePrefixRoute(
	r *core.Route, msg core.Message, group core.Group, chatJid string,
) bool {
	name, stripped, ok := parsePrefix(msg.Content)
	if !ok || name == "" {
		return false
	}
	switch r.Match {
	case "@":
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
							g.sendMessage(originJid, clean)
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
