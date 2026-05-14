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

	"maps"
	"slices"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/diary"
	"github.com/kronael/arizuko/grants"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/queue"
	"github.com/kronael/arizuko/router"
	"github.com/kronael/arizuko/store"
)

type Gateway struct {
	cfg     *core.Config
	store   *store.Store
	queue   *queue.GroupQueue
	folders *groupfolder.Resolver
	runner  container.Runner

	mu       sync.RWMutex
	channels []core.Channel
	gatedFns ipc.GatedFns
	storeFns ipc.StoreFns

	// lastTimestamp is persisted and queried as RFC3339Nano throughout to
	// preserve nanosecond precision; seconds-only formats risk missed messages.
	lastTimestamp time.Time

	// steeredTs tracks the latest message timestamp steered into a
	// running container per chat JID. processGroupMessages uses this
	// to advance the cursor past steered messages on completion.
	steeredTs map[string]time.Time

	// turnsMu guards inFlightTurns. Each entry is the active run for a
	// folder; submit_turn over MCP looks itself up here and invokes the
	// per-run callback (text delivery + session capture).
	turnsMu       sync.Mutex
	inFlightTurns map[string]*turnState

	impulse *impulseGate

	// ctx is the gateway's root context set at Run. Used to abort
	// long-running HTTP calls (media download, whisper) on shutdown.
	ctx context.Context
}

