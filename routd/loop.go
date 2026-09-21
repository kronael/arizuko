package routd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/queue"
	"github.com/kronael/arizuko/router"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

// Runner dispatches a rendered batch to the execution plane (runed). The Loop
// calls it; production wires the runed HTTP client, tests inject a stub. routd
// renders the prompt, runed runs it (the POST /v1/runs contract).
type Runner interface {
	Run(ctx context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error)
}

// RunStopper is the operator-kill path (routd's /stop): map a folder to its
// live spawn in runed and kill it. The production *runedv1.Client satisfies it;
// it is separate from Runner so the spawn-only test stubs don't have to
// implement a method they never exercise. A runner that lacks it reports
// no-active-container (local-dev / stubs that launch no containers).
type RunStopper interface {
	StopFolder(ctx context.Context, folder string) (runedv1.StopRunResponse, error)
}

// Loop is routd's orchestration loop: poll routd.db for new messages, resolve
// each chat's owning group, and dispatch a run to runed through the per-folder
// queue. It is the SOLE driver of turns; routd is the sole appender. Single
// process per instance.
type Loop struct {
	db         *DB
	q          *queue.GroupQueue
	runner     Runner
	deliver    Deliverer
	pollEvery  time.Duration
	runTimeout time.Duration

	// done closes when Run returns. Wait reads it so a caller can hold the DB
	// open until the last poll has finished (BUGS L160).
	done chan struct{}

	// srv stands up the per-turn agent MCP socket in-process (ServeTurnMCP);
	// set post-construction via BindServer (Loop↔Server is a deliberate cycle).
	// folders resolves the per-folder ipc dir so routd and runed agree on the
	// socket path. Both nil-safe: runTurn skips the socket when srv is nil.
	srv     *Server
	folders *groupfolder.Resolver

	// Prompt-envelope inputs (buildAgentPrompt). instanceName feeds the
	// autocalls block; observeMessages/observeChars are the cfg-default
	// observe window (group + route rows override per folder); groupsDir is
	// where PERSONA.md lives.
	instanceName    string
	observeMessages int
	observeChars    int
	groupsDir       string

	// appSrcDir is the in-container source root (APP_SRC_DIR, falling back to
	// HOST_APP_DIR). checkMigrationVersion reads the upstream MIGRATION_VERSION
	// under it. Empty disables the auto-migrate check.
	appSrcDir string

	// Proactive interjection. zero-value cfg (Enabled=false) → the scan is never
	// driven. modes caches per-group CLAUDE.md proactive mode; pendingReason
	// carries a fired turn's <proactive_reason> to the prompt build keyed on
	// turn_id; nextScanAt advances the loop-driven sweep.
	proactive     ProactiveConfig
	modes         *modeCache
	pendingReason sync.Map // turn_id → proactiveResult
	// pendingElevation carries the operator /root elevation signal from cmdRoot
	// (trusted, gated on `**`) to runTurn, keyed on the injected message id. It is
	// the ONLY source of Elevated: an agent/user cannot forge a map entry, so root
	// can never be reached by message content. One-shot (LoadAndDelete per turn).
	pendingElevation sync.Map // message_id → struct{}
	nextScanAt       time.Time

	// busyWait backs off the re-dispatch of a batch nothing consumed — refused
	// by runed as Busy, or failed with a retry still owed — per chat
	// (chatJID → busyState). Without it a refused batch is re-fed
	// on every 2s poll and each re-feed does the FULL pre-dispatch setup —
	// capability resolve, MCP socket, prompt build — before runed refuses it
	// again: a restart's migrate wave on krons produced 4,704 dispatches for
	// 18 spawns in 15 minutes (BUGS L55). A new inbound message bypasses the
	// backoff; any cursor advance clears it.
	busyWait sync.Map // chatJID → busyState

	// Maintenance cadence, loop-driven (no free tickers). nextRetryAt paces
	// the outbound retry sweep (pending bot rows >30s); nextGCAt paces the
	// hourly GC (expired stale running turns).
	nextRetryAt time.Time
	nextGCAt    time.Time

	// costCapsEnabled drives the pre-spawn budget gate. False bypasses the gate
	// entirely (the operator escape hatch). When true, dispatchRun refuses a turn
	// whose folder is at/over its daily cost cap.
	costCapsEnabled bool

	// gateway is the model gateway's admin client (spec 6/23). Non-nil means
	// every turn mints its own virtual key and the GATEWAY is the ledger —
	// arizuko stores no spend of its own (spec 6/39). nil means no gateway:
	// the container keeps the model credential and the agent's submit_turn
	// report is the only number there is.
	gateway *modelGateway

	// media is the inbound enrichment config (MEDIA_*/WHISPER_*/VOICE_/VIDEO_
	// env). Disabled (Enabled=false) → enrichAttachments is a no-op.
	media mediaConfig

	// sessionIdle is the spawn-time stale-session threshold (SESSION_IDLE_EXPIRY,
	// default 2 days). A folder whose chat cursor is older than this gets its
	// session reset on the next spawn so a long-idle conversation starts fresh.
	sessionIdle time.Duration

	// onbod federates the /invite + /gate slash commands to onbod. nil → the
	// commands report the federation gap (ONBOD_URL unset / no service key). Set
	// via SetOnbodClient.
	onbod OnbodClient

	// Chat-initiated onboarding via onbod on route miss.
	// onboardingEnabled false → a route miss never inserts an onboarding row.
	// onboardingPlatforms restricts which jid platform prefixes may onboard
	// (empty = all). onbod OWNS the onboarding table; routd federates the insert
	// over HTTP (POST /v1/onboarding).
	onboardingEnabled   bool
	onboardingPlatforms []string

	// defaultModel is the instance-wide fallback model (ARIZUKO_DEFAULT_MODEL)
	// applied when a group has no per-group model. A per-group model wins; if
	// both are empty the container's ant SDK uses its own hardcoded default.
	defaultModel string
	// defaultEffort is the instance-wide reasoning depth (ARIZUKO_DEFAULT_EFFORT).
	defaultEffort string

	// maxTurnRetry is the number of times to retry a turn that fails without
	// delivering a reply (SIGKILL/OOM/timeout). Default 3.
	maxTurnRetry int

	// maxOutboundRetry is the number of transient delivery failures a pending
	// bot row absorbs before the sweep dead-letters it. Default 10.
	maxOutboundRetry int
}

