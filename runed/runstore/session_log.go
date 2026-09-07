package runstore

// session_log.go holds every read and write of `session_log`: one item per
// agent session, opened when a run starts and closed when it exits.
//
// THE PARTITION IS THE FOLDER AND THE SORT KEY IS THE INSTANT IT STARTED, which
// is what 6/37 rules 4 and 5 ask for and the one entity here where the rule
// landed exactly. Every read is the same shape — "the n newest sessions in this
// folder" — so the folder is the coordinate the caller filters on and the time
// is the order, the page and the identity all at once. The two readers are
// routd's continuity hint and its inspect_session tool, both per folder, both
// per turn.
//
// A FOLDER, NOT A CONSTANT, because this entity has no drainage: nothing sweeps
// it and it grows one item per agent turn forever. Measured 2026-09-07: 15,008
// items on krons, 12,381 on sloth. A constant partition would put every folder's
// history into one collection that a per-turn read walks — authd's mistake
// (6/37 step 3) with no bound to excuse it. Concentration is real and it is
// still the right side of the trade: one krons folder holds 11,993 of those
// 15,008, and the read that touches it is `Descending` with a `Limit`, so the
// driver answers it as a range and never as a scan.
//
// THE ID IS THE SORT KEY AND THERE IS NO SEQUENCE. The table's id was
// AUTOINCREMENT, which is what 6/37 rule 4 forbids ordering by and what no
// document store has, so the id is now the instant plus a suffix — a fixed
// width, so byte order is time order (BUGS L97). What that costs every caller
// is that the id compares as text: the wire field, `core.SessionRecord.ID` and
// every test that asserted on a number all take a string, which is the same
// price `audit.Row.ID` already paid.
//
// TWO WRITES TO ONE KEY, and it stays that way. Closing is not a second item:
// nothing else ever writes the row, no invariant spans it, and a
// read-modify-write of content never happens — Close sets fields Open left
// absent. That is history a second engine carries as easily as the first
// (6/37 rule 5, "History is not always one write").

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// SessionIDWidth is the width of a session-log id, which is the sort key:
// core.TS's thirty bytes, a separator, and eight hex digits.
const SessionIDWidth = tsWidth + 1 + sessionSuffixHex

// sessionSuffixHex is how many hex digits separate two sessions that start in
// one instant. They are not hypothetical: the stored started_at was
// second-resolution and 760 krons rows share a (folder, second) with another,
// 39 of them at once.
const sessionSuffixHex = 8

// The ordered declaration is the whole reason a string id can be a time cursor:
// every driver refuses a key of another width, so a short one cannot sort into
// the wrong place (BUGS L97).
func init() { objstore.OrderedSort(SessionLog, SessionIDWidth) }

// The attributes an item carries. started_at is NOT among them: the id IS the
// instant, and storing the time twice is two facts that can disagree — the
// ordering guarantee rests on the key rather than on an attribute beside it.
const (
	attrSessSessionID = "session_id"
	attrSessEndedAt   = "ended_at"
	attrSessResult    = "result"
	attrSessError     = "error"
	attrSessMsgCount  = "message_count"
)

// defaultSessionPage is how many sessions a read answers when the caller names
// no limit. It was the reader's silent default and it stays one.
const defaultSessionPage = 20

// Session is one session_log item as a caller reads it. It is the WHOLE record:
// the dashboard's page and routd's federated read are two projections of one
// question, and answering them from one reader is what stops the two field
// lists drifting (the two statements this replaces already differed by
// group_folder and error).
type Session struct {
	ID           string
	GroupFolder  string
	SessionID    string
	StartedAt    string
	EndedAt      string
	Result       string
	Error        string
	MessageCount int
}

// sessionKey is the coordinate of one session.
func sessionKey(folder, id string) objstore.Key {
	return objstore.Key{Partition: folder, Sort: id}
}

// newSessionID is one session's identity and its position in time: the instant
// it started, at core.TS's fixed width, plus the suffix that makes two sessions
// in one instant two items.
func newSessionID(at time.Time) string { return sessionIDAt(core.TS(at), randomSessionSuffix()) }

