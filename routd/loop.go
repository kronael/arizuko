package routd

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/queue"
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
	StopFolder(ctx context.Context, folder string) (runedv1.StopRunResponse, error)
}

// Loop is routd's orchestration loop: poll for new messages, resolve each
// chat's owning group, dispatch a run to runed through the per-folder queue.
// It is the SOLE driver of turns; routd is the sole appender.
type Loop struct {
	db         *DB
	q          *queue.GroupQueue
	runner     Runner
	deliver    Deliverer
	pollEvery  time.Duration
	runTimeout time.Duration

	// Wait reads done so a caller holds the DB open until the last poll has
	// finished (BUGS L160).
	done chan struct{}

	// Set post-construction via BindServer (Loop↔Server is a deliberate
	// cycle); runTurn skips the socket when srv is nil.
	srv     *Server
	folders *groupfolder.Resolver

	// Prompt-envelope inputs (buildAgentPrompt).
	instanceName    string
	observeMessages int
	observeChars    int
	groupsDir       string

	// zero-value cfg (Enabled=false) → the scan is never driven.
	proactive     ProactiveConfig
	modes         *modeCache
	pendingReason sync.Map // turn_id → proactiveResult
	// pendingElevation is the ONLY source of Elevated: cmdRoot (gated on `**`)
	// writes it, an agent/user cannot forge a map entry, so root is never
	// reached by message content. One-shot (LoadAndDelete per turn).
	pendingElevation sync.Map // message_id → struct{}
	nextScanAt       time.Time

	// busyWait backs off the re-dispatch of a batch nothing consumed; without
	// it every 2s re-feed does the FULL pre-dispatch setup before runed refuses
	// again (BUGS L55). A new inbound bypasses it; any cursor advance clears it.
	busyWait sync.Map // chatJID → busyState

	// Loop-driven maintenance cadence, no free tickers.
	nextRetryAt time.Time
	nextGCAt    time.Time

	// False bypasses the pre-spawn budget gate — the operator escape hatch.
	costCapsEnabled bool

	// Non-nil: every turn mints its own virtual key and the GATEWAY is the
	// ledger; arizuko stores no spend of its own (specs 6/23, 6/39).
	gateway *modelGateway

	media mediaConfig

	sessionIdle time.Duration

	// nil → the /invite + /gate commands report the federation gap.
	onbod OnbodClient

	// onbod OWNS the onboarding table; routd federates the insert over HTTP.
	onboardingEnabled   bool
	onboardingPlatforms []string

	defaultModel  string
	defaultEffort string

	maxTurnRetry int

	maxOutboundRetry int
}

// SetOnbodClient wires the onbod federation for the /invite + /gate commands.
func (l *Loop) SetOnbodClient(c OnbodClient) { l.onbod = c }

// LoopConfig wires the Loop. RunTimeout is the hard deadline routd applies
// to a run (RUNED_RUN_TIMEOUT); 0 leaves it to the runner's context.
type LoopConfig struct {
	PollEvery  time.Duration
	RunTimeout time.Duration
	MaxRuns    int
	IpcDir     string

	// Prompt-envelope inputs (buildAgentPrompt). The observe window is the cfg
	// default; folder group/route rows override it.
	InstanceName          string
	ObserveWindowMessages int
	ObserveWindowChars    int

	// nil = no redelivery / no failure notice (pure REST tests).
	Deliver Deliverer

	// GroupsDir is where per-group CLAUDE.md lives (the proactive-mode source).
	Proactive ProactiveConfig
	GroupsDir string

	// CostCapsEnabled toggles the pre-spawn budget gate (COST_CAPS_ENABLED).
	// Default-on at the cmd layer.
	CostCapsEnabled bool

	// The model gateway pair (MODEL_GATEWAY_URL, MODEL_GATEWAY_KEY). With a
	// URL, every turn mints its own virtual key and the gateway's measurement
	// is the ledger (specs 6/23, 6/39); empty leaves the container holding the
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

	// DefaultModel (ARIZUKO_DEFAULT_MODEL) applies when a group has no model;
	// empty leaves the container's ant SDK default.
	DefaultModel string
	// DefaultEffort (ARIZUKO_DEFAULT_EFFORT): low|medium|high|xhigh|max; empty
	// leaves the harness CLI's own default.
	DefaultEffort string

	// MaxTurnRetry bounds retries of a turn that fails without a reply
	// (SIGKILL/OOM/timeout). Default 3.
	MaxTurnRetry int

	// MaxOutboundRetry bounds the transient delivery failures a pending bot
	// row absorbs before dead-letter. Default 10; -1 dead-letters on the first.
	MaxOutboundRetry int
}

