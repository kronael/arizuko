package runstore

// import.go writes a whole runed.db into the entities this package reads: the
// destination half of the one-way data move (spec 6/37 step 7). The tool
// knows the old schema and nothing about items; this file knows items and
// nothing about the old schema; a Snapshot of plain values is all that
// crosses. Two things are rewritten rather than copied: EVERY TIMESTAMP goes
// through core.TS, because the sweep compares created_at by byte order (BUGS
// L97), and THE SESSION LOG'S IDENTITY becomes its sort key, with the old id
// as the suffix so order survives.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// A Snapshot is one runed.db as plain values.
type Snapshot struct {
	Spawns   []SpawnRow
	Sessions []SessionRow
	Breakers []BreakerRow
}

// SpawnRow is one legacy `spawns` row. SessionLogID is the old sequence number
// the row referenced, zero when it opened no session.
type SpawnRow struct {
	RunID         string
	Folder        string
	Topic         string
	ContainerName string
	SessionLogID  int64
	SessionID     string
	Kind          string
	State         string
	Outcome       string
	ExitCode      int
	Steered       bool
	CreatedAt     string
	StartedAt     string
	EndedAt       string
}

// SessionRow is one legacy `session_log` row, carrying the sequence number its
// new id is built from.
type SessionRow struct {
	ID           int64
	GroupFolder  string
	SessionID    string
	StartedAt    string
	EndedAt      string
	Result       string
	Error        string
	MessageCount int
}

// BreakerRow is one legacy `circuit_breaker` row.
type BreakerRow struct {
	Folder   string
	Failures int
}

// Counts is what an import wrote, per entity, for the operator to check against
// the source.
type Counts struct {
	Spawns   int
	Sessions int
	Breakers int
}

// LegacySessionID is the id a migrated session gets: the instant it started at
// core.TS's fixed width, plus the row's own sequence number as the suffix.
// The sequence number rather than randomness makes the import RE-RUNNABLE and
// carries the ORDER through a second-resolution column (760 krons rows share
// a (folder, second) with another).
func LegacySessionID(r SessionRow) (string, error) {
	at, err := time.Parse(time.RFC3339Nano, r.StartedAt)
	if err != nil {
		return "", fmt.Errorf("session %d started at %q, which is not a time: %w",
			r.ID, r.StartedAt, err)
	}
	if r.ID < 0 {
		return "", fmt.Errorf("session %d has no sequence number to carry", r.ID)
	}
	return sessionIDAt(core.TS(at), fmt.Sprintf("%0*x", sessionSuffixHex, r.ID)), nil
}

// Import writes snap into the entities and answers what landed. The writes are
// UNCONDITIONAL: a migration is re-runnable and every id is a function of the
// source row, so re-importing writes the same item. The create-if-absent that
// protects a RUNNING system stays on the live writers.
func (s *Store) Import(ctx context.Context, snap Snapshot) (Counts, error) {
	writes, counts, err := importWrites(snap)
	if err != nil {
		return Counts{}, err
	}
	if _, err := s.applyInChunks(ctx, "import runed.db", writes); err != nil {
		return Counts{}, err
	}
	return counts, nil
}

// CheckSnapshot reports whether the entities can represent snap, writing
// nothing: a row the store cannot address is named BEFORE the run.
func CheckSnapshot(snap Snapshot) (Counts, error) {
	_, counts, err := importWrites(snap)
	return counts, err
}