// sessionIDAt assembles an id from a rendered instant and a suffix, so the live
// writer and the import that carries an existing row build the same shape.
func sessionIDAt(ts, suffix string) string { return ts + "-" + suffix }

// randomSessionSuffix answers eight hex digits. rand.Read on crypto/rand cannot
// fail.
func randomSessionSuffix() string {
	var b [sessionSuffixHex / 2]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("runstore: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// startedAtOf reads the instant back out of an id.
func startedAtOf(id string) string {
	if len(id) < tsWidth {
		return ""
	}
	return id[:tsWidth]
}

// OpenSession records that a run started in folder and answers the id that
// closes it.
//
// The Put carries ItemAbsent, so a suffix collision refuses rather than
// replacing a session already on file. A refusal is a fault: the suffix is four
// bytes of randomness inside one instant, so a collision means something is
// minting ids that are not random.
func (s *Store) OpenSession(ctx context.Context, folder, sessionID string) (string, error) {
	id := newSessionID(time.Now())
	attrs := map[string]string{}
	set(attrs, attrSessSessionID, sessionID)
	committed, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Put, Entity: SessionLog, Key: sessionKey(folder, id), Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}})
	if err != nil {
		return "", fmt.Errorf("open a session for %s: %w", folder, err)
	}
	if !committed {
		return "", fmt.Errorf("open a session for %s: %s is already recorded", folder, id)
	}
	return id, nil
}

// CloseSession records how a run ended on the item OpenSession created.
//
// newSessionID is the id the HARNESS emitted, which is not always the one the
// run was started with, and an empty one leaves the opened id alone — the
// COALESCE the statement carried, spelled as "do not write the attribute".
//
// A refusal means there is no such session, which is a caller fault rather than
// a race: only the run that opened the item ever closes it, and it holds the id.
func (s *Store) CloseSession(ctx context.Context, folder, id, newSessionID, result, errMsg string,
	msgs int) error {
	attrs := map[string]string{
		attrSessEndedAt:  core.NowTS(),
		attrSessMsgCount: strconv.Itoa(msgs),
	}
	set(attrs, attrSessSessionID, newSessionID)
	set(attrs, attrSessResult, result)
	set(attrs, attrSessError, errMsg)
	committed, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Update, Entity: SessionLog, Key: sessionKey(folder, id), Attrs: attrs,
	}})
	if err != nil {
		return fmt.Errorf("close session %s in %s: %w", id, folder, err)
	}
	if !committed {
		return fmt.Errorf("close session %s in %s: no such session", id, folder)
	}
	return nil
}

// RecentSessions answers the n newest sessions a folder ran, newest first.
//
// Ordering, paging and the limit are one indexed range because the sort key is
// the instant the session started: the driver reads the key backwards and stops
// at the limit, so a folder holding twelve thousand items still answers a page.
func (s *Store) RecentSessions(ctx context.Context, folder string, limit int) ([]Session, error) {
	if limit <= 0 {
		limit = defaultSessionPage
	}
	items, err := s.s.Query(ctx, objstore.Query{
		Entity: SessionLog, Partition: folder,
		Descending: true, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("read %s's recent sessions: %w", folder, err)
	}
	out := make([]Session, 0, len(items))
	for _, it := range items {
		out = append(out, sessionFrom(folder, it))
	}
	return out, nil
}

// sessionFrom renders one stored item as the record a caller reads.
func sessionFrom(folder string, it objstore.Item) Session {
	a := it.Attrs
	msgs, _ := strconv.Atoi(a[attrSessMsgCount])
	return Session{
		ID:           it.Key.Sort,
		GroupFolder:  folder,
		SessionID:    a[attrSessSessionID],
		StartedAt:    startedAtOf(it.Key.Sort),
		EndedAt:      a[attrSessEndedAt],
		Result:       a[attrSessResult],
		Error:        a[attrSessError],
		MessageCount: msgs,
	}
}
