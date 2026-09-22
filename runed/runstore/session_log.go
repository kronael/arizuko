package runstore

// session_log.go holds every read and write of `session_log`: one item per
// agent session, opened when a run starts and closed when it exits.
//
// THE PARTITION IS THE FOLDER AND THE SORT KEY IS THE INSTANT IT STARTED
// (6/37 rules 4 and 5): every read is "the n newest sessions in this folder",
// so the time is the order, the page and the identity at once. A folder, not
// a constant, because this entity has no drainage and grows one item per turn
// forever; the read that touches the largest folder is `Descending` with a
// `Limit`, so the driver answers it as a range. The id is the instant plus a
// suffix at a fixed width, so byte order is time order (BUGS L97), and it
// compares as text. Open and Close are TWO WRITES TO ONE KEY: Close sets
// fields Open left absent, and nothing else writes the row.

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
// one instant; 760 krons rows share a (folder, second) with another.
const sessionSuffixHex = 8

// The ordered declaration is what lets a string id be a time cursor: every
// driver refuses a key of another width (BUGS L97).
func init() { objstore.OrderedSort(SessionLog, SessionIDWidth) }

// The attributes an item carries. started_at is NOT among them: the id IS the
// instant, and storing the time twice is two facts that can disagree.
const (
	attrSessSessionID = "session_id"
	attrSessEndedAt   = "ended_at"
	attrSessResult    = "result"
	attrSessError     = "error"
	attrSessMsgCount  = "message_count"
)

// defaultSessionPage is how many sessions a read answers when the caller names
// no limit.
const defaultSessionPage = 20

// Session is one session_log item as a caller reads it. It is the WHOLE
// record: the dashboard's page and routd's federated read are two projections
// of one question, so one reader stops the two field lists drifting.
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

// newSessionID is one session's identity and its position in time: the
// instant at core.TS's fixed width, plus a suffix that makes two sessions in
// one instant two items.
func newSessionID(at time.Time) string { return sessionIDAt(core.TS(at), randomSessionSuffix()) }

// sessionIDAt assembles an id from a rendered instant and a suffix.
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
// closes it. The Put carries ItemAbsent: a suffix collision is a fault, since
// it means something is minting ids that are not random.
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
// newSessionID is the id the HARNESS emitted, not always the one the run
// started with; an empty one leaves the opened id alone. A refusal means no
// such session: a caller fault, since only the run that opened the item
// closes it.
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

// RecentSessions answers the n newest sessions a folder ran, newest first: one
// indexed range, since the sort key is the instant.
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
