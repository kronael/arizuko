package store

// turn_context.go holds every read and write of `turn_context`: one item per
// turn routd has dispatched, carrying where the turn came from, where its answer
// goes, what state it is in, and the hash of the gateway key that metered it.
//
// THE PARTITION IS OwnerRoutd AND THE SORT KEY IS THE TURN ID. Eleven of the
// fifteen operations name one turn — three point reads and eight point writes —
// and that is the whole callback surface: a late frame arrives carrying a turn id
// and nothing else, which is why the id has to be the coordinate rather than a
// field beside one.
//
// A TURN HAS A FOLDER AND IT STILL CANNOT BE THE PARTITION. Four operations name
// no turn: the typing-owner read names a run id, crash recovery names a state,
// the expiry sweep names a state and an instant, and the proactive scan names a
// chat. None of the four carries a folder, and a query without a partition is
// refused by every driver.
//
// WHAT THE CONSTANT COSTS IS THOSE FOUR, and each was already a scan on the
// engine this replaces (EXPLAIN QUERY PLAN, krons' routd.db, 2026-09-09):
// `WHERE run_id=?`, `WHERE state IN (…)`, the sweep's UPDATE and `WHERE
// chat_jid=? AND state='running'` all answer SCAN, because the table has no
// index but its turn_id autoindex. Three of the four narrow at the driver now —
// a filter is a conjunction and each of those predicates is one — where the
// statements they replace examined every row and dropped it in the engine.
//
// THIS IS THE ONE ENTITY IN THE HOT TIER WITH NO DRAINAGE AND A PER-TURN
// ARRIVAL, and that is the honest cost. Nothing deletes a turn context; a group
// delete keeps it (folder_scope.go: a re-created folder mints new turn ids). The
// partition is 2,954 items on krons, 2,607 on sloth and 1,089 on marinade
// (2026-09-09), about thirty a day, and 2,846 of krons' are `done` — which is
// what the state filters drop first.
//
// started_at IS THE ONE ORDERED ATTRIBUTE. The expiry sweep compares it against
// `core.TS(now - timeout)` with `<`, and the live rows carry FOUR widths (27 to
// 30 bytes on all three instances), so byte order disagrees with time order at
// every pair inside one second of the cutoff — a stale turn that survives its
// own sweep and is re-fed by crash recovery until the next one. The declaration
// refuses a short value at write time and `arizuko migrate-hot` re-renders the
// ones on disk (BUGS L97).
//
// gateway_key_hash IS A POINTER AND THE ONLY ONE. arizuko stores WHICH key
// metered a turn and never the number, so `GET /v1/cost?turn_id=` resolves this
// hash and asks the gateway (spec 6/39) — and anteval reads that endpoint as the
// settle barrier between eval cases. Losing the attribute makes every turn's cost
// unreadable and every barrier a timeout.

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a turn item carries. `turn_id` is not among them: it is the
// coordinate, and storing it twice is two facts that can disagree.
const (
	attrTurnFolder      = "folder"
	attrTurnTopic       = "topic"
	attrTurnChatJID     = "chat_jid"
	attrTurnTrigger     = "trigger_sender"
	attrTurnTriggerMsg  = "trigger_msg_id"
	attrTurnReturnTo    = "return_to"
	attrTurnRunID       = "run_id"
	attrTurnState       = "state"
	attrTurnRunReturned = "run_returned"
	attrTurnRetryCount  = "retry_count"
	attrTurnGatewayKey  = "gateway_key_hash"
	// AttrTurnStartedAt is when the turn was dispatched, and the only attribute
	// here compared by order. It is exported because the move that carries these
	// rows has to name the instant it re-renders.
	AttrTurnStartedAt = "started_at"
)

// The lifecycle states a turn passes through. `done` and `expired` are the two
// terminal ones, and they are DISTINCT: expiring a stale turn must not trip the
// done-guard against a legitimate re-dispatch.
const (
	TurnRunning      = "running"
	TurnPendingRetry = "pending_retry"
	TurnDone         = "done"
	TurnExpired      = "expired"
)

