// Package msgsearch is ranked message search: an in-memory BM25 index over the
// content of the messages routd holds, built from the store at boot and fed by
// the writes routd makes (spec 6/42-dynamo-over-postgres §8).
//
// THE INDEX IS DERIVED AND THE STORE IS THE RECORD. Nothing here is ever the
// only copy of anything: every document comes from an item in the `messages`
// entity, addressed by that item's own coordinate, so the answer to "is this
// index wrong" is always "rebuild it from the store". That is why it can be
// memory: there is no file to migrate, no second schema, no second engine, and
// no write path that can leave the record and the index disagreeing about what
// was said, because the record is written first and the index second.
//
// UNTIL THE WALK FINISHES A SEARCH IS REFUSED, NOT ANSWERED EMPTY. An empty hit
// list is a statement — "no message says that" — and an agent repeats it as
// fact. A half-built index would make that statement about the half it has not
// read yet, so Search answers ErrWarming until Build has been through every
// chat.
//
// WHAT IT DOES NOT SEE. A message written or deleted by another process — the
// CLI's `arizuko send`, an archive import, a group delete — is invisible until
// the next boot walk. A stale hit does not leak: every caller re-resolves the
// hit's chat to a folder before returning it (ipc.find_messages), and a purged
// chat routes nowhere.
package msgsearch

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/store"
)

// FoundMessage is one hit. Content is the snippet — the matched fragment of the
// message with «» around each hit — and not the whole body. Rank is BM25, and
// lower is the better match.
type FoundMessage struct {
	ChatJID      string    `json:"chat_jid"`
	Sender       string    `json:"sender"`
	Timestamp    time.Time `json:"timestamp"`
	IsFromMe     bool      `json:"is_from_me"`
	IsBotMessage bool      `json:"is_bot_message"`
	Content      string    `json:"content"`
	Rank         float64   `json:"rank"`
}

// ErrWarming is the refusal a search gets while the boot walk is still reading
// the store. It is a refusal the caller can show, and it is not an empty result.
var ErrWarming = errors.New("the ranked message index is still being built from the store, " +
	"so no search can be answered yet; retry in a moment")

// How many hits a search returns when the caller does not choose, and the most
// it will return however large a number the caller passes.
const (
	DefaultLimit = 20
	MaxLimit     = 200
)

// The BM25 constants are SQLite FTS5's, and the rank is negated the way its
// bm25() negates it, so ascending rank is best-first on either engine and a
// query's top ten is the same ten (parity_test.go measures that).
const (
	bm25K1 = 1.2
	bm25B  = 0.75
	// An idf of zero ranks every document identically, which loses the length
	// normalisation with it; FTS5 floors it, and a term half the corpus holds
	// is exactly where that happens.
	minIDF = 1e-6
)

// coord is a message's own coordinate — the chat it is in and its position in
// that chat — which is the address the store holds it at and the only identity
// a document has here.
type coord struct {
	chat string
	sort string
}

// doc is one indexed message. The content is kept whole because the snippet is
// cut from it at query time: tokenising a hit again costs a message, and
// storing every token's offsets costs the corpus.
type doc struct {
	coord
	ts       time.Time
	sender   string
	routedTo string
	content  string
	fromMe   bool
	bot      bool
	length   int32
	// terms is this document's distinct terms, which is what makes a delete a
	// delete: without them a removed message's postings could only be found by
	// reading every posting list in the index.
	terms []string
}

// posting is one term's occurrence count in one document.
type posting struct {
	doc int32
	tf  int32
}

// Index is the whole index: the documents, the term postings over them, and the
// two totals BM25 needs.
//
// One mutex covers all of it. The writers are routd's own message writes, one
// at a time behind the store's own write, and the boot walk; the readers are
// `find_messages` and `GET /v1/messages/find`. A lock per term would buy
// nothing at 24,296 documents and would make the two totals unreadable.
type Index struct {
	mu    sync.RWMutex
	ready bool
	// failed is a boot walk that did not finish. It is returned from every
	// search, because a search that answered from a partial index would answer
	// "no such message" about the chats the walk never reached.
	failed error

	docs []*doc
	at   map[coord]int32
	post map[string][]posting
	// live and lengths are the corpus BM25 scores against: the document count
	// and the sum of their lengths, kept as the documents move so no query
	// walks the corpus to find them.
	live    int
	lengths int64
}

// New builds an empty index. It refuses every search until Build finishes.
func New() *Index {
	return &Index{at: map[coord]int32{}, post: map[string][]posting{}}
}

// Source is the store an index is built from: the partition list, and one
// chat's messages projected to what a document holds. *store.Store is one.
type Source interface {
	ChatJIDs(ctx context.Context) ([]string, error)
	MessagesToIndex(ctx context.Context, chatJID string) ([]store.IndexedMessage, error)
}

// Build walks every chat and indexes what each holds, then opens the index to
// searches. A failure is recorded and returned to every later search: an index
// that stopped half way must not answer, and the daemon must not have to
// remember that it did.
//
// The walk is per chat because the chat is the partition: 136 reads of one
// partition each on krons, against a whole-table scan no store answers.
func (ix *Index) Build(ctx context.Context, src Source) error {
	err := ix.build(ctx, src)
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err != nil {
		ix.failed = err
		return err
	}
	ix.ready = true
	return nil
}

