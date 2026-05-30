package routd

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/queue"
	"github.com/kronael/arizuko/router"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/types"
)

// Runner dispatches a rendered batch to the execution plane (runed). The
// Loop calls it; production wires the runed HTTP client, tests inject a
// stub. It mirrors the PINNED POST /v1/runs contract (spec 5/E § The
// routd↔runed interface) — routd renders the prompt, runed runs it.
type Runner interface {
	Run(ctx context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error)
}

// RoundDoneFn notifies a web SSE channel that a turn closed (the
// round_done event). folder is the chat JID's folder (web:<folder>
// trimmed), NOT the routing-target folder — the routed-web-submission
// gotcha (spec 5/E § turn lifecycle). nil = no web SSE wired.
type RoundDoneFn func(folder, turnID, status, errMsg string)

// Loop is routd's orchestration loop: poll routd.db for new messages,
// resolve each chat's owning group, and dispatch a run to runed through
// the per-folder queue. It is the SOLE driver of turns; routd is the sole
// appender. Single process per instance (spec 5/E § Concurrency model).
type Loop struct {
	db         *DB
	q          *queue.GroupQueue
	runner     Runner
	deliver    Deliverer
	roundDone  RoundDoneFn
	pollEvery  time.Duration
	runTimeout time.Duration
	scopes     []types.Scope

	// Proactive interjection (5/33). zero-value cfg (Enabled=false) → the
	// scan is never driven. modes caches per-group CLAUDE.md proactive mode;
	// pendingReason carries a fired turn's <proactive_reason> to the prompt
	// build keyed on turn_id; nextScanAt advances the loop-driven sweep.
	proactive     ProactiveConfig
	modes         *modeCache
	pendingReason sync.Map // turn_id → proactiveResult
	nextScanAt    time.Time

	// Maintenance cadence, loop-driven (no free tickers). nextRetryAt paces
	// the outbound retry sweep (pending bot rows >30s); nextGCAt paces the
	// hourly GC (expired stale running turns + idempotency-ledger sweep).
	nextRetryAt time.Time
	nextGCAt    time.Time
}

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
	RoundDone  RoundDoneFn

	// Deliver is the egress half used for the outbound retry loop and the
	// run-error failure notice; the same Deliverer the Server uses. nil = no
	// redelivery / no notice (pure REST tests).
	Deliver Deliverer

	// Proactive (5/33). Proactive.Enabled false → no scan ever runs.
	// GroupsDir is where per-group CLAUDE.md lives (the proactive-mode
	// source); empty when proactive is disabled.
	Proactive ProactiveConfig
	GroupsDir string
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
	l := &Loop{
		db:         db,
		runner:     runner,
		deliver:    cfg.Deliver,
		roundDone:  cfg.RoundDone,
		pollEvery:  cfg.PollEvery,
		runTimeout: cfg.RunTimeout,
		scopes:     cfg.RunScopes,
		proactive:  cfg.Proactive,
	}
	if cfg.Proactive.Enabled {
		l.modes = newModeCache(cfg.GroupsDir)
	}
	l.q = queue.New(cfg.MaxRuns, cfg.IpcDir)
	l.q.SetProcessMessagesFn(l.processGroupMessages)
	l.q.SetFolderForJidFn(l.folderForJid)
	return l
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
	if l == nil || l.roundDone == nil {
		return
	}
	l.roundDone(folder, turnID, "success", "")
}

