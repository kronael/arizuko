package routd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/queue"
	"github.com/kronael/arizuko/router"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/types"
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
	scopes     []types.Scope

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
	nextScanAt    time.Time

	// Maintenance cadence, loop-driven (no free tickers). nextRetryAt paces
	// the outbound retry sweep (pending bot rows >30s); nextGCAt paces the
	// hourly GC (expired stale running turns + idempotency-ledger sweep).
	nextRetryAt time.Time
	nextGCAt    time.Time

	// costCapsEnabled drives the pre-spawn budget gate. False bypasses the gate
	// entirely (the operator escape hatch). When true, dispatchRun refuses a turn
	// whose folder is at/over its daily cost cap.
	costCapsEnabled bool

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

	// Chat-initiated onboarding (ported from gateway.pollOnce route-miss branch).
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

	// maxTurnRetry is the number of times to retry a turn that fails without
	// delivering a reply (SIGKILL/OOM/timeout). Default 3.
	maxTurnRetry int
}

// SetOnbodClient wires the onbod federation for the /invite + /gate commands.
// nil leaves them reporting the federation gap.
func (l *Loop) SetOnbodClient(c OnbodClient) { l.onbod = c }

// LoopConfig wires the Loop. RunScopes is the capability set every
// dispatched run requests from runed's broker (downscoped ⊆ runed's
// service scope). RunTimeout is the hard deadline routd applies to a run
// (RUNED_RUN_TIMEOUT); 0 leaves it to the runner's context.
type LoopConfig struct {
	PollEvery  time.Duration
	RunTimeout time.Duration
	MaxRuns    int
	IpcDir     string
	RunScopes  []types.Scope

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

	// MaxTurnRetry is the number of retry attempts when a turn fails without
	// delivering a reply (SIGKILL/OOM/timeout). Default 3.
	MaxTurnRetry int
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
	l := &Loop{
		db:              db,
		runner:          runner,
		deliver:         cfg.Deliver,
		pollEvery:       cfg.PollEvery,
		runTimeout:      cfg.RunTimeout,
		scopes:          cfg.RunScopes,
		proactive:       cfg.Proactive,
		instanceName:    cfg.InstanceName,
		observeMessages: cfg.ObserveWindowMessages,
		observeChars:    cfg.ObserveWindowChars,
		groupsDir:       cfg.GroupsDir,
		appSrcDir:       cfg.AppSrcDir,
		costCapsEnabled: cfg.CostCapsEnabled,
		media:           cfg.Media,
		sessionIdle:     cfg.SessionIdle,
		folders:         &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir},

		onboardingEnabled:   cfg.OnboardingEnabled,
		onboardingPlatforms: cfg.OnboardingPlatforms,
		defaultModel:        cfg.DefaultModel,
		maxTurnRetry:        maxRetry,
	}
	if cfg.Proactive.Enabled {
		l.modes = newModeCache(cfg.GroupsDir)
	}
	l.q = queue.New(cfg.MaxRuns, cfg.IpcDir)
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

// StopQueue halts the per-folder queue (no new async dispatch). Used by
// tests to drive processGroupMessages deterministically, and by shutdown.
func (l *Loop) StopQueue() {
	if l != nil && l.q != nil {
		l.q.Shutdown()
	}
}

// publishRoundDone fans a closed-turn notice to the web SSE channel. The
// turns.go /result handler calls it on the first terminal signal.
func (l *Loop) publishRoundDone(folder, turnID string) {
	if l == nil || l.deliver == nil {
		return
	}
	_ = l.deliver.RoundDone(folder, turnID, "success", "")
}

// Run drives the poll loop until ctx is cancelled. On start it re-feeds
// chats whose turn_context is still 'running' (crash recovery) from the
// un-advanced agent_cursor.
func (l *Loop) Run(ctx context.Context) {
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
// sweeps stale running turns and the idempotency ledger.
const (
	outboundRetryEvery = 30 * time.Second
	outboundRetryAfter = 30 * time.Second
	outboundFailAfter  = 24 * time.Hour
	gcEvery            = time.Hour
)

// maybeRetryOutbound re-dispatches bot rows still 'pending' older than 30s and
// fails them after 24h. The adapter dedups on the row's stable message_id, so a
// redelivered row never creates a second platform message. No-op when no
// Deliverer is wired.
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
	for _, m := range rows {
		if now.Sub(m.Timestamp) > outboundFailAfter {
			_ = l.db.MarkStatus(m.ID, core.MessageStatusFailed)
			continue
		}
		pid, derr := l.deliver.Send(m.ChatJID, m.Content, m.ReplyToID, m.Topic, "", m.ID)
		if derr == nil {
			_ = l.db.MarkBotPlatformID(m.ID, pid)
		}
	}
}

