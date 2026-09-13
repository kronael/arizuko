package msgsearch

// The index answers an agent's question about what was said, so what is proved
// here is every step between a message and a hit: what counts as a word, what
// the query language admits, which document ranks first, what the snippet
// shows, what each narrowing removes — and that a search made before the boot
// walk finishes is REFUSED rather than answered with an empty list.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// corpus is a Source over rows a test wrote, so the boot walk is exercised by
// the same door routd's store answers.
type corpus struct {
	chats []string
	rows  map[string][]store.IndexedMessage
	fail  error
}

func (c *corpus) ChatJIDs(context.Context) ([]string, error) {
	if c.fail != nil {
		return nil, c.fail
	}
	return c.chats, nil
}

func (c *corpus) MessagesToIndex(_ context.Context, jid string) ([]store.IndexedMessage, error) {
	if c.fail != nil {
		return nil, c.fail
	}
	return c.rows[jid], nil
}

// at is a fixed instant, so a test's expectations do not move with the clock.
var at = time.Date(2026, 9, 13, 8, 0, 0, 0, time.UTC)

// row renders one message the way store.IndexedFrom renders it: the position
// carries the fixed-width instant and the id.
func row(id, chat, sender, folder, content string, minute int) store.IndexedMessage {
	ts := at.Add(time.Duration(minute) * time.Minute)
	return store.IndexedMessage{
		ChatJID:   chat,
		Sort:      store.MsgStamp(core.TS(ts), id),
		Timestamp: ts,
		Sender:    sender,
		RoutedTo:  folder,
		Content:   content,
	}
}

// built is an index over rows, already through its boot walk.
func built(t *testing.T, rows ...store.IndexedMessage) *Index {
	t.Helper()
	c := &corpus{rows: map[string][]store.IndexedMessage{}}
	for _, r := range rows {
		if _, seen := c.rows[r.ChatJID]; !seen {
			c.chats = append(c.chats, r.ChatJID)
		}
		c.rows[r.ChatJID] = append(c.rows[r.ChatJID], r)
	}
	ix := New()
	if err := ix.Build(context.Background(), c); err != nil {
		t.Fatalf("build the index: %v", err)
	}
	return ix
}

// hitIDs is what a caller can tell two hits apart by here: the chat, since one
// message per chat is what most bodies below write.
func hitIDs(hits []FoundMessage) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.ChatJID
	}
	return out
}

// A search before the walk finishes is a refusal the caller can show. An empty
// hit list would be a statement — "no message says that" — about chats the walk
// has not read, and an agent repeats it as fact.
func TestAWarmingIndexRefusesRatherThanAnswersEmpty(t *testing.T) {
	ix := New()
	hits, err := ix.Search("budget", "", "", "", 10)
	if !errors.Is(err, ErrWarming) {
		t.Fatalf("a warming index answered err=%v, %d hits; want ErrWarming", err, len(hits))
	}
	if hits != nil {
		t.Errorf("a refused search carried %d hits", len(hits))
	}
	// It opens once the walk is through, and an EMPTY store then answers an
	// empty list, because that answer is now true.
	if err := ix.Build(context.Background(), &corpus{}); err != nil {
		t.Fatal(err)
	}
	if hits, err := ix.Search("budget", "", "", "", 10); err != nil || len(hits) != 0 {
		t.Errorf("a built empty index answered %d hits, err=%v; want none and no error",
			len(hits), err)
	}
}

// A walk that failed leaves an index that must never answer: it holds whatever
// it read before the failure, and a search over that says "no such message"
// about every chat it never reached. The error reaches the caller, not just a
// log.
func TestAFailedWalkRefusesEverySearch(t *testing.T) {
	ix := New()
	boom := errors.New("the endpoint refused the read")
	if err := ix.Build(context.Background(), &corpus{fail: boom}); !errors.Is(err, boom) {
		t.Fatalf("Build answered %v, want the read's own error", err)
	}
	_, err := ix.Search("budget", "", "", "", 10)
	if !errors.Is(err, boom) {
		t.Errorf("a search after a failed walk answered %v, want the walk's error", err)
	}
}