// Run drives the poll loop until ctx is cancelled. On start it re-feeds
// chats whose turn_context is still 'running' (crash recovery) from the
// un-advanced agent_cursor.
func (l *Loop) Run(ctx context.Context) {
	l.recoverPending()
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

// Maintenance cadence (spec 5/E § Outbound is poll-reconciled, § turn
// lifecycle). The retry sweep re-dispatches pending bot rows; the GC sweeps
// stale running turns and the idempotency ledger.
const (
	outboundRetryEvery = 30 * time.Second
	outboundRetryAfter = 30 * time.Second
	outboundFailAfter  = 24 * time.Hour
	gcEvery            = time.Hour
)

// maybeRetryOutbound re-dispatches bot rows still 'pending' older than 30s
// and fails them after 24h (spec 5/E § Outbound is poll-reconciled). The
// adapter dedups on the row's stable message_id, so a redelivered row never
// creates a second platform message. No-op when no Deliverer is wired.
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
		pid, derr := l.deliver.Send(m.ChatJID, m.Content, m.ReplyToID, m.Topic, m.ID)
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

// maybeScanProactive runs one proactive sweep when due (spec 5/33 § The
// proactive sweep): loop-driven, not a free ticker. Advances by
// ScanInterval with no catch-up — a long iteration just delays the next
// scan. No-op unless PROACTIVE_ENABLED.
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

// pollOnce reads new messages across all chats, groups by chat, resolves
// the owning group, and enqueues each chat that routes (spec 5/E § The
// orchestration loop). A route miss advances the cursor and drops.
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
		if _, ok := l.resolveGroup(chatJID, last); !ok {
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

// resolveGroup is routd's single route renderer, shared by pollOnce and
// processGroupMessages (spec 5/E § Route resolution). Direct address →
// route table → engagement override.
func (l *Loop) resolveGroup(chatJID string, last core.Message) (string, bool) {
	// 1. Direct address: web:<folder> or a bare registered folder.
	if direct := directFolder(chatJID); direct != "" && l.db.GroupExists(direct) {
		return direct, true
	}
	// 2. Route table.
	routes, err := l.db.Routes()
	if err == nil {
		rt := router.ResolveRouteTarget(last, routes)
		if rt.Folder != "" {
			topic := last.Topic
			if rt.Topic != "" {
				topic = rt.Topic
			}
			if folder, ok := l.engaged(chatJID, topic); ok {
				return folder, true
			}
			if rt.Mode == "observe" {
				return "", false // silent ingest; no turn (5/B)
			}
			return rt.Folder, true
		}
	}
	// 3. Engagement fallback on a route miss.
	if folder, ok := l.engaged(chatJID, last.Topic); ok {
		return folder, true
	}
	return "", false
}

func (l *Loop) engaged(chatJID, topic string) (string, bool) {
	return l.db.Engaged(chatJID, topic)
}

// directFolder returns the folder a JID directly addresses, or "" if the
// JID is not a direct address. web:<folder> and bare folder JIDs (no
// platform prefix) address a group directly; the route table does not
// apply (spec 5/E § Route resolution).
func directFolder(jid string) string {
	if strings.HasPrefix(jid, "web:") {
		return strings.TrimPrefix(jid, "web:")
	}
	if !strings.Contains(jid, ":") {
		return jid
	}
	return ""
}

// processGroupMessages is the queue's per-folder-serialized worker. It
// renders the batch and dispatches one run to runed, recording the
// outcome. Returns (hadOutput, err) per the queue's processMessagesFn.
func (l *Loop) processGroupMessages(chatJID string) (bool, error) {
	cursor := l.db.GetAgentCursor(chatJID)
	msgs, err := l.db.MessagesSince(chatJID, cursor)
	if err != nil {
		return false, err
	}
	if len(msgs) == 0 {
		return false, nil
	}
	last := msgs[len(msgs)-1]
	folder, ok := l.resolveGroup(chatJID, last)
	if !ok {
		l.advance(chatJID, last)
		return false, nil
	}
	// Strip bot rows from the trigger batch (don't feed the agent its own
	// output) but keep them in the rendered context.
	var trigger []core.Message
	for _, m := range msgs {
		if !m.BotMsg {
			trigger = append(trigger, m)
		}
	}
	if len(trigger) == 0 {
		l.advance(chatJID, last)
		return false, nil
	}
	topic := last.Topic
	turnID := last.ID
	rendered := router.FormatMessages(trigger)
	// A proactive turn carries one ephemeral <proactive_reason> block ahead
	// of the feed (5/33 § Per-turn envelope); single renderer, dropped after
	// it is consumed so a re-fed turn does not re-attach a stale reason.
	if v, ok := l.pendingReason.LoadAndDelete(turnID); ok {
		r := v.(proactiveResult)
		rendered = proactiveReasonBlock(r.check, r.reason) + rendered
	}

	if err := l.db.PutTurnContext(turnID, folder, topic, chatJID, last.Sender); err != nil {
		return false, err
	}
	_ = l.db.SetLastReply(chatJID, topic, last.ID, folder)

	out, derr := l.dispatchRun(folder, topic, chatJID, turnID, last.Sender, rendered)
	if derr != nil {
		// Transport failure: do NOT advance — re-fed next poll
		// (at-least-once; turn_results dedups). State stays running.
		slog.Warn("dispatch run transport failure", "folder", folder, "turn_id", turnID, "err", derr)
		return false, derr
	}
	if out.Steered {
		// Steer ack: the original run's response governs the batch.
		// Do not advance the cursor here.
		return true, nil
	}
	if out.BreakerOpen {
		slog.Warn("circuit breaker open", "folder", folder, "turn_id", turnID)
	}
	// POST /v1/runs has returned: close the callback surface so a late frame
	// 409s, even if an early submit_turn already flipped state→done.
	_ = l.db.SetRunReturned(turnID)
	if out.Outcome == runedv1.OutcomeError {
		// The run definitively failed. Advance past the batch (starvation
		// guard), flag the chat errored, and notify the chat — don't treat a
		// failed run as a silent success (spec 5/E § outcome).
		slog.Warn("run outcome error", "folder", folder, "turn_id", turnID, "err", out.Error)
		_ = l.db.MarkChatErrored(chatJID)
		if l.deliver != nil {
			_, _ = l.deliver.Send(chatJID, runFailureNotice, "", topic, "fail-"+turnID)
		}
		_ = l.db.SetTurnState(turnID, "done")
		l.advance(chatJID, last)
		return false, nil
	}
	// Clean turn-boundary outcome: persist session_id backstop UNLESS
	// submit_turn already recorded one (its value wins; spec 5/E
	// § Completion reconciliation), then advance.
	if out.SessionID != "" && !l.db.TurnResultRecorded(folder, turnID) {
		_ = l.db.PutSession(folder, topic, out.SessionID)
	}
	_ = l.db.SetTurnState(turnID, "done")
	l.advance(chatJID, last)
	return out.Outcome != runedv1.OutcomeSilent, nil
}

// runFailureNotice is sent to the chat when a run returns outcome:error and
// produced no usable output, so the user isn't left silent.
const runFailureNotice = "Failed: agent error on that message. Try rephrasing or send a different message."

// dispatchRun renders the run request and calls runed POST /v1/runs. The
// agent's conversation frames arrive out-of-band during the run via the
// /v1/turns/{turn_id}/* callbacks; this returns the turn-boundary outcome.
func (l *Loop) dispatchRun(folder, topic, chatJID, turnID, trigger, batch string) (runedv1.RunOutcome, error) {
	ctx := context.Background()
	var cancel context.CancelFunc
	if l.runTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, l.runTimeout)
		defer cancel()
	}
	caller := types.UserSub("service:routd")
	if trigger != "" && !strings.HasPrefix(trigger, "timed-") && !strings.HasPrefix(trigger, "system") {
		caller = types.UserSub(trigger)
	}
	return l.runner.Run(ctx, runedv1.RunRequest{
		Folder:           types.Folder(folder),
		Topic:            topic,
		ChatJID:          chatJID,
		SessionID:        l.db.SessionID(folder, topic),
		MessageBatch:     batch,
		TriggerSender:    trigger,
		CallerSub:        caller,
		TurnID:           turnID,
		CapabilityScopes: l.scopes,
	})
}

// advance moves the chat's agent_cursor past the last seen row so the
// poll loop doesn't re-feed it.
func (l *Loop) advance(chatJID string, last core.Message) {
	_ = l.db.SetAgentCursor(chatJID, last.Timestamp.UTC().Format(time.RFC3339Nano))
}