// The ordered declaration is what makes the expiry sweep a time comparison
// rather than a width comparison (BUGS L97).
func init() { objstore.OrderedAttr(TurnContexts, AttrTurnStartedAt, tsWidth) }

// TurnContext is a turn's bound run-start context.
type TurnContext struct {
	TurnID       string
	Folder       string
	Topic        string
	ChatJID      string
	Trigger      string
	TriggerMsgID string
	ReturnTo     string
	State        string
	RunReturned  bool
	RetryCount   int
}

// turnKey is the coordinate of one turn.
func turnKey(turnID string) (objstore.Key, error) {
	if turnID == "" {
		return objstore.Key{}, fmt.Errorf("turn context carries no turn_id, which is its " +
			"coordinate")
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: turnID}, nil
}

// turnFrom reads one item back.
func turnFrom(it objstore.Item) TurnContext {
	tc := TurnContext{
		TurnID:       it.Key.Sort,
		Folder:       it.Attrs[attrTurnFolder],
		Topic:        it.Attrs[attrTurnTopic],
		ChatJID:      it.Attrs[attrTurnChatJID],
		Trigger:      it.Attrs[attrTurnTrigger],
		TriggerMsgID: it.Attrs[attrTurnTriggerMsg],
		ReturnTo:     it.Attrs[attrTurnReturnTo],
		State:        it.Attrs[attrTurnState],
		RunReturned:  it.Attrs[attrTurnRunReturned] == flagSet,
	}
	tc.RetryCount, _ = strconv.Atoi(it.Attrs[attrTurnRetryCount])
	return tc
}

// OpenTurnContext binds a turn to the (folder, topic, chat) it came from, so a
// late callback carrying only a turn id can resolve its target. returnTo is the
// delegation return address: when set, the callback surface delivers there
// instead of to the child folder's JID.
//
// IT ANSWERS live=false WHEN THE TURN IS ALREADY DONE, and that answer is the
// duplicate-output guard: when an earlier batch finishes and a later batch in
// the same poll steers, the cursor does not advance and the next poll re-feeds
// the completed batch. Re-dispatching it would replay finished output into the
// chat.
//
// THE DECISION IS THE WRITE'S OWN CONDITION and not a read. Two shapes because
// the interface has two: the create commits when nothing has bound this turn,
// and its REFUSAL sends the caller to the update, whose condition is the whole
// operation. A read and then a write is two decisions with the turn's state free
// to move between them.
//
// `started_at` is written ONCE, by the create. A re-dispatch of a live turn
// leaves it where it was, so the expiry sweep still measures from the turn's
// first attempt.
func (s *Store) OpenTurnContext(turnID, folder, topic, chatJID, trigger, returnTo string) (bool, error) {
	key, err := turnKey(turnID)
	if err != nil {
		return false, err
	}
	bound := map[string]string{attrTurnState: TurnRunning}
	setAttr(bound, attrTurnFolder, folder)
	setAttr(bound, attrTurnTopic, topic)
	setAttr(bound, attrTurnChatJID, chatJID)
	setAttr(bound, attrTurnTrigger, trigger)
	setAttr(bound, attrTurnReturnTo, returnTo)

	ctx := context.Background()
	fresh := map[string]string{AttrTurnStartedAt: core.NowTS()}
	for k, v := range bound {
		fresh[k] = v
	}
	opened, err := s.TryWriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Put, Entity: TurnContexts, Key: key, Attrs: fresh,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
	if err != nil {
		return false, fmt.Errorf("bind turn %s: %w", turnID, err)
	}
	if opened {
		return true, nil
	}
	// A re-dispatch REPLACES the binding and clears the run-returned flag, and
	// the two must land together: a turn re-opened with the previous attempt's
	// flag still set answers every late callback with turn_done.
	//
	// The empty fields of `bound` are removed rather than left, because absent is
	// the only null this model has and a stale return address would deliver the
	// next answer to the previous delegator.
	live, err := s.TryWriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Update, Entity: TurnContexts, Key: key, Attrs: bound,
		Remove: append(missingAttrs(bound, attrTurnFolder, attrTurnTopic, attrTurnChatJID,
			attrTurnTrigger, attrTurnReturnTo), attrTurnRunReturned),
		Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: attrTurnState, Op: objstore.Ne, Value: TurnDone},
		}},
	})
	if err != nil {
		return false, fmt.Errorf("re-bind turn %s: %w", turnID, err)
	}
	return live, nil
}

