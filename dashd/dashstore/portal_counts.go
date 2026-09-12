package dashstore

import (
	"context"
	"time"

	"github.com/kronael/arizuko/store"
)

// The portal's status dots. Each one comes as a pair: a total for an operator,
// who sees everything, and the folder-bearing column for a scoped caller, whom
// authz.go filters row by row. The pair is what makes the two answers the same
// question — a lone total would count rows the caller may not see.
//
// The list halves carry their own ceiling because a dot is a nudge, not a
// report: past the limit the number is "at least this many" and the page still
// renders.

// GroupCount is every group on the instance.
func (s *Store) GroupCount() (int, error) {
	folders, err := s.st().GroupFolders(0)
	return len(folders), err
}

// GroupFolders lists group folders in path order, up to the dot's ceiling. The
// folder IS the groups coordinate, so the read arrives in path order.
func (s *Store) GroupFolders() ([]string, error) {
	return s.st().GroupFolders(500)
}

// ErroredChatCount is the number of chats flagged errored.
func (s *Store) ErroredChatCount() (int, error) {
	jids, err := s.ErroredChatJIDs()
	return len(jids), err
}

// ErroredChatJIDs lists those chats' JIDs. The caller resolves each JID to the
// folder it routes into, because a JID does not name its own folder.
//
// IT READS THE CHAT'S OWN FLAG rather than its messages. The two statements this
// replaces asked `SELECT DISTINCT chat_jid FROM messages WHERE errored=1`, which
// is a whole-table read for a question the chat already answers — and routd's own
// twin has read the flag since the chats move, so the two surfaces disagreed
// about which chats are errored until now.
func (s *Store) ErroredChatJIDs() ([]string, error) {
	return s.st().ErroredChatJIDs()
}

// failedRunWindow is how far back the failed-run dot looks.
const failedRunWindow = 24 * time.Hour

// failedRunCeiling is the dot's own limit: past it the number is "at least this
// many" and the page still renders.
const failedRunCeiling = 1000

// FailedTaskRunCount is the number of task runs that errored in the last day.
func (s *Store) FailedTaskRunCount() (int, error) {
	runs, err := s.failedRuns()
	return len(runs), err
}

// FailedTaskOwners lists the owning folder of each of those runs, one row per
// run rather than per task: the count above counts runs too, and a pair that
// aggregated differently would disagree with itself. They read through ONE
// method for that reason.
//
// It is the one read here that crosses the boundary, and it CANNOT be one
// statement: a run log and a task are two entities, and an entity has no join.
// So the log names the task ids and the task partition answers who owns them. A
// run whose task is gone is dropped, which is what the inner join did.
func (s *Store) FailedTaskOwners() ([]string, error) {
	runs, err := s.failedRuns()
	if err != nil {
		return nil, err
	}
	st := s.st()
	ids := make([]string, 0, len(runs))
	for _, r := range runs {
		ids = append(ids, r.TaskID)
	}
	owners := map[string]string{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		owner, known := owners[id]
		if !known {
			t, ok, err := st.TaskByID(id)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			owner = t.Owner
			owners[id] = owner
		}
		out = append(out, owner)
	}
	return out, nil
}

// failedRuns is the window both halves of the dot read, so neither can drift
// from the other about which runs it counts.
func (s *Store) failedRuns() ([]store.TaskRunLog, error) {
	return s.st().FailedRunsSince(context.Background(),
		time.Now().Add(-failedRunWindow), failedRunCeiling)
}

// strings runs a single-column query and collects the column. A row that fails
// to scan is skipped rather than fatal — the dot is a nudge and one unreadable
// row must not blank the whole number.
func (s *Store) strings(q string) ([]string, error) {
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			continue
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
