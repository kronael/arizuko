package routd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/retry"
	"github.com/kronael/arizuko/router"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

// Runner dispatches a rendered batch to runed (the POST /v1/runs contract);
// tests inject a stub.
type Runner interface {
	Run(ctx context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error)
}

// RunStopper is the operator-kill path (/stop). Separate from Runner so
// spawn-only test stubs need not implement it; a runner without it reports
// no-active-container.
type RunStopper interface {
	StopFolder(ctx context.Context, folder, turnID string) (runedv1.StopRunResponse, error)
}

type RunKiller interface {
	Kill(ctx context.Context, runID string) error
}

// Loop is routd's orchestration loop: poll for new messages, resolve each
// chat's owning group, dispatch a run to runed through the per-folder queue.
// It is the SOLE driver of turns; routd is the sole appender.
type Loop struct {
	db         *DB
	q          *turnControl
	runner     Runner
	deliver    Deliverer
	pollEvery  time.Duration
	runTimeout time.Duration

	// Wait reads done so a caller holds the DB open until the last poll has
	// finished (BUGS L160).
	done chan struct{}

	srv     *Server
	folders *groupfolder.Resolver

	instanceName    string
	observeMessages int
	observeChars    int
	groupsDir       string

	pendingElevation sync.Map
	engagementTTL    time.Duration

	// waiting is the chats whose typing is on while no run owns it: parked
	// behind another run, or refused Busy. Value is the presence that shows it.
	// A run's start hands it to the run; a cursor advance without a run clears it.
	waiting sync.Map

	nextRetryAt time.Time
	nextTurnsAt time.Time
	nextGCAt    time.Time

	costCapsEnabled bool

	gateway *modelGateway

	media mediaConfig

	sessionIdle time.Duration

	onbod OnbodClient

	onboardingEnabled   bool
	onboardingPlatforms []string
}

// SetOnbodClient wires the onbod federation for the /invite + /gate commands.
func (l *Loop) SetOnbodClient(c OnbodClient) { l.onbod = c }

// LoopConfig wires the Loop. RunTimeout is the hard deadline routd applies
// to a run (RUNED_RUN_TIMEOUT); 0 leaves it to the runner's context.
type LoopConfig struct {
	PollEvery  time.Duration
	RunTimeout time.Duration
	MaxAgents  int
	IpcDir     string

	// Prompt-envelope inputs (buildAgentPrompt). The observe window is the cfg
	// default; folder group/route rows override it.
	InstanceName          string
	ObserveWindowMessages int
	ObserveWindowChars    int

	// nil = no redelivery / no failure notice (pure REST tests).
	Deliver Deliverer

	GroupsDir string

	// CostCapsEnabled toggles the cap reconcile at the gateway (COST_CAPS_ENABLED).
	// Default-on at the cmd layer.
	CostCapsEnabled bool

	// The model gateway pair (MODEL_GATEWAY_URL, MODEL_GATEWAY_KEY). With a
	// URL, every turn mints its own virtual key and the gateway's measurement
	// is the ledger (routd/README.md); empty leaves the container holding the
	// model credential and the agent reporting its own spend.
	GatewayURL string
	GatewayKey string

	// Media is the inbound enrichment config (MEDIA_*/WHISPER_*/VOICE_/VIDEO_
	// env); the zero value leaves media un-downloaded and voice un-transcribed.
	Media mediaConfig

	// SessionIdle is the spawn-time stale-session reset threshold
	// (SESSION_IDLE_EXPIRY); zero → sessionIdleExpiry.
	SessionIdle time.Duration

	// Chat-initiated onboarding (ONBOARDING_ENABLED / ONBOARDING_PLATFORMS);
	// empty platforms = all.
	OnboardingEnabled   bool
	OnboardingPlatforms []string

	// EngagementTTL (ENGAGEMENT_TTL) is the window an @name delegation opens for
	// its target; zero opens none.
	EngagementTTL time.Duration
}