// missingAttrs names the attributes of `names` that attrs does not carry, so a
// merge can take them away in the same write that sets the rest.
func missingAttrs(attrs map[string]string, names ...string) []string {
	var gone []string
	for _, name := range names {
		if _, set := attrs[name]; !set {
			gone = append(gone, name)
		}
	}
	return gone
}

// TurnContextOf recovers a turn's context by turn id, and whether the turn is
// bound at all.
func (s *Store) TurnContextOf(turnID string) (TurnContext, bool) {
	key, err := turnKey(turnID)
	if err != nil {
		return TurnContext{}, false
	}
	it, ok, err := s.itemAt(context.Background(), TurnContexts, key)
	if err != nil || !ok {
		return TurnContext{}, false
	}
	return turnFrom(it), true
}

// setTurnAttrs merges attributes into a bound turn. An Update against a turn
// nobody bound writes nothing, which is what the statements this replaces did by
// matching no row.
func (s *Store) setTurnAttrs(turnID string, attrs map[string]string, remove ...string) error {
	key, err := turnKey(turnID)
	if err != nil {
		return err
	}
	// A refusal is a turn nobody bound, which the doc above allows.
	if _, err := s.TryWriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Update, Entity: TurnContexts, Key: key, Attrs: attrs, Remove: remove,
	}); err != nil {
		return fmt.Errorf("update turn %s: %w", turnID, err)
	}
	return nil
}

// SetTurnTriggerMsg records the platform id of the message that triggered a
// turn — the message a threaded reply roots a new platform thread on.
func (s *Store) SetTurnTriggerMsg(turnID, msgID string) error {
	return s.setTurnAttrs(turnID, map[string]string{attrTurnTriggerMsg: msgID})
}

// SetTurnState flips a turn's lifecycle state.
func (s *Store) SetTurnState(turnID, state string) error {
	return s.setTurnAttrs(turnID, map[string]string{attrTurnState: state})
}

// SetRunReturned marks that the run request has returned for a turn. After this
// a late callback is answered turn_done; before it, trailing frames from a
// still-live run stay valid even past an early submit_turn.
func (s *Store) SetRunReturned(turnID string) error {
	return s.setTurnAttrs(turnID, map[string]string{attrTurnRunReturned: flagSet})
}

// SetTurnRunID records the run handle runed assigned, which is what
// reconciliation and the typing-owner read join on.
func (s *Store) SetTurnRunID(turnID, runID string) error {
	return s.setTurnAttrs(turnID, map[string]string{attrTurnRunID: runID})
}

// SetTurnGatewayKey records the sha256 of the key the gateway minted for this
// turn. The gateway names a key in its spend log by that hash and never by the
// key itself, so this is the handle that resolves a turn to its own numbers —
// the pointer arizuko keeps in place of a copy of them (spec 6/39).
func (s *Store) SetTurnGatewayKey(turnID, keyHash string) error {
	return s.setTurnAttrs(turnID, map[string]string{attrTurnGatewayKey: keyHash})
}

// TurnGatewayKey answers the key hash that metered a turn. Empty means the turn
// ran with no gateway, so no meter holds a number for it — which the cost
// endpoint reports as a 404 rather than a zero.
func (s *Store) TurnGatewayKey(turnID string) (string, error) {
	key, err := turnKey(turnID)
	if err != nil {
		return "", err
	}
	it, ok, err := s.itemAt(context.Background(), TurnContexts, key)
	if err != nil || !ok {
		return "", err
	}
	return it.Attrs[attrTurnGatewayKey], nil
}

// ResetTurnForRetry puts a turn back in flight for its next attempt, and clears
// the run-returned flag with it: a retry whose flag survived would answer every
// callback of the new attempt with turn_done.
func (s *Store) ResetTurnForRetry(turnID string) error {
	return s.setTurnAttrs(turnID, map[string]string{attrTurnState: TurnRunning},
		attrTurnRunReturned)
}