// maybeGC runs the hourly housekeeping: sweep stale 'running' turns to
// 'expired' (so a crashed-never-completed turn stops being re-fed yet does
// not trip the done-guard) and drop expired idempotency-ledger rows.
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
	if err := l.db.SweepIdempotency(); err != nil {
		slog.Warn("gc: sweep idempotency", "err", err)
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

// checkMigrationVersion enqueues a /migrate system message for every root group
// whose on-disk skill version is behind the upstream MIGRATION_VERSION. Bumping
// MIGRATION_VERSION is the single trigger for both skill updates AND release
// broadcasts. Child groups get skills seeded at container start; the root's
// /migrate skill fans out, so only roots are triggered here.
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
		if !groupfolder.IsRoot(folder) {
			continue
		}
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

// pollOnce reads new messages across all chats, groups by chat, resolves the
// owning group, and enqueues each chat that routes. A route miss advances the
// cursor and drops.
func (l *Loop) pollOnce() {
	msgs, _, err := l.db.NewMessages(l.pollCursor())
	if err != nil {
		slog.Warn("poll new messages", "err", err)
		return
	}
	byChat := map[string][]core.Message{}
	for _, m := range msgs {
		byChat[m.ChatJID] = append(byChat[m.ChatJID], m)
	}
	for chatJID, chatMsgs := range byChat {
		last := chatMsgs[len(chatMsgs)-1]
		// Global poll feeds from the min cursor; skip chats already current on
		// their own cursor — enqueuing them would (false,nil)→trip the queue
		// breaker (cost a 3-attempt split cutover to find).
		if last.Timestamp.UTC().Format(time.RFC3339Nano) <= l.db.GetAgentCursor(chatJID) {
			continue
		}
		r := l.resolve(chatJID, last)
		if !r.ok {
			if r.Observe != "" {
				// Route-table observe rule: ingest silently into the folder's
				// ambient context (is_observed=1), no turn. Enrich attachments so
				// later turns see downloaded media, not raw URLs (fix: observed
				// messages were missing media enrichment, only triggers got it).
				ids := make([]string, len(chatMsgs))
				for i := range chatMsgs {
					ids[i] = chatMsgs[i].ID
					if l.media.Enabled {
						l.enrichAttachments(context.Background(), &chatMsgs[i], r.Observe)
					}
				}
				if err := l.db.MarkMessagesObserved(r.Observe, ids); err != nil {
					slog.Warn("poll: mark observed", "jid", chatJID, "err", err)
				}
			} else if l.onboardingEnabled && l.onbod != nil {
				// Genuine route miss (no observe rule): chat-initiated onboarding.
				// Ported from gateway.pollOnce — discord guild channels only onboard
				// on an explicit @mention so a busy server doesn't queue every poster.
				// onbod OWNS the onboarding table (onbod.db); routd isn't mounted to
				// it, so the insert federates over onbod's HTTP API (POST /v1/onboarding).
				discordGuild := strings.HasPrefix(chatJID, "discord:") &&
					!strings.HasPrefix(chatJID, "discord:dm/")
				if onboardingAllowed(chatJID, l.onboardingPlatforms) &&
					(!discordGuild || last.Verb == "mention") {
					if err := l.onbod.InsertOnboarding(chatJID); err != nil {
						slog.Warn("insert onboarding", "jid", chatJID, "err", err)
					}
				}
			}
			l.advance(chatJID, last)
			continue
		}
		// Steering layer (sticky-nav / slash / @child delegation) consumes the
		// latest message BEFORE enqueue; a consumed message advances the cursor
		// and never reaches a turn.
		if l.steer(chatJID, last, r.Folder) {
			l.advance(chatJID, last)
			continue
		}
		l.Enqueue(chatJID)
	}
}

// pollCursor is the global high-water mark: the max agent_cursor across
// chats. The simple poll feeds anything newer than the lowest unprocessed
// row; per-chat cursors gate the actual dispatch in processGroupMessages.
func (l *Loop) pollCursor() string {
	return l.db.MinAgentCursor()
}

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

// resolveGroup is routd's single route renderer, shared by pollOnce and
// processGroupMessages. Direct address → route table → engagement override, with
// topic-root normalization and observe-mode signalling.
func (l *Loop) resolveGroup(chatJID string, last core.Message) (string, bool) {
	r := l.resolve(chatJID, last)
	return r.Folder, r.ok
}

func (l *Loop) resolve(chatJID string, last core.Message) resolution {
	// 1. Direct address: web:<folder> or a bare registered folder.
	if direct := directFolder(chatJID); direct != "" && l.db.GroupExists(direct) {
		return resolution{Folder: direct, ok: true}
	}
	// 2. Route table.
	routes, err := l.db.Routes()
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
				return resolution{Observe: rt.Folder} // silent ingest; no turn
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
// not a direct address. web:<folder> and bare folder JIDs (no platform prefix)
// address a group directly; the route table does not apply.
func directFolder(jid string) string {
	if strings.HasPrefix(jid, "web:") {
		return strings.TrimPrefix(jid, "web:")
	}
	if !strings.Contains(jid, ":") {
		return jid
	}
	return ""
}

// advance moves the chat's agent_cursor past the last seen row so the
// poll loop doesn't re-feed it.
func (l *Loop) advance(chatJID string, last core.Message) {
	_ = l.db.SetAgentCursor(chatJID, last.Timestamp.UTC().Format(time.RFC3339Nano))
}

// sessionIdleExpiry is the default spawn-time stale-session threshold.
// SESSION_IDLE_EXPIRY overrides it (LoopConfig.SessionIdle).
const sessionIdleExpiry = 2 * 24 * time.Hour

// sessionIdleExpired reports whether the chat's last activity (its agent_cursor,
// an RFC3339Nano timestamp) is older than the idle threshold. A zero/empty or
// unparseable cursor is never expired (fresh chat — no session to reset anyway).
func (l *Loop) sessionIdleExpired(chatJID string) bool {
	cur := l.db.GetAgentCursor(chatJID)
	if cur == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, cur)
	if err != nil || t.IsZero() {
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
// chat-initiated onboarding. Empty platforms = all allowed. Ported verbatim from
// gateway.onboardingAllowed.
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
