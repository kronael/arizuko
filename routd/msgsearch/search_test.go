package msgsearch

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

var at = time.Date(2026, 9, 13, 8, 0, 0, 0, time.UTC)

func row(id, chat, sender, content string, minute int) store.ScannedMessage {
	ts := at.Add(time.Duration(minute) * time.Minute)
	return store.ScannedMessage{
		ChatJID:   chat,
		Sort:      store.MsgStamp(core.TS(ts), id),
		Timestamp: ts,
		Sender:    sender,
		Content:   content,
	}
}

// scope is a Pages over rows a test wrote, newest first, as the store's scans
// answer them: size rows a page, and a page never ends inside an instant.
type scope struct {
	rows  []store.ScannedMessage
	size  int
	pages int
	read  int
}

func newScope(size int, before string, rows ...store.ScannedMessage) *scope {
	s := &scope{size: size}
	bound, _ := time.Parse(time.RFC3339Nano, before)
	for i := len(rows) - 1; i >= 0; i-- {
		if before == "" || rows[i].Timestamp.Before(bound) {
			s.rows = append(s.rows, rows[i])
		}
	}
	return s
}

func (s *scope) Next(context.Context) ([]store.ScannedMessage, error) {
	if len(s.rows) == 0 {
		return nil, nil
	}
	end := min(s.size, len(s.rows))
	for end < len(s.rows) && s.rows[end].Timestamp.Equal(s.rows[end-1].Timestamp) {
		end++
	}
	page := s.rows[:end]
	s.rows = s.rows[end:]
	s.pages++
	s.read += len(page)
	return page, nil
}

func (s *scope) Read() int { return s.read }

func search(t *testing.T, pages Pages, query, sender string, since time.Time,
	limit int) apiv1.FindResponse {
	t.Helper()
	found, err := Search(t.Context(), pages, query, sender, since, limit)
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	if found.Count != len(found.Messages) {
		t.Fatalf("count = %d over %d messages", found.Count, len(found.Messages))
	}
	return found
}

func chats(found apiv1.FindResponse) string {
	out := make([]string, len(found.Messages))
	for i, m := range found.Messages {
		out[i] = m.ChatJID
	}
	return strings.Join(out, " ")
}

// Letters and digits make a term, everything else is a boundary, and
// diacritics are dropped so "uroven" answers "úroveň" from a plain keyboard.
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
		{"czech, decomposed", "nová úroveň", "uroven", true},
		{"accented query, plain content", "nova uroven", "úroveň", true},
		{"a prefix is not a match", "budgeting workshop", "budget", false},
		{"a different word is not a match", "budget meeting", "budgets", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			found := search(t, newScope(10, "", row("m", "tg:1", "u", c.content, 0)),
				c.query, "", time.Time{}, 10)
			if got := found.Count == 1; got != c.want {
				t.Errorf("%q over %q: %d hits, want match=%v", c.query, c.content,
					found.Count, c.want)
			}
		})
	}
}

