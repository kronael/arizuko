package routd

// search.go is routd's ranked message search: the in-memory index this database
// answers `find_messages` and `GET /v1/messages/find` from, the boot walk that
// fills it, and the four writes that keep it in step (spec 6/42 §8).
//
// ROUTD IS THE SOLE APPENDER OF MESSAGES, WHICH IS WHY THE FEED IS HERE. Every
// message a live instance records goes through PutMessage on this type; the two
// writes that rewrite an indexed attribute — media enrichment rewrites a body,
// an observe rule stamps `routed_to` — and the one that deletes rows are
// methods of it too. So the feed is one line beside each of four writes rather
// than a reader of its own, and every one of them runs AFTER the store's write
// has committed: the store is the record, the index follows it.
//
// The index is derived, so nothing here is a second copy of anything; what it
// cannot see is a write another process makes (`arizuko send`, an archive
// import, a group delete), which the next boot walk picks up (msgsearch).

import (
	"context"
	"log/slog"

	"github.com/kronael/arizuko/routd/msgsearch"
)

// FoundMessage is one find_messages hit: the «»-marked snippet, the message's
// own metadata and a BM25 rank where lower is better.
//
// It is the index's type under routd's name. The hit crosses no boundary —
// routd's own MCP and REST handlers are the only readers — and a second
// declaration of the same seven fields is a second thing to keep in step with
// the one search that fills them.
type FoundMessage = msgsearch.FoundMessage

// startSearch opens the index and walks the store into it in the background.
//
// The walk is not waited for: a routd that served nothing until it finished
// would hold every adapter behind one read per chat. Until it is through, a
// search is REFUSED (msgsearch.ErrWarming) rather than answered empty — an
// empty hit list says "no message says that", and an agent repeats that as
// fact.
func (d *DB) startSearch() {
	d.search = msgsearch.New()
	ctx, cancel := context.WithCancel(context.Background())
	d.searchStop = cancel
	src := d.store()
	go func() {
		if err := d.search.Build(ctx, src); err != nil {
			// The index answers this same error to every search, so the refusal
			// reaches the caller as well as the log.
			slog.Error("build the ranked message index", "err", err)
		}
	}()
}

// FindMessages answers the ranked hits for one query. `scope` is polymorphic:
// a value holding ':' is a chat, anything else a `routed_to` folder subtree.
// `sender` is exact, `since` is an RFC3339 lower bound, and ACL filtering is
// the caller's job — only the caller knows which folder asked.
func (d *DB) FindMessages(query, scope, sender, since string, limit int) ([]FoundMessage, error) {
	return d.search.Search(query, scope, sender, since, limit)
}