func (ix *Index) build(ctx context.Context, src Source) error {
	jids, err := src.ChatJIDs(ctx)
	if err != nil {
		return fmt.Errorf("read the chat list the search index is built from: %w", err)
	}
	for _, jid := range jids {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, err := src.MessagesToIndex(ctx, jid)
		if err != nil {
			return err
		}
		ix.Put(rows...)
	}
	return nil
}

// Put indexes every row, replacing whatever the index holds at each row's
// coordinate. A row the index already holds is the same message being
// re-indexed — media enrichment rewrites a body, a route miss stamps a folder —
// so the coordinate decides, never the arrival order.
//
// Tokenising happens before the lock, because it is the expensive half and the
// readers are agents waiting on a search.
func (ix *Index) Put(rows ...store.IndexedMessage) {
	// A process that answers no search holds no index, and its writes still go
	// through this path — the CLI writes messages too.
	if ix == nil {
		return
	}
	ready := make([]*doc, 0, len(rows))
	freqs := make([]map[string]int32, 0, len(rows))
	for _, row := range rows {
		d, tf := prepare(row)
		ready = append(ready, d)
		freqs = append(freqs, tf)
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for i, d := range ready {
		ix.install(d, freqs[i])
	}
}

// Remove drops one message's position, for a caller that has just deleted it
// from the store.
func (ix *Index) Remove(chatJID, sortKey string) {
	if ix == nil {
		return
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if id, ok := ix.at[coord{chat: chatJID, sort: sortKey}]; ok {
		ix.drop(id)
	}
}

// size reports how many messages the index holds. It is unexported because no
// caller outside this package asks: the measurement of the walk does
// (parity_test.go), and a number nobody reads is surface.
func (ix *Index) size() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.live
}

// prepare renders one row as the document and term counts the index installs.
func prepare(row store.IndexedMessage) (*doc, map[string]int32) {
	toks := tokenize(row.Content)
	tf := make(map[string]int32, len(toks))
	for _, t := range toks {
		tf[t.term]++
	}
	d := &doc{
		coord:    coord{chat: row.ChatJID, sort: row.Sort},
		ts:       row.Timestamp,
		sender:   row.Sender,
		routedTo: row.RoutedTo,
		content:  row.Content,
		fromMe:   row.FromMe,
		bot:      row.BotMsg,
		length:   int32(len(toks)),
		terms:    make([]string, 0, len(tf)),
	}
	for term := range tf {
		d.terms = append(d.terms, term)
	}
	return d, tf
}

// install puts one document in, under the lock. A replaced coordinate leaves its
// slot in docs holding nil — eight bytes per rewritten message, which is what a
// stable document id costs, and the document itself is freed.
func (ix *Index) install(d *doc, tf map[string]int32) {
	if id, ok := ix.at[d.coord]; ok {
		ix.drop(id)
	}
	id := int32(len(ix.docs))
	ix.docs = append(ix.docs, d)
	ix.at[d.coord] = id
	for term, n := range tf {
		ix.post[term] = append(ix.post[term], posting{doc: id, tf: n})
	}
	ix.live++
	ix.lengths += int64(d.length)
}

// drop takes one document out of the postings and the totals. Caller holds the
// lock.
func (ix *Index) drop(id int32) {
	d := ix.docs[id]
	if d == nil {
		return
	}
	for _, term := range d.terms {
		ps := ix.post[term]
		for i := range ps {
			if ps[i].doc == id {
				ps = append(ps[:i], ps[i+1:]...)
				break
			}
		}
		if len(ps) == 0 {
			delete(ix.post, term)
		} else {
			ix.post[term] = ps
		}
	}
	delete(ix.at, d.coord)
	ix.docs[id] = nil
	ix.live--
	ix.lengths -= int64(d.length)
}

// Search answers the ranked hits for one query, best first.
//
// `scope` is polymorphic: a value holding ':' is a chat, anything else a folder
// subtree over the message's `routed_to`. `sender` is exact and `since` is an
// RFC3339 lower bound. ACL filtering is the caller's: a hit carries the chat it
// is in, and only the caller knows which folder asked.
//
// THE SUBTREE IS AN EXACT STRING COMPARISON. A folder name may legally hold `%`,
// `_` or a capital, and all three widen a SQL LIKE — one tenant named `%` reads
// every other tenant's messages (BUGS L122). groupfolder.Contains is the one
// containment predicate and it has no metacharacters.
func (ix *Index) Search(query, scope, sender, since string, limit int) ([]FoundMessage, error) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if ix.failed != nil {
		return nil, ix.failed
	}
	if !ix.ready {
		return nil, ErrWarming
	}
	items, err := parseQuery(query)
	if err != nil {
		return nil, err
	}
	f, err := parseFilter(scope, sender, since)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxLimit {
		limit = DefaultLimit
	}
	if ix.live == 0 {
		return nil, nil
	}

	// Every clause's own matches, positives first: the positives are AND-ed and
	// carry the term frequencies the score is made of, the negatives only
	// remove.
	var matched []map[int32]int32
	for _, it := range items {
		if !it.not {
			matched = append(matched, ix.match(it))
		}
	}
	// The smallest clause first, so the intersection shrinks as fast as it can.
	order := make([]int, len(matched))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		return len(matched[order[a]]) < len(matched[order[b]])
	})
	selected := map[int32]bool{}
	for id := range matched[order[0]] {
		selected[id] = true
	}
	for _, i := range order[1:] {
		for id := range selected {
			if _, ok := matched[i][id]; !ok {
				delete(selected, id)
			}
		}
	}
	for _, it := range items {
		if !it.not {
			continue
		}
		for id := range ix.match(it) {
			delete(selected, id)
		}
	}

	idf := make([]float64, len(matched))
	n := float64(ix.live)
	for i, m := range matched {
		idf[i] = math.Log((n - float64(len(m)) + 0.5) / (float64(len(m)) + 0.5))
		if idf[i] <= 0 {
			idf[i] = minIDF
		}
	}
	avg := float64(ix.lengths) / n

	type scored struct {
		d    *doc
		rank float64
	}
	hits := make([]scored, 0, len(selected))
	for id := range selected {
		d := ix.docs[id]
		if !f.keep(d) {
			continue
		}
		score := 0.0
		for i, m := range matched {
			tf := float64(m[id])
			score += idf[i] * (tf * (bm25K1 + 1)) /
				(tf + bm25K1*(1-bm25B+bm25B*float64(d.length)/avg))
		}
		hits = append(hits, scored{d: d, rank: -score})
	}
	// Best rank first, and the newest message first inside one rank — two
	// messages carrying the same words score identically, and the recent one is
	// what a caller asking "when did we say that" wants at the top.
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].rank != hits[b].rank {
			return hits[a].rank < hits[b].rank
		}
		return hits[a].d.sort > hits[b].d.sort
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	want := wanted(items)
	out := make([]FoundMessage, len(hits))
	for i, h := range hits {
		out[i] = FoundMessage{
			ChatJID:      h.d.chat,
			Sender:       h.d.sender,
			Timestamp:    h.d.ts,
			IsFromMe:     h.d.fromMe,
			IsBotMessage: h.d.bot,
			Content:      snippet(h.d.content, tokenize(h.d.content), want),
			Rank:         h.rank,
		}
	}
	return out, nil
}