// NewLoop builds the Loop and its per-folder queue; folder-for-jid maps a
// chat JID to its routed folder so folder-exclusivity holds across JIDs.
func NewLoop(db *DB, runner Runner, cfg LoopConfig) *Loop {
	if cfg.PollEvery == 0 {
		cfg.PollEvery = 2 * time.Second
	}
	if cfg.MaxRuns == 0 {
		cfg.MaxRuns = 5
	}
	if cfg.SessionIdle == 0 {
		cfg.SessionIdle = sessionIdleExpiry
	}
	maxRetry := cfg.MaxTurnRetry
	if maxRetry < 0 {
		maxRetry = 0 // explicit disable via -1
	} else if maxRetry == 0 {
		maxRetry = 3
	}
	maxOutRetry := cfg.MaxOutboundRetry
	if maxOutRetry < 0 {
		maxOutRetry = 1 // explicit disable via -1: the first transient failure dead-letters
	} else if maxOutRetry == 0 {
		maxOutRetry = defaultMaxOutboundRetry
	}
	l := &Loop{
		db:              db,
		done:            make(chan struct{}),
		runner:          runner,
		deliver:         cfg.Deliver,
		pollEvery:       cfg.PollEvery,
		runTimeout:      cfg.RunTimeout,
		proactive:       cfg.Proactive,
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
		defaultModel:        cfg.DefaultModel,
		defaultEffort:       cfg.DefaultEffort,
		maxTurnRetry:        maxRetry,
		maxOutboundRetry:    maxOutRetry,
	}
	if cfg.Proactive.Enabled {
		l.modes = newModeCache(cfg.GroupsDir)
	}
	l.q = queue.New(cfg.MaxRuns)
	l.q.SetProcessMessagesFn(l.processGroupMessages)
	l.q.SetFolderForJidFn(l.folderForJid)
	return l
}

// BindServer closes the Loop↔Server cycle so runTurn can stand up the
// per-turn MCP socket.
func (l *Loop) BindServer(s *Server) { l.srv = s }

// recentSessions federates the prior-session continuity hint through the
// bound Server; nil srv → nil ("no prior session").
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

// publishRoundDone fans a closed-turn notice to the web SSE channel, keyed on
// the chat JID verbatim.
func (l *Loop) publishRoundDone(chatJID, turnID string) {
	if l == nil || l.deliver == nil {
		return
	}
	_ = l.deliver.RoundDone(chatJID, turnID, "success", "")
}

// Run drives the poll loop until ctx is cancelled. On start it re-feeds chats
// whose turn_context is still 'running' from the un-advanced agent_cursor.
func (l *Loop) Run(ctx context.Context) {
	defer close(l.done)
	l.sweepOrphanSocks()
	l.recoverPending()
	if n, err := l.db.RecoverFiringTasks(); err != nil {
		slog.Warn("recover firing tasks", "err", err)
	} else if n > 0 {
		slog.Info("re-armed scheduled tasks stranded in 'firing'", "count", n)
	}
	if l.proactive.Enabled {
		l.nextScanAt = time.Now().Add(l.proactive.ScanInterval)
	}
	now := time.Now()
	l.nextRetryAt = now.Add(outboundRetryEvery)
	l.nextGCAt = now.Add(gcEvery)
	t := time.NewTicker(l.pollEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			l.q.Shutdown()
			return
		case <-t.C:
			l.pollOnce()
			l.maybeScanProactive(time.Now())
			l.maybeRetryOutbound(time.Now())
			l.maybeGC(time.Now())
		}
	}
}

// sweepOrphanSocks removes stale per-folder socks a crashed prior run left.
// ipc.ServeMCP also removes its sock pre-listen, so this matters only for
// folders that get no run this boot.
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