// The tokeniser decides what a word is, and every language the instance carries
// depends on it: letters and digits make a term, everything else is a boundary,
// and diacritics are dropped so "uroven" answers "úroveň" from a plain
// keyboard.
func TestTheTokeniserFoldsCaseAndDiacritics(t *testing.T) {
	for _, c := range []struct {
		name, content, query string
		want                 bool
	}{
		{"plain word", "Q3 budget meeting", "budget", true},
		{"upper case query", "q3 budget meeting", "BUDGET", true},
		{"upper case content", "Q3 BUDGET meeting", "budget", true},
		{"a digit is a term", "invoice 2026 paid", "2026", true},
		{"punctuation is a boundary", "budget,meeting;done", "meeting", true},
		{"czech, precomposed", "nová úroveň zabezpečení", "uroven", true},
		{"czech, decomposed", "nová úroveň", "uroven", true},
		{"accented query, plain content", "nova uroven", "úroveň", true},
		{"a prefix is not a match", "budgeting workshop", "budget", false},
		{"a different word is not a match", "budget meeting", "budgets", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ix := built(t, row("m", "tg:1", "u", "", c.content, 0))
			hits, err := ix.Search(c.query, "", "", "", 10)
			if err != nil {
				t.Fatalf("search %q: %v", c.query, err)
			}
			if got := len(hits) == 1; got != c.want {
				t.Errorf("%q over %q: %d hits, want match=%v", c.query, c.content,
					len(hits), c.want)
			}
		})
	}
}

// The grammar is terms, "phrases" and `-`. Every form is here, and so is every
// refusal: a malformed query that answered an empty list would be read as "no
// message says that".
func TestTheQueryGrammar(t *testing.T) {
	rows := []store.IndexedMessage{
		row("a", "tg:a", "u", "", "budget meeting tomorrow", 0),
		row("b", "tg:b", "u", "", "plan for next quarter", 1),
		row("c", "tg:c", "u", "", "budget review with finance", 2),
		row("d", "tg:d", "u", "", "meeting about the budget", 3),
	}
	ix := built(t, rows...)
	for _, c := range []struct {
		name, query string
		want        []string
	}{
		{"one term", "budget", []string{"tg:a", "tg:c", "tg:d"}},
		{"two terms are both required", "budget meeting", []string{"tg:a", "tg:d"}},
		{"a phrase is adjacent and in order", `"budget meeting"`, []string{"tg:a"}},
		{"the same words out of order are not that phrase", `"meeting budget"`, nil},
		{"a leading dash excludes", "budget -review", []string{"tg:a", "tg:d"}},
		{"a phrase can be excluded", `budget -"budget meeting"`, []string{"tg:c", "tg:d"}},
		{"a dash inside a word is a boundary", "next-quarter", []string{"tg:b"}},
		{"the query folds too", "BUDGET Meeting", []string{"tg:a", "tg:d"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			hits, err := ix.Search(c.query, "", "", "", 10)
			if err != nil {
				t.Fatalf("search %q: %v", c.query, err)
			}
			got := hitIDs(hits)
			if len(got) != len(c.want) {
				t.Fatalf("%q hit %v, want %v", c.query, got, c.want)
			}
			for _, want := range c.want {
				if !strings.Contains(strings.Join(got, " "), want) {
					t.Errorf("%q hit %v, want %v among them", c.query, got, want)
				}
			}
		})
	}
	for _, c := range []struct{ name, query string }{
		{"an unclosed phrase", `"unbalanced`},
		{"a query with nothing to match", "   "},
		{"punctuation only", "!!!"},
		{"only an exclusion", "-budget"},
		{"a dash with nothing after it", "budget -"},
		{"an empty phrase", `"" budget`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if hits, err := ix.Search(c.query, "", "", "", 10); err == nil {
				t.Errorf("search %q answered %d hits, want a refusal the caller can read",
					c.query, len(hits))
			}
		})
	}
}

