package msgsearch

// parity_test.go is the measurement spec 6/42 §12 item 4 asks for, on a corpus
// the size of the instance that runs it: how long the boot walk takes and what
// the index costs in memory.
//
// It is skipped under -short, because 24,296 messages is not a unit test's
// second.

import (
	"context"
	"fmt"
	"math/rand"
	"runtime"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// The krons census of 2026-09-12: what the live instance holds, which is the
// size the walk has to be fast at.
const (
	corpusMessages = 24296
	corpusChats    = 136
)

// vocabulary is drawn from with a skew, so a query term can be common or rare
// the way a real one is: word i is picked with weight 1/(i+1), which is the
// shape word frequencies have.
var vocabulary = buildVocabulary()

func buildVocabulary() []string {
	out := make([]string, 0, 400)
	for i := range 400 {
		out = append(out, fmt.Sprintf("w%03d", i))
	}
	return out
}

// seedCorpus builds a deterministic corpus: every message in one of
// corpusChats chats, at its own instant, with eight to forty words.
func seedCorpus() *corpus {
	r := rand.New(rand.NewSource(42))
	c := &corpus{rows: map[string][]store.IndexedMessage{}}
	for i := range corpusChats {
		c.chats = append(c.chats, fmt.Sprintf("tg:%03d", i))
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range corpusMessages {
		chat := c.chats[i%corpusChats]
		words := make([]byte, 0, 256)
		for range 8 + r.Intn(33) {
			// 1/(n+1) weighting: the square of a uniform draw over the
			// vocabulary index, which makes the head of the list common.
			u := r.Float64()
			idx := int(u * u * float64(len(vocabulary)))
			words = append(words, vocabulary[idx]...)
			words = append(words, ' ')
		}
		ts := base.Add(time.Duration(i) * time.Second)
		c.rows[chat] = append(c.rows[chat], store.IndexedMessage{
			ChatJID:   chat,
			Sort:      store.MsgStamp(core.TS(ts), fmt.Sprintf("m%05d", i)),
			Timestamp: ts,
			Sender:    fmt.Sprintf("u%02d", i%20),
			Content:   string(words),
		})
	}
	return c
}

// TestTheWalkAndTheIndexAreAffordable reports the two numbers the reopen
// trigger of spec 6/42 §8 is written against: above one second of boot or a
// hundred megabytes of index, ranked search leaves routd's process.
//
// The walk's number is wall clock, so it is a measurement of the machine as
// well as of the index — on a box already running another suite the same walk
// doubles. Read the logged number before believing a failure here.
func TestTheWalkAndTheIndexAreAffordable(t *testing.T) {
	if testing.Short() {
		t.Skip("a corpus of 24,296 messages is not a -short test")
	}
	if raceDetector {
		t.Skip("under -race the number measured is the instrumentation's")
	}
	src := seedCorpus()
	var base, loaded runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&base)

	ix := New()
	start := time.Now()
	if err := ix.Build(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	walk := time.Since(start)

	// The corpus goes, so what is measured is the index and the message bodies
	// it keeps for the snippet — which on a live instance is the only copy.
	src = nil
	runtime.GC()
	runtime.ReadMemStats(&loaded)
	heap := float64(loaded.HeapAlloc-base.HeapAlloc) / (1 << 20)
	t.Logf("boot walk: %v for %d messages over %d chats; index heap: %.1f MB",
		walk, ix.size(), corpusChats, heap)
	if ix.size() != corpusMessages {
		t.Fatalf("the walk indexed %d of %d messages", ix.size(), corpusMessages)
	}
	if walk > time.Second {
		t.Errorf("the boot walk took %v; above a second the reopen trigger of 6/42 §8 fires",
			walk)
	}
	if heap > 100 {
		t.Errorf("the index holds %.1f MB; above a hundred the reopen trigger fires", heap)
	}

	// One search over the whole corpus, so the query cost is measured too.
	start = time.Now()
	hits, err := ix.Search(vocabulary[0], "", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("one common-term search over %d messages: %v for %d hits",
		ix.size(), time.Since(start), len(hits))
	runtime.KeepAlive(ix)
}