// Maintenance cadence: the retry sweep and the stale-turn GC.
const (
	outboundRetryEvery = 30 * time.Second
	outboundRetryAfter = 30 * time.Second
	gcEvery            = time.Hour

	// MAX_OUTBOUND_RETRY raises it for a flaky platform.
	defaultMaxOutboundRetry = 10

	// maxOutboundBackoff caps one retry gap: uncapped doubling spends the
	// attempts over ~17h, and a flat 30s cadence dead-lettered live traffic
	// during a routine adapter restart (BUGS L22).
	maxOutboundBackoff = time.Hour
)

// backoffDelay returns how long a pending row with attempts prior failures
// must age past its own timestamp before the sweep retries it.
func backoffDelay(attempts int) time.Duration {
	d := outboundRetryAfter
	for range attempts {
		d *= 2
		if d >= maxOutboundBackoff {
			return maxOutboundBackoff
		}
	}
	return d
}

// maybeRetryOutbound re-dispatches bot rows that still owe a delivery, each
// gated by its own backoffDelay; the adapter dedups on message_id. A permanent
// refusal dead-letters at once and spends no attempt; a transient failure
// spends one. There is no outright age bound: one killed a row the sweep had
// never tried, losing a message no platform refused.
func (l *Loop) maybeRetryOutbound(now time.Time) {
	if l.deliver == nil || now.Before(l.nextRetryAt) {
		return
	}
	l.nextRetryAt = now.Add(outboundRetryEvery)
	rows, err := l.db.PendingOutbound(now.Add(-outboundRetryAfter), 200)
	if err != nil {
		slog.Warn("retry outbound: list pending", "err", err)
		return
	}
	for _, p := range rows {
		m := p.Message
		// PendingOutbound's fixed 30s floor only bounds the youngest rows.
		if now.Sub(m.Timestamp) < backoffDelay(p.Attempts) {
			continue
		}
		place := deliveryPlace{jid: m.ChatJID, threadID: m.Topic}
		if tc, ok := l.db.GetTurnContext(m.TurnID); ok && m.Verb != store.VerbStatusSummary {
			resolved, err := placeTurnDelivery(l.db, l.deliver, tc, m.ChatJID, m.Topic, m.Topic != "")
			if err != nil {
				slog.Warn("retry outbound: resolve thread", "id", m.ID, "err", err)
				continue
			}
			place = resolved
		}
		pid, derr := l.deliver.Send(place.jid, m.Content, m.ReplyToID, place.threadID, place.threadRoot, m.ID)
		switch {
		case derr == nil:
			if err := l.db.MarkDelivered(m, pid); err != nil {
				slog.Error("retry outbound: mark sent", "id", m.ID, "err", err)
			}
		case errors.Is(derr, chanlib.ErrPermanent):
			if err := l.db.DeadLetter(m, 0); err != nil {
				slog.Error("retry outbound: dead-letter", "id", m.ID, "err", err)
			}
			slog.Error("outbound dead-lettered: platform refused the message",
				"id", m.ID, "jid", m.ChatJID, "err", derr)
		default:
			attempts, dead, err := l.db.RecordDeliveryFailure(m, l.maxOutboundRetry)
			if err != nil {
				slog.Error("retry outbound: count attempt", "id", m.ID, "err", err)
				continue
			}
			if dead {
				slog.Error("outbound dead-lettered: retries exhausted",
					"id", m.ID, "jid", m.ChatJID, "attempts", attempts, "err", derr)
			} else {
				slog.Warn("retry outbound: delivery failed",
					"id", m.ID, "jid", m.ChatJID, "attempts", attempts,
					"max", l.maxOutboundRetry, "err", derr)
			}
		}
	}
}

// maybeGC runs the hourly housekeeping.
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
	// The activity feed is an index with a retention window; it drains here.
	if n, err := l.db.SweepActivity(now); err != nil {
		slog.Warn("gc: sweep the activity feed", "err", err)
	} else if n > 0 {
		slog.Info("gc: swept the activity feed", "count", n)
	}
}

// maybeScanProactive runs one proactive sweep when due: loop-driven, no
// catch-up — a long iteration just delays the next scan.
func (l *Loop) maybeScanProactive(now time.Time) {
	if !l.proactive.Enabled || now.Before(l.nextScanAt) {
		return
	}
	l.scanProactive(now)
	l.nextScanAt = now.Add(l.proactive.ScanInterval)
}