// NewLoop builds the Loop and its per-folder queue; folder-for-jid maps a
// chat JID to its routed folder so folder-exclusivity holds across JIDs.
func NewLoop(db *DB, runner Runner, cfg LoopConfig) *Loop {
	if cfg.PollEvery == 0 {
		cfg.PollEvery = 2 * time.Second
	}
	if cfg.MaxAgents == 0 {
		cfg.MaxAgents = 5
	}
	if cfg.SessionIdle == 0 {
		cfg.SessionIdle = sessionIdleExpiry
	}
	l := &Loop{
		db:              db,
		done:            make(chan struct{}),
		runner:          runner,
		deliver:         cfg.Deliver,
		pollEvery:       cfg.PollEvery,
		runTimeout:      cfg.RunTimeout,
		instanceName:    cfg.InstanceName,
		observeMessages: cfg.ObserveWindowMessages,
		observeChars:    cfg.ObserveWindowChars,
		groupsDir:       cfg.GroupsDir,
		costCapsEnabled: cfg.CostCapsEnabled,
		gateway:         newGatewayIfConfigured(cfg.GatewayURL, cfg.GatewayKey),
		media:           cfg.Media,
		sessionIdle:     cfg.SessionIdle,
		folders:         &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir},

		onboardingEnabled:   cfg.OnboardingEnabled,
		onboardingPlatforms: cfg.OnboardingPlatforms,
		engagementTTL:       cfg.EngagementTTL,
	}
	l.q = newTurnControl(cfg.MaxAgents)
	l.q.SetProcessMessagesFn(l.processGroupMessages)
	l.q.SetParkedFn(l.noteParked)
	return l
}

// BindServer closes the Loop↔Server cycle so runTurn can stand up the
// per-turn MCP socket.
func (l *Loop) BindServer(s *Server) { l.srv = s }

func (l *Loop) recentSessions(folder string, n int) []core.SessionRecord {
	if l.srv == nil {
		return nil
	}
	return l.srv.recentSessions(folder, n)
}

// Enqueue schedules a chat for processing (ingress calls it after a
// PutMessage). The queue serializes per folder and collapses duplicates.
func (l *Loop) Enqueue(chatJID string) {
	if l == nil || l.q == nil {
		return
	}
	l.q.mu.Lock()
	active := l.q.getGroup(chatJID).active
	l.q.mu.Unlock()
	if active && !strings.HasPrefix(chatJID, deferredWorkPrefix) {
		if _, err := l.processChat(chatJID, true); err != nil {
			slog.Error("process commands", "jid", chatJID, "err", err)
		}
	}
	l.q.EnqueueMessageCheck(chatJID)
}

// StopQueue halts the per-folder queue; tests use it to drive
// processGroupMessages deterministically.
func (l *Loop) StopQueue() {
	if l != nil && l.q != nil {
		l.q.Shutdown()
	}
}

// Wait blocks until Run has returned or timeout elapses. The caller must not
// close the DB before it does: Routes() then fails mid-poll and resolve()
// fails FORWARD — cursor advanced, the message dropped as a route-miss (BUGS
// L160). A timeout means a poll is genuinely stuck and the DB still in use.
func (l *Loop) Wait(timeout time.Duration) bool {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-l.done:
		return true
	case <-t.C:
		return false
	}
}

func (l *Loop) publishRoundDone(chatJID, turnID string) {
	if l == nil || l.deliver == nil {
		return
	}
	_ = l.deliver.RoundDone(chatJID, turnID, "success", "")
}

// Run drives the maintenance loop until ctx is cancelled. On start it recovers
// interrupted turns, then sweeps the chats once (sweepChats).
func (l *Loop) Run(ctx context.Context) {
	defer close(l.done)
	l.sweepOrphanSocks()
	l.recoverPending()
	if n, err := l.db.RecoverFiringTasks(); err != nil {
		slog.Warn("recover firing tasks", "err", err)
	} else if n > 0 {
		slog.Info("re-armed scheduled tasks stranded in 'firing'", "count", n)
	}
	if l.srv != nil {
		scheduleDone := make(chan struct{})
		go func() { defer close(scheduleDone); l.srv.ScheduleLoop(ctx) }()
		defer func() { <-scheduleDone }()
	}
	now := time.Now()
	l.nextRetryAt = now.Add(outboundRetryEvery)
	l.nextTurnsAt = now.Add(turnsEvery)
	l.sweepChats(ctx)
	l.nextGCAt = now.Add(gcEvery)
	t := time.NewTicker(l.pollEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			l.q.Shutdown()
			return
		case <-t.C:
			attachments, err := l.db.store().DueAttachments(ctx, time.Now())
			if err != nil {
				slog.Warn("read attachment retries", "err", err)
			} else {
				for _, attachment := range attachments {
					l.q.EnqueueMessageCheck(attachment.Message.ChatJID)
				}
			}
			l.srv.flushProgress(time.Now())
			l.maybeRecoverTurns(time.Now())
			l.maybeRetryOutbound(time.Now())
			l.maybeGC(time.Now())
		}
	}
}