// retryRounds bounds RecordRetryAttempt's compare-and-swap. A refusal is a lost
// race and nothing else — the condition is the count this call read one round
// trip ago — so it is the transient class that may be retried.
const retryRounds = 8

// RecordRetryAttempt moves a turn to its next attempt and answers which attempt
// that is.
//
// THE COUNT AND THE STATE ARE ONE WRITE, and that is the invariant: a turn whose
// count moved but whose state stayed `running` has burned an attempt and is
// still fed to crash recovery as live. objstore.Increment cannot carry them
// together — a batch holds one action per item and the state belongs to the
// counter's own item — so the compare-and-swap is written here, guarded on the
// count this call read.
func (s *Store) RecordRetryAttempt(turnID string) (int, error) {
	key, err := turnKey(turnID)
	if err != nil {
		return 0, err
	}
	ctx := context.Background()
	for range retryRounds {
		it, ok, err := s.itemAt(ctx, TurnContexts, key)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, fmt.Errorf("record a retry of %s: no such turn", turnID)
		}
		was := it.Attrs[attrTurnRetryCount]
		count, err := strconv.Atoi(was)
		if was != "" && err != nil {
			return 0, fmt.Errorf("the retry count of %s is %q, which is not a number: %w",
				turnID, was, err)
		}
		next := count + 1
		guard := objstore.Cmp{Attr: attrTurnRetryCount, Op: objstore.Eq, Value: was}
		if was == "" {
			guard = objstore.Cmp{Attr: attrTurnRetryCount, Op: objstore.Absent}
		}
		moved, err := s.TryWriteEntities(ctx, nil, objstore.Write{
			Op: objstore.Update, Entity: TurnContexts, Key: key,
			Attrs: map[string]string{
				attrTurnRetryCount: strconv.Itoa(next),
				attrTurnState:      TurnPendingRetry,
			},
			Cond: objstore.Cond{Where: []objstore.Cmp{guard}},
		})
		if err != nil {
			return 0, fmt.Errorf("record a retry of %s: %w", turnID, err)
		}
		if moved {
			return next, nil
		}
	}
	return 0, fmt.Errorf("record a retry of %s: %d guarded writes were all refused, so the "+
		"turn is being retried faster than this can read it", turnID, retryRounds)
}

// TypingJIDsForRun answers the chats whose typing indicator a run owns: one per
// turn recorded against the run, resolved the way dispatch picks the indicator's
// chat.
//
// A follow-up that arrives mid-run is STEERED into the live container, and
// steering is by folder, so the steered turn's chat is not always the running
// turn's chat. Its indicator was handed to a submit_turn that never fires when
// the run dies at the timeout wall (BUGS T1).
//
// The run id is an attribute, so the read narrows on it at the driver and the
// duplicates are dropped here.
func (s *Store) TypingJIDsForRun(runID string) []string {
	if runID == "" {
		return nil
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: TurnContexts, Partition: OwnerRoutd,
		Where: []objstore.Cmp{{Attr: attrTurnRunID, Op: objstore.Eq, Value: runID}},
	})
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		jid := it.Attrs[attrTurnReturnTo]
		if jid == "" {
			jid = it.Attrs[attrTurnChatJID]
		}
		if jid == "" || seen[jid] {
			continue
		}
		seen[jid] = true
		out = append(out, jid)
	}
	return out
}

// RunningTurns lists the turns crash recovery re-feeds: the ones still running
// or waiting for a retry.
//
// The driver drops the terminal majority — 2,846 of krons' 2,954 items are
// `done` — and the exact set is named HERE, because a filter is a conjunction
// and "one of these two states" is not one. Naming the set rather than testing
// for "not terminal" is what keeps a state added later out of the crash-recovery
// feed until someone decides it belongs there.
func (s *Store) RunningTurns() ([]TurnContext, error) {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: TurnContexts, Partition: OwnerRoutd,
		Where: []objstore.Cmp{{Attr: attrTurnState, Op: objstore.Ne, Value: TurnDone}},
	})
	if err != nil {
		return nil, fmt.Errorf("read the turns still in flight: %w", err)
	}
	var out []TurnContext
	for _, it := range items {
		switch it.Attrs[attrTurnState] {
		case TurnRunning, TurnPendingRetry:
			out = append(out, turnFrom(it))
		}
	}
	return out, nil
}

