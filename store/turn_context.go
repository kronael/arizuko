package store

// turn_context.go holds every read and write of `turn_context`: one item per
// live turn, carrying where the turn came from, where its answer goes, what
// state it is in, and the hash of the gateway key that metered it. A finished
// turn moves to `turn_archive` (turn_archive.go).
//
// The partition is OwnerRoutd and the sort key the turn id: a late frame
// arrives carrying a turn id and nothing else. Crash recovery, the deferred
// and owed-status passes and the expiry sweep read the one partition, which
// holds only the live turns.
//
// started_at is ordered: the expiry sweep compares it with
// `<`, and the live rows carried FOUR widths, so a stale turn survived its own
// sweep (BUGS L97). gateway_key_hash IS A POINTER AND THE ONLY ONE: arizuko
// stores WHICH key metered a turn and never the number; `GET /v1/cost?turn_id=`
// resolves it at the gateway (spec 6/39), and anteval reads that endpoint as
// its settle barrier.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	attrTurnNudged      = "nudged"
	// AttrTurnStartedAt is when the turn was dispatched, the only attribute
	// used by the expiry sweep. Exported because the move that carries these
	// rows names the instant it re-renders.
	AttrTurnStartedAt = "started_at"
)

var ErrGroupMissing = errors.New("group is missing")

func IsActiveTurnState(state string) bool {
	return state == TurnRunning || state == TurnPendingRetry || state == TurnWaitingCapacity
}

// The lifecycle states a turn passes through. `done` and `expired` are the two
// terminal ones, and they are DISTINCT: expiring a stale turn must not trip the
// done-guard against a legitimate re-dispatch.
const (
	TurnRunning         = "running"
	TurnPendingRetry    = "pending_retry"
	TurnWaitingCapacity = "waiting_capacity"
	TurnDone            = "done"
	TurnExpired         = "expired"
)

// The ordered declaration is what makes the expiry sweep a time comparison
// rather than a width comparison (BUGS L97).
func init() {
	objstore.OrderedAttr(TurnContexts, AttrTurnStartedAt, tsWidth)
	objstore.OrderedAttr(TurnContexts, "next_attempt_at", tsWidth)
}