func (l *Loop) sweepOrphanSocks() {
	base := l.folders.IpcDir
	if base == "" {
		return
	}
	subs, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, s := range subs {
		if s.IsDir() {
			_ = os.Remove(groupfolder.IpcSocket(filepath.Join(base, s.Name())))
		}
	}
}

const (
	outboundRetryEvery = 30 * time.Second

	turnsEvery = 30 * time.Second
	gcEvery    = time.Hour
)

// maybeRetryOutbound retries due rows; adapters deduplicate on message ID.
func (l *Loop) maybeRetryOutbound(now time.Time) {
	if l.deliver == nil || now.Before(l.nextRetryAt) {
		return
	}
	l.nextRetryAt = now.Add(outboundRetryEvery)
	l.recoverStatuses()
	rows, err := l.db.PendingOutbound(now, 200)
	if err != nil {
		slog.Warn("retry outbound: list pending", "err", err)
		return
	}
	for _, p := range rows {
		if l.retryProgress(now, p) {
			continue
		}
		l.retryOutbound(now, p)
	}
}

func (l *Loop) retryOutbound(now time.Time, p store.PendingMessage) {
	m := p.Message
	if !p.ExpiresAt.IsZero() && !now.Before(p.ExpiresAt) {
		if err := l.db.DeadLetter(p, p.Attempts); err != nil {
			slog.Error("expire outbound", "id", m.ID, "err", err)
		}
		return
	}
	if now.Before(p.NextAttemptAt) {
		return
	}
	place := deliveryPlace{jid: m.ChatJID, threadID: m.Topic}
	if tc, ok := l.db.GetTurnContext(m.TurnID); ok {
		if m.Verb == "reply" && m.ChatJID == tc.ChatJID {
			place.jid = returnTarget(tc, m.ChatJID)
		}
		if m.Topic != "" && tc.Topic == "" &&
			(m.ChatJID == tc.ChatJID || m.ChatJID == returnTarget(tc, tc.ChatJID)) {
			place.threadID, place.threadRoot = "", m.Topic
		}
	}
	pid, derr := l.db.sendOutbound(m.RoutedTo, func() (string, error) {
		return l.deliver.Send(place.jid, m.Content, m.ReplyToID, place.threadID, place.threadRoot, m.ID, m.Title)
	})
	switch derr {
	case nil:
		if err := l.db.MarkDelivered(m, pid); err != nil {
			slog.Error("retry outbound: mark sent", "id", m.ID, "err", err)
		}
	default:
		failure := retry.FromError(derr, time.Now())
		attempts, dead, err := l.db.RecordDeliveryFailure(p, failure)
		if err != nil {
			slog.Error("retry outbound: count attempt", "id", m.ID, "err", err)
			return
		}
		if dead {
			slog.Error(retry.Notice("Delivery failed", retry.Decision{Stop: true}), "id", m.ID, "jid", m.ChatJID, "attempts", attempts, "err", derr)
		} else {
			slog.Warn("retry outbound: delivery failed", "id", m.ID, "jid", m.ChatJID, "attempts", attempts, "err", derr)
		}
	}
}

func (l *Loop) maybeGC(now time.Time) {
	if now.Before(l.nextGCAt) {
		return
	}
	l.nextGCAt = now.Add(gcEvery)
	if l.runTimeout > 0 {
		if n, err := l.db.SweepExpiredRunning(l.runTimeout); err != nil {
			slog.Warn("gc: sweep expired running", "err", err)
		} else if n > 0 {
			slog.Info("gc: swept stale running turns", "count", n)
		}
	}

	if n, err := l.db.SweepActivity(now); err != nil {
		slog.Warn("gc: sweep the activity feed", "err", err)
	} else if n > 0 {
		slog.Info("gc: swept the activity feed", "count", n)
	}
}