// recoverPending re-enqueues chats with a still-running turn so a crash
// mid-turn re-attempts from the un-advanced cursor (turn_results dedups any
// submit_turn the old run delivered).
func (l *Loop) recoverPending() {
	tcs, err := l.db.RunningTurns()
	if err != nil {
		slog.Warn("recover pending turns", "err", err)
		return
	}
	for _, tc := range tcs {
		l.Enqueue(tc.ChatJID)
	}
}

// folderForJid resolves a chat JID to the folder its pending batch dispatches
// to, so the queue's folder-exclusivity (one live run per folder) holds across
// JIDs. It reads the batch the worker reads and decides it with the worker's
// resolveBatch: a key from any other row lets two chats run in one folder.
func (l *Loop) folderForJid(chatJID string) string {
	msgs, err := l.db.NewMessagesForChat(chatJID, l.db.GetAgentCursor(chatJID), pollBatch)
	if err != nil || newestUserIdx(msgs) < 0 {
		return chatJID
	}
	b := l.resolveBatch(chatJID, msgs)
	if !b.ok {
		return chatJID
	}
	return b.Folder
}

// pollBatch bounds how many of one chat's messages a single poll takes. The
// cursor is a whole message key, so a batch that stops mid-instant resumes
// exactly there.
const pollBatch = 200

// pollOnce reads each chat from its OWN watermark and enqueues each chat that
// routes; a miss advances the cursor and drops. Per-chat reads, not one read
// from the lowest cursor: measured 2026-09-12, krons returned 23,936 of its
// 24,013 rows every two seconds because one chat's cursor had not moved since
// March, against 0 rows when each chat is read from its own.
func (l *Loop) pollOnce() {
	jids, err := l.db.ChatJIDs()
	if err != nil {
		slog.Warn("poll new messages", "err", err)
		return
	}
	for _, chatJID := range jids {
		chatMsgs, err := l.db.NewMessagesForChat(chatJID, l.db.GetAgentCursor(chatJID), pollBatch)
		if err != nil {
			slog.Warn("poll new messages", "jid", chatJID, "err", err)
			continue
		}
		if len(chatMsgs) == 0 {
			continue
		}
		// An all-bot batch is the agent's own output: the worker owns that advance.
		if newestUserIdx(chatMsgs) >= 0 {
			if b := l.resolveBatch(chatJID, chatMsgs); !b.ok {
				l.routeMiss(chatJID, chatMsgs, b.resolution)
				continue
			}
		}
		// Steering happens in processGroupMessages, the one path this backstop
		// and adapter ingest converge to, so a command cannot race past it.
		l.Enqueue(chatJID)
	}
}

// routeMiss is the ONLY miss handler, shared by both processing paths: two
// copies drifted, and a new chat's first message vanished without onboarding
// (krons 2026-07-12). The cursor advances past the batch either way — a re-fed
// miss would replay forever.
func (l *Loop) routeMiss(chatJID string, msgs []core.Message, r resolution) {
	last := msgs[len(msgs)-1]
	if r.Observe != "" {
		// Observe rule: silent ingest, no turn.
		l.observeRows(chatJID, r.Observe, msgs)
	} else {
		// A route-missed /chatid still answers: it is how a NEW user self-reports
		// their JID for `arizuko group add`; the routed twin is in handleCommand.
		// It does not consume the miss — the message still queues onboarding.
		if head, _ := lookupCommand(last.Content); head == "/chatid" &&
			!last.BotMsg && !strings.HasPrefix(last.Sender, "timed-") {
			l.ack(chatJID, chatJID)
		}
		if l.onboardingEnabled && l.onbod != nil {
			// Discord guild channels onboard only on an explicit @mention, so a
			// busy server does not queue every poster.
			discordGuild := strings.HasPrefix(chatJID, "discord:") &&
				!strings.HasPrefix(chatJID, "discord:dm/")
			if onboardingAllowed(chatJID, l.onboardingPlatforms) &&
				(!discordGuild || last.Verb == "mention") {
				if err := l.onbod.InsertOnboarding(chatJID, last.Sender); err != nil {
					// Without the row the new user gets silence forever: tell the chat.
					slog.Error("insert onboarding failed", "jid", chatJID, "err", err)
					if l.deliver != nil {
						_, _ = l.deliver.Send(chatJID, onboardingFailedNotice, "", "", "", "onboard-"+last.ID)
					}
				}
			}
		}
	}
	l.advance(chatJID, last)
}

