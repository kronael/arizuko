package store

// turn_results.go holds every read and write of `turn_results`: one item per
// (folder, turn) the agent has reported an outcome for, and the claim that makes
// reporting it once mean once.
//
// THE PARTITION IS OwnerRoutd AND THE SORT KEY IS folder + turn_id. Every one of
// the four operations names BOTH — the claim, the outcome read the browser polls,
// and the "was it recorded" probe the run-response path checks — so there is no
// read that names only a folder, only a turn, or neither. The folder leads
// because it is the half a reader groups by; nothing reads a subtree, because a
// group delete keeps these items (folder_scope.go: a re-created folder mints new
// turn ids).
//
// THE FOLDER CANNOT BE THE PARTITION even though every operation names one, and
// that is not the usual reason. It would work — no read is folder-blind — but a
// folder partition buys nothing here: there is no subtree read to make cheap and
// no whole-partition read to make expensive, so the choice is free, and the
// constant is what every other entity in this package uses. The partition is
// 4,242 items on krons, 4,927 on sloth and 2,474 on marinade (2026-09-09), and
// it has no drainage: one item per turn, kept as history.
//
// NOTHING HERE IS COMPARED BY ORDER. `recorded_at` is written and read by
// nothing — no statement this file replaces ever selected it — so it carries no
// width declaration. The move re-renders it anyway, because the live rows carry
// four widths (27 to 30 bytes across the three instances) and there is no reason
// for the one instant this entity holds to keep them.
//
// THE CLAIM IS THE WHOLE FILE. `RecordTurnResult` answers whether THIS call is
// the one that recorded the outcome, and five side effects hang off that bit:
// the timeout warning, the delivery of the agent's prose answer, the session id,
// the self-reported cost and the round-done publish. A second caller that got
// `true` would deliver the same answer to the chat twice. So the create's own
// REFUSAL is the answer — and unlike every other create-if-absent in this
// package there is NO merge on the refusal: a second report writes nothing at
// all.

import (
	"context"
	"fmt"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a turn result carries. `folder` and `turn_id` are the
// coordinate and are not among them.
const (
	attrTurnSessionID = "session_id"
	attrTurnStatus    = "status"
	// AttrTurnRecordedAt is when the outcome landed. It is exported because the
	// move that carries these rows has to name the instant it re-renders.
	AttrTurnRecordedAt = "recorded_at"
)

// turnResultPending is the status a turn with no recorded outcome reads as.
const turnResultPending = "pending"

// TurnInfo is the durable summary of a round (one container spawn). Status is
// "pending" when no outcome has been recorded.
type TurnInfo struct {
	Folder    string
	TurnID    string
	SessionID string
	Status    string
}

// turnResultKey is the coordinate of one turn's outcome.
func turnResultKey(folder, turnID string) (objstore.Key, error) {
	if folder == "" || turnID == "" {
		return objstore.Key{}, fmt.Errorf("turn result carries folder=%q turn_id=%q, and both "+
			"are its coordinate", folder, turnID)
	}
	for name, v := range map[string]string{"folder": folder, "turn_id": turnID} {
		if strings.Contains(v, KeySep) {
			return objstore.Key{}, fmt.Errorf("turn result %s=%q carries the separator that "+
				"divides the two fields of its coordinate", name, v)
		}
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: folder + KeySep + turnID}, nil
}

// RecordTurnResult records the agent-submitted outcome for one turn, and answers
// whether THIS call is the one that recorded it.
//
// The create's refusal IS the answer, so a second report writes nothing: the
// five side effects the caller hangs off `first` — the prose reply above all —
// must happen once per turn and not once per report. There is deliberately no
// merge on the refusal.
func (s *Store) RecordTurnResult(folder, turnID, sessionID, status string) (bool, error) {
	key, err := turnResultKey(folder, turnID)
	if err != nil {
		return false, err
	}
	attrs := map[string]string{AttrTurnRecordedAt: core.NowTS()}
	setAttr(attrs, attrTurnSessionID, sessionID)
	setAttr(attrs, attrTurnStatus, status)
	first, err := s.WriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Put, Entity: TurnResults, Key: key, Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
	if err != nil {
		return false, fmt.Errorf("record the outcome of %s in %s: %w", turnID, folder, err)
	}
	return first, nil
}

// GetTurnResult answers what the agent reported for one turn.
//
// A turn with no item is "pending", and that is DISTINCT from a read that
// failed: the browser's stream closes on a finished round, so an error read as
// pending is a stream that never ends, and an error read as finished is one that
// closes on a live turn (BUGS L83). The error is returned; the zero TurnInfo is
// not a status.
func (s *Store) GetTurnResult(folder, turnID string) (TurnInfo, error) {
	key, err := turnResultKey(folder, turnID)
	if err != nil {
		return TurnInfo{}, err
	}
	it, ok, err := s.itemAt(context.Background(), TurnResults, key)
	if err != nil {
		return TurnInfo{}, err
	}
	if !ok {
		return TurnInfo{Folder: folder, TurnID: turnID, Status: turnResultPending}, nil
	}
	return TurnInfo{
		Folder:    folder,
		TurnID:    turnID,
		SessionID: it.Attrs[attrTurnSessionID],
		Status:    it.Attrs[attrTurnStatus],
	}, nil
}

// TurnResultRecorded reports whether an outcome has been recorded for one turn.
//
// The run-response path reads it before it writes its own session-id backstop,
// so the agent's own value wins, and it is half of the silent-turn alarm. A read
// that FAILED is not "not recorded" — both of those consumers act on a false —
// so the error travels rather than being folded into the answer.
func (s *Store) TurnResultRecorded(folder, turnID string) (bool, error) {
	key, err := turnResultKey(folder, turnID)
	if err != nil {
		return false, err
	}
	_, ok, err := s.itemAt(context.Background(), TurnResults, key)
	return ok, err
}

// CountTurnResults answers how many turns hold an outcome. `arizuko migrate-hot`
// reads it back to check that every row travelled.
func (s *Store) CountTurnResults(ctx context.Context) (int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: TurnResults, Partition: OwnerRoutd,
	})
	if err != nil {
		return 0, fmt.Errorf("read the turn results: %w", err)
	}
	return len(items), nil
}
