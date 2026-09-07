package emaistore

// email_threads.go holds every read and write of `email_threads` AND of
// `email_msg_ids`. The two are one entity here: the index exists only to reach
// a thread, and neither operation below touches one without the other.
// Splitting them by entity would split the batch that keeps a Message-ID from
// being recorded without the thread it names, which is 6/37's third rule.
//
// The threading model is the one RFC 5322 gives: a mail's Message-ID addresses
// it forever, and a reply names its parent in In-Reply-To. So `email_msg_ids`
// maps every Message-ID emaid has seen to a thread, and `email_threads` carries
// what a reply needs — the address to answer and the root the References header
// is built from.

import (
	"fmt"

	"github.com/kronael/arizuko/objstore"
)

// A Thread is one conversation: who emaid answers, and the Message-ID every
// reply threads under.
type Thread struct {
	ThreadID    string
	FromAddress string
	RootMsgID   string
}

// Thread answers what a reply into threadID needs. It is the read behind both
// Send and FetchHistory, so what the two address is decided in one place.
//
// A store error reads as "no such thread". Both callers answer the agent with
// "thread not found" and carry no other path, so a fault is indistinguishable
// from a missing thread at the surface. That is a real cost: a broken engine
// looks to the agent like a forgotten conversation.
func (s *Store) Thread(threadID string) (Thread, bool) {
	it, ok, err := s.point(EmailThreads, threadID, "from_address", "root_msg_id")
	if err != nil || !ok {
		return Thread{}, false
	}
	return Thread{
		ThreadID:    threadID,
		FromAddress: it.Attrs["from_address"],
		RootMsgID:   it.Attrs["root_msg_id"],
	}, true
}

// ThreadForMessage answers which thread a Message-ID belongs to. The poller
// asks it of every In-Reply-To an inbound mail carries, and the first hit is
// the thread the mail joins; a miss opens a new one.
//
// It is two point reads and not a join: the index names the thread, the thread
// carries the rest. A store error reads as a miss, so a broken engine opens a
// new thread rather than joining the one the mail belongs to.
func (s *Store) ThreadForMessage(msgID string) (Thread, bool) {
	idx, ok, err := s.point(EmailMsgIDs, msgID, "thread_id")
	if err != nil || !ok {
		return Thread{}, false
	}
	return s.Thread(idx.Attrs["thread_id"])
}

// LinkMessageToThread records that msgID belongs to threadID, opening the
// thread if it is new. THE TWO WRITES SHARE ONE BATCH, and that is the
// invariant this file exists to protect: either half without the other is a
// lookup that misses, and a miss opens a SECOND thread for a conversation the
// user is already in (BUGS L3). The caller aborts the mail on any error here,
// so the message stays unseen and the next poll retries the whole of it.
//
// Neither write displaces what is there. A Message-ID already mapped keeps the
// thread it was mapped to, and a thread already open keeps the address and root
// it was opened with — both are ItemAbsent puts, so the engine's own uniqueness
// decides it rather than a read this package performed.
func (s *Store) LinkMessageToThread(msgID, threadID, fromAddress, rootMsgID string) error {
	// A refusal means an item this call read as absent has since been written,
	// and an item here is never deleted — so every round leaves strictly fewer
	// absent items than the last. Two items, so the third round has none left
	// to write and returns above.
	for range 3 {
		writes, err := s.linkWrites(msgID, threadID, fromAddress, rootMsgID)
		if err != nil {
			return fmt.Errorf("read what %s already names: %w", msgID, err)
		}
		if len(writes) == 0 {
			return nil
		}
		committed, err := s.s.GuardedBatch(ctx(), nil, writes)
		if err != nil {
			return fmt.Errorf("record thread %s for %s: %w", threadID, msgID, err)
		}
		if committed {
			return nil
		}
	}
	return fmt.Errorf("record thread %s for %s: three rounds refused, which cannot "+
		"happen while an item here is never deleted", threadID, msgID)
}

// linkWrites is the pair of writes the link still needs — the ones whose items
// are absent. An item that is already there is left exactly as it is, which is
// what makes a re-polled message a no-op rather than a rewrite.
func (s *Store) linkWrites(msgID, threadID, fromAddress, rootMsgID string) ([]objstore.Write, error) {
	var writes []objstore.Write
	_, open, err := s.point(EmailThreads, threadID, "root_msg_id")
	if err != nil {
		return nil, err
	}
	if !open {
		writes = append(writes, objstore.Write{
			Op: objstore.Put, Entity: EmailThreads, Key: key(threadID),
			Attrs: map[string]string{"from_address": fromAddress, "root_msg_id": rootMsgID},
			Cond:  objstore.Cond{Item: objstore.ItemAbsent},
		})
	}
	_, mapped, err := s.point(EmailMsgIDs, msgID, "thread_id")
	if err != nil {
		return nil, err
	}
	if !mapped {
		writes = append(writes, objstore.Write{
			Op: objstore.Put, Entity: EmailMsgIDs, Key: key(msgID),
			Attrs: map[string]string{"thread_id": threadID},
			Cond:  objstore.Cond{Item: objstore.ItemAbsent},
		})
	}
	return writes, nil
}
