package store

// turn_results.go holds every read and write of `turn_results`: one item per
// (folder, turn) the agent has reported an outcome for, and the claim that makes
// reporting it once mean once.
//
// The partition is OwnerRoutd and the sort key folder + turn_id: every
// operation names both, and a group delete keeps these items (a re-created
// folder mints new turn ids). No drainage: one item per turn, kept as history
// (4,242 on krons). The engine compares nothing here by order, so
// `recorded_at` carries no width declaration; ListTurnUsage filters on it
// after the read.
//
// The item also holds what the turn used, as the harness reported it
// (specs/6/39-gateway-owns-spend.md §"The turn usage record"): written in the
// claim's own Put, so a recorded turn and its usage land together or not at
// all.
//
// THE CLAIM IS THE WHOLE FILE. RecordTurnResult answers whether THIS call
// recorded the outcome, and five side effects hang off that bit, the delivery
// of the agent's prose above all: a second `true` would deliver the same answer
// to the chat twice. The create's own REFUSAL is the answer, and there is NO
// merge on the refusal: a second report writes nothing at all.

import (
	"context"
	"fmt"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a turn result carries. `folder` and `turn_id` are the
// coordinate.
const (
	attrTurnSessionID = "session_id"
	attrTurnStatus    = "status"
	// attrTurnError is the agent's OWN report of why the turn failed. The
	// container's stderr tail says the same thing worse — truncated mid-word
	// and mixed with ant's log lines (BUGS L349).
	attrTurnError = "error"
	// AttrTurnRecordedAt is when the outcome landed. Exported because the move
	// that carries these rows names the instant it re-renders.
	AttrTurnRecordedAt = "recorded_at"
	attrTurnHarness    = "harness"
	attrTurnMetered    = "metered"
	attrTurnModels     = "models"
)

// TurnUsage is what one turn used, as its harness reported it. Models is the
// harness's per-model report as JSON; routd owns its shape (routd/api/v1
// ModelUsage) and this package stores the text. Metered says the instance
// had a model gateway, which then holds the turn's cost.
type TurnUsage struct {
	Folder     string
	TurnID     string
	RecordedAt string
	Harness    string
	Metered    bool
	Models     string
}

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

// RecordTurnResult records the agent-submitted outcome and usage for one turn,
// and answers whether THIS call is the one that recorded it (file header).
// usage's Folder, TurnID and RecordedAt are ignored: the coordinate is folder
// and turnID, and the instant is now.
func (s *Store) RecordTurnResult(folder, turnID, sessionID, status, failure string,
	usage TurnUsage) (bool, error) {
	key, err := turnResultKey(folder, turnID)
	if err != nil {
		return false, err
	}
	attrs := map[string]string{AttrTurnRecordedAt: core.NowTS()}
	setAttr(attrs, attrTurnSessionID, sessionID)
	setAttr(attrs, attrTurnStatus, status)
	setAttr(attrs, attrTurnError, failure)
	setAttr(attrs, attrTurnHarness, usage.Harness)
	setAttr(attrs, attrTurnModels, usage.Models)
	if usage.Metered {
		attrs[attrTurnMetered] = flagSet
	}
	first, err := s.TryWriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Put, Entity: TurnResults, Key: key, Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
	if err != nil {
		return false, fmt.Errorf("record the outcome of %s in %s: %w", turnID, folder, err)
	}
	return first, nil
}

// GetTurnResult answers what the agent reported for one turn. A turn with no
// item is "pending", DISTINCT from a read that failed: an error read as
// pending is a browser stream that never ends, and an error read as finished
// is one that closes on a live turn (BUGS L83).
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
// A read that FAILED is not "not recorded": the session-id backstop and the
// silent-turn alarm both act on a false, so the error travels.
func (s *Store) TurnResultRecorded(folder, turnID string) (bool, error) {
	key, err := turnResultKey(folder, turnID)
	if err != nil {
		return false, err
	}
	_, ok, err := s.itemAt(context.Background(), TurnResults, key)
	return ok, err
}

// ListTurnUsage answers the usage of every turn recorded at or after since, a
// core.TS instant; "" reads them all. A turn whose harness reported no usage
// is not listed.
func (s *Store) ListTurnUsage(ctx context.Context, since string) ([]TurnUsage, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: TurnResults, Partition: OwnerRoutd,
		Where: []objstore.Cmp{{Attr: attrTurnModels, Op: objstore.Present}},
		Project: []string{AttrTurnRecordedAt, attrTurnHarness, attrTurnMetered,
			attrTurnModels},
	})
	if err != nil {
		return nil, fmt.Errorf("read the turn usage: %w", err)
	}
	out := []TurnUsage{}
	for _, it := range items {
		at := it.Attrs[AttrTurnRecordedAt]
		if at < since {
			continue
		}
		folder, turnID, ok := strings.Cut(it.Key.Sort, KeySep)
		if !ok {
			return nil, fmt.Errorf("turn result key %q carries no separator", it.Key.Sort)
		}
		out = append(out, TurnUsage{
			Folder: folder, TurnID: turnID, RecordedAt: at,
			Harness: it.Attrs[attrTurnHarness],
			Metered: it.Attrs[attrTurnMetered] == flagSet,
			Models:  it.Attrs[attrTurnModels],
		})
	}
	return out, nil
}

// CountTurnResults answers how many turns hold an outcome.
func (s *Store) CountTurnResults(ctx context.Context) (int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: TurnResults, Partition: OwnerRoutd,
	})
	if err != nil {
		return 0, fmt.Errorf("read the turn results: %w", err)
	}
	return len(items), nil
}

// TurnFailure answers the agent's own report of why a turn failed, or "" when
// it reported none. routd prefers it over the container's stderr tail when it
// tells the user a turn died.
func (s *Store) TurnFailure(folder, turnID string) string {
	key, err := turnResultKey(folder, turnID)
	if err != nil {
		return ""
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: TurnResults, Partition: key.Partition,
		Key:     objstore.KeyPredicate{Op: objstore.ExactKey, Value: key.Sort},
		Project: []string{attrTurnError},
	})
	if err != nil || len(items) == 0 {
		return ""
	}
	return items[0].Attrs[attrTurnError]
}