// turnState is the per-active-run state needed to absorb submit_turn
// callbacks coming back over MCP.
type turnState struct {
	onOutput     func(result, status string)
	newSessionID string
	lastError    string
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

func New(cfg *core.Config, s *store.Store) *Gateway {
	g := &Gateway{
		cfg:           cfg,
		store:         s,
		queue:         queue.New(cfg.MaxContainers, cfg.IpcDir),
		runner:        container.DockerRunner{},
		steeredTs:     make(map[string]time.Time),
		inFlightTurns: make(map[string]*turnState),
		folders: &groupfolder.Resolver{
			GroupsDir: cfg.GroupsDir,
			IpcDir:    cfg.IpcDir,
		},
		impulse: newImpulseGate(),
	}
	// Bind submit_turn at construction so the agent path is wired even
	// in unit tests that drive runAgentWithOpts without calling Run().
	g.gatedFns.SubmitTurn = g.handleSubmitTurn
	return g
}

func (g *Gateway) SetRunner(r container.Runner) { g.runner = r }

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
	g.ctx = ctx
	if err := container.EnsureRunning(); err != nil {
		return fmt.Errorf("runtime check failed: %w", err)
	}
	container.CleanupOrphans(g.cfg.Name, g.cfg.Image)

	g.loadState()

	lc := NewLocalChannel(g.store)
	lc.SetEnqueue(func(jid string) { g.queue.EnqueueMessageCheck(jid) })
	g.AddChannel(lc)

	g.gatedFns = ipc.GatedFns{
		SendMessage: func(jid, text string) (string, error) {
			return g.sendMessageReply(jid, text, "", "")
		},
		SendReply: func(jid, text, replyTo string) (string, error) {
			return g.sendMessageReply(jid, text, replyTo, "")
		},
		SendDocument: g.sendDocument,
		SendVoice:    g.sendVoice,
		Post:         g.postToJID,
		Like:         g.likeOnJID,
		Delete:       g.deleteOnJID,
		Forward:      g.forwardToJID,
		Quote:        g.quoteToJID,
		Repost:       g.repostToJID,
		Dislike:      g.dislikeOnJID,
		Edit:         g.editOnJID,
		ClearSession: g.clearSession,
		GroupsDir:    g.cfg.GroupsDir,
		WebDir:       g.cfg.WebDir,
		InjectMessage: g.injectMessage,
		RegisterGroup: g.registerGroupIPC,
		SetupGroup: func(folder string) error {
			if err := container.SetupGroup(g.cfg, folder, ""); err != nil {
				return err
			}
			return g.store.SeedDefaultTasks(folder, folder)
		},
		GetGroups:           g.store.AllGroups,
		EnqueueMessageCheck: g.queue.EnqueueMessageCheck,
		UpdateGroupConfig: func(folder string, cfg core.GroupConfig) error {
			gr, ok := g.store.GroupByFolder(folder)
			if !ok {
				return fmt.Errorf("group not found: %s", folder)
			}
			gr.Config = cfg
			return g.store.PutGroup(gr)
		},
		FetchPlatformHistory: g.fetchPlatformHistory,
		SpawnGroup: func(parentFolder, childJID string) (core.Group, error) {
			if _, ok := g.store.GroupByFolder(parentFolder); !ok {
				return core.Group{}, fmt.Errorf("parent group not found: %s", parentFolder)
			}
			return g.spawnFromPrototype(parentFolder, childJID)
		},
		CreateInvite: func(targetGlob, issuedBySub string, maxUses int, expiresAt *time.Time) (ipc.InviteInfo, error) {
			inv, err := g.store.CreateInvite(targetGlob, issuedBySub, maxUses, expiresAt)
			if err != nil {
				return ipc.InviteInfo{}, err
			}
			return ipc.InviteInfo{
				Token:       inv.Token,
				TargetGlob:  inv.TargetGlob,
				IssuedBySub: inv.IssuedBySub,
				IssuedAt:    inv.IssuedAt,
				ExpiresAt:   inv.ExpiresAt,
				MaxUses:     inv.MaxUses,
				UsedCount:   inv.UsedCount,
			}, nil
		},
		SubmitTurn:    g.handleSubmitTurn,
		AcceptURLBase: g.cfg.AuthBaseURL,
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
		MessagesByThread:    g.store.MessagesByThread,
		JIDRoutedToFolder:   g.store.JIDRoutedToFolder,
		ErroredChats: func(folder string, isRoot bool) []ipc.ErroredChat {
			rows := g.store.ErroredChats(folder, isRoot)
			out := make([]ipc.ErroredChat, len(rows))
			for i, r := range rows {
				out[i] = ipc.ErroredChat{ChatJID: r.ChatJID, Count: r.Count, LastAt: r.LastAt, RoutedTo: r.RoutedTo}
			}
			return out
		},
		TaskRunLogs: func(taskID string, limit int) []ipc.TaskRunLog {
			rows := g.store.TaskRunLogs(taskID, limit)
			out := make([]ipc.TaskRunLog, len(rows))
			for i, r := range rows {
				out[i] = ipc.TaskRunLog{
					ID: r.ID, TaskID: r.TaskID, RunAt: r.RunAt,
					DurationMS: r.DurationMS, Status: r.Status,
					Result: r.Result, Error: r.Error,
				}
			}
			return out
		},
		RecentSessions: g.store.RecentSessions,
		GetSession:     g.store.GetSession,
		GetIdentityForSub: func(sub string) (ipc.Identity, []string, bool) {
			idn, subs, ok := g.store.GetIdentityForSub(sub)
			if !ok {
				return ipc.Identity{}, nil, false
			}
			return ipc.Identity{ID: idn.ID, Name: idn.Name, CreatedAt: idn.CreatedAt}, subs, true
		},
		SetWebRoute: func(pathPrefix, access, redirectTo, folder string) error {
			return g.store.SetWebRoute(store.WebRoute{
				PathPrefix: pathPrefix,
				Access:     access,
				RedirectTo: redirectTo,
				Folder:     folder,
				CreatedAt:  time.Now(),
			})
		},
		DelWebRoute: func(pathPrefix, folder string) (bool, error) {
			return g.store.DelWebRoute(pathPrefix, folder)
		},
		LookupSecret: func(scope, scopeID, key string) (string, bool) {
			return g.store.LookupSecret(store.SecretScope(scope), scopeID, key)
		},
		LogSecretUse: func(r ipc.SecretUseRow) error {
			return g.store.LogSecretUse(store.SecretUseRow{
				SpawnID: r.SpawnID, CallerSub: r.CallerSub, Folder: r.Folder,
				Tool: r.Tool, Key: r.Key, Scope: r.Scope,
				Status: r.Status, LatencyMS: r.LatencyMS,
			})
		},
		LogExternalCost: func(folder, provider, model string, inputTok, outputTok, costCents int) error {
			return g.logCost(folder, "", provider+":"+model, ipc.ModelUsage{
				Input:     inputTok,
				Output:    outputTok,
				CostCents: costCents,
			})
		},
		ListWebRoutes: func(folder string) []ipc.WebRoute {
			rows := g.store.ListWebRoutes(folder)
			out := make([]ipc.WebRoute, len(rows))
			for i, r := range rows {
				out[i] = ipc.WebRoute{
					PathPrefix: r.PathPrefix,
					Access:     r.Access,
					RedirectTo: r.RedirectTo,
					Folder:     r.Folder,
					CreatedAt:  r.CreatedAt.UTC().Format(time.RFC3339),
				}
			}
			return out
		},
	}

	// Connectors: load <data_dir>/connectors.toml (or $CONNECTORS_TOML),
	// spawn each connector once to harvest its tool catalog, register
	// through the broker chain. Spec 9/11 M6. Missing file is fine.
	if conns, err := LoadConnectors(ctx, g.cfg.ProjectRoot); err != nil {
		slog.Error("connectors: load failed", "err", err)
	} else {
		g.storeFns.Connectors = conns
		if len(conns) > 0 {
			slog.Info("connectors loaded", "tools", len(conns))
		}
	}

	g.queue.SetProcessMessagesFn(g.processGroupMessages)
	g.queue.SetHasPendingFn(func(jid string) bool {
		return g.store.HasPendingMessages(jid, g.cfg.Name)
	})
	g.queue.SetFolderForJidFn(g.folderForJid)
	g.queue.SetNotifyErrorFn(g.onCircuitBreakerOpen)

	for _, ch := range g.channels {
		if err := ch.Connect(ctx); err != nil {
			slog.Error("channel connect failed",
				"channel", ch.Name(), "err", err)
			continue
		}
	}
	slog.Info("channels connected", "count", len(g.channels))

	g.recoverPendingMessages()
	g.seedCodexDirs()
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
	if raw := g.store.GetState("last_timestamp"); raw != "" {
		g.lastTimestamp, _ = time.Parse(time.RFC3339Nano, raw)
	}
}

func (g *Gateway) saveState() {
	g.mu.RLock()
	defer g.mu.RUnlock()
	g.store.SetState("last_timestamp",
		g.lastTimestamp.Format(time.RFC3339Nano))
}