func (l *Loop) recoverPending() {
	if err := l.q.recover(l, true); err != nil {
		slog.Error("recover deferred turns", "err", err)
		return
	}
	l.q.tick(l)
}

func (l *Loop) folderForJid(chatJID string) string {
	if turnID, ok := strings.CutPrefix(chatJID, deferredWorkPrefix); ok {
		tc, _ := l.db.GetTurnContext(turnID)
		return tc.Folder
	}
	msgs, b, err := l.pendingBatch(chatJID, l.db.GetAgentCursor(chatJID), pollBatch)
	if err != nil || newestUserIdx(msgs) < 0 {
		return chatJID
	}
	if !b.ok {
		return chatJID
	}
	return b.Folder
}

const pollBatch = 200

func (l *Loop) maybeRecoverTurns(now time.Time) {
	if now.Before(l.nextTurnsAt) {
		return
	}
	l.nextTurnsAt = now.Add(turnsEvery)
	l.q.tick(l)
}

func (l *Loop) sweepChats(ctx context.Context) {
	jids, err := l.db.ChatJIDs()
	for err != nil {
		slog.Error("sweep chats: list the chats", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(turnsEvery):
		}
		jids, err = l.db.ChatJIDs()
	}
	for _, chatJID := range jids {
		msgs, err := l.db.NewMessagesForChat(chatJID, l.db.GetAgentCursor(chatJID), 1)
		if err != nil {
			slog.Error("sweep chats: read a chat; its next message wakes it",
				"jid", chatJID, "err", err)
		} else if len(msgs) > 0 {
			l.Enqueue(chatJID)
		}
	}
}

// routeMiss is the ONLY miss handler: two copies drifted, and a new chat's
// first message vanished without onboarding (krons 2026-07-12). The cursor
// advances past the batch either way — a re-fed miss would replay forever.
func (l *Loop) routeMiss(chatJID string, msgs []core.Message, r resolution) {
	last := msgs[len(msgs)-1]
	if r.Observe != "" {

		l.observeRows(chatJID, r.Observe, msgs)
	} else {

		if head, _ := lookupCommand(last.Content); head == "/chatid" &&
			!last.BotMsg && !strings.HasPrefix(last.Sender, "timed-") {
			l.ack(chatJID, chatJID)
		}
		if l.onboardingEnabled && l.onbod != nil {

			discordGuild := strings.HasPrefix(chatJID, "discord:") &&
				!strings.HasPrefix(chatJID, "discord:dm/")
			if onboardingAllowed(chatJID, l.onboardingPlatforms) &&
				(!discordGuild || last.Verb == "mention") {
				created, err := l.onbod.InsertOnboarding(chatJID, last.Sender)
				if created {
					l.noticeOnboarding(chatJID, last.Sender)
				}
				if err != nil {

					slog.Error("insert onboarding failed", "jid", chatJID, "err", err)
					if l.deliver != nil {
						_, _ = l.deliver.Send(chatJID, onboardingFailedNotice, "", "", "", "onboard-"+last.ID, "")
					}
				}
			}
		}
	}
	l.advance(chatJID, last)
}

func (l *Loop) noticeOnboarding(chatJID, sender string) {
	text := fmt.Sprintf("New onboarding request: %s in chat %s. Approve or deny it on the onbod dashboard, /dash/onbod/.", sender, chatJID)
	if _, err := l.db.QueueIssueNotice(text); err != nil {
		slog.Warn("onboarding notice", "jid", chatJID, "err", err)
	}
}

func (l *Loop) observeRows(chatJID, folder string, rows []core.Message) {
	for i := range rows {
		if l.media.Enabled {

			_ = l.enrichAttachments(context.Background(), &rows[i], folder, false)
		}
	}
	if err := l.db.MarkMessagesObserved(folder, rows); err != nil {
		slog.Warn("mark observed", "jid", chatJID, "folder", folder, "err", err)
	}
}

const onboardingFailedNotice = "⚠️ Couldn't register this chat for setup — please try again later."

