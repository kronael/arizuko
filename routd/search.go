package routd

// search.go is routd's ranked message search: the in-memory index that
// `find_messages` and `GET /v1/messages/find` answer from, the boot walk that
// fills it, and the four writes that keep it in step (spec 6/42 §8). routd
// appends every message a channel produces, so the feed is one line beside
// each of four writes on this type rather than a reader of its own, and each
// runs AFTER the store's write has committed: the store is the record, the
// index follows it. A write another process makes (`arizuko send`, an archive
// import, a group delete) is picked up by the next boot walk (msgsearch).

import (
	"context"
	"errors"
	"log/slog"

	"github.com/kronael/arizuko/routd/msgsearch"
)

// FoundMessage is one find_messages hit: the «»-marked snippet, the message's
// own metadata and a BM25 rank where lower is better. An alias: a second
// declaration of the same seven fields is a second thing to keep in step.
type FoundMessage = msgsearch.FoundMessage

// StartSearch opens the index and walks the store into it in the background.
// Only the process that ANSWERS searches calls it (routd's main): an opener
// that only reads or writes rows does not, because the walk's cancellation on
// Close reported as a failed index build on every admin command (BUGS L295).
// The walk is not waited for; until it is through a search is REFUSED
// (msgsearch.ErrWarming) rather than answered empty — an empty hit list says
// "no message says that", and an agent repeats that as fact.
func (d *DB) StartSearch() {
	d.search = msgsearch.New()
	ctx, cancel := context.WithCancel(context.Background())
	d.searchStop = cancel
	d.searchDone = make(chan struct{})
	src := d.store()
	go func() {
		defer close(d.searchDone)
		if err := d.search.Build(ctx, src); err != nil {
			// The index answers this same error to every search.
			slog.Error("build the ranked message index", "err", err)
		}
	}()
}

// FindMessages answers the ranked hits for one query. `scope` is polymorphic:
// a value holding ':' is a chat, anything else a `routed_to` folder subtree.
// ACL filtering is the caller's job — only the caller knows which folder asked.
func (d *DB) FindMessages(query, scope, sender, since string, limit int) ([]FoundMessage, error) {
	if d.search == nil {
		// Loud: the alternative is "no hits" from a process that never built
		// an index.
		return nil, errors.New("this process holds no ranked message index")
	}
	return d.search.Search(query, scope, sender, since, limit)
}