// match answers the documents one clause selects, with the occurrences each
// carries. Caller holds the lock.
func (ix *Index) match(it item) map[int32]int32 {
	if len(it.terms) == 1 {
		ps := ix.post[it.terms[0]]
		out := make(map[int32]int32, len(ps))
		for _, p := range ps {
			out[p.doc] = p.tf
		}
		return out
	}
	// A phrase: only a document holding every one of its terms can hold them
	// adjacent, so the rarest term is the candidate list and the rest is read
	// out of each candidate's own text.
	rarest := it.terms[0]
	for _, t := range it.terms[1:] {
		if len(ix.post[t]) < len(ix.post[rarest]) {
			rarest = t
		}
	}
	out := map[int32]int32{}
	for _, p := range ix.post[rarest] {
		if n := ix.phraseHits(p.doc, it.terms); n > 0 {
			out[p.doc] = n
		}
	}
	return out
}

// phraseHits counts how many times one document holds the terms adjacent and in
// order. Caller holds the lock.
func (ix *Index) phraseHits(id int32, phrase []string) int32 {
	d := ix.docs[id]
	if d == nil {
		return 0
	}
	toks := tokenize(d.content)
	var n int32
	for i := 0; i+len(phrase) <= len(toks); i++ {
		hit := true
		for j, t := range phrase {
			if toks[i+j].term != t {
				hit = false
				break
			}
		}
		if hit {
			n++
		}
	}
	return n
}

// filter is the three narrowings a caller may ask for on top of the query.
type filter struct {
	chat   string
	folder string
	sender string
	since  time.Time
}

// parseFilter reads the narrowings, and refuses a `since` that is not an
// instant. A silently ignored bound would answer a question nobody asked with
// hits from outside the window the caller named.
func parseFilter(scope, sender, since string) (filter, error) {
	f := filter{sender: sender}
	switch {
	case scope == "":
	case strings.Contains(scope, ":"):
		f.chat = scope
	default:
		f.folder = scope
	}
	if since != "" {
		t, err := time.Parse(time.RFC3339Nano, since)
		if err != nil {
			return f, fmt.Errorf("%q is not an RFC3339 instant, so there is no lower bound to "+
				"search from: %w", since, err)
		}
		f.since = t
	}
	return f, nil
}

// keep reports whether one document survives the narrowings.
func (f filter) keep(d *doc) bool {
	switch {
	case f.chat != "" && d.chat != f.chat:
		return false
	case f.folder != "" && !groupfolder.Contains(f.folder, d.routedTo):
		return false
	case f.sender != "" && d.sender != f.sender:
		return false
	case !f.since.IsZero() && d.ts.Before(f.since):
		return false
	}
	return true
}