// TurnContext is a turn's bound run-start context.
type TurnContext struct {
	Revision               string `json:"-"`
	CapacityFailures       int
	CapacityWakeCursor     string
	AttemptID              string
	InputLast              string
	ProviderFailureKind    string `json:"-"`
	ProviderFailureAttempt string
	TurnID                 string
	StartedAt              time.Time
	Folder                 string
	Topic                  string
	ChatJID                string
	Trigger                string
	TriggerMsgID           string
	ReturnTo               string
	State                  string
	RunID                  string
	RunReturned            bool
	RetryCount             int
	// Addressed: a person put a question to the folder and is owed an answer.
	// False for a turn bound before the attribute existed.
	Addressed     bool
	Nudged        int
	InputKeys     []objstore.Key
	AmbientKeys   []objstore.Key
	NextAttemptAt time.Time
	FailureCause  string
	StatusOwed    string
	// StatusChats are the chats this turn's status reached, in first-post order.
	StatusChats []string
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
func turnFrom(it objstore.Item) (TurnContext, error) {
	tc := TurnContext{
		Revision:               it.Attrs[objstore.IndexRevision],
		CapacityWakeCursor:     it.Attrs["capacity_wake_cursor"],
		AttemptID:              it.Attrs["attempt_id"],
		InputLast:              it.Attrs["input_last"],
		ProviderFailureAttempt: it.Attrs["provider_failure_attempt"],
		TurnID:                 it.Key.Sort,
		Folder:                 it.Attrs[attrTurnFolder],
		Topic:                  it.Attrs[attrTurnTopic],
		ChatJID:                it.Attrs[attrTurnChatJID],
		Trigger:                it.Attrs[attrTurnTrigger],
		TriggerMsgID:           it.Attrs[attrTurnTriggerMsg],
		ReturnTo:               it.Attrs[attrTurnReturnTo],
		State:                  it.Attrs[attrTurnState],
		RunID:                  it.Attrs[attrTurnRunID],
		RunReturned:            it.Attrs[attrTurnRunReturned] == flagSet,
		Addressed:              it.Attrs[attrTurnAddressed] == flagSet,
	}
	if kind, present := it.Attrs["provider_failure_kind"]; present {
		if kind != "transient" && kind != "permanent" {
			return TurnContext{}, fmt.Errorf("invalid provider failure kind %q", kind)
		}
		tc.ProviderFailureKind = kind
	}
	tc.StartedAt, _ = time.Parse(time.RFC3339Nano, it.Attrs[AttrTurnStartedAt])
	tc.RetryCount, _ = strconv.Atoi(it.Attrs[attrTurnRetryCount])
	tc.Nudged, _ = strconv.Atoi(it.Attrs[attrTurnNudged])
	_ = json.Unmarshal([]byte(it.Attrs["input_keys"]), &tc.InputKeys)
	_ = json.Unmarshal([]byte(it.Attrs["ambient_keys"]), &tc.AmbientKeys)
	tc.NextAttemptAt, _ = time.Parse(time.RFC3339Nano, it.Attrs["next_attempt_at"])
	tc.FailureCause = it.Attrs["failure_cause"]
	tc.StatusOwed = it.Attrs["status_owed"]
	_ = json.Unmarshal([]byte(it.Attrs["status_chats"]), &tc.StatusChats)
	if value, ok := it.Attrs["capacity_failures"]; ok {
		var err error
		tc.CapacityFailures, err = strconv.Atoi(value)
		if err != nil || tc.CapacityFailures <= 0 {
			return TurnContext{}, fmt.Errorf("turn %s: invalid capacity failures", tc.TurnID)
		}
	}
	if value, ok := it.Attrs["capacity_wake_cursor"]; ok {
		_, valid := CursorInstant(value)
		if requireMessageCursor(value) != nil || !valid {
			return TurnContext{}, fmt.Errorf("turn %s: invalid capacity wake cursor", tc.TurnID)
		}
	}
	if tc.State == TurnWaitingCapacity {
		_, hasRun := it.Attrs[attrTurnRunID]
		_, hasReturned := it.Attrs[attrTurnRunReturned]
		_, valid := CursorInstant(tc.InputLast)
		if hasRun || hasReturned || requireMessageCursor(tc.InputLast) != nil || !valid || tc.NextAttemptAt.IsZero() || len(tc.InputKeys) == 0 || tc.CapacityFailures == 0 {
			return TurnContext{}, fmt.Errorf("turn %s: invalid capacity wait", tc.TurnID)
		}
		for _, key := range tc.InputKeys {
			_, valid := CursorInstant(key.Sort)
			if key.Partition != tc.ChatJID || requireMessageCursor(key.Sort) != nil || !valid {
				return TurnContext{}, fmt.Errorf("turn %s: invalid capacity input", tc.TurnID)
			}
		}
		if tc.InputKeys[len(tc.InputKeys)-1].Sort != tc.InputLast {
			return TurnContext{}, fmt.Errorf("turn %s: invalid capacity input last", tc.TurnID)
		}
	}
	return tc, nil
}

// AddTurnStatusChat records that turnID's status reached jid, so a later status
// operation finds every chat it posted to without reading message partitions.
// The caller holds the turn's lock.
func (s *Store) AddTurnStatusChat(ctx context.Context, turnID, jid string) error {
	tc, ok := s.TurnContextOf(turnID)
	if !ok || slices.Contains(tc.StatusChats, jid) {
		return nil
	}
	chats, err := json.Marshal(append(tc.StatusChats, jid))
	if err != nil {
		return err
	}
	key, err := turnKey(turnID)
	if err != nil {
		return err
	}
	written, err := s.writeTurn(ctx, nil, nil, objstore.Write{Op: objstore.Update, Entity: TurnContexts,
		Key: key, Attrs: map[string]string{"status_chats": string(chats)}})
	if err == nil && !written {
		return fmt.Errorf("record the status chats of turn %s: the turn is gone", turnID)
	}
	return err
}

// OpenTurnContext binds a turn to the (folder, topic, chat) it came from, so a
// late callback carrying only a turn id can resolve its target. returnTo is
// the delegation return address: when set, the callback surface delivers
// there instead of to the child folder's JID.
//
// It answers live=false WHEN THE TURN IS ALREADY DONE, live or archived, the duplicate-output
// guard: when an earlier batch finishes and a later batch in the same poll
// steers, the cursor does not advance and the next poll re-feeds the
// completed batch. The decision is the write's own condition, not a read: the
// create commits when nothing has bound this turn, and its refusal sends the
// caller to the conditional update.
func (s *Store) OpenTurnContext(turnID, folder, topic, chatJID, trigger, returnTo string, admission *TurnAdmission) (bool, error) {
	key, err := turnKey(turnID)
	if err != nil {
		return false, err
	}
	ctx := context.Background()
	binding, checks, claim, err := s.prepareTurnBinding(ctx, TurnContext{TurnID: turnID, Folder: folder, Topic: topic, ChatJID: chatJID, Trigger: trigger, ReturnTo: returnTo}, admission)
	if err != nil {
		return false, err
	}
	group, err := groupKey(folder)
	if err != nil {
		return false, err
	}
	bound := binding.Attrs
	if admission != nil && admission.Controlled {
		controlChecks, controlWrites := controlAdmission(admission.ControlBefore, admission.ControlAfter)
		checks = append(checks, controlChecks...)
		claim = append(claim, controlWrites...)
	}
	fresh := map[string]string{AttrTurnStartedAt: core.NowTS()}
	for k, v := range bound {
		fresh[k] = v
	}
	put := objstore.Write{
		Op: objstore.Put, Entity: TurnContexts, Key: key, Attrs: fresh,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}
	unarchived := objstore.Check{Entity: TurnArchive, Key: key,
		Cond: objstore.Cond{Item: objstore.ItemAbsent}}
	opened, err := s.TryWriteEntities(ctx, slices.Concat(checks, []objstore.Check{unarchived}),
		slices.Concat(claim, []objstore.Write{put})...)
	if err != nil {
		return false, fmt.Errorf("bind turn %s: %w", turnID, err)
	}
	if opened {
		return true, nil
	}
	if err := s.checkAdmissionGroup(ctx, group); err != nil {
		return false, err
	}
	prior, exists, err := s.itemAt(ctx, TurnContexts, key)
	if err != nil {
		return false, err
	}
	if !exists {
		archived, ok, err := s.itemAt(ctx, TurnArchive, key)
		if err != nil {
			return false, err
		}
		if ok && archived.Attrs[attrTurnState] == TurnDone {
			return false, nil
		}
		if ok {
			opened, err := s.TryWriteEntities(ctx, checks, slices.Concat(claim, []objstore.Write{put})...)
			if err != nil || opened {
				return opened, err
			}
		}
	}
	if exists && prior.Attrs[attrTurnState] == TurnDone {
		return false, nil
	}
	// A re-dispatch REPLACES the binding and clears the run-returned flag in
	// one write: a turn re-opened with the previous attempt's flag still set
	// answers every late callback with turn_done. The empty fields of `bound`
	// are removed rather than left: a stale return address would deliver the
	// next answer to the previous delegator.
	binding.Cond = objstore.Cond{Where: []objstore.Cmp{
		{Attr: attrTurnState, Op: objstore.Ne, Value: TurnDone},
		{Attr: "next_attempt_at", Op: objstore.Absent},
	}}
	live, err := s.TryWriteEntities(ctx, checks, append(claim, binding)...)
	if err != nil {
		return false, fmt.Errorf("re-bind turn %s: %w", turnID, err)
	}
	if !live {
		if err := s.checkAdmissionGroup(ctx, group); err != nil {
			return false, err
		}
		return false, ErrModelPaused
	}
	return true, nil
}

func (s *Store) prepareTurnBinding(ctx context.Context, binding TurnContext,
	admission *TurnAdmission) (objstore.Write, []objstore.Check, []objstore.Write, error) {
	key, err := turnKey(binding.TurnID)
	if err != nil {
		return objstore.Write{}, nil, nil, err
	}
	group, err := groupKey(binding.Folder)
	if err != nil {
		return objstore.Write{}, nil, nil, err
	}
	bound := map[string]string{attrTurnState: TurnRunning, "attempt_id": rand.Text()}
	if admission != nil {
		if admission.AttemptID != "" {
			bound["attempt_id"] = admission.AttemptID
		}
		setAttr(bound, "input_last", admission.InputLast)
		if admission.InputKeys != nil {
			input, err := json.Marshal(admission.InputKeys)
			if err != nil {
				return objstore.Write{}, nil, nil, err
			}
			bound["input_keys"] = string(input)
		}
		if admission.AmbientKeys != nil {
			ambient, err := json.Marshal(admission.AmbientKeys)
			if err != nil {
				return objstore.Write{}, nil, nil, err
			}
			bound["ambient_keys"] = string(ambient)
		}
	}
	setAttr(bound, attrTurnFolder, binding.Folder)
	setAttr(bound, attrTurnTopic, binding.Topic)
	setAttr(bound, attrTurnChatJID, binding.ChatJID)
	setAttr(bound, attrTurnTrigger, binding.Trigger)
	setAttr(bound, attrTurnReturnTo, binding.ReturnTo)

	checks := []objstore.Check{{Entity: Groups, Key: group,
		Cond: objstore.Cond{Item: objstore.ItemPresent}}}
	var claim []objstore.Write
	if admission != nil && len(admission.InputKeys) > 0 {
		chat, err := chatKey(binding.ChatJID)
		if err != nil {
			return objstore.Write{}, nil, nil, err
		}
		_, exists, err := s.itemAt(ctx, Chats, chat)
		if err != nil {
			return objstore.Write{}, nil, nil, err
		}
		if exists {
			claim = append(claim, objstore.Write{Op: objstore.Update, Entity: Chats, Key: chat, Cond: objstore.Cond{Item: objstore.ItemPresent}})
		} else {
			checks = append(checks, objstore.Check{Entity: Chats, Key: chat, Cond: objstore.Cond{Item: objstore.ItemAbsent}})
		}
	}
	return objstore.Write{
		Op: objstore.Update, Entity: TurnContexts, Key: key, Attrs: bound,
		Remove: append(missingAttrs(bound, attrTurnFolder, attrTurnTopic, attrTurnChatJID,
			attrTurnTrigger, attrTurnReturnTo), attrTurnRunReturned, attrTurnRunID),
	}, checks, claim, nil
}

func (s *Store) checkAdmissionGroup(ctx context.Context, group objstore.Key) error {
	_, exists, err := s.itemAt(ctx, Groups, group)
	if err != nil {
		return err
	}
	if !exists {
		return ErrGroupMissing
	}
	return nil
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
	tc, ok, _ := s.ReadTurnContext(context.Background(), turnID)
	return tc, ok
}

func (s *Store) ReadTurnContext(ctx context.Context, turnID string) (TurnContext, bool, error) {
	it, ok, err := s.turnItem(ctx, turnID)
	if err != nil || !ok {
		return TurnContext{}, false, err
	}
	tc, err := turnFrom(it)
	return tc, err == nil, err
}

// setTurnAttrs merges attributes into a bound turn. An Update against a turn
// nobody bound writes nothing, and that is not an error.
func (s *Store) setTurnAttrs(turnID string, attrs map[string]string, remove ...string) error {
	key, err := turnKey(turnID)
	if err != nil {
		return err
	}
	// A refusal is a turn nobody bound.
	if _, err := s.TryWriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Update, Entity: TurnContexts, Key: key, Attrs: attrs, Remove: remove,
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
	key, err := turnKey(turnID)
	if err != nil {
		return err
	}
	if _, err := s.writeTurn(context.Background(), nil, nil, objstore.Write{
		Op: objstore.Update, Entity: TurnContexts, Key: key,
		Attrs: map[string]string{attrTurnState: state},
	}); err != nil {
		return fmt.Errorf("update turn %s: %w", turnID, err)
	}
	return nil
}

// IncrementNudged records a handed-out delivery nudge. The caller serializes
// the turn's decision and this write under its turn lock.
func (s *Store) IncrementNudged(turnID string) error {
	tc, ok := s.TurnContextOf(turnID)
	if !ok {
		return fmt.Errorf("no turn context for turn_id")
	}
	return s.setTurnAttrs(turnID, map[string]string{attrTurnNudged: strconv.Itoa(tc.Nudged + 1)})
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
	if err := s.WriteEntities(context.Background(), nil, write); err != nil {
		return fmt.Errorf("update turn %s: %w", turnID, err)
	}
	return nil
}

// SetRunReturned marks that the run request has returned for a turn. After this
// a late callback is answered turn_done; before it, trailing frames from a
// still-live run stay valid even past an early submit_turn.
func (s *Store) SetRunReturned(turnID string) error {
	return s.setTurnAttrs(turnID, map[string]string{attrTurnRunReturned: flagSet}, "capacity_failures", "capacity_wake_cursor")
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
	it, ok, err := s.turnItem(context.Background(), turnID)
	if err != nil || !ok {
		return "", err
	}
	return it.Attrs[attrTurnGatewayKey], nil
}

func (s *Store) RunningTurns() ([]TurnContext, error) {
	return s.indexedTurns(context.Background(), "turns/live", objstore.KeyPredicate{})
}

// TurnsSince answers every turn dispatched at or after `since`, whatever state
// it reached: the operator's outcome read, over the live turns and the archive.
// A turn bound again after it expired is answered once, live.
func (s *Store) TurnsSince(since time.Time) ([]TurnContext, error) {
	ctx := context.Background()
	live, err := objstore.IndexItems(ctx, s.Obj(), TurnContexts+"/started",
		objstore.KeyPredicate{Op: objstore.GeKey, Value: core.TS(since)}, 0)
	if err != nil {
		return nil, fmt.Errorf("read the turns dispatched since %s: %w", core.TS(since), err)
	}
	archived, err := s.archivedTurnsSince(ctx, core.TS(since))
	if err != nil {
		return nil, fmt.Errorf("read the turns archived since %s: %w", core.TS(since), err)
	}
	out := make([]TurnContext, 0, len(live)+len(archived))
	seen := map[string]bool{}
	for _, it := range append(live, archived...) {
		if !seen[it.Key.Sort] {
			seen[it.Key.Sort] = true
			tc, err := turnFrom(it)
			if err != nil {
				return nil, err
			}
			out = append(out, tc)
		}
	}
	return out, nil
}

// SweepExpiredRunning expires stale running turns without a provider refusal
// and answers how many moved. `expired` is DISTINCT
// from `done` on purpose: it stops crash recovery re-feeding the turn without
// tripping the done-guard against a legitimate re-dispatch. Each write carries
// the state it read as its own condition, so a turn that finished between the
// read and the write is left alone.
func (s *Store) SweepExpiredRunning(timeout time.Duration) (int64, error) {
	ctx := context.Background()
	cutoff := core.TS(time.Now().Add(-timeout))
	items, err := objstore.IndexItems(ctx, s.Obj(), "turns/expiry",
		objstore.KeyPredicate{Op: objstore.LtKey, Value: cutoff}, 0)
	if err != nil {
		return 0, fmt.Errorf("read the stale running turns: %w", err)
	}
	var swept int64
	for _, it := range items {
		marker := objstore.Cmp{Attr: "provider_failure_attempt", Op: objstore.Eq,
			Value: it.Attrs["provider_failure_attempt"]}
		if marker.Value == "" {
			marker.Op = objstore.Absent
		} else if marker.Value == it.Attrs["attempt_id"] {
			continue
		}
		moved, err := s.writeTurn(ctx, nil, nil, objstore.Write{
			Op: objstore.Update, Entity: TurnContexts, Key: it.Key,
			Attrs: map[string]string{attrTurnState: TurnExpired},
			Cond: objstore.Cond{Where: []objstore.Cmp{
				{Attr: attrTurnState, Op: objstore.Eq, Value: TurnRunning},
				{Attr: AttrTurnStartedAt, Op: objstore.Lt, Value: cutoff},
				marker,
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

// CountTurnContexts answers how many turns are live.
func (s *Store) CountTurnContexts(ctx context.Context) (int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: TurnContexts, Partition: OwnerRoutd,
	})
	if err != nil {
		return 0, fmt.Errorf("read the turn contexts: %w", err)
	}
	return len(items), nil
}