// SetOnbodClient wires the onbod federation for the /invite + /gate commands.
// nil leaves them reporting the federation gap.
func (l *Loop) SetOnbodClient(c OnbodClient) { l.onbod = c }

// LoopConfig wires the Loop. RunTimeout is the hard deadline routd applies
// to a run (RUNED_RUN_TIMEOUT); 0 leaves it to the runner's context.
type LoopConfig struct {
	PollEvery  time.Duration
	RunTimeout time.Duration
	MaxRuns    int
	IpcDir     string

	// Prompt-envelope inputs (buildAgentPrompt). InstanceName feeds the
	// autocalls <instance> field; ObserveWindowMessages/Chars are the
	// cfg-default observe window (folder group/route rows override).
	InstanceName          string
	ObserveWindowMessages int
	ObserveWindowChars    int

	// Deliver is the egress half used for the outbound retry loop and the
	// run-error failure notice; the same Deliverer the Server uses. nil = no
	// redelivery / no notice (pure REST tests).
	Deliver Deliverer

	// Proactive.Enabled false → no scan ever runs. GroupsDir is where per-group
	// CLAUDE.md lives (the proactive-mode source); empty when disabled.
	Proactive ProactiveConfig
	GroupsDir string

	// AppSrcDir is the in-container source root holding ant/skills/self/
	// MIGRATION_VERSION (APP_SRC_DIR, falling back to HOST_APP_DIR). The
	// startup auto-migrate check reads the upstream version from it; empty
	// disables the check.
	AppSrcDir string

	// CostCapsEnabled toggles the pre-spawn budget gate (COST_CAPS_ENABLED). When
	// true, dispatchRun refuses a turn whose folder is at/over its daily cost cap.
	// Default-on at the cmd layer.
	CostCapsEnabled bool

	// GatewayURL/GatewayKey are the model gateway pair (MODEL_GATEWAY_URL,
	// MODEL_GATEWAY_KEY). With a URL set, every turn gets its own virtual key
	// minted against the gateway with this master key, and the gateway's own
	// measurement is the ledger rather than the agent's report (specs 6/23,
	// 6/39). Empty URL leaves the container holding the model credential and
	// the agent reporting its own spend.
	GatewayURL string
	GatewayKey string

	// Media is the inbound enrichment config (MEDIA_*/WHISPER_*/VOICE_/VIDEO_
	// env). Zero value (Enabled=false) leaves inbound media un-downloaded and
	// voice un-transcribed.
	Media mediaConfig

	// SessionIdle is the spawn-time stale-session reset threshold
	// (SESSION_IDLE_EXPIRY). Zero falls back to sessionIdleExpiry (2 days).
	SessionIdle time.Duration

	// Chat-initiated onboarding (ONBOARDING_ENABLED / ONBOARDING_PLATFORMS).
	// OnboardingEnabled false → a route miss never inserts an onboarding row.
	// OnboardingPlatforms restricts which jid platform prefixes may onboard
	// (empty = all).
	OnboardingEnabled   bool
	OnboardingPlatforms []string

	// DefaultModel is the instance-wide fallback model (ARIZUKO_DEFAULT_MODEL)
	// applied when a group has no per-group model. Empty leaves resolution to the
	// container's ant SDK default.
	DefaultModel string
	// DefaultEffort is the instance-wide reasoning depth (ARIZUKO_DEFAULT_EFFORT):
	// low|medium|high|xhigh|max. Empty leaves the harness CLI's own default.
	DefaultEffort string

	// MaxTurnRetry is the number of retry attempts when a turn fails without
	// delivering a reply (SIGKILL/OOM/timeout). Default 3.
	MaxTurnRetry int

	// MaxOutboundRetry is the number of transient delivery failures a pending
	// bot row absorbs before maybeRetryOutbound dead-letters it. Default 10;
	// -1 dead-letters on the first transient failure.
	MaxOutboundRetry int
}

