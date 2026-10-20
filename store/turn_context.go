package store

// turn_context.go holds every read and write of `turn_context`: one item per
// turn routd has dispatched, carrying where the turn came from, where its answer
// goes, what state it is in, and the hash of the gateway key that metered it.
//
// The partition is OwnerRoutd and the sort key the turn id: a late frame
// arrives carrying a turn id and nothing else. A turn has a folder and it still
// cannot be the partition: the typing-owner read, crash recovery, the expiry
// sweep and the proactive scan carry none. Those four filter the partition,
// the one hot-tier entity with no drainage and a per-turn arrival (2,954 items
// on krons, about thirty a day, 2,846 of them `done`).
//
// started_at IS THE ONE ORDERED ATTRIBUTE: the expiry sweep compares it with
// `<`, and the live rows carried FOUR widths, so a stale turn survived its own
// sweep (BUGS L97). gateway_key_hash IS A POINTER AND THE ONLY ONE: arizuko
// stores WHICH key metered a turn and never the number; `GET /v1/cost?turn_id=`
// resolves it at the gateway (spec 6/39), and anteval reads that endpoint as
// its settle barrier.

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a turn item carries. `turn_id` is the coordinate.
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
	attrTurnAddressed   = "addressed"
	// AttrTurnStartedAt is when the turn was dispatched, the only attribute
	// here compared by order. Exported because the move that carries these
	// rows names the instant it re-renders.
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
	StartedAt    time.Time
	Folder       string
	Topic        string
	ChatJID      string
	Trigger      string
	TriggerMsgID string
	ReturnTo     string
	State        string
	RunID        string
	RunReturned  bool
	RetryCount   int
	// Addressed: a person put a question to the folder and is owed an answer.
	// False for a turn bound before the attribute existed.
	Addressed bool
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
		RunID:        it.Attrs[attrTurnRunID],
		RunReturned:  it.Attrs[attrTurnRunReturned] == flagSet,
		Addressed:    it.Attrs[attrTurnAddressed] == flagSet,
	}
	tc.StartedAt, _ = time.Parse(time.RFC3339Nano, it.Attrs[AttrTurnStartedAt])
	tc.RetryCount, _ = strconv.Atoi(it.Attrs[attrTurnRetryCount])
	return tc
}

// OpenTurnContext binds a turn to the (folder, topic, chat) it came from, so a
// late callback carrying only a turn id can resolve its target. returnTo is
// the delegation return address: when set, the callback surface delivers
// there instead of to the child folder's JID.
//
// It answers live=false WHEN THE TURN IS ALREADY DONE, the duplicate-output
// guard: when an earlier batch finishes and a later batch in the same poll
// steers, the cursor does not advance and the next poll re-feeds the
// completed batch. The decision is the write's own condition, not a read: the
// create commits when nothing has bound this turn, and its REFUSAL sends the
// caller to the conditional update. `started_at` is written ONCE, by the
// create, so the expiry sweep measures from the turn's first attempt.
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
	// A re-dispatch REPLACES the binding and clears the run-returned flag in
	// one write: a turn re-opened with the previous attempt's flag still set
	// answers every late callback with turn_done. The empty fields of `bound`
	// are removed rather than left: a stale return address would deliver the
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
// nobody bound writes nothing, and that is not an error.
func (s *Store) setTurnAttrs(turnID string, attrs map[string]string) error {
	key, err := turnKey(turnID)
	if err != nil {
		return err
	}
	// A refusal is a turn nobody bound.
	if _, err := s.TryWriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Update, Entity: TurnContexts, Key: key, Attrs: attrs,
	}); err != nil {
		return fmt.Errorf("update turn %s: %w", turnID, err)
	}
	return nil
}

// SetTurnTriggerMsg records the platform id of the message that triggered a
// turn: the message a threaded reply roots a new platform thread on.
func (s *Store) SetTurnTriggerMsg(turnID, msgID string) error {
	return s.setTurnAttrs(turnID, map[string]string{attrTurnTriggerMsg: msgID})
}

// SetTurnState flips a turn's lifecycle state.
func (s *Store) SetTurnState(turnID, state string) error {
	return s.setTurnAttrs(turnID, map[string]string{attrTurnState: state})
}

// SetTurnAddressed records whether a person put a question to the turn's folder
// (spec 6/47 §"Addressed"). The attribute is a set flag and false removes it, so
// a re-dispatch that resolves differently clears a stale true.
func (s *Store) SetTurnAddressed(turnID string, addressed bool) error {
	key, err := turnKey(turnID)
	if err != nil {
		return err
	}
	write := objstore.Write{Op: objstore.Update, Entity: TurnContexts, Key: key}
	if addressed {
		write.Attrs = map[string]string{attrTurnAddressed: flagSet}
	} else {
		write.Remove = []string{attrTurnAddressed}
	}
	// A refusal is a turn nobody bound.
	if _, err := s.TryWriteEntities(context.Background(), nil, write); err != nil {
		return fmt.Errorf("update turn %s: %w", turnID, err)
	}
	return nil
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
// turn: the handle that resolves a turn to its own numbers, the pointer
// arizuko keeps in place of a copy of them (spec 6/39).
func (s *Store) SetTurnGatewayKey(turnID, keyHash string) error {
	return s.setTurnAttrs(turnID, map[string]string{attrTurnGatewayKey: keyHash})
}

// TurnGatewayKey answers the key hash that metered a turn. Empty means the turn
// ran with no gateway, so no meter holds a number for it, which the cost
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

// retryRounds bounds RecordRetryAttempt's compare-and-swap. A refusal is a lost
// race and nothing else, so it is the transient class that may be retried.
const retryRounds = 8

// RecordRetryAttempt moves a turn to its next attempt and answers which attempt
// that is. THE COUNT AND THE STATE ARE ONE WRITE: a turn whose count moved but
// whose state stayed `running` has burned an attempt and is still fed to crash
// recovery as live. objstore.Increment cannot carry them together (a batch
// holds one action per item), so the compare-and-swap is written here, guarded
// on the count this call read.
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
// turn recorded against the run, resolved the way dispatch picks the
// indicator's chat. A follow-up that arrives mid-run is STEERED into the live
// container by folder, so the steered turn's chat is not always the running
// turn's chat, and its indicator was handed to a submit_turn that never fires
// when the run dies at the timeout wall (BUGS T1).
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
// or waiting for a retry. The exact set is named HERE, because a filter is a
// conjunction and "one of these two states" is not one; naming the set rather
// than testing for "not terminal" keeps a state added later out of the
// crash-recovery feed until someone decides it belongs there.
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
// it reached: the operator's outcome read. The filter narrows on started_at,
// but a filter is not an index, so the driver still examines the partition; a
// window is an operator's read, not a hot path.
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

// SweepExpiredRunning moves every turn that has been running longer than the
// run timeout to `expired`, and answers how many moved. `expired` is DISTINCT
// from `done` on purpose: it stops crash recovery re-feeding the turn without
// tripping the done-guard against a legitimate re-dispatch. Each write carries
// the state it read as its own condition, so a turn that finished between the
// read and the write is left alone.
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

// ChatHasRunningTurn reports whether a chat has a turn still running: the
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

// CountTurnContexts answers how many turns are bound.
func (s *Store) CountTurnContexts(ctx context.Context) (int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: TurnContexts, Partition: OwnerRoutd,
	})
	if err != nil {
		return 0, fmt.Errorf("read the turn contexts: %w", err)
	}
	return len(items), nil
}
