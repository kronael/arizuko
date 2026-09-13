package msgsearch

// parity_test.go is the measurement spec 6/42 §12 item 4 asks for, on a corpus
// the size of the instance that runs it: how long the boot walk takes, what the
// index costs in memory, and whether its top ten is SQLite FTS5's top ten for
// an exact-term query.
//
// THE ORACLE IS AN FTS5 TABLE, declared the way ranked search declares this
// index — one indexed `content` column, `unicode61 remove_diacritics 2` —
// and queried with `bm25()`, so a difference is a difference in this package
// and not in the shape of the question. The database comes from dbtest, the
// one door a test reaches an engine through, and the body skips when that
// door is pointed at Postgres: FTS5 has no spelling there.
//
// It is skipped under -short, because 24,296 messages through two engines is
// not a unit test's second.

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/db_utils/dbtest"
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
		walk, ix.Size(), corpusChats, heap)
	if ix.Size() != corpusMessages {
		t.Fatalf("the walk indexed %d of %d messages", ix.Size(), corpusMessages)
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
		ix.Size(), time.Since(start), len(hits))
	runtime.KeepAlive(ix)
}

// TestTheTopTenIsFTS5sTopTen is the acceptance line of spec 6/42 §9: for an
// exact-term query, the ten messages this index puts on top are the ten FTS5
// puts on top.
func TestTheTopTenIsFTS5sTopTen(t *testing.T) {
	if testing.Short() {
		t.Skip("a corpus of 24,296 messages through two engines is not a -short test")
	}
	if raceDetector {
		t.Skip("two engines over 24,296 messages under -race is minutes, and the two " +
			"agree or not whatever the instrumentation costs")
	}
	if os.Getenv(db_utils.TestPGDSNEnv) != "" {
		t.Skip("the oracle is SQLite FTS5 and the test engine is Postgres")
	}
	src := seedCorpus()
	ix := New()
	if err := ix.Build(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	oracle := fts5Oracle(t, src)

	// Ten terms across the frequency range: the head of the vocabulary is in
	// thousands of messages, the tail in a handful.
	queries := []string{
		vocabulary[0], vocabulary[1], vocabulary[3], vocabulary[7], vocabulary[15],
		vocabulary[31], vocabulary[63], vocabulary[127], vocabulary[255], vocabulary[399],
		vocabulary[2] + " " + vocabulary[5],
		vocabulary[11] + " " + vocabulary[97],
	}
	for _, q := range queries {
		mine, err := ix.Search(q, "", "", "", 10)
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		want := oracle(t, q, 10)
		got := make([]string, len(mine))
		for i, h := range mine {
			got[i] = h.ChatJID + "@" + core.TS(h.Timestamp)
		}
		sorted := func(in []string) []string {
			out := append([]string(nil), in...)
			sort.Strings(out)
			return out
		}
		a, b := sorted(got), sorted(want)
		if len(a) != len(b) {
			t.Errorf("%q: %d hits in the top ten, FTS5 answered %d", q, len(a), len(b))
			continue
		}
		same := 0
		for i := range a {
			if a[i] == b[i] {
				same++
			}
		}
		if same != len(b) {
			t.Errorf("%q: the top ten agrees on %d of %d\n mine: %v\n fts5: %v",
				q, same, len(b), a, b)
			continue
		}
		t.Logf("%q: the top %d is FTS5's top %d", q, len(b), len(b))
	}
}

// fts5Oracle loads the corpus into an fts5 table and answers the same question
// against it, hit identity being the message's own coordinate.
func fts5Oracle(t *testing.T, src *corpus) func(*testing.T, string, int) []string {
	t.Helper()
	db := dbtest.Open(t)
	if _, err := db.Exec("CREATE VIRTUAL TABLE m USING fts5(" +
		"content, part UNINDEXED, sort UNINDEXED, " +
		"tokenize='unicode61 remove_diacritics 2')"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	ins, err := tx.Prepare("INSERT INTO m(content, part, sort) VALUES (?, ?, ?)")
	if err != nil {
		t.Fatal(err)
	}
	for _, jid := range src.chats {
		for _, row := range src.rows[jid] {
			if _, err := ins.Exec(row.Content, row.ChatJID, row.Sort); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return func(t *testing.T, query string, limit int) []string {
		t.Helper()
		rows, err := db.Query("SELECT part, sort, bm25(m) AS rank FROM m WHERE m MATCH ? "+
			"ORDER BY rank, sort DESC LIMIT ?", query, limit)
		if err != nil {
			t.Fatalf("fts5 %q: %v", query, err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var part, sortKey string
			var rank float64
			if err := rows.Scan(&part, &sortKey, &rank); err != nil {
				t.Fatal(err)
			}
			stamp, _, _ := store.MsgCoord(sortKey)
			out = append(out, part+"@"+stamp)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
}
