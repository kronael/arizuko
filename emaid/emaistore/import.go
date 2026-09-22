package emaistore

// import.go writes emaid's threads and its Message-ID index as items from plain
// values, and owns the item shape.
//
// NOTHING IN PRODUCTION CALLS IT. LinkMessageToThread writes the thread and the
// index row that names it in ONE batch, which is 6/37's third rule, so an index
// row naming a thread that has no row — the dangling case Counts reports — is a
// state no live method reaches. A test writes one here.

import (
	"fmt"
	"strings"

	"github.com/kronael/arizuko/objstore"
)

// A Snapshot is one emaid.db as plain values.
type Snapshot struct {
	Threads []ThreadRow
	MsgIDs  []MsgIDRow
}

// ThreadRow is one conversation.
type ThreadRow struct {
	ThreadID    string
	FromAddress string
	RootMsgID   string
}

// MsgIDRow is one Message-ID and the thread it names.
type MsgIDRow struct {
	MsgID    string
	ThreadID string
}

// Counts is what an import wrote, per entity, for the operator to check against
// the source. Dangling counts the index rows naming a thread the source has no
// row for — dead weight rather than a fault, because a lookup through one
// answers "no such thread", which is what the old inner join answered too.
type Counts struct {
	Threads  int
	MsgIDs   int
	Dangling int
}

// Import writes snap into the entities. It is idempotent — every write replaces
// the item at its coordinate — so a run that failed part way is re-runnable.
// Run it with the adapter stopped: it rewrites what it is given, and a mail the
// poller recorded after the snapshot was read is not in the snapshot.
func (s *Store) Import(snap Snapshot) (Counts, error) {
	writes, counts, err := importWrites(snap)
	if err != nil {
		return Counts{}, err
	}
	// MaxBatchActions is the weakest engine's transaction bound, so the import
	// respects it too. The writes are independent replacements, so a chunk that
	// lands and a chunk that does not leave a partial import a re-run finishes.
	for len(writes) > 0 {
		n := min(len(writes), objstore.MaxBatchActions)
		committed, err := s.s.GuardedBatch(ctx(), nil, writes[:n])
		if err != nil {
			return Counts{}, err
		}
		if !committed {
			return Counts{}, fmt.Errorf("the import batch was refused; nothing in it conditions "+
				"on anything, so the store rejected %d unconditional writes", n)
		}
		writes = writes[n:]
	}
	return counts, nil
}

// CheckSnapshot reports whether the entities can represent snap, writing
// nothing. It is what a rehearsal runs: an id the store cannot address should
// be named before the run that would stop half way through, not during it.
func CheckSnapshot(snap Snapshot) (Counts, error) {
	_, counts, err := importWrites(snap)
	return counts, err
}

// importWrites turns a snapshot into the writes that represent it, or names why
// it cannot be represented.
func importWrites(snap Snapshot) ([]objstore.Write, Counts, error) {
	var (
		writes  []objstore.Write
		counts  Counts
		threads = map[string]bool{}
		seen    = map[string]bool{}
		refused []string
	)
	// An id IS the partition, and a query without a partition is refused by
	// every driver, so a blank id writes an item nothing can ever read back.
	// Two rows at one id are one item, and the second would blow the batch up
	// mid-import rather than here.
	for _, t := range snap.Threads {
		switch {
		case t.ThreadID == "":
			refused = append(refused, fmt.Sprintf("a thread row has no thread_id (from %q)", t.FromAddress))
			continue
		case threads[t.ThreadID]:
			refused = append(refused, fmt.Sprintf("thread %s appears twice", t.ThreadID))
			continue
		}
		threads[t.ThreadID] = true
		writes = append(writes, objstore.Write{
			Op: objstore.Put, Entity: EmailThreads, Key: key(t.ThreadID),
			Attrs: map[string]string{"from_address": t.FromAddress, "root_msg_id": t.RootMsgID},
		})
		counts.Threads++
	}
	for _, m := range snap.MsgIDs {
		switch {
		case m.MsgID == "":
			refused = append(refused, fmt.Sprintf("an index row has no msg_id (naming thread %q)", m.ThreadID))
			continue
		case seen[m.MsgID]:
			refused = append(refused, fmt.Sprintf("message %s appears twice", m.MsgID))
			continue
		}
		seen[m.MsgID] = true
		if !threads[m.ThreadID] {
			counts.Dangling++
		}
		writes = append(writes, objstore.Write{
			Op: objstore.Put, Entity: EmailMsgIDs, Key: key(m.MsgID),
			Attrs: map[string]string{"thread_id": m.ThreadID},
		})
		counts.MsgIDs++
	}
	// Every refusal is reported at once: an operator deciding what to do about
	// the data needs the whole picture, not the first row the loop met.
	if len(refused) > 0 {
		return nil, Counts{}, fmt.Errorf("%d rows cannot be represented:\n  %s",
			len(refused), strings.Join(refused, "\n  "))
	}
	return writes, counts, nil
}