// observeRows stores rows as ambient context of folder, the one writer for the
// observe rule and for the rows a dispatch consumes without a turn of their
// own. Enriched so later turns see downloaded media, not raw URLs.
func (l *Loop) observeRows(chatJID, folder string, rows []core.Message) {
	for i := range rows {
		if l.media.Enabled {
			l.enrichAttachments(context.Background(), &rows[i], folder)
		}
	}
	if err := l.db.MarkMessagesObserved(folder, rows); err != nil {
		slog.Warn("mark observed", "jid", chatJID, "folder", folder, "err", err)
	}
}

// onboardingFailedNotice is sent to an unrouted chat when the admission-queue
// insert fails — the alternative is permanent silence.
const onboardingFailedNotice = "⚠️ Couldn't register this chat for setup — please try again later."

// resolution is the outcome of route resolution for one chat's latest message.
// ok=true → dispatch to Folder; Observe non-empty → silent ingest, no turn;
// neither → route miss.
type resolution struct {
	Folder  string
	Topic   string // route-pinned topic (rt.Topic), "" leaves the message's own
	Observe string // observe-mode folder; messages get is_observed=1, no turn
	// Addressed: a person put a question to this folder and is owed an answer
	// (spec 6/47 §"Addressed"). Declared here once and stored on the turn.
	Addressed bool
	ok        bool
}

// resolveGroup is resolve for callers that only need (folder, ok).
func (l *Loop) resolveGroup(chatJID string, last core.Message) (string, bool) {
	r := l.resolve(chatJID, last)
	return r.Folder, r.ok
}

// resolve picks the folder for the chat's latest message and records whether
// the turn is addressed. A timed or system sender is never addressed: a task
// is not a person (the same predicate turnCallerSub reads).
func (l *Loop) resolve(chatJID string, last core.Message) resolution {
	return l.resolveRows(chatJID, []core.Message{last})[0]
}

// batchResolution is the one decision on a batch of rows. The embedded
// resolution is the newest user row's that dispatches on its own, or the newest
// user row's when none does. rows[i] is msgs[i]'s own resolution (zero for a
// bot row): whether it dispatches and whether it is addressed are facts about
// the row, so each turn takes them from its own rows.
type batchResolution struct {
	resolution
	rows []resolution
}

// resolveBatch decides a batch for both processing paths: the poll backstop and
// the worker. The route table is read once. A bot row never resolves a batch,
// so msgs must hold a user row (newestUserIdx >= 0).
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