// importWrites turns a snapshot into the writes that represent it, or names
// why it cannot be represented. Every refusal is reported at once: an operator
// deciding what to do about the data needs the whole picture.
func importWrites(snap Snapshot) ([]objstore.Write, Counts, error) {
	var (
		writes  []objstore.Write
		counts  Counts
		refused []string
	)
	// sessionKeys maps a legacy sequence number to the (folder, id) a spawn's
	// session_log_id has to name; the spawn writes resolve through it.
	sessionKeys := map[int64]sessionRef{}
	for _, r := range snap.Sessions {
		if r.GroupFolder == "" {
			// The folder IS the partition, and a query without one is refused by
			// every driver, so a blank folder writes an item nothing reads back.
			refused = append(refused, fmt.Sprintf("session %d names no group_folder", r.ID))
			continue
		}
		id, err := LegacySessionID(r)
		if err != nil {
			refused = append(refused, err.Error())
			continue
		}
		if was, dup := sessionKeys[r.ID]; dup {
			refused = append(refused, fmt.Sprintf("two sessions carry the sequence number %d, "+
				"and both address %s", r.ID, was.id))
			continue
		}
		ended, err := normalizeTS(fmt.Sprintf("session %d ended_at", r.ID), r.EndedAt)
		if err != nil {
			refused = append(refused, err.Error())
			continue
		}
		sessionKeys[r.ID] = sessionRef{id: id, folder: r.GroupFolder}
		attrs := map[string]string{}
		set(attrs, attrSessSessionID, r.SessionID)
		set(attrs, attrSessEndedAt, ended)
		set(attrs, attrSessResult, r.Result)
		set(attrs, attrSessError, r.Error)
		if r.MessageCount != 0 {
			attrs[attrSessMsgCount] = strconv.Itoa(r.MessageCount)
		}
		writes = append(writes, objstore.Write{
			Op: objstore.Put, Entity: SessionLog,
			Key: sessionKey(r.GroupFolder, id), Attrs: attrs,
		})
		counts.Sessions++
	}

	claimed := map[string]bool{}
	for _, r := range snap.Spawns {
		if r.RunID == "" {
			refused = append(refused, fmt.Sprintf("a spawn in %q has no run_id, and every "+
				"read and write addresses one by it", r.Folder))
			continue
		}
		if claimed[r.RunID] {
			refused = append(refused, fmt.Sprintf("run %s appears twice", r.RunID))
			continue
		}
		created, err := normalizeTS("run "+r.RunID+" created_at", r.CreatedAt)
		if err != nil {
			refused = append(refused, err.Error())
			continue
		}
		if created == "" {
			// The sweep compares it, so a spawn with no created_at would never
			// age out and would hold the partition open forever.
			refused = append(refused, "run "+r.RunID+" has no created_at, and the sweep "+
				"deletes by it")
			continue
		}
		started, serr := normalizeTS("run "+r.RunID+" started_at", r.StartedAt)
		ended, eerr := normalizeTS("run "+r.RunID+" ended_at", r.EndedAt)
		if serr != nil || eerr != nil {
			refused = append(refused, firstError(serr, eerr).Error())
			continue
		}
		logID := ""
		if r.SessionLogID != 0 {
			sess, ok := sessionKeys[r.SessionLogID]
			switch {
			case !ok:
				refused = append(refused, fmt.Sprintf("run %s points at session %d, which the "+
					"snapshot does not carry", r.RunID, r.SessionLogID))
				continue
			case sess.folder != r.Folder:
				// The link is a sort key inside the spawn's own folder partition,
				// so a session in another folder is not addressable from here.
				refused = append(refused, fmt.Sprintf("run %s in %q points at session %d in %q",
					r.RunID, r.Folder, r.SessionLogID, sess.folder))
				continue
			default:
				logID = sess.id
			}
		}
		claimed[r.RunID] = true
		kind := r.Kind
		if kind == "" {
			kind = KindAgent
		}
		attrs := map[string]string{
			attrFolder:    r.Folder,
			attrContainer: r.ContainerName,
			attrKind:      kind,
			attrState:     r.State,
			attrCreatedAt: created,
		}
		set(attrs, attrTopic, r.Topic)
		set(attrs, attrSessionLogID, logID)
		set(attrs, attrSessionID, r.SessionID)
		set(attrs, attrOutcome, r.Outcome)
		set(attrs, attrStartedAt, started)
		set(attrs, attrEndedAt, ended)
		if r.ExitCode != 0 {
			attrs[attrExitCode] = strconv.Itoa(r.ExitCode)
		}
		if r.Steered {
			attrs[attrSteered] = "1"
		}
		writes = append(writes, objstore.Write{
			Op: objstore.Put, Entity: Spawns, Key: spawnKey(r.RunID), Attrs: attrs,
		})
		counts.Spawns++
	}

	seenFolder := map[string]bool{}
	for _, r := range snap.Breakers {
		switch {
		case r.Folder == "":
			refused = append(refused, "a circuit_breaker row names no folder, and the folder "+
				"is its whole coordinate")
			continue
		case seenFolder[r.Folder]:
			refused = append(refused, fmt.Sprintf("folder %s has two breaker rows", r.Folder))
			continue
		}
		seenFolder[r.Folder] = true
		writes = append(writes, objstore.Write{
			Op: objstore.Put, Entity: CircuitBreaker, Key: breakerKey(r.Folder),
			Attrs: map[string]string{attrFailures: strconv.Itoa(r.Failures)},
		})
		counts.Breakers++
	}

	if len(refused) > 0 {
		return nil, Counts{}, fmt.Errorf("%d rows cannot be represented:\n  %s",
			len(refused), strings.Join(refused, "\n  "))
	}
	return writes, counts, nil
}

// sessionRef is where a migrated session landed: its id and folder partition.
type sessionRef struct {
	id     string
	folder string
}

// normalizeTS re-renders one stored timestamp at core.TS's width. An empty
// value stays empty, which becomes an absent attribute on the other side.
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

// firstError answers the first non-nil of two.
func firstError(a, b error) error {
	if a != nil {
		return a
	}
	return b
}