// The grammar is terms, "phrases" and `-`. A malformed query is refused: an
// empty answer would be read as "no message says that".
func TestTheQueryGrammar(t *testing.T) {
	rows := []store.ScannedMessage{
		row("a", "tg:a", "u", "budget meeting tomorrow", 0),
		row("b", "tg:b", "u", "plan for next quarter", 1),
		row("c", "tg:c", "u", "budget review with finance", 2),
		row("d", "tg:d", "u", "meeting about the budget", 3),
	}
	for _, c := range []struct{ name, query, want string }{
		{"one term", "budget", "tg:d tg:c tg:a"},
		{"two terms are both required", "budget meeting", "tg:d tg:a"},
		{"a phrase is adjacent and in order", `"budget meeting"`, "tg:a"},
		{"the same words out of order are not that phrase", `"meeting budget"`, ""},
		{"a leading dash excludes", "budget -review", "tg:d tg:a"},
		{"a phrase can be excluded", `budget -"budget meeting"`, "tg:d tg:c"},
		{"a dash inside a word is a boundary", "next-quarter", "tg:b"},
		{"the query folds too", "BUDGET Meeting", "tg:d tg:a"},
	} {
		t.Run(c.name, func(t *testing.T) {
			found := search(t, newScope(10, "", rows...), c.query, "", time.Time{}, 10)
			if got := chats(found); got != c.want {
				t.Errorf("%q hit %q, want %q, newest first", c.query, got, c.want)
			}
			if !found.Complete {
				t.Errorf("%q read the whole scope and answered complete=false", c.query)
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
			found, err := Search(t.Context(), newScope(10, "", rows...), c.query, "",
				time.Time{}, 10)
			if err == nil {
				t.Errorf("search %q answered %d hits, want a refusal the caller can read",
					c.query, found.Count)
			}
		})
	}
}

// The snippet is what every caller renders. A hit far into a long message
// must still be in the window: a snippet with no mark reads as a false positive.
func TestTheSnippet(t *testing.T) {
	one := func(content, query string) string {
		found := search(t, newScope(10, "", row("m", "tg:1", "u", content, 0)),
			query, "", time.Time{}, 10)
		if found.Count != 1 {
			t.Fatalf("%q over %q: %d hits", query, content, found.Count)
		}
		return found.Messages[0].Content
	}
	if got := one("Q3 budget meeting tomorrow at noon", "budget"); got != "Q3 «budget» meeting tomorrow at noon" {
		t.Errorf("snippet = %q, want the whole short message with the hit marked", got)
	}
	if got := one("Úroveň up, uroven down", "uroven"); got != "«Úroveň» up, «uroven» down" {
		t.Errorf("snippet = %q, want both occurrences marked in the writer's own spelling", got)
	}
	long := strings.Repeat("filler ", 60) + "needle " + strings.Repeat("tail ", 60)
	got := one(long, "needle")
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

// A hit carries the message's own chat, sender, instant and flags.
func TestAHitCarriesItsMessage(t *testing.T) {
	m := row("m", "tg:1", "alice", "the budget", 7)
	m.FromMe, m.BotMsg = true, true
	found := search(t, newScope(10, "", m), "budget", "", time.Time{}, 10)
	want := apiv1.FoundMessage{
		ChatJID: "tg:1", Sender: "alice", Timestamp: "2026-09-13T08:07:00Z",
		IsFromMe: true, IsBotMessage: true, Content: "the «budget»",
	}
	if found.Count != 1 || found.Messages[0] != want {
		t.Errorf("hit = %+v, want %+v", found.Messages, want)
	}
}

// sender is exact. since is an inclusive lower bound that ends the search: a
// scan that reached it read everything the caller asked about.
func TestSenderAndSinceNarrow(t *testing.T) {
	rows := []store.ScannedMessage{
		row("a", "tg:a", "alice", "budget one", 0),
		row("b", "tg:b", "bob", "budget two", 1),
		row("c", "tg:c", "alice", "budget three", 2),
		row("d", "tg:d", "alice", "budget four", 3),
	}
	found := search(t, newScope(10, "", rows...), "budget", "alice", time.Time{}, 10)
	if got := chats(found); got != "tg:d tg:c tg:a" {
		t.Errorf("sender alice hit %q", got)
	}
	found = search(t, newScope(10, "", rows...), "budget", "", at.Add(2*time.Minute), 10)
	if got := chats(found); got != "tg:d tg:c" {
		t.Errorf("since minute 2 hit %q, want the bound itself and what is newer", got)
	}
	if !found.Complete || found.SearchedTo != "2026-09-13T08:02:00Z" {
		t.Errorf("a search that reached since answered complete=%v searched_to=%q",
			found.Complete, found.SearchedTo)
	}
}

// A search that stops at limit says so, and the same search continued from
// searched_to returns each remaining hit one time.
func TestTheLimitStopsAndTheContinuationReadsEachMessageOnce(t *testing.T) {
	var rows []store.ScannedMessage
	for i := range 10 {
		rows = append(rows, row(fmt.Sprint(i), fmt.Sprintf("tg:%d", i), "u", "needle", i))
	}
	var got []string
	before := ""
	for call := 0; ; call++ {
		if call > 10 {
			t.Fatal("the continuation never completes")
		}
		found := search(t, newScope(4, before, rows...), "needle", "", time.Time{}, 3)
		for _, m := range found.Messages {
			got = append(got, m.ChatJID)
		}
		if found.Complete {
			break
		}
		if found.Count != 3 {
			t.Fatalf("call %d answered %d hits and complete=false, want 3", call, found.Count)
		}
		before = found.SearchedTo
	}
	want := "tg:9 tg:8 tg:7 tg:6 tg:5 tg:4 tg:3 tg:2 tg:1 tg:0"
	if strings.Join(got, " ") != want {
		t.Errorf("the calls returned %v, want %s", got, want)
	}
}

// `before` is an instant, so a search that stopped between two messages of one
// instant would lose the second. It stops only between two instants.
func TestMessagesOfOneInstantStayTogether(t *testing.T) {
	rows := []store.ScannedMessage{
		row("a", "tg:a", "u", "needle", 0),
		row("b1", "tg:b1", "u", "needle", 1),
		row("b2", "tg:b2", "u", "needle", 1),
		row("b3", "tg:b3", "u", "needle", 1),
		row("c", "tg:c", "u", "needle", 2),
	}
	first := search(t, newScope(10, "", rows...), "needle", "", time.Time{}, 2)
	if got := chats(first); got != "tg:c tg:b3 tg:b2 tg:b1" {
		t.Fatalf("limit 2 answered %q, want the newest and every message of the next instant", got)
	}
	if first.Complete || first.SearchedTo != "2026-09-13T08:01:00Z" {
		t.Fatalf("complete=%v searched_to=%q", first.Complete, first.SearchedTo)
	}
	rest := search(t, newScope(10, first.SearchedTo, rows...), "needle", "", time.Time{}, 2)
	if got := chats(rest); got != "tg:a" || !rest.Complete {
		t.Errorf("the continuation answered %q complete=%v, want tg:a complete", got, rest.Complete)
	}
}

// One call reads Budget rows and no more. It answers complete=false with the
// instant it reached, so "no hits" is never a claim about rows it did not read.
func TestTheBudgetEndsASearchThatIsNotComplete(t *testing.T) {
	rows := make([]store.ScannedMessage, 0, Budget+2500)
	for i := range Budget + 2500 {
		rows = append(rows, row(fmt.Sprint(i), "tg:big", "u", "filler", i))
	}
	rows[100].Content = "needle"
	pages := newScope(1000, "", rows...)
	found := search(t, pages, "needle", "", time.Time{}, 10)
	if found.Count != 0 || found.Complete {
		t.Fatalf("count=%d complete=%v; the needle lies below the budget, so the answer "+
			"must be no hits and complete=false", found.Count, found.Complete)
	}
	if pages.read != Budget {
		t.Errorf("the search read %d rows, want exactly the budget of %d", pages.read, Budget)
	}
	oldest := rows[len(rows)-Budget].Timestamp.Format(time.RFC3339Nano)
	if found.SearchedTo != oldest {
		t.Errorf("searched_to = %q, want %q, the oldest row read", found.SearchedTo, oldest)
	}
	for !found.Complete {
		found = search(t, newScope(1000, found.SearchedTo, rows...), "needle", "", time.Time{}, 10)
	}
	if found.Count != 1 {
		t.Errorf("the continuation found %d needles, want 1", found.Count)
	}
}

// The default and the ceiling are the two the MCP tool advertises.
func TestTheLimit(t *testing.T) {
	var rows []store.ScannedMessage
	for i := range MaxLimit + 50 {
		rows = append(rows, row(fmt.Sprint(i), "tg:1", "u", "needle", i))
	}
	for _, c := range []struct{ limit, want int }{
		{0, DefaultLimit}, {-1, DefaultLimit}, {5, 5}, {MaxLimit, MaxLimit},
		{MaxLimit + 1, DefaultLimit},
	} {
		found := search(t, newScope(1000, "", rows...), "needle", "", time.Time{}, c.limit)
		if found.Count != c.want {
			t.Errorf("limit %d answered %d hits, want %d", c.limit, found.Count, c.want)
		}
	}
}

// An empty scope is complete, with no instant to report.
func TestAnEmptyScopeIsComplete(t *testing.T) {
	found := search(t, newScope(10, ""), "needle", "", time.Time{}, 10)
	if found.Count != 0 || !found.Complete || found.SearchedTo != "" || found.Messages == nil {
		t.Errorf("empty scope answered %+v, want an empty list, complete, no instant", found)
	}
}
