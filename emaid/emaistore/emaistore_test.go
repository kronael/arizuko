package emaistore_test

// The storage layer's own bodies, run against the endpoint dyntest lends each
// test as its own namespace.
//
// They are about the one invariant emaid would be dangerous without: a thread
// and the Message-ID index that reaches it are written together. Without it the
// next reply to a recorded mail misses the index and opens a second thread, and
// the user sees one conversation split in two (BUGS L3).

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/kronael/arizuko/emaid/emaistore"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

// newDriver hands a test an empty emaid store in a namespace of its own.
func newDriver(t *testing.T) *dynstore.Store {
	t.Helper()
	s := dyntest.Store(t)
	for _, entity := range emaistore.Entities {
		if err := s.CreateEntity(t.Context(), entity); err != nil {
			t.Fatalf("create entity %s: %v", entity, err)
		}
	}
	return s
}

func newStore(t *testing.T) *emaistore.Store {
	t.Helper()
	return emaistore.New(newDriver(t))
}

// A linked message reads back as its thread, with the address and root a reply
// needs.
func TestLinkedMessageReadsBackAsItsThread(t *testing.T) {
	st := newStore(t)
	if err := st.LinkMessageToThread("msg-1@x.com", "aabbcc112233", "alice@x.com", "msg-1@x.com"); err != nil {
		t.Fatal(err)
	}
	got, ok := st.ThreadForMessage("msg-1@x.com")
	if !ok {
		t.Fatal("the message just linked resolves to no thread")
	}
	want := emaistore.Thread{ThreadID: "aabbcc112233", FromAddress: "alice@x.com", RootMsgID: "msg-1@x.com"}
	if got != want {
		t.Fatalf("thread reads back as %+v, want %+v", got, want)
	}
	if direct, ok := st.Thread("aabbcc112233"); !ok || direct != want {
		t.Fatalf("the same thread by id reads back as %+v (found=%v)", direct, ok)
	}
}

// An unknown Message-ID and an unknown thread are both misses, which is what
// opens a new conversation rather than answering into a wrong one.
func TestUnknownMessageAndUnknownThreadAreMisses(t *testing.T) {
	st := newStore(t)
	if _, ok := st.ThreadForMessage("never-seen@x.com"); ok {
		t.Error("an unseen Message-ID resolved to a thread")
	}
	if _, ok := st.Thread("no-such-thread"); ok {
		t.Error("an unknown thread id resolved")
	}
}

// A Message-ID keeps the thread it was FIRST linked to. The poller re-polls a
// mail whose delivery failed, so a second link with a recomputed thread must
// not move the mail into it.
func TestAMessageKeepsTheThreadItWasFirstLinkedTo(t *testing.T) {
	st := newStore(t)
	if err := st.LinkMessageToThread("msg-1@x.com", "tid1", "alice@x.com", "msg-1@x.com"); err != nil {
		t.Fatal(err)
	}
	if err := st.LinkMessageToThread("msg-1@x.com", "tid2", "bob@x.com", "msg-1@x.com"); err != nil {
		t.Fatal(err)
	}
	got, ok := st.ThreadForMessage("msg-1@x.com")
	if !ok {
		t.Fatal("the message lost its thread on the second link")
	}
	if got.ThreadID != "tid1" {
		t.Errorf("thread_id = %q, want tid1", got.ThreadID)
	}
}

// A reply linked under an existing thread resolves to that thread's root, which
// is what the References header of the answer is built from.
func TestAReplyResolvesToTheThreadRoot(t *testing.T) {
	st := newStore(t)
	if err := st.LinkMessageToThread("root@x.com", "rootTID", "alice@x.com", "root@x.com"); err != nil {
		t.Fatal(err)
	}
	if err := st.LinkMessageToThread("reply@x.com", "rootTID", "alice@x.com", "root@x.com"); err != nil {
		t.Fatal(err)
	}
	got, ok := st.ThreadForMessage("reply@x.com")
	if !ok {
		t.Fatal("the reply resolves to no thread")
	}
	if got.ThreadID != "rootTID" || got.RootMsgID != "root@x.com" {
		t.Errorf("reply resolves to %+v, want rootTID under root@x.com", got)
	}
}