// seedCodexDirs pre-creates per-group `.codex/` so cold-start parallel
// spawns don't race Docker into creating the bind source as root.
// At cold start, auto-migrate fires spawns for many groups in parallel;
// the runner's lazy MkdirAll loses the race against `docker run` and
// Docker materializes the bind source as root, leaving the agent (uid
// 1000) unable to write codex state. Pre-seeding here, synchronously,
// before any spawn enqueues, guarantees the dir exists with gated's
// uid 1000 ownership.
func (g *Gateway) seedCodexDirs() {
	if g.cfg.HostCodexDir == "" {
		return
	}
	for _, gr := range g.store.AllGroups() {
		groupDir, err := g.folders.GroupPath(gr.Folder)
		if err != nil {
			continue
		}
		codexDir := filepath.Join(groupDir, ".codex")
		if err := os.MkdirAll(codexDir, 0o755); err != nil {
			slog.Warn("seed codex dir", "group", gr.Folder, "err", err)
		}
	}
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
		if !groupfolder.IsRoot(gr.Folder) {
			continue
		}
		agent := container.MigrationVersion(
			filepath.Join(g.cfg.GroupsDir, gr.Folder, ".claude", "skills", "self", "MIGRATION_VERSION"))
		if agent >= latest {
			continue
		}
		slog.Info("auto-migrate: version behind, triggering /migrate",
			"group", gr.Folder, "agent", agent, "latest", latest)
		prompt := fmt.Sprintf("/migrate\n\nSystem update: skills v%d → v%d.",
			agent, latest)
		g.store.PutMessage(core.Message{
			ID:        core.MsgID("auto-migrate-" + gr.Folder),
			ChatJID:   gr.Folder,
			Sender:    "system",
			Content:   prompt,
			Timestamp: time.Now(),
		})
		g.queue.EnqueueMessageCheck(gr.Folder)

		// Notify child groups directly — don't rely on root agent.
		note := fmt.Sprintf("System update: skills v%d → v%d applied. "+
			"New capabilities may be available — check /self for details.", agent, latest)
		for _, child := range groups {
			if groupfolder.ParentOf(child.Folder) != gr.Folder {
				continue
			}
			g.store.PutMessage(core.Message{
				ID:        core.MsgID("auto-migrate-notify-" + child.Folder),
				ChatJID:   child.Folder,
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
	go g.outboundRetryLoop(ctx)
	for {
		g.pollOnce()
		select {
		case <-ctx.Done():
			return
		case <-time.After(g.cfg.PollInterval):
		}
	}
}

// outboundRetryLoop polls the messages table for rows with status='pending'
// older than retryAge and re-dispatches them to the owning channel. This
// is the recovery path for crashes mid-send and adapter reconnects: the
// fast in-line path in makeOutputCallback marks 'sent' on success, so
// 'pending' rows are by definition the failed-or-crashed slice. After
// outboundMaxAge the row is marked 'failed' and stops being retried.
func (g *Gateway) outboundRetryLoop(ctx context.Context) {
	const (
		retryEvery     = 30 * time.Second
		retryAge       = 30 * time.Second
		outboundMaxAge = 24 * time.Hour
	)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryEvery):
		}
		g.retryPendingOutbound(retryAge, outboundMaxAge)
	}
}

func (g *Gateway) retryPendingOutbound(retryAge, maxAge time.Duration) {
	rows, err := g.store.PendingOutboundOlderThan(time.Now().Add(-retryAge), 50)
	if err != nil {
		slog.Warn("outbound retry: scan pending failed", "err", err)
		return
	}
	for _, m := range rows {
		if time.Since(m.Timestamp) > maxAge {
			slog.Warn("outbound retry: giving up after max age",
				"jid", m.ChatJID, "msg_id", m.ID, "age", time.Since(m.Timestamp))
			if err := g.store.MarkMessageStatus(m.ID, core.MessageStatusFailed); err != nil {
				slog.Warn("outbound retry: mark failed", "msg_id", m.ID, "err", err)
			}
			continue
		}
		platformID, err := g.dispatchOutbound(nil, m.ChatJID, m.Content, "", m.Topic, m.TurnID)
		if err != nil {
			slog.Debug("outbound retry: dispatch still failing",
				"jid", m.ChatJID, "msg_id", m.ID, "err", err)
			continue
		}
		if err := g.store.MarkMessageDelivered(m.ID, platformID); err != nil {
			slog.Warn("outbound retry: mark delivered", "msg_id", m.ID, "err", err)
		}
	}
}

