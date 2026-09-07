package store

// import.go is the destination half of the data move: the source reads a
// database's old tables and hands the rows here, and this file owns the item
// shape so the tool that reads the tables does not have to (spec 6/37 step 7).
// The source half is cmd/arizuko/migrate_store.go.
//
// The split is what keeps the model declared once. The tool knows the old
// schema and nothing about items; this file knows items and nothing about the
// old schema; a Snapshot of plain values is the only thing that crosses.
//
// EVERY TIMESTAMP IS REWRITTEN RATHER THAN COPIED, through core.TS. That is not
// tidiness: `pane_sessions.opened_at` is a DECLARED ordered attribute because
// the channel read picks its winner by it, and 8 of marinade's 81 rows store a
// fraction whose trailing zero was stripped — 29 bytes against the other 73's
// 30. Byte order and time order already disagree there (BUGS L97), every driver
// refuses the short value, and re-rendering the same instant at the declared
// width is exactly what routd migration 0044 did for the three columns it
// reached. It did not reach this one.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// PaneRow is one legacy `pane_sessions` row as the source reads it. A NULL
// column arrives as the empty string, which is what an absent attribute means
// on the other side.
type PaneRow struct {
	TeamID     string
	UserID     string
	ThreadTS   string
	ChannelID  string
	ContextJID string
	OpenedAt   string
}

// UseRow is one legacy `secret_use_log` row, carrying the rowid its new id is
// built from. The table declared no primary key, so the rowid is the only thing
// that separated two uses recorded in one instant.
type UseRow struct {
	RowID     int64
	TS        string
	SpawnID   string
	CallerSub string
	Folder    string
	Tool      string
	Key       string
	Scope     string
	Status    string
	LatencyMS int64
}

// Snapshot is the legacy tables this package has moved, as plain values.
type Snapshot struct {
	Panes []PaneRow
	Uses  []UseRow
}

// Counts is what an import wrote, per entity, for the operator to check against
// the source.
type Counts struct {
	Panes int
	Uses  int
}

// CheckSnapshot reports whether the entities can represent snap, writing
// nothing. It is what a rehearsal runs: a row the store cannot address should
// be named before the run that would stop half way through, not during it.
func CheckSnapshot(snap Snapshot) (Counts, error) {
	_, counts, err := importWrites(snap)
	return counts, err
}

// Import writes snap into the entities and answers what landed.
//
// The writes are UNCONDITIONAL, which is the one place this package drops the
// create-if-absent a live write carries. A migration is re-runnable by design,
// and every key it builds is a function of the source row, so re-importing a
// source writes the same item with the same content. Refusing there would make
// a retry a failure; the condition that protects a RUNNING system stays on the
// live writers.
func (s *Store) Import(ctx context.Context, snap Snapshot) (Counts, error) {
	writes, counts, err := importWrites(snap)
	if err != nil {
		return Counts{}, err
	}
	if _, err := s.applyInChunks(ctx, "import the moved tables", writes); err != nil {
		return Counts{}, err
	}
	return counts, nil
}

// importWrites turns a snapshot into the writes that represent it, or names why
// it cannot be represented.
//
// Every refusal is collected and reported at once: an operator deciding what to
// do about the data needs the whole picture, not the first row the loop met.
func importWrites(snap Snapshot) ([]objstore.Write, Counts, error) {
	var (
		writes  []objstore.Write
		counts  Counts
		refused []string
	)
	claimed := map[string]bool{}
	for _, r := range snap.Panes {
		key, err := paneKey(r.TeamID, r.UserID, r.ThreadTS)
		if err != nil {
			refused = append(refused, err.Error())
			continue
		}
		if claimed[key.Sort] {
			refused = append(refused, fmt.Sprintf("two panes address %s", key.Sort))
			continue
		}
		opened, err := normalizeTS("pane "+key.Sort+" opened_at", r.OpenedAt)
		if err != nil {
			refused = append(refused, err.Error())
			continue
		}
		if opened == "" {
			// The channel read picks the newest by it, so a pane with no
			// opened_at would lose every comparison and could never be the pane
			// its channel is showing.
			refused = append(refused, "pane "+key.Sort+" has no opened_at, and the channel "+
				"read picks the newest by it")
			continue
		}
		claimed[key.Sort] = true
		attrs := map[string]string{
			attrPaneChannel:  r.ChannelID,
			attrPaneOpenedAt: opened,
		}
		setAttr(attrs, attrPaneContext, r.ContextJID)
		writes = append(writes, objstore.Write{
			Op: objstore.Put, Entity: PaneSessions, Key: key, Attrs: attrs,
		})
		counts.Panes++
	}

	ids := map[string]int64{}
	for _, r := range snap.Uses {
		id, err := LegacyUseID(r)
		if err != nil {
			refused = append(refused, err.Error())
			continue
		}
		if was, dup := ids[id]; dup {
			refused = append(refused, fmt.Sprintf("rows %d and %d both address %s",
				was, r.RowID, id))
			continue
		}
		ids[id] = r.RowID
		w := secretUseWrite(id, SecretUseRow{
			SpawnID:   r.SpawnID,
			CallerSub: r.CallerSub,
			Folder:    r.Folder,
			Tool:      r.Tool,
			Key:       r.Key,
			Scope:     r.Scope,
			Status:    r.Status,
			LatencyMS: r.LatencyMS,
		})
		w.Cond = objstore.Cond{}
		writes = append(writes, w)
		counts.Uses++
	}
	if len(refused) > 0 {
		return nil, Counts{}, fmt.Errorf("%d rows the entities cannot address:\n  %s",
			len(refused), strings.Join(refused, "\n  "))
	}
	return writes, counts, nil
}

// LegacyUseID is the id one legacy `secret_use_log` row becomes: the instant it
// recorded, at core.TS's width, and the row's own rowid as the suffix.
//
// THE ROWID RATHER THAN RANDOMNESS, for two reasons. It makes the import
// RE-RUNNABLE — the same source row builds the same item every time — and it is
// what carries the order through a column the broker writes several times per
// turn, where the rowid was the only thing separating two uses in one instant.
func LegacyUseID(r UseRow) (string, error) {
	at, err := time.Parse(time.RFC3339Nano, r.TS)
	if err != nil {
		return "", fmt.Errorf("use %d records %q, which is not a time: %w", r.RowID, r.TS, err)
	}
	if r.RowID < 0 {
		return "", fmt.Errorf("use %d has no rowid to carry", r.RowID)
	}
	return core.TS(at) + "-" + fmt.Sprintf("%0*x", secretUseSuffixHex, r.RowID), nil
}

// normalizeTS re-renders a stored instant at core.TS's width, so a column
// written at several widths becomes one that compares by time. An empty value
// stays empty: absent is the only null, and the caller decides whether it may
// be.
func normalizeTS(what, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", fmt.Errorf("%s is %q, which is not a time: %w", what, value, err)
	}
	return core.TS(at), nil
}