// Two links of the same mail at once both succeed and leave one thread. Each
// write is a create-if-absent, so the loser of the race is REFUSED rather than
// failed, and the link re-reads and writes what is still missing.
func TestConcurrentLinksOfOneMessageBothSucceed(t *testing.T) {
	st := newStore(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			errs <- st.LinkMessageToThread("msg-dup@x.com", "tidX", "a@x.com", "msg-dup@x.com")
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("a concurrent link returned %v", err)
		}
	}
	got, ok := st.ThreadForMessage("msg-dup@x.com")
	if !ok || got.ThreadID != "tidX" {
		t.Fatalf("after two concurrent links the message resolves to %+v (found=%v)", got, ok)
	}
}

// THE INVARIANT. A link that cannot write the index must not leave the thread
// behind, because a thread with no index entry is the split conversation the
// whole operation exists to prevent. Break the two writes into two batches and
// this body goes red: the thread commits and the index does not.
func TestTheThreadAndItsIndexLandTogether(t *testing.T) {
	driver := newDriver(t)
	st := emaistore.New(refuseWrites{Store: driver, entity: emaistore.EmailMsgIDs})

	err := st.LinkMessageToThread("msg-2@x.com", "tid2", "bob@x.com", "msg-2@x.com")
	if err == nil {
		t.Fatal("a refused index write returned nil: the caller would deliver the " +
			"message as if the thread had been recorded")
	}
	if !strings.Contains(err.Error(), "msg-2@x.com") {
		t.Errorf("error %q does not name the message that was lost", err)
	}
	if got, ok := emaistore.New(driver).Thread("tid2"); ok {
		t.Fatalf("the thread landed without its index (%+v): the two writes are not one "+
			"batch, so the next reply to this mail opens a second thread", got)
	}
}

// refuseWrites answers reads out of the store it wraps and fails any batch that
// writes to one entity. It is how the invariant above is watched failing
// without breaking the entity's READ, which the link makes first.
type refuseWrites struct {
	objstore.Store
	entity string
}

func (r refuseWrites) GuardedBatch(ctx context.Context, checks []objstore.Check,
	writes []objstore.Write) (bool, error) {
	for _, w := range writes {
		if w.Entity == r.entity {
			return false, fmt.Errorf("the engine refused a write to %s", r.entity)
		}
	}
	return r.Store.GuardedBatch(ctx, checks, writes)
}

// An imported snapshot reads back through the methods emaid itself calls, which
// is the half a count cannot prove: an import writing the wrong attribute name
// would report its counts happily.
func TestImportReadsBackThroughTheStore(t *testing.T) {
	st := newStore(t)
	counts, err := st.Import(emaistore.Snapshot{
		Threads: []emaistore.ThreadRow{
			{ThreadID: "t1", FromAddress: "alice@x.com", RootMsgID: "root@x.com"},
		},
		MsgIDs: []emaistore.MsgIDRow{
			{MsgID: "root@x.com", ThreadID: "t1"},
			{MsgID: "reply@x.com", ThreadID: "t1"},
			{MsgID: "orphan@x.com", ThreadID: "gone"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if counts.Threads != 1 || counts.MsgIDs != 3 || counts.Dangling != 1 {
		t.Fatalf("counts = %+v, want 1 thread, 3 index rows, 1 dangling", counts)
	}
	got, ok := st.ThreadForMessage("reply@x.com")
	if !ok || got.FromAddress != "alice@x.com" || got.RootMsgID != "root@x.com" {
		t.Fatalf("an imported reply resolves to %+v (found=%v)", got, ok)
	}
	// A dangling index row is dead weight, not a wrong answer: it reads as a
	// miss, which is what the old inner join answered too.
	if _, ok := st.ThreadForMessage("orphan@x.com"); ok {
		t.Error("an index row naming a thread nobody imported resolved to one")
	}
}

// Import refuses what the store cannot address rather than writing an item no
// read can ever reach, and it names every such row at once.
func TestImportRefusesRowsItCannotAddress(t *testing.T) {
	_, err := emaistore.CheckSnapshot(emaistore.Snapshot{
		Threads: []emaistore.ThreadRow{
			{ThreadID: "", FromAddress: "alice@x.com"},
			{ThreadID: "t1"},
			{ThreadID: "t1"},
		},
		MsgIDs: []emaistore.MsgIDRow{{MsgID: "", ThreadID: "t1"}},
	})
	if err == nil {
		t.Fatal("a snapshot with an unaddressable id was accepted")
	}
	for _, want := range []string{"no thread_id", "appears twice", "no msg_id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not report %q", err, want)
		}
	}
}