func (g *Gateway) pollOnce() {
	g.mu.RLock()
	since := g.lastTimestamp
	g.mu.RUnlock()

	msgs, hi, err := g.store.NewMessages(nil, since, g.cfg.Name)
	if err != nil {
		slog.Error("error in message loop", "since", since, "err", err)
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
		last := chatMsgs[len(chatMsgs)-1]
		group, ok := g.resolveGroup(last)
		if !ok {
			discordGuild := strings.HasPrefix(chatJid, "discord:") && !strings.HasPrefix(chatJid, "discord:dm/")
			if g.cfg.OnboardingEnabled && onboardingAllowed(chatJid, g.cfg.OnboardingPlatforms) && (!discordGuild || last.Verb == "mention") {
				if err := g.store.InsertOnboarding(chatJid); err != nil {
					slog.Warn("insert onboarding", "jid", chatJid, "err", err)
				}
			}
			slog.Debug("poll: no route for message", "jid", chatJid)
			g.advanceAgentCursor(chatJid, chatMsgs)
			continue
		}

		if g.handleStickyCommand(chatJid, last) {
			slog.Debug("poll: handled sticky command", "jid", chatJid)
			continue
		}

		if g.handleCommand(last, group) {
			slog.Debug("poll: handled command", "jid", chatJid, "sender", last.Sender)
			continue
		}

		if g.tryExternalRoute(routes, last, group, chatJid) {
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

		ctx := g.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		for i := range chatMsgs {
			g.enrichAttachments(ctx, &chatMsgs[i], group.Folder)
		}
		rendered := router.FormatMessages(chatMsgs)
		if g.queue.SendMessages(chatJid, []string{rendered}) {
			slog.Info("poll: steered messages into running container",
				"jid", chatJid, "count", len(chatMsgs))
			g.store.SetLastReplyID(chatJid, g.effectiveTopic(chatJid, last.Topic), last.ID)
			g.recordSteeredTs(chatJid, chatMsgs)
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
	agentTs := g.store.GetAgentCursor(chatJid)

	msgs, err := g.store.MessagesSince(chatJid, agentTs, g.cfg.Name)
	if err != nil {
		return false, fmt.Errorf("query messages: %w", err)
	}
	if len(msgs) == 0 {
		return true, nil
	}

	group, ok := g.resolveGroup(msgs[len(msgs)-1])
	if !ok {
		g.advanceAgentCursor(chatJid, msgs)
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

		if g.tryExternalRoute(routes, last, group, chatJid) {
			continue
		}

		if !g.processSenderBatch(group, chatJid, batch, agentTs) {
			// Keep cursor where it was; errored rows stay visible so
			// they're re-fed (tagged) on the next run.
			return false, fmt.Errorf("sender batch failed: %s", last.Sender)
		}
	}

	slog.Debug("process: completed all sender batches",
		"jid", chatJid, "group", group.Folder, "batches", len(senderBatches))
	g.advanceAgentCursor(chatJid, msgs)
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

func (g *Gateway) tryExternalRoute(
	routes []core.Route, msg core.Message, group core.Group, chatJid string,
) bool {
	if g.handlePrefixLayer(msg, group, chatJid) {
		slog.Debug("routed via prefix layer", "jid", chatJid, "sender", msg.Sender)
		return true
	}

	target := g.resolveTarget(msg, routes, group.Folder)
	if target != "" && router.IsAuthorizedRoutingTarget(group.Folder, target) {
		slog.Debug("delegating to child",
			"jid", chatJid, "sender", msg.Sender, "target", target)
		if err := g.delegateViaMessage(target, msg.Content, chatJid, 0); err != nil {
			slog.Warn("delegate failed",
				"jid", chatJid, "sender", msg.Sender, "target", target, "err", err)
		}
		return true
	}
	return false
}

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

	ctx := g.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	for i := range msgs {
		g.enrichAttachments(ctx, &msgs[i], group.Folder)
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
	topic := g.effectiveTopic(chatJid, last.Topic)
	prompt := sysMsgs + g.autocallsBlock(group.Folder, topic) + g.personaBlock(group.Folder) + router.FormatMessages(msgs, observed)

	if deliverCh != nil {
		slog.Debug("typing start", "jid", deliverTo, "channel", deliverCh.Name())
		deliverCh.Typing(deliverTo, true)
	} else {
		slog.Warn("typing skip: no channel", "jid", deliverTo)
	}

	isolated := strings.HasPrefix(last.Sender, "timed-isolated")
	onOutput, hadOutput := g.makeOutputCallback(deliverCh, deliverTo, topic, last.ID, group.Folder)
	out := g.runAgentWithOpts(group, prompt, chatJid, last.Sender,
		onOutput, isolated, topic, last.ID, len(msgs))

	if deliverCh != nil {
		slog.Debug("typing stop", "jid", deliverTo, "channel", deliverCh.Name())
		deliverCh.Typing(deliverTo, false)
	}

	if out.Error != "" {
		g.logAgentError(group, "sender", last.Sender, out.Error)
		if *hadOutput {
			return true
		}
		// Advance the cursor PAST the failed batch so the next poll
		// doesn't re-feed the same messages. Without this, a permanent
		// failure (e.g. egress register can't reach a misconfigured
		// crackbox) replays every inbound message forever and burns
		// every channel's poll loop. Errored messages stay flagged in
		// the DB for the agent to see if it does run later.
		g.store.SetAgentCursor(chatJid, last.Timestamp)
		g.sendMessage(deliverTo, "Failed: agent error on that message. Try rephrasing or send a different message.")
		ids := make([]string, len(msgs))
		for i, m := range msgs {
			ids[i] = m.ID
		}
		g.store.MarkMessagesErrored(ids)
		return false
	}

	if !*hadOutput {
		slog.Info("agent silent",
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
		effectiveTopic := g.effectiveTopic(chatJid, topic)
		prompt := sysMsgs + g.autocallsBlock(group.Folder, effectiveTopic) + g.personaBlock(group.Folder) + router.FormatMessages(topicMsgs)

		if ch != nil {
			ch.Typing(chatJid, true)
		}

		onOutput, hadOutput := g.makeOutputCallback(ch, chatJid, effectiveTopic, last.ID, group.Folder)
		out := g.runAgentWithOpts(group, prompt, chatJid, last.Sender,
			onOutput, false, effectiveTopic, last.ID, len(topicMsgs))

		if ch != nil {
			ch.Typing(chatJid, false)
		}

		if out.Error != "" {
			g.logAgentError(group, "topic", topic, out.Error)
			if *hadOutput {
				g.advanceAgentCursor(chatJid, topicMsgs)
				return true, nil
			}
			g.sendMessage(chatJid, "Failed: agent error on that message. Try rephrasing or send a different message.")
			ids := make([]string, len(topicMsgs))
			for i, m := range topicMsgs {
				ids[i] = m.ID
			}
			g.store.MarkMessagesErrored(ids)
			return false, fmt.Errorf("agent: %s", out.Error)
		}

		if !*hadOutput {
			slog.Warn("agent completed with no output delivered",
				"jid", chatJid, "group", group.Folder, "topic", topic)
		}
		g.advanceAgentCursor(chatJid, topicMsgs)
	}

	return true, nil
}

func (g *Gateway) makeOutputCallback(
	ch core.Channel, chatJid, topic, firstMsgID, groupFolder string,
) (func(string, string), *bool) {
	var hadOutput bool
	turnID := firstMsgID
	replyTo := firstMsgID

	// putAndDeliver writes the bot row with status='pending', invokes the
	// adapter, then transitions the row to 'sent' or leaves it 'pending'
	// for the gateway poll loop to retry. Returns the platform-side ID
	// (may be ""), used to chain reply threading on the next send.
	putAndDeliver := func(text, replyToID, threadID string) string {
		muted := !g.canSendToGroup(groupFolder)
		if muted {
			slog.Info("group muted, recording without platform send",
				"group", groupFolder, "jid", chatJid)
		}

		row := core.Message{
			ID:        core.MsgID("out"),
			ChatJID:   chatJid,
			Sender:    groupFolder,
			Content:   text,
			Timestamp: time.Now(),
			FromMe:    true,
			BotMsg:    true,
			RoutedTo:  chatJid,
			Topic:     threadID,
			TurnID:    turnID,
			Status:    core.MessageStatusPending,
		}
		if muted || !g.canSendToJID(chatJid) {
			// Suppressed: persist as already-sent so the poll loop ignores it.
			row.Status = core.MessageStatusSent
			g.store.PutMessage(row)
			return ""
		}
		g.store.PutMessage(row)

		// Self-routed local dispatch is redundant — the row above is the
		// authoritative outbound. Without this skip, LocalChannel.Send
		// re-persists as inbound and spawns another agent run that replies
		// to its own echo (witnessed sloth/main 2026-05-10 21:12–21:14,
		// 5 self-acknowledgments in 51s). Cross-group sends (groupFolder !=
		// chatJid) still flow through normally — that's the legit
		// escalation path.
		if !strings.Contains(chatJid, ":") && chatJid == groupFolder {
			row.Status = core.MessageStatusSent
			g.store.MarkMessageDelivered(row.ID, "")
			return ""
		}

		platformID, err := g.dispatchOutbound(ch, chatJid, text, replyToID, threadID, turnID)
		if err != nil {
			slog.Error("dispatch outbound failed (poll loop will retry)",
				"jid", chatJid, "group", groupFolder, "msg_id", row.ID, "err", err)
			return ""
		}
		if err := g.store.MarkMessageDelivered(row.ID, platformID); err != nil {
			slog.Warn("mark delivered failed", "msg_id", row.ID, "err", err)
		}
		return platformID
	}

	return func(text, _ string) {
		if text == "" {
			return
		}

		stripped, statuses := router.ExtractStatusBlocks(router.StripThinkBlocks(text))
		for _, s := range statuses {
			hadOutput = true
			putAndDeliver("⏳ "+s, "", "")
		}
		if clean := router.FormatOutbound(stripped); clean != "" {
			hadOutput = true
			sentID := putAndDeliver(clean, replyTo, topic)
			if sentID != "" {
				replyTo = sentID
				g.store.SetLastReplyID(chatJid, topic, sentID)
			}
		}
	}, &hadOutput
}

func (g *Gateway) dispatchOutbound(ch core.Channel, chatJid, text, replyToID, threadID, turnID string) (string, error) {
	c := ch
	if c == nil {
		c = g.findChannelForJID(chatJid)
	}
	if c == nil {
		return "", fmt.Errorf("no channel for jid %s", chatJid)
	}
	return c.Send(chatJid, text, replyToID, threadID, turnID)
}

func (g *Gateway) onCircuitBreakerOpen(jid string, err error) {
	if pruneErr := g.store.DeleteErroredMessages(jid); pruneErr != nil {
		slog.Warn("prune errored messages failed", "jid", jid, "err", pruneErr)
	}
	if folder := g.store.DefaultFolderForJID(jid); folder != "" {
		g.store.DeleteSession(folder, "")
	}
	msg := fmt.Sprintf("⚠️ Agent error: %v\n\nSend another message to retry.", err)
	g.sendMessage(jid, msg)
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
	topic string, msgID string, msgCount int,
) container.Output {
	// Spec 5/34 pre-spawn budget gate. If today's spend hits the cap,
	// send a short channel-visible refusal (no LLM call) and return.
	// callerSubOfMsg picks JWT-derived senders only; adapter and anon
	// senders fall back to folder-only enforcement until spec 6/5's
	// Caller shape lands.
	if msg := g.budgetGate(group.Folder, callerSubOfMsg(sender)); msg != "" {
		onOutput(msg, "ok")
		return container.Output{}
	}

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

	id := auth.Resolve(group.Folder)
	rules := grants.DeriveRules(g.store, group.Folder, id.Tier, auth.WorldOf(group.Folder))
	rules = append(rules, g.store.GetGrants(group.Folder)...)

	groupPath, err := g.folders.GroupPath(group.Folder)
	if err != nil {
		return container.Output{Error: err.Error()}
	}

	sanitized := container.SanitizeFolder(group.Folder)
	var cname string
	if taskID := strings.TrimPrefix(sender, "timed-isolated:"); taskID != sender {
		cname = fmt.Sprintf("arizuko-%s-%s-task-%s", g.cfg.Name, sanitized, container.SanitizeFolder(taskID))
	} else {
		cname = fmt.Sprintf("arizuko-%s-%s-%d", g.cfg.Name, sanitized, time.Now().UnixMilli())
	}

	var chanName string
	if ch := g.findChannelForJID(chatJid); ch != nil {
		chanName = ch.Name()
	}

	g.queue.RegisterProcess(chatJid, cname, group.Folder)

	isRoot := groupfolder.IsRoot(group.Folder)
	container.WriteTasksSnapshot(
		g.folders, group.Folder, isRoot, g.store.ListTasks("", true))
	container.WriteGroupsSnapshot(
		g.folders, group.Folder, isRoot, slices.Collect(maps.Values(g.store.AllGroups())))

	input := container.Input{
		Prompt:          prompt,
		SessionID:       sessionID,
		ChatJID:         chatJid,
		Folder:          group.Folder,
		Topic:           topic,
		GroupPath:       groupPath,
		Name:            cname,
		Config:          group.Config,
		SlinkToken:      group.SlinkToken,
		Channel:         chanName,
		MessageID:       msgID,
		Sender:          sender,
		Grants:          rules,
		GatedFns:        g.gatedFns,
		StoreFns:        g.storeFns,
		SecretsResolver: g.store,
		Egress: container.EgressConfig{
			NetworkPrefix:     g.cfg.EgressNetworkPrefix,
			CrackboxContainer: g.cfg.EgressCrackbox,
			ParentSubnet:      g.cfg.EgressParentSubnet,
			ProxyURL:          g.cfg.EgressProxyURL,
			AdminURL:          g.cfg.EgressAPI,
			AdminSecret:       g.cfg.EgressAdminSecret,
			AllowlistFn:       g.store.ResolveAllowlist,
		},
	}

	st := g.beginTurnRun(group.Folder, onOutput)
	defer g.endTurnRun(group.Folder)
	out := g.runner.Run(g.cfg, g.folders, input)
	if st.newSessionID != "" {
		out.NewSessionID = st.newSessionID
	}
	if out.Error == "" && st.lastError != "" {
		out.Error = st.lastError
	}

	if isolated {
		return out
	}

	result := "ok"
	if strings.Contains(out.Error, "timed out") {
		result = "timeout"
	} else if out.Error != "" {
		result = "error"
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
// no source is recorded (e.g. bare folder / web: JIDs that bypass adapters).
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

// fetchPlatformHistory proxies to the adapter owning jid, decodes the
// HistoryResponse, caches rows in the local DB (dedup by ID), and returns
// the decoded messages. On adapter error returns source:"cache" with
// whatever the local DB has.
func (g *Gateway) fetchPlatformHistory(jid string, before time.Time, limit int) (ipc.PlatformHistory, error) {
	fallback := func(source string) (ipc.PlatformHistory, error) {
		msgs, err := g.store.MessagesBefore(jid, before, limit)
		if err != nil {
			return ipc.PlatformHistory{}, err
		}
		return ipc.PlatformHistory{Source: source, Messages: msgs}, nil
	}
	ch := g.findChannelForJID(jid)
	if ch == nil {
		return fallback("cache-only")
	}
	hf, ok := ch.(core.HistoryFetcher)
	if !ok {
		return fallback("cache-only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := hf.FetchHistory(ctx, jid, before, limit)
	if err != nil {
		slog.Warn("fetch_history: adapter failed, falling back to cache",
			"jid", jid, "err", err)
		return fallback("cache")
	}
	var resp chanlib.HistoryResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fallback("cache")
	}
	out := ipc.PlatformHistory{Source: resp.Source, Cap: resp.Cap}
	for _, m := range resp.Messages {
		cm := core.Message{
			ID:        m.ID,
			ChatJID:   m.ChatJID,
			Sender:    m.Sender,
			Content:   m.Content,
			Timestamp: time.Unix(m.Timestamp, 0).UTC(),
			ReplyToID: m.ReplyTo,
		}
		if cm.ID != "" {
			_ = g.store.PutMessage(cm)
		}
		out.Messages = append(out.Messages, cm)
	}
	return out, nil
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
	return ch.Send(jid, text, replyTo, threadID, "")
}

func (g *Gateway) sendDocument(jid, path, name, caption string) error {
	ch := g.findChannelForJID(jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.SendFile(jid, path, name, caption)
}

func (g *Gateway) channelSocial(jid string) (core.Socializer, error) {
	ch := g.findChannelForJID(jid)
	if ch == nil {
		return nil, fmt.Errorf("no channel for jid %s", jid)
	}
	s, ok := ch.(core.Socializer)
	if !ok {
		return nil, chanreg.ErrUnsupported
	}
	return s, nil
}

func socialCall[T any](g *Gateway, jid string, fn func(s core.Socializer, ctx context.Context) (T, error)) (T, error) {
	var zero T
	s, err := g.channelSocial(jid)
	if err != nil {
		return zero, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return fn(s, ctx)
}

func socialDo(g *Gateway, jid string, fn func(s core.Socializer, ctx context.Context) error) error {
	_, err := socialCall(g, jid, func(s core.Socializer, ctx context.Context) (struct{}, error) {
		return struct{}{}, fn(s, ctx)
	})
	return err
}

func (g *Gateway) postToJID(jid, content string, mediaPaths []string) (string, error) {
	if !g.canSendToJID(jid) {
		return "", nil
	}
	return socialCall(g, jid, func(s core.Socializer, ctx context.Context) (string, error) {
		return s.Post(ctx, jid, content, mediaPaths)
	})
}

func (g *Gateway) likeOnJID(jid, targetID, reaction string) error {
	return socialDo(g, jid, func(s core.Socializer, ctx context.Context) error {
		return s.Like(ctx, jid, targetID, reaction)
	})
}

func (g *Gateway) deleteOnJID(jid, targetID string) error {
	return socialDo(g, jid, func(s core.Socializer, ctx context.Context) error {
		return s.Delete(ctx, jid, targetID)
	})
}

func (g *Gateway) forwardToJID(sourceMsgID, targetJID, comment string) (string, error) {
	if !g.canSendToJID(targetJID) {
		return "", nil
	}
	return socialCall(g, targetJID, func(s core.Socializer, ctx context.Context) (string, error) {
		return s.Forward(ctx, sourceMsgID, targetJID, comment)
	})
}

func (g *Gateway) quoteToJID(jid, sourceMsgID, comment string) (string, error) {
	if !g.canSendToJID(jid) {
		return "", nil
	}
	return socialCall(g, jid, func(s core.Socializer, ctx context.Context) (string, error) {
		return s.Quote(ctx, jid, sourceMsgID, comment)
	})
}

func (g *Gateway) repostToJID(jid, sourceMsgID string) (string, error) {
	if !g.canSendToJID(jid) {
		return "", nil
	}
	return socialCall(g, jid, func(s core.Socializer, ctx context.Context) (string, error) {
		return s.Repost(ctx, jid, sourceMsgID)
	})
}

func (g *Gateway) dislikeOnJID(jid, targetID string) error {
	return socialDo(g, jid, func(s core.Socializer, ctx context.Context) error {
		return s.Dislike(ctx, jid, targetID)
	})
}

func (g *Gateway) editOnJID(jid, targetID, content string) error {
	return socialDo(g, jid, func(s core.Socializer, ctx context.Context) error {
		return s.Edit(ctx, jid, targetID, content)
	})
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
	return id, nil
}

func (g *Gateway) enrichAttachments(ctx context.Context, msg *core.Message, folder string) {
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
			if err := downloadFile(ctx, att.URL, dest, g.cfg.ChannelSecret, g.cfg.MediaMaxBytes); err != nil {
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
				transcript = whisperTranscribe(ctx, g.cfg.WhisperURL, g.cfg.WhisperModel, dest, att.Mime, langs)
			} else if strings.HasPrefix(att.Mime, "video/") {
				if audioPath := extractVideoAudio(dest); audioPath != "" {
					transcript = whisperTranscribe(ctx, g.cfg.WhisperURL, g.cfg.WhisperModel, audioPath, "audio/mpeg", langs)
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

func downloadFile(ctx context.Context, url, dest, secret string, maxBytes int64) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
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
	body := io.Reader(resp.Body)
	if maxBytes > 0 {
		body = io.LimitReader(resp.Body, maxBytes)
	}
	_, cpErr := io.Copy(f, body)
	if closeErr := f.Close(); cpErr == nil {
		cpErr = closeErr
	}
	if cpErr != nil {
		os.Remove(dest)
	}
	return cpErr
}

func whisperTranscribe(ctx context.Context, baseURL, model, path, mime string, langs []string) string {
	if len(langs) == 0 {
		langs = []string{""}
	}
	var results []string
	for _, lang := range langs {
		if t := transcribeOnce(ctx, baseURL, model, path, lang, mime); t != "" {
			results = append(results, t)
		}
	}
	return strings.Join(results, "\n")
}

func transcribeOnce(ctx context.Context, baseURL, model, path, lang, mime string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	url := baseURL + "/v1/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, f)
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", mime)
	q := req.URL.Query()
	q.Set("model", model)
	if lang != "" {
		q.Set("language", lang)
	}
	req.URL.RawQuery = q.Encode()
	resp, err := httpClient.Do(req)
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

var escOriginRe = regexp.MustCompile(`<escalation_origin\s[^/]*folder="([^"]+)"[^/]*jid="([^"]+)"[^/]*/>`)

func escalationWorker(prompt string) string {
	m := escOriginRe.FindStringSubmatch(prompt)
	if m == nil {
		return ""
	}
	return m[1]
}

func (g *Gateway) resolveGroup(msg core.Message) (core.Group, bool) {
	if folder, ok := strings.CutPrefix(msg.ChatJID, "web:"); ok {
		return g.store.GroupByFolder(folder)
	}
	// Bare folder paths (no `:`) address groups directly when the path
	// matches a registered group; otherwise fall through to route lookup
	// so external JIDs lacking `:` (e.g. test fixtures) still resolve.
	if !strings.Contains(msg.ChatJID, ":") {
		if gr, ok := g.store.GroupByFolder(msg.ChatJID); ok {
			return gr, true
		}
	}
	folder := router.ResolveRoute(msg, g.store.AllRoutes())
	if folder != "" {
		return g.store.GroupByFolder(folder)
	}
	return core.Group{}, false
}

// folderForJid maps a chat JID to its destination group folder so the
// queue can serialize concurrent runs by folder. Returns "" when no
// folder can be determined (queue then falls back to JID-only guard).
func (g *Gateway) folderForJid(jid string) string {
	if folder, ok := strings.CutPrefix(jid, "web:"); ok {
		if _, exists := g.store.GroupByFolder(folder); exists {
			return folder
		}
		return ""
	}
	if !strings.Contains(jid, ":") {
		if _, exists := g.store.GroupByFolder(jid); exists {
			return jid
		}
	}
	return g.store.DefaultFolderForJID(jid)
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

	name := content[1:]
	switch content[0] {
	case '@':
		if name == "" {
			g.store.SetStickyGroup(chatJid, "")
			g.sendMessage(chatJid, "routing reset to default")
			return true
		}
		if _, ok := g.store.GroupByFolder(name); !ok {
			g.sendMessage(chatJid, fmt.Sprintf("Failed: group %q not found", name))
			return true
		}
		g.store.SetStickyGroup(chatJid, name)
		g.sendMessage(chatJid, fmt.Sprintf("routing → %s", name))
		return true
	case '#':
		g.store.SetStickyTopic(chatJid, name)
		if name == "" {
			g.sendMessage(chatJid, "topic reset to default")
		} else {
			g.sendMessage(chatJid, fmt.Sprintf("topic → %s", name))
		}
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
		if err := g.delegateViaMessage(childFolder, stripped, chatJid, 0); err != nil {
			slog.Warn("@prefix: delegate failed",
				"jid", chatJid, "sender", msg.Sender, "target", childFolder, "err", err)
		}
		return true
	}
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
	ts := msgs[len(msgs)-1].Timestamp
	// Include any messages steered into the container during its run.
	g.mu.Lock()
	if st, ok := g.steeredTs[chatJid]; ok && st.After(ts) {
		ts = st
	}
	delete(g.steeredTs, chatJid)
	g.mu.Unlock()
	g.store.SetAgentCursor(chatJid, ts)
}

func (g *Gateway) recordSteeredTs(chatJid string, msgs []core.Message) {
	if len(msgs) == 0 {
		return
	}
	ts := msgs[len(msgs)-1].Timestamp
	g.mu.Lock()
	if cur, ok := g.steeredTs[chatJid]; !ok || ts.After(cur) {
		g.steeredTs[chatJid] = ts
	}
	g.mu.Unlock()
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

	cursor := g.store.GetAgentCursor(chatJid)
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
	if worker := escalationWorker(prompt); worker != "" {
		fwdFrom = worker
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
		ChatJID:       targetFolder,
		Sender:        "delegate",
		Content:       prompt,
		Timestamp:     time.Now(),
		ForwardedFrom: fwdFrom,
	})
	g.queue.EnqueueMessageCheck(targetFolder)
	return nil
}

func (g *Gateway) recoverPendingMessages() {
	for _, jid := range g.store.PendingChatJIDs(g.cfg.Name) {
		slog.Info("recovering pending messages", "jid", jid)
		g.queue.EnqueueMessageCheck(jid)
	}
}

func (g *Gateway) beginTurnRun(folder string, onOutput func(result, status string)) *turnState {
	st := &turnState{onOutput: onOutput}
	g.turnsMu.Lock()
	g.inFlightTurns[folder] = st
	g.turnsMu.Unlock()
	return st
}

func (g *Gateway) endTurnRun(folder string) {
	g.turnsMu.Lock()
	delete(g.inFlightTurns, folder)
	g.turnsMu.Unlock()
}

func (g *Gateway) handleSubmitTurn(folder string, t ipc.TurnResult) error {
	recorded, err := g.store.RecordTurnResult(folder, t.TurnID, t.SessionID, t.Status)
	if err != nil {
		return fmt.Errorf("record turn: %w", err)
	}
	if !recorded {
		slog.Info("submit_turn duplicate, ignoring",
			"folder", folder, "turn_id", t.TurnID, "status", t.Status)
		return nil
	}

	// Spec 5/34: write cost_log rows when the agent reports per-model usage.
	// Empty Models is the pre-cutover case; recordTurnCost no-ops.
	g.recordTurnCost(folder, t.CallerSub, t.Models)

	g.publishRoundDone(folder, t.TurnID, t.Status, t.Error)

	g.turnsMu.Lock()
	st := g.inFlightTurns[folder]
	g.turnsMu.Unlock()
	if st == nil {
		slog.Warn("submit_turn with no in-flight run",
			"folder", folder, "turn_id", t.TurnID)
		return nil
	}
	if t.SessionID != "" {
		st.newSessionID = t.SessionID
	}
	if t.Error != "" {
		st.lastError = t.Error
	}
	if st.onOutput != nil && t.Result != "" {
		st.onOutput(t.Result, t.Status)
	}
	return nil
}

// publishRoundDone notifies the web channel that a round closed so /sse
// streams subscribed via the slink round-handle protocol can emit a
// terminal round_done event and disconnect. No-op for chats that aren't
// served by webd.
func (g *Gateway) publishRoundDone(folder, turnID, status, errMsg string) {
	ch := g.findChannelForJID("web:" + folder)
	if ch == nil {
		return
	}
	hc, ok := ch.(*chanreg.HTTPChannel)
	if !ok {
		return
	}
	if err := hc.PostRoundDone(folder, turnID, status, errMsg); err != nil {
		slog.Warn("round_done publish failed",
			"folder", folder, "turn_id", turnID, "err", err)
	}
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