// NewLoop builds the Loop and its per-folder queue. The queue's
// process-messages callback is processGroupMessages (the serialized
// per-folder dispatch); folder-for-jid maps a chat JID to its routed
// folder so folder-exclusivity holds across JIDs.
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
		maxRetry = 3 // unset: default
	}
	maxOutRetry := cfg.MaxOutboundRetry
	if maxOutRetry < 0 {
		maxOutRetry = 1 // explicit disable via -1: the first transient failure dead-letters
	} else if maxOutRetry == 0 {
		maxOutRetry = defaultMaxOutboundRetry // unset: default
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
		appSrcDir:       cfg.AppSrcDir,
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

// BindServer wires the Server post-construction so runTurn can stand up the
// per-turn MCP socket (the Server holds the DB+Deliverer the in-process tools
// close over). Server already holds loop; this closes the cycle.
func (l *Loop) BindServer(s *Server) { l.srv = s }

// recentSessions federates the prior-session continuity hint through the bound
// Server's runed session client. nil srv / no resolver → nil ("no prior
// session").
func (l *Loop) recentSessions(folder string, n int) []core.SessionRecord {
	if l.srv == nil {
		return nil
	}
	return l.srv.recentSessions(folder, n)
}

// Enqueue schedules a chat for processing (called by ingress after a
// PutMessage). The queue serializes per folder and collapses duplicates.
func (l *Loop) Enqueue(chatJID string) {
	if l == nil || l.q == nil {
		return
	}
	l.q.EnqueueMessageCheck(chatJID)
}

// StopQueue halts the per-folder queue (no new async dispatch). Tests use it
// to drive processGroupMessages deterministically; the daemon's own shutdown
// path is Run's ctx.Done, which calls q.Shutdown directly.
func (l *Loop) StopQueue() {
	if l != nil && l.q != nil {
		l.q.Shutdown()
	}
}

// Wait blocks until Run has returned, or until timeout elapses; it reports
// whether the loop stopped. The caller must not close the DB before it does:
// Routes() then fails mid-poll and resolve() fails FORWARD — cursor advanced,
// the inbound message dropped as a route-miss (BUGS L160, sloth 2026-09-04,
// 22 ms after "routd stopping").
//
// The loop returns as soon as its current pollOnce finishes, so a timeout here
// means a poll is genuinely stuck and the DB is still in use.
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
// the chat JID verbatim. The turns.go /result handler calls it on the first
// terminal signal.
func (l *Loop) publishRoundDone(chatJID, turnID string) {
	if l == nil || l.deliver == nil {
		return
	}
	_ = l.deliver.RoundDone(chatJID, turnID, "success", "")
}

// Run drives the poll loop until ctx is cancelled. On start it re-feeds
// chats whose turn_context is still 'running' (crash recovery) from the
// un-advanced agent_cursor.
func (l *Loop) Run(ctx context.Context) {
	defer close(l.done)
	l.sweepOrphanSocks()
	l.recoverPending()
	if n, err := l.db.RecoverFiringTasks(); err != nil {
		slog.Warn("recover firing tasks", "err", err)
	} else if n > 0 {
		slog.Info("re-armed scheduled tasks stranded in 'firing'", "count", n)
	}
	l.checkMigrationVersion()
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

// sweepOrphanSocks best-effort removes stale per-folder gated.sock files left
// by a crashed prior run (ipc.ServeMCP also removes its sock pre-listen, so
// this only matters for folders that won't get a fresh run this boot).
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

// Maintenance cadence. The retry sweep re-dispatches pending bot rows; the GC
// sweeps stale running turns.
const (
	outboundRetryEvery = 30 * time.Second
	outboundRetryAfter = 30 * time.Second
	gcEvery            = time.Hour

	// defaultMaxOutboundRetry bounds the transient-failure retries on one
	// pending bot row. MAX_OUTBOUND_RETRY raises it for a flaky platform.
	defaultMaxOutboundRetry = 10

	// maxOutboundBackoff caps how far backoffDelay spreads a single retry
	// gap. Doubling from outboundRetryAfter uncapped would spend
	// defaultMaxOutboundRetry attempts over ~17h; capping at an hour still
	// spreads them over ~1h instead of the flat 5 minutes a constant 30s
	// cadence gave every row regardless of how many times it had already
	// failed (L22 — that flat cadence dead-lettered live traffic mid a
	// routine whapd reconnect / adapter container restart).
	maxOutboundBackoff = time.Hour
)

// backoffDelay returns how long a pending row with attempts prior failures
// must age past its own timestamp before the sweep retries it again.
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

// maybeRetryOutbound re-dispatches bot rows that still owe a delivery, each gated
// by its own backoffDelay. The adapter dedups on the row's stable message_id, so
// a redelivered row never creates a second platform message.
//
// Two bounds stop an endless retry. A permanent refusal dead-letters the row at
// once and spends no attempt; a transient failure spends one, and the row
// dead-letters at maxOutboundRetry. There is no OUTRIGHT age bound — one once
// killed a row the sweep had never tried, losing a message no platform refused.
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
		// backoffDelay grows with prior attempts, so a row that just failed
		// waits longer than one on its first try — PendingOutbound's fixed
		// 30s floor only bounds the youngest rows out of the query.
		if now.Sub(m.Timestamp) < backoffDelay(p.Attempts) {
			continue
		}
		pid, derr := l.deliver.Send(m.ChatJID, m.Content, m.ReplyToID, m.Topic, "", m.ID)
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

// maybeGC runs the hourly housekeeping: sweep stale 'running' turns to
// 'expired' (so a crashed-never-completed turn stops being re-fed yet does
// not trip the done-guard).
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
	// The activity feed is an index with a retention window, and this is where
	// it drains. An index that only grows is the mistake turn_context records.
	if n, err := l.db.SweepActivity(now); err != nil {
		slog.Warn("gc: sweep the activity feed", "err", err)
	} else if n > 0 {
		slog.Info("gc: swept the activity feed", "count", n)
	}
}

// maybeScanProactive runs one proactive sweep when due: loop-driven, not a free
// ticker. Advances by ScanInterval with no catch-up — a long iteration just
// delays the next scan. No-op unless PROACTIVE_ENABLED.
func (l *Loop) maybeScanProactive(now time.Time) {
	if !l.proactive.Enabled || now.Before(l.nextScanAt) {
		return
	}
	l.scanProactive(now)
	l.nextScanAt = now.Add(l.proactive.ScanInterval)
}

// recoverPending re-enqueues chats with a still-running turn so a crash
// mid-turn re-attempts from the un-advanced cursor (turn_results dedups
// any submit_turn the old run delivered).
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

// checkMigrationVersion enqueues a /migrate system message for EVERY group whose
// on-disk skill version is behind the upstream MIGRATION_VERSION — the single
// trigger for both skill updates and release broadcasts. /migrate is a per-group
// self-migrate, so there is no root fan-out. Dropping the old top-level-only skip
// is what made this fire for every behind group.
func (l *Loop) checkMigrationVersion() {
	if l.appSrcDir == "" {
		return
	}
	latest := container.MigrationVersion(
		filepath.Join(l.appSrcDir, "ant", "skills", "self", "MIGRATION_VERSION"))
	if latest == 0 {
		return
	}
	for folder := range l.db.AllGroups() {
		agent := container.MigrationVersion(
			filepath.Join(l.groupsDir, folder, ".claude", "skills", "self", "MIGRATION_VERSION"))
		if agent >= latest {
			continue
		}
		slog.Info("auto-migrate: version behind, triggering /migrate",
			"group", folder, "agent", agent, "latest", latest)
		// Clear the session before /migrate: a group stuck on a stale migrate
		// skill accumulated turns of "/migrate blocked" baggage and will recall
		// that conclusion instead of reading the just-refreshed skill (cost the
		// marinade/atlas 11-day freeze — the skill file was current but the agent
		// re-ran `ls /workspace` from session memory). A clean spawn reads the
		// current skill and actually runs the merge.
		_ = l.db.DeleteSession(folder, "")
		prompt := fmt.Sprintf("/migrate\n\nSystem update: skills v%d → v%d.", agent, latest)
		if err := l.db.PutMessage(core.Message{
			ID:        core.MsgID("auto-migrate-" + folder),
			ChatJID:   folder,
			Sender:    "system",
			Content:   prompt,
			Timestamp: time.Now(),
		}); err != nil {
			slog.Warn("auto-migrate: put message", "group", folder, "err", err)
			continue
		}
		l.Enqueue(folder)
	}
}

// folderForJid resolves a chat JID to the folder that owns it, so the
// queue's folder-exclusivity (one live run per folder) holds across JIDs
// routing to the same group.
func (l *Loop) folderForJid(chatJID string) string {
	msgs, _ := l.db.MessagesSince(chatJID, "")
	if len(msgs) == 0 {
		return chatJID
	}
	g, ok := l.resolveGroup(chatJID, msgs[len(msgs)-1])
	if !ok {
		return chatJID
	}
	return g
}

// pollBatch bounds how many of one chat's messages a single poll takes. The
// cursor is a whole message key, so a batch that stops mid-instant resumes
// exactly where it stopped rather than re-feeding or skipping that instant.
const pollBatch = 200

// pollOnce reads each chat from its OWN watermark, resolves the owning group,
// and enqueues each chat that routes. A route miss advances the cursor and drops.
//
// `chats` IS THE PARTITION LIST, which is what makes this cheaper than the global
// read it replaces. That read fed from the LOWEST cursor across all chats and
// threw most rows away: measured 2026-09-12, krons returned 23,936 of its 24,013
// rows every two seconds because one chat's cursor had not moved since March,
// against 0 rows when each chat is read from its own. sloth returned 18,091 of
// 18,975 and marinade 8,060 of 10,267.
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
		r := l.resolve(chatJID, chatMsgs[len(chatMsgs)-1])
		if !r.ok {
			l.routeMiss(chatJID, chatMsgs, r)
			continue
		}
		// Steering (sticky-nav / slash / @child delegation) happens in
		// processGroupMessages — the one path both this backstop and adapter
		// ingest converge to — so a command can't race past it.
		l.Enqueue(chatJID)
	}
}