type resolution struct {
	Folder      string
	Topic       string
	TopicPinned bool
	Observe     string
	// Addressed: a person put a question to this folder and is owed an answer
	// (spec 6/47 §"Addressed"). Declared here once and stored on the turn.
	Addressed bool
	// AtName: the message's leading @name selected Folder. The turn that takes
	// the row drops the prefix and engages the chat and topic for Folder.
	AtName bool
	ok     bool
}

func (l *Loop) resolve(chatJID string, last core.Message) resolution {
	return l.resolveRows(chatJID, []core.Message{last})[0]
}

type batchResolution struct {
	resolution
	rows []resolution
}

func (l *Loop) resolveBatch(chatJID string, msgs []core.Message) batchResolution {
	var users []core.Message
	var at []int
	for i, m := range msgs {
		if !m.BotMsg {
			users = append(users, m)
			at = append(at, i)
		}
	}
	rows := l.resolveRows(chatJID, users)
	b := batchResolution{
		resolution: rows[len(rows)-1],
		rows:       make([]resolution, len(msgs)),
	}
	picked := false
	for k := len(rows) - 1; k >= 0; k-- {
		b.rows[at[k]] = rows[k]
		if rows[k].ok && !picked {
			b.resolution, picked = rows[k], true
		}
	}
	return b
}

func (l *Loop) resolveRows(chatJID string, rows []core.Message) []resolution {
	out := make([]resolution, len(rows))

	direct := l.directFolder(chatJID)
	isDirect := direct != "" && l.db.GroupExists(direct)
	var routes []core.Route
	routesOK := false
	if !isDirect {
		var err error
		routes, err = l.db.Routes()
		if err != nil {

			slog.Error("resolve: route table read failed; message dropped as route-miss",
				"jid", chatJID, "err", err)
		}
		routesOK = err == nil
	}
	reads := &chatReads{db: l.db, chatJID: chatJID}
	for i, m := range rows {
		if isDirect {

			out[i] = resolution{Folder: direct, ok: true, Addressed: core.JidPlatform(chatJID) == "web"}
		} else {
			out[i] = l.resolveFolder(reads, m, routes, routesOK)
		}
		if isSystemTrigger(m.Sender) {
			out[i].Addressed = false
		}
	}
	return out
}

func (l *Loop) resolveFolder(reads *chatReads, last core.Message, routes []core.Route, routesOK bool) resolution {

	probe := last
	_, probe.Content = typedText(last.Content)
	var row core.Route
	var rt core.RouteTarget
	if routesOK {
		var target string
		row, target, _ = router.MatchRoute(probe, routes)
		rt = core.ParseRouteTarget(target)
	}
	name, _, hasAt := leadingAt(last.Content)
	hasAt = hasAt && !last.BotMsg && !isSystemTrigger(last.Sender)
	atRow := router.HasAt(row)
	if hasAt && atRow && rt.Folder != "" && rt.Mode != "observe" && rt.Mode != "engaged" {
		return resolution{Folder: rt.Folder, Topic: last.Topic, TopicPinned: rt.HasTopic, ok: true, Addressed: true, AtName: true}
	}
	res := l.routedFolder(reads, last, rt)
	if hasAt && !atRow {
		base := res.Folder
		if base == "" {
			base = res.Observe
		}
		if t := l.findNamedGroup(base, name); base != "" && t != "" {
			return resolution{Folder: t, ok: true, Addressed: true, AtName: true}
		}
	}
	return res
}

func (l *Loop) routedFolder(reads *chatReads, last core.Message, rt core.RouteTarget) resolution {

	if rt.Folder != "" {
		engTopic := reads.effectiveTopic(last.Topic)
		if rt.HasTopic {
			engTopic = last.Topic
		}

		if rt.Mode == "engaged" {
			if folder, ok := reads.engagedFolder(engTopic); ok {
				return resolution{Folder: folder, Topic: last.Topic, TopicPinned: rt.HasTopic, ok: true}
			}
			return resolution{Observe: rt.Folder, Topic: last.Topic, TopicPinned: rt.HasTopic}
		}

		addressed := rt.Mode != "observe"
		if folder, ok := reads.engaged(engTopic, last); ok {
			return resolution{Folder: folder, Topic: last.Topic, TopicPinned: rt.HasTopic, ok: true, Addressed: addressed}
		}
		if rt.Mode == "observe" {
			return resolution{Observe: rt.Folder, Topic: last.Topic, TopicPinned: rt.HasTopic}
		}
		return resolution{Folder: rt.Folder, Topic: last.Topic, TopicPinned: rt.HasTopic, ok: true, Addressed: true}
	}

	engTopic := reads.effectiveTopic(last.Topic)
	if folder, ok := reads.engaged(engTopic, last); ok {
		return resolution{Folder: folder, ok: true}
	}
	return resolution{}
}