// TurnsSince answers every turn dispatched at or after `since`, whatever state
// it reached. It is the operator's outcome read: the states these rows carry are
// what says whether turns are finishing or expiring.
//
// It narrows on started_at, which is the one ordered attribute here, so the
// answer is bounded by the window rather than by the partition. The READ is not:
// a filter is not an index, so the driver still examines the partition — 2,954
// items on krons — and the header above says why that is the cost of this
// entity's coordinate. A window is an operator's read, not a hot path.
func (s *Store) TurnsSince(since time.Time) ([]TurnContext, error) {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: TurnContexts, Partition: OwnerRoutd,
		Where: []objstore.Cmp{
			{Attr: AttrTurnStartedAt, Op: objstore.Ge, Value: core.TS(since)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("read the turns dispatched since %s: %w", core.TS(since), err)
	}
	out := make([]TurnContext, 0, len(items))
	for _, it := range items {
		out = append(out, turnFrom(it))
	}
	return out, nil
}

// SweepExpiredRunning moves every turn that has been running longer than the run
// timeout to `expired`, and answers how many moved.
//
// `expired` is DISTINCT from `done` on purpose: it stops crash recovery re-feeding
// the turn without tripping the done-guard against a legitimate re-dispatch.
//
// The read narrows on both terms at the driver — a state and an ordered instant
// are a conjunction — and each write carries the state it read as its own
// condition, so a turn that finished between the read and the write is left
// alone instead of being expired on top of its own answer.
func (s *Store) SweepExpiredRunning(timeout time.Duration) (int64, error) {
	ctx := context.Background()
	cutoff := core.TS(time.Now().Add(-timeout))
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: TurnContexts, Partition: OwnerRoutd,
		Where: []objstore.Cmp{
			{Attr: attrTurnState, Op: objstore.Eq, Value: TurnRunning},
			{Attr: AttrTurnStartedAt, Op: objstore.Lt, Value: cutoff},
		},
	})
	if err != nil {
		return 0, fmt.Errorf("read the stale running turns: %w", err)
	}
	var swept int64
	for _, it := range items {
		moved, err := s.TryWriteEntities(ctx, nil, objstore.Write{
			Op: objstore.Update, Entity: TurnContexts, Key: it.Key,
			Attrs: map[string]string{attrTurnState: TurnExpired},
			Cond: objstore.Cond{Where: []objstore.Cmp{
				{Attr: attrTurnState, Op: objstore.Eq, Value: TurnRunning},
			}},
		})
		if err != nil {
			return swept, fmt.Errorf("expire turn %s: %w", it.Key.Sort, err)
		}
		if moved {
			swept++
		}
	}
	return swept, nil
}

// ChatHasRunningTurn reports whether a chat has a turn still running — the
// proactive scan skips such a chat, because the per-folder queue serialises and
// a proactive inbound never steers a live turn.
func (s *Store) ChatHasRunningTurn(jid string) bool {
	if jid == "" {
		return false
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: TurnContexts, Partition: OwnerRoutd,
		Where: []objstore.Cmp{
			{Attr: attrTurnChatJID, Op: objstore.Eq, Value: jid},
			{Attr: attrTurnState, Op: objstore.Eq, Value: TurnRunning},
		},
		Limit: 1,
	})
	if err != nil {
		return false
	}
	return len(items) > 0
}

// CountTurnContexts answers how many turns are bound. `arizuko migrate-hot`
// reads it back to check that every row travelled.
func (s *Store) CountTurnContexts(ctx context.Context) (int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: TurnContexts, Partition: OwnerRoutd,
	})
	if err != nil {
		return 0, fmt.Errorf("read the turn contexts: %w", err)
	}
	return len(items), nil
}