// routeMiss consumes a route-missed batch. It is the ONLY miss handler, shared by
// both processing paths. It used to exist twice and the copies drifted: the queue
// path advanced the cursor without onboarding, so a new chat's first message
// vanished silently and every new user needed a hand-run `arizuko group add`
// (krons 2026-07-12). Either way the cursor advances past the batch — a re-fed
// miss would replay forever.
func (l *Loop) routeMiss(chatJID string, msgs []core.Message, r resolution) {
	last := msgs[len(msgs)-1]
	if r.Observe != "" {
		// Route-table observe rule: ingest silently into the folder's
		// ambient context (is_observed=1), no turn. Enrich attachments so
		// later turns see downloaded media, not raw URLs (fix: observed
		// messages were missing media enrichment, only triggers got it).
		for i := range msgs {
			if l.media.Enabled {
				l.enrichAttachments(context.Background(), &msgs[i], r.Observe)
			}
		}
		if err := l.db.MarkMessagesObserved(r.Observe, msgs); err != nil {
			slog.Warn("route miss: mark observed", "jid", chatJID, "err", err)
		}
	} else {
		// A route-missed /chatid still answers: it is how a NEW (unrouted)
		// user self-reports their JID for `arizuko group add` — the routed
		// twin lives in handleCommand, and steer never runs on a miss. Same
		// parser + ack path, not a second dispatcher. It does not consume the
		// miss: the message still queues onboarding below.
		if head, _ := lookupCommand(last.Content); head == "/chatid" &&
			!last.BotMsg && !strings.HasPrefix(last.Sender, "timed-") {
			l.ack(chatJID, chatJID)
		}
		if l.onboardingEnabled && l.onbod != nil {
			// Genuine route miss (no observe rule): chat-initiated onboarding.
			// Discord guild channels only onboard on an explicit @mention so a
			// busy server doesn't queue every poster.
			// onbod OWNS the onboarding table (onbod.db); routd isn't mounted to
			// it, so the insert federates over onbod's HTTP API (POST /v1/onboarding).
			discordGuild := strings.HasPrefix(chatJID, "discord:") &&
				!strings.HasPrefix(chatJID, "discord:dm/")
			if onboardingAllowed(chatJID, l.onboardingPlatforms) &&
				(!discordGuild || last.Verb == "mention") {
				if err := l.onbod.InsertOnboarding(chatJID); err != nil {
					// Fail loud: without the row the new user gets silence forever
					// and no operator queue entry — tell the chat, not just the log.
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

// onboardingFailedNotice is sent to an unrouted chat when the admission-queue
// insert fails — the alternative is permanent silence (see routeMiss).
const onboardingFailedNotice = "⚠️ Couldn't register this chat for setup — please try again later."

// resolution is the outcome of route resolution for one chat's latest
// message. Exactly one of {ok, observe} is meaningful: ok=true → dispatch to
// Folder; Observe non-empty → silent ingest into that folder (no turn);
// neither → route miss (drop).
type resolution struct {
	Folder  string
	Topic   string // route-pinned topic (rt.Topic), "" leaves the message's own
	Observe string // observe-mode folder; messages get is_observed=1, no turn
	ok      bool
}

// resolveGroup wraps resolve for callers (folderForJid, proactive) that only
// need (folder, ok) and not the full resolution. Direct address → route table
// → engagement override, with topic-root normalization and observe-mode signalling.
func (l *Loop) resolveGroup(chatJID string, last core.Message) (string, bool) {
	r := l.resolve(chatJID, last)
	return r.Folder, r.ok
}

func (l *Loop) resolve(chatJID string, last core.Message) resolution {
	// 1. Direct address: a web:/hook: JID or a bare registered folder.
	if direct := l.directFolder(chatJID); direct != "" && l.db.GroupExists(direct) {
		return resolution{Folder: direct, ok: true}
	}
	// 2. Route table.
	routes, err := l.db.Routes()
	if err != nil {
		// A transient Routes() read failure otherwise looks identical to a
		// clean route-miss: the poll caller advances the cursor and drops the
		// message. We keep the fail-forward — retrying here risks a
		// poison-message loop — but make the drop LOUD instead of silent.
		slog.Error("resolve: route table read failed; message dropped as route-miss",
			"jid", chatJID, "err", err)
	}
	if err == nil {
		rt := router.ResolveRouteTarget(last, routes)
		if rt.Folder != "" {
			// Engagement overrides the route. Engagement is recorded on the
			// root topic (topic="") when the agent replies to an @mention;
			// thread replies arrive with topic="<thread>" which won't match —
			// normalize thread→root when the thread topic has no own record.
			engTopic := l.effectiveTopic(chatJID, last.Topic)
			if engTopic != "" && !l.db.IsEngaged(chatJID, engTopic) {
				engTopic = ""
			}
			if folder, ok := l.engaged(chatJID, engTopic); ok {
				return resolution{Folder: folder, ok: true}
			}
			if rt.Mode == "observe" {
				return resolution{Observe: rt.Folder}
			}
			return resolution{Folder: rt.Folder, Topic: rt.Topic, ok: true}
		}
	}
	// 3. Engagement fallback on a route miss (topic-root normalized).
	engTopic := l.effectiveTopic(chatJID, last.Topic)
	if engTopic != "" && !l.db.IsEngaged(chatJID, engTopic) {
		engTopic = ""
	}
	if folder, ok := l.engaged(chatJID, engTopic); ok {
		return resolution{Folder: folder, ok: true}
	}
	return resolution{}
}

// effectiveTopic resolves the topic a turn dispatches under: a set sticky #topic
// (chats.sticky_topic) overrides the message's own topic, else the message topic
// stands.
func (l *Loop) effectiveTopic(chatJID, msgTopic string) string {
	if _, sticky := l.db.StickyState(chatJID); sticky != "" {
		return sticky
	}
	return msgTopic
}

// engaged returns the live engagement folder for (chatJID, topic). An empty
// engaged folder (e.g. a stale ingress claim) is NOT a route — return false
// so resolveGroup falls through instead of dispatching to "".
func (l *Loop) engaged(chatJID, topic string) (string, bool) {
	folder, ok := l.db.Engaged(chatJID, topic)
	if !ok || folder == "" {
		return "", false
	}
	return folder, true
}

// directFolder returns the folder a JID directly addresses, or "" if the JID is
// not a direct address. Bare folder JIDs address a group directly; the route table
// does not apply.
//
// For a web:/hook: JID the mint RECORDS the delivery folder (spec 5/W). Read the
// record first: the grammar cannot recover the folder once the mint adds a suffix,
// because `web:t/sfx` reads as folder `t/sfx` and `hook:a/b/c` reads as `a/b`
// (BUGS L4, L1). The grammar stays the fallback for a token minted before the
// column existed.
//
// Reading the record only after the grammar misses would be cheaper and WRONG:
// `hook:a/b/c` minted at folder `a` hits an existing folder `a/b` and delivers the
// payload to the wrong tenant.
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

// advance moves the chat's agent_cursor past the last seen row so the
// poll loop doesn't re-feed it.
func (l *Loop) advance(chatJID string, last core.Message) {
	// Any cursor advance is progress past the batch a Busy refusal parked.
	l.busyWait.Delete(chatJID)
	_ = l.db.SetAgentCursor(chatJID, store.MessageCursor(last))
}

// sessionIdleExpiry is the default spawn-time stale-session threshold.
// SESSION_IDLE_EXPIRY overrides it (LoopConfig.SessionIdle).
const sessionIdleExpiry = 2 * 24 * time.Hour

// sessionIdleExpired reports whether the chat's last activity (its agent_cursor,
// a stored timestamp) is older than the idle threshold. A zero/empty or
// unparseable cursor is never expired (fresh chat — no session to reset anyway).
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
// root session when the breaker trips. A broken session is poison: dropping the
// failed batch + resetting the session gives the next inbound a clean spawn
// instead of replaying the same failure. The chat-visible failure notice is sent
// by the OutcomeError path (runTurn); this is the prune+reset half only.
func (l *Loop) onCircuitBreakerOpen(chatJID, folder string) {
	obs.SetCircuitBreakerState(folder, 2) // 2=open (spec 5/O)
	if err := l.db.DeleteErroredMessages(chatJID); err != nil {
		slog.Warn("prune errored messages failed", "jid", chatJID, "err", err)
	}
	if folder != "" {
		_ = l.db.DeleteSession(folder, "")
	}
}

// onboardingAllowed reports whether jid's platform prefix is eligible for
// chat-initiated onboarding. Empty platforms = all allowed.
func onboardingAllowed(jid string, platforms []string) bool {
	if len(platforms) == 0 {
		return true
	}
	p, _, _ := strings.Cut(jid, ":")
	return slices.Contains(platforms, p)
}