// resolveRows resolves each row on its own, reading the route table once.
func (l *Loop) resolveRows(chatJID string, rows []core.Message) []resolution {
	out := make([]resolution, len(rows))
	// 1. Direct address: a web:/hook: JID or a bare registered folder.
	direct := l.directFolder(chatJID)
	isDirect := direct != "" && l.db.GroupExists(direct)
	var routes []core.Route
	routesOK := false
	if !isDirect {
		var err error
		routes, err = l.db.Routes()
		if err != nil {
			// A read failure looks identical to a clean miss. The fail-forward
			// stays (a retry risks a poison-message loop) but the drop is LOUD.
			slog.Error("resolve: route table read failed; message dropped as route-miss",
				"jid", chatJID, "err", err)
		}
		routesOK = err == nil
	}
	reads := &chatReads{db: l.db, chatJID: chatJID}
	for i, m := range rows {
		if isDirect {
			// Only a person typing in a web chat is a question; a hook is an event
			// and a bare folder is a system turn.
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
	// 2. Route table.
	if routesOK {
		rt := router.ResolveRouteTarget(last, routes)
		if rt.Folder != "" {
			engTopic := reads.effectiveTopic(last.Topic)
			// #engaged extends engagement to the rows the rule matches, whatever
			// their verb: it fires in the engaged folder while the chat and topic
			// are engaged and is observed under the target otherwise. Never addressed.
			if rt.Mode == "engaged" {
				if folder, ok := reads.engagedFolder(engTopic); ok {
					return resolution{Folder: folder, ok: true}
				}
				return resolution{Observe: rt.Folder}
			}
			// A trigger rule addresses the folder whether or not the chat is also
			// engaged; an observe rule on an engaged chat is engagement alone.
			addressed := rt.Mode != "observe"
			if folder, ok := reads.engaged(engTopic, last.Verb); ok {
				return resolution{Folder: folder, ok: true, Addressed: addressed}
			}
			if rt.Mode == "observe" {
				return resolution{Observe: rt.Folder}
			}
			return resolution{Folder: rt.Folder, Topic: rt.Topic, ok: true, Addressed: true}
		}
	}
	// 3. Engagement fallback on a route miss (topic-root normalized).
	engTopic := reads.effectiveTopic(last.Topic)
	if folder, ok := reads.engaged(engTopic, last.Verb); ok {
		return resolution{Folder: folder, ok: true}
	}
	return resolution{}
}

// chatReads holds the engagement facts of one chat that a resolveRows call has
// read: a batch of 200 rows on one topic asks the store once, not 200 times.
// It lives for one call; nothing here is shared across calls.
type chatReads struct {
	db      *DB
	chatJID string
	pin     *string
	// folders maps a topic to its live engagement folder, "" when none is live.
	folders map[string]string
}

// effectiveTopic resolves the topic a turn dispatches under: a set sticky
// #topic overrides the message's own topic.
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

// engaged returns the live engagement folder for (chatJID, topic) when a row
// with verb rides engagement: only a person's message or mention does. An empty
// engaged folder (a stale ingress claim) is NOT a route: false, so resolve
// falls through instead of dispatching to "".
func (r *chatReads) engaged(topic, verb string) (string, bool) {
	if verb != "" && verb != "message" && verb != "mention" {
		return "", false
	}
	return r.engagedFolder(topic)
}

// engagedFolder is the live engagement folder for (chatJID, topic), whatever
// the verb of the row asking. The topic falls back to the root window unless
// the thread was disengaged.
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
// web:/hook: JID the mint RECORDS the delivery folder (spec 5/W) and the record
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

// advance moves the chat's agent_cursor past the last seen row.
func (l *Loop) advance(chatJID string, last core.Message) {
	// Any cursor advance is progress past the batch a Busy refusal parked.
	l.busyWait.Delete(chatJID)
	_ = l.db.SetAgentCursor(chatJID, store.MessageCursor(last))
}

// sessionIdleExpiry is the default spawn-time stale-session threshold;
// SESSION_IDLE_EXPIRY overrides it.
const sessionIdleExpiry = 2 * 24 * time.Hour

// sessionIdleExpired reports whether the chat's agent_cursor is older than the
// idle threshold. An empty or unparseable cursor is never expired.
func (l *Loop) sessionIdleExpired(chatJID string) bool {
	cur := l.db.GetAgentCursor(chatJID)
	if cur == "" {
		return false
	}
	// The cursor is a message key, so the instant is its lead.
	t, ok := store.CursorInstant(cur)
	if !ok || t.IsZero() {
		return false
	}
	return time.Since(t) > l.sessionIdle
}

// onCircuitBreakerOpen prunes the chat's errored rows and clears the folder's
// root session: a broken session is poison, and the next inbound gets a clean
// spawn. The chat-visible notice is runTurn's; this is the prune+reset half.
func (l *Loop) onCircuitBreakerOpen(chatJID, folder string) {
	obs.SetCircuitBreakerState(folder, 2) // 2=open (spec 5/O-observability)
	if err := l.db.DeleteErroredMessages(chatJID); err != nil {
		slog.Warn("prune errored messages failed", "jid", chatJID, "err", err)
	}
	if folder != "" {
		_ = l.db.DeleteSession(folder, "")
	}
}

// onboardingAllowed reports whether jid's platform prefix is eligible for
// chat-initiated onboarding. Empty platforms = all allowed. A JID with no
// platform prefix is a bare folder chat, the platform's own system chat, never
// a person.
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