// Rank is BM25 and lower is better, so the order a caller renders is the order
// the index returns. The two things BM25 buys over a count are here: a rarer
// word weighs more than a common one, and the same word in a shorter message
// weighs more than in a longer one.
func TestRankingOrder(t *testing.T) {
	// "budget" is in all three; "audit" in one. A document holding both must
	// outrank the ones holding only the common word.
	ix := built(t,
		row("a", "tg:common1", "u", "", "budget notes", 0),
		row("b", "tg:common2", "u", "", "budget notes again", 1),
		row("c", "tg:rare", "u", "", "budget audit notes", 2),
	)
	hits, err := ix.Search("budget audit", "", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ChatJID != "tg:rare" {
		t.Fatalf("both terms required: hits = %v", hitIDs(hits))
	}
	if hits[0].Rank >= 0 {
		t.Errorf("rank = %v; BM25 is negated so that ascending rank is best-first", hits[0].Rank)
	}

	// Length normalisation: one occurrence in five words beats one in fifty.
	long := "budget " + strings.Repeat("filler ", 50)
	ix = built(t,
		row("s", "tg:short", "u", "", "the budget is agreed", 0),
		row("l", "tg:long", "u", "", long, 1),
	)
	hits, err = ix.Search("budget", "", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := hitIDs(hits); len(got) != 2 || got[0] != "tg:short" {
		t.Errorf("length normalisation: order = %v, want the short message first", got)
	}
	if hits[0].Rank > hits[1].Rank {
		t.Errorf("ranks %v are not ascending", []float64{hits[0].Rank, hits[1].Rank})
	}

	// Two messages carrying the same words score identically, and the newer one
	// is what a caller asking "when did we say that" wants at the top.
	ix = built(t,
		row("old", "tg:x", "u", "", "budget agreed", 0),
		row("new", "tg:x", "u", "", "budget agreed", 5),
	)
	hits, err = ix.Search("budget", "", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || !hits[0].Timestamp.After(hits[1].Timestamp) {
		t.Errorf("equal ranks did not break to the newest first: %+v", hits)
	}
}

// The snippet is what every caller renders, so the marks and the clipping are
// the contract. A hit far into a long message must still be IN the window: a
// snippet with no mark in it reads as a false positive.
func TestTheSnippet(t *testing.T) {
	ix := built(t, row("m", "tg:1", "u", "", "Q3 budget meeting tomorrow at noon", 0))
	hits, err := ix.Search("budget", "", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := hits[0].Content; got != "Q3 «budget» meeting tomorrow at noon" {
		t.Errorf("snippet = %q, want the whole short message with the hit marked", got)
	}

	// Every occurrence inside the window is marked, and the original casing and
	// accents are what comes back — the term is folded, the text is not.
	ix = built(t, row("m", "tg:1", "u", "", "Úroveň up, uroven down", 0))
	hits, err = ix.Search("uroven", "", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := hits[0].Content; got != "«Úroveň» up, «uroven» down" {
		t.Errorf("snippet = %q, want both occurrences marked in the writer's own spelling", got)
	}

	// A hit 60 words in: the window is cut on both sides, says so with an
	// ellipsis, and holds the mark.
	long := strings.Repeat("filler ", 60) + "needle " + strings.Repeat("tail ", 60)
	ix = built(t, row("m", "tg:1", "u", "", long, 0))
	hits, err = ix.Search("needle", "", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := hits[0].Content
	if !strings.Contains(got, "«needle»") {
		t.Errorf("snippet %q does not hold the hit it was cut around", got)
	}
	if !strings.HasPrefix(got, ellipsis) || !strings.HasSuffix(got, ellipsis) {
		t.Errorf("snippet %q does not report that it is cut on both sides", got)
	}
	if words := len(tokenize(got)); words > snippetTokens {
		t.Errorf("snippet holds %d tokens, want at most %d", words, snippetTokens)
	}
}

// scope is polymorphic — a value holding ':' is a chat, anything else a folder
// subtree over routed_to — and sender and since narrow on top of it.
func TestTheNarrowings(t *testing.T) {
	ix := built(t,
		row("a1", "tg:1", "alice", "atlas/eng", "budget alpha", 0),
		row("a2", "tg:1", "bob", "atlas/eng", "budget beta", 60),
		row("a3", "tg:2", "alice", "atlas/support", "budget gamma", 120),
		row("a4", "tg:3", "alice", "other", "budget delta", 120),
	)
	for _, c := range []struct {
		name, scope, sender, since string
		want                       int
	}{
		{"no narrowing", "", "", "", 4},
		{"one chat", "tg:1", "", "", 2},
		{"a folder subtree", "atlas", "", "", 3},
		{"one folder exactly", "atlas/eng", "", "", 2},
		{"a folder nothing is routed to", "nobody", "", "", 0},
		{"one sender", "", "alice", "", 3},
		{"an instant lower bound", "", "", at.Add(30 * time.Minute).Format(time.RFC3339), 3},
		{"the bound is inclusive", "", "", at.Format(time.RFC3339), 4},
		{"all three at once", "atlas", "alice", at.Format(time.RFC3339), 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			hits, err := ix.Search("budget", c.scope, c.sender, c.since, 10)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if len(hits) != c.want {
				t.Errorf("hits = %d (%v), want %d", len(hits), hitIDs(hits), c.want)
			}
		})
	}
	// A since that is not an instant is refused: silently ignoring it would
	// answer with hits from outside the window the caller named.
	if _, err := ix.Search("budget", "", "", "last tuesday", 10); err == nil {
		t.Error("a since that is not RFC3339 was accepted")
	}
}

// A folder name may legally hold `%`, `_` or a capital. All three widen a SQL
// LIKE, and a tenant named `%` then reads every other tenant's message content
// (BUGS L122). Containment here is one exact string comparison, and this is the
// search path's proof of it.
func TestAFolderScopeIsNotWidenedByMetacharacters(t *testing.T) {
	ix := built(t,
		row("1", "tg:a_b/one", "u", "a_b/one", "needle", 0),
		row("2", "tg:aXb/two", "u", "aXb/two", "needle", 1),
		row("3", "tg:coach", "u", "coach/k", "needle", 2),
		row("4", "tg:Coach", "u", "Coach/private", "needle", 3),
		row("5", "tg:xy", "u", "x/y", "needle", 4),
	)
	for _, c := range []struct{ scope, leak string }{
		{"a_b", "tg:aXb/two"},
		{"%", "tg:xy"},
		{"coach", "tg:Coach"},
	} {
		hits, err := ix.Search("needle", c.scope, "", "", 50)
		if err != nil {
			t.Fatalf("scope %q: %v", c.scope, err)
		}
		for _, h := range hits {
			if h.ChatJID == c.leak {
				t.Errorf("scope %q read %q's messages", c.scope, c.leak)
			}
		}
	}
	hits, err := ix.Search("needle", "a_b", "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ChatJID != "tg:a_b/one" {
		t.Errorf("the folder's own subtree was lost: %v", hitIDs(hits))
	}
}

// A document is addressed by the message's own coordinate, so re-indexing one
// message REPLACES it. Both live rewrites depend on that: media enrichment
// rewrites a body, and an observe rule stamps the folder a subtree scope reads.
func TestAReIndexedMessageIsReplacedNotDoubled(t *testing.T) {
	first := row("m", "tg:1", "u", "", "raw <attachment ref>", 0)
	ix := built(t, first)

	enriched := first
	enriched.Content = "raw photo of the whiteboard"
	ix.Put(enriched)
	if n := ix.size(); n != 1 {
		t.Errorf("the index holds %d documents for one message", n)
	}
	if hits, err := ix.Search("attachment", "", "", "", 10); err != nil || len(hits) != 0 {
		t.Errorf("the rewritten body still answers its old words: %d hits, err=%v",
			len(hits), err)
	}
	if hits, err := ix.Search("whiteboard", "", "", "", 10); err != nil || len(hits) != 1 {
		t.Fatalf("the rewritten body does not answer: %d hits, err=%v", len(hits), err)
	}

	stamped := enriched
	stamped.RoutedTo = "atlas/eng"
	ix.Put(stamped)
	if hits, err := ix.Search("whiteboard", "atlas", "", "", 10); err != nil || len(hits) != 1 {
		t.Errorf("the stamped folder is not what the subtree scope reads: %d hits, err=%v",
			len(hits), err)
	}
}

// A message the store has deleted must stop answering searches, content and
// all: the delete path hands back the positions it dropped for exactly this.
func TestARemovedMessageStopsAnswering(t *testing.T) {
	r := row("d1", "tg:err", "u", "", "to be deleted soon", 0)
	ix := built(t, r)
	if hits, _ := ix.Search("deleted", "", "", "", 10); len(hits) != 1 {
		t.Fatalf("pre-delete hits = %d, want 1", len(hits))
	}
	ix.Remove(r.ChatJID, r.Sort)
	if hits, err := ix.Search("deleted", "", "", "", 10); err != nil || len(hits) != 0 {
		t.Errorf("post-delete hits = %d, err=%v; want none", len(hits), err)
	}
	if n := ix.size(); n != 0 {
		t.Errorf("the index still holds %d documents", n)
	}
	// A position the index does not hold is not an error and not a panic: the
	// store may have deleted a row this index never read.
	ix.Remove("tg:err", "nothing-here")
}

// limit is the caller's bound, and the default and the ceiling are the two the
// MCP tool advertises.
func TestTheLimit(t *testing.T) {
	rows := make([]store.IndexedMessage, 0, 300)
	for i := range 300 {
		rows = append(rows, row(fmt.Sprintf("m%03d", i), "tg:bulk", "u", "",
			fmt.Sprintf("keyword entry %d", i), i))
	}
	ix := built(t, rows...)
	for _, c := range []struct {
		name  string
		limit int
		want  int
	}{
		{"the caller's own bound", 5, 5},
		{"zero is the default", 0, DefaultLimit},
		{"a negative is the default", -1, DefaultLimit},
		{"above the ceiling is the default", MaxLimit + 1, DefaultLimit},
		{"the ceiling itself", MaxLimit, MaxLimit},
	} {
		t.Run(c.name, func(t *testing.T) {
			hits, err := ix.Search("keyword", "", "", "", c.limit)
			if err != nil {
				t.Fatal(err)
			}
			if len(hits) != c.want {
				t.Errorf("limit %d answered %d hits, want %d", c.limit, len(hits), c.want)
			}
		})
	}
	// Every hit carries its snippet, whatever the volume: the caller renders
	// that string directly.
	hits, err := ix.Search("keyword", "", "", "", MaxLimit)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if !strings.Contains(h.Content, markOpen+"keyword"+markClose) {
			t.Fatalf("a hit carries no mark: %q", h.Content)
		}
	}
}
