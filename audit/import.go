package audit

// import.go is the destination half of the data move: the source reads a
// database's old audit_log table and hands the rows here, and this file owns the
// item shape so the tool that reads the table does not have to (spec 6/37 step
// 7). The source half is cmd/arizuko/migrate_audit.go.

import (
	"context"
	"fmt"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// LegacyRow is one row of the table an entity replaces, as the source reads it.
// A NULL column arrives as the empty string, which is what an absent attribute
// means on the other side.
type LegacyRow struct {
	ID            int64
	CreatedAt     string
	Category      string
	Action        string
	Actor         string
	ActorSub      string
	Resource      string
	Scope         string
	Surface       string
	ParamsSummary string
	Outcome       string
	ErrorMsg      string
	DurationMS    int64
	TurnID        string
	Folder        string
	Instance      string
	RequestID     string
	SourceIP      string
}

// Snapshot is everything one database's audit_log holds.
type Snapshot struct{ Rows []LegacyRow }

// importBatch is how many items one write carries. objstore bounds a batch at
// MaxBatchActions because DynamoDB's transaction does, and a history import is
// the one caller that would otherwise send a hundred thousand.
const importBatch = objstore.MaxBatchActions

// CheckSnapshot reports how many rows the entity can address and refuses the
// whole snapshot on the first one it cannot, so a row that would stop a run
// half way is named BEFORE the run.
func CheckSnapshot(snap Snapshot) (int, error) {
	seen := make(map[string]int64, len(snap.Rows))
	for _, r := range snap.Rows {
		id, err := LegacyID(r)
		if err != nil {
			return 0, err
		}
		if was, dup := seen[id]; dup {
			return 0, fmt.Errorf("rows %d and %d both address %s", was, r.ID, id)
		}
		seen[id] = r.ID
	}
	return len(snap.Rows), nil
}

// LegacyID is the id one legacy row becomes: the instant it recorded, at
// core.TS's width, and the row's own sequence number as the suffix.
//
// THE SUFFIX IS THE OLD id, not a random value, and that is what carries the
// order across. `created_at` alone is not unique — 59 of krons' 163,750 rows
// share an instant with another — and the old id is what separated them.
// Measured on all twelve live audit_log tables: `created_at` never disagrees
// with `id` about order, so the assembled key sorts every row exactly where the
// sequence had it.
func LegacyID(r LegacyRow) (string, error) {
	at, err := time.Parse(time.RFC3339Nano, r.CreatedAt)
	if err != nil {
		return "", fmt.Errorf("row %d records %q, which is not a time: %w", r.ID, r.CreatedAt, err)
	}
	if r.ID < 0 {
		return "", fmt.Errorf("row %d has no sequence number to carry", r.ID)
	}
	return idAt(core.TS(at), fmt.Sprintf("%0*x", suffixHex, r.ID)), nil
}

// Import writes every row of snap as an item and answers how many landed.
//
// The writes are UNCONDITIONAL, which is the one place this package drops the
// create-if-absent a live emit carries. An import is re-runnable by design — an
// operator retries a migration against the same source — and an id is the
// instant plus the row's own sequence number, so re-importing writes the same
// item with the same content. Refusing there would make a retry a failure; the
// condition that protects a RUNNING system stays on writeFor, where a live emit
// uses it.
func Import(ctx context.Context, s objstore.Store, snap Snapshot) (int, error) {
	written := 0
	for start := 0; start < len(snap.Rows); start += importBatch {
		end := min(start+importBatch, len(snap.Rows))
		writes := make([]objstore.Write, 0, end-start)
		for _, r := range snap.Rows[start:end] {
			id, err := LegacyID(r)
			if err != nil {
				return written, err
			}
			w := writeFor(id, eventOf(r), r.ParamsSummary)
			w.Cond = objstore.Cond{}
			writes = append(writes, w)
		}
		committed, err := s.GuardedBatch(ctx, nil, writes)
		if err != nil {
			return written, err
		}
		if !committed {
			return written, fmt.Errorf("rows %d..%d did not commit", start, end-1)
		}
		written += len(writes)
	}
	return written, nil
}

// eventOf renders a legacy row as the event writeFor stores, so the import
// builds its items through the same renderer a live emit does.
//
// ParamsSummary is not on the event: it arrives already redacted and already
// encoded, so writeFor takes it beside the event rather than through the map a
// live caller fills in.
func eventOf(r LegacyRow) Event {
	return Event{
		Category:   r.Category,
		Action:     r.Action,
		Actor:      r.Actor,
		ActorSub:   r.ActorSub,
		Resource:   r.Resource,
		Scope:      r.Scope,
		Surface:    r.Surface,
		Outcome:    r.Outcome,
		ErrorMsg:   r.ErrorMsg,
		DurationMS: r.DurationMS,
		TurnID:     r.TurnID,
		Folder:     r.Folder,
		Instance:   r.Instance,
		RequestID:  r.RequestID,
		SourceIP:   r.SourceIP,
	}
}