type chatReads struct {
	db      *DB
	chatJID string
	pin     *string

	folders map[string]string
}

func (r *chatReads) effectiveTopic(msgTopic string) string {
	if r.pin == nil {
		_, sticky := r.db.StickyState(r.chatJID)
		r.pin = &sticky
	}
	if *r.pin != "" {
		return *r.pin
	}
	return msgTopic
}

func (r *chatReads) engaged(topic string, last core.Message) (string, bool) {
	if last.Verb != "" && last.Verb != "message" && last.Verb != "mention" {
		return "", false
	}

	// A reply to a stored message that is not the bot's and does not mention it
	// is addressed to that message's author (BUGS L652): an observation, not a
	// turn. An explicit mention (verb=mention) still rides.
	if last.Verb != "mention" && last.ReplyToID != "" {
		if parent, ok := r.db.MessageByID(r.chatJID, last.ReplyToID); ok && !parent.AddressesBot() {
			return "", false
		}
	}
	return r.engagedFolder(topic)
}

func (r *chatReads) engagedFolder(topic string) (string, bool) {
	if folder, read := r.folders[topic]; read {
		return folder, folder != ""
	}
	folder, ok := r.db.Engaged(r.chatJID, topic)
	if !ok && topic != "" {
		stopped, err := r.db.store().ThreadDisengaged(r.chatJID, topic)
		if err != nil {
			slog.Error("read thread engagement", "jid", r.chatJID, "topic", topic, "err", err)
			r.remember(topic, "")
			return "", false
		}
		if !stopped {
			folder, ok = r.engagedFolder("")
		}
	}
	if !ok {
		folder = ""
	}
	r.remember(topic, folder)
	return folder, folder != ""
}

func (r *chatReads) remember(topic, folder string) {
	if r.folders == nil {
		r.folders = map[string]string{}
	}
	r.folders[topic] = folder
}

// directFolder returns the folder a JID directly addresses, or "". For a
// web:/hook: JID the mint RECORDS the delivery folder (ROUTING.md) and the record
// is read FIRST: the grammar cannot recover it once a suffix is added —
// `web:t/sfx` reads as `t/sfx`, `hook:a/b/c` as `a/b` (BUGS L4, L1) — and
// reading it only after a grammar miss delivers `hook:a/b/c` to an existing
// folder `a/b`, the wrong tenant. The grammar is the fallback for a token
// minted before the column existed.
func (l *Loop) directFolder(jid string) string {
	if folder := groupfolder.JidFolder(jid); folder != "" {
		if target := l.db.RouteTokenTarget(jid); target != "" {
			return target
		}
		return folder
	}
	if !strings.Contains(jid, ":") {
		return jid
	}
	return ""
}

func (l *Loop) advance(chatJID string, last core.Message) {
	// Any cursor advance is progress past the batch a Busy refusal parked.
	if reply, ok := l.waiting.LoadAndDelete(chatJID); ok {
		p := reply.(presence)
		l.pauseProgress(p.jid, p.topic, "")
	}
	if err := l.db.store().AdvanceAgentCursor(chatJID, store.MessageCursor(last)); err != nil {
		slog.Error("advance chat cursor", "jid", chatJID, "err", err)
	}
}

const sessionIdleExpiry = 2 * 24 * time.Hour

func (l *Loop) sessionIdleExpired(chatJID string) bool {
	cur := l.db.GetAgentCursor(chatJID)
	if cur == "" {
		return false
	}

	t, ok := store.CursorInstant(cur)
	if !ok || t.IsZero() {
		return false
	}
	return time.Since(t) > l.sessionIdle
}

func onboardingAllowed(jid string, platforms []string) bool {
	if core.JidPlatform(jid) == "" {
		return false
	}
	if len(platforms) == 0 {
		return true
	}
	p, _, _ := strings.Cut(jid, ":")
	return slices.Contains(platforms, p)
}
