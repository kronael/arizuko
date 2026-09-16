package routd

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/router"
)

// reads.go holds the routd.DB query methods backing the agent's read/manage MCP
// tools (federated via the /v1/messages, /v1/routing, /v1/engagement, /v1/cost
// surfaces). routd owns the conversation/routing state.

// MessagesBefore returns rows for one chat_jid older than `before`, oldest first
// (inspect_messages). before="" → now.
func (d *DB) MessagesBefore(jid, before string, limit int) ([]core.Message, error) {
	t, err := readStoredInstant(before)
	if err != nil {
		return nil, err
	}
	return d.store().MessagesBefore(jid, t, limit)
}

// MessagesByThread returns rows for one (chat_jid, topic), newest first
// (get_thread). before="" → now.
func (d *DB) MessagesByThread(jid, topic, before string, limit int) ([]core.Message, error) {
	t, err := readStoredInstant(before)
	if err != nil {
		return nil, err
	}
	return d.store().MessagesByThread(jid, topic, t, limit)
}

// DefaultFolderForJID resolves the routing target folder for jid against the
// current route table. "" when no route matches.
func (d *DB) DefaultFolderForJID(jid string) string {
	routes, err := d.Routes()
	if err != nil {
		return ""
	}
	t := router.ResolveRoute(core.Message{ChatJID: jid, Verb: "message"}, routes)
	return core.ParseRouteTarget(t).Folder
}

// JIDRoutedToFolder reports whether jid's default route target is folder or a
// descendant of it.
func (d *DB) JIDRoutedToFolder(jid, folder string) bool {
	target := d.DefaultFolderForJID(jid)
	if target == "" {
		return false
	}
	return groupfolder.Contains(folder, target)
}

// JIDRoutableToFolder reports whether folder (or a descendant) is the target of
// any route matching jid when the verb predicate is ignored. Lets a mention-only
// sub-folder agent reply.
func (d *DB) JIDRoutableToFolder(jid, folder string) bool {
	routes, err := d.Routes()
	if err != nil {
		return false
	}
	msg := core.Message{ChatJID: jid}
	for _, r := range routes {
		if !router.RouteMatchesIgnoreVerb(r, msg) {
			continue
		}
		t := core.ParseRouteTarget(r.Target).Folder
		if groupfolder.Contains(folder, t) {
			return true
		}
	}
	return false
}

// ErrNoRoute is what GetRoute answers for an id the table does not hold. It is
// this package's own sentinel: the store is addressed by key and has no rows to
// borrow an error from.
var ErrNoRoute = errors.New("routd: no such route")

// GetRoute returns one route by id. The error is ErrNoRoute when the route
// doesn't exist (caller → 404) vs a real store error (caller → 500) — callers
// MUST distinguish them with errors.Is so a DB fault isn't reported as not-found.
//
// The id is an ATTRIBUTE of a route item, not its coordinate, so store finds it
// by reading the table and comparing. store.GetRoute collapses both misses into
// ok=false; this face keeps them apart, which is what the 404-vs-500 split rests
// on, so it reads the table here.
func (d *DB) GetRoute(id int64) (core.Route, error) {
	routes, err := d.store().RouteTable()
	if err != nil {
		return core.Route{}, err
	}
	for _, r := range routes {
		if r.ID == id {
			return r, nil
		}
	}
	return core.Route{}, ErrNoRoute
}

// WebRouteOwner reports which folder owns an exact path_prefix row, if any
// (backs set_web_route first-claim). The path prefix IS the coordinate, so the
// answer is one item.
func (d *DB) WebRouteOwner(pathPrefix string) (string, bool) {
	routes, err := d.store().AllWebRoutes()
	if err != nil {
		return "", false
	}
	for _, r := range routes {
		if r.PathPrefix == pathPrefix {
			return r.Folder, true
		}
	}
	return "", false
}

// ErroredChat is one chat in the errored set with its inbound-since-error count
// and resolved routing target.
type ErroredChat struct {
	ChatJID  string    `json:"chat_jid"`
	Count    int       `json:"count"`
	LastAt   time.Time `json:"last_at"`
	RoutedTo string    `json:"routed_to"`
}

// ErroredChats lists chats flagged errored=1 whose resolved folder is owned by
// `folder` (isRoot sees all). Scopes by the live route resolution since routd
// has no per-chat folder column.
func (d *DB) ErroredChats(folder string, isRoot bool) ([]ErroredChat, error) {
	// The flagged chats come from the entity and the per-chat numbers from
	// `messages`, which is still a table: a join is not an operation the store
	// has, so the scope filter runs before the counts and only a flagged,
	// visible chat costs a read. Krons carries 136 chats and flags a handful.
	jids, err := d.store().ErroredChatJIDs()
	if err != nil {
		return nil, err
	}
	var out []ErroredChat
	for _, jid := range jids {
		ec := ErroredChat{ChatJID: jid, RoutedTo: d.DefaultFolderForJID(jid)}
		if !isRoot && ec.RoutedTo != folder && !strings.HasPrefix(ec.RoutedTo, folder+"/") {
			continue
		}
		// The count is inbound rows and the instant is the newest row of any
		// kind, which is what the two correlated subqueries this replaces asked.
		count, lastAt, err := d.store().ChatActivity(context.Background(), jid)
		if err != nil {
			return out, err
		}
		ec.Count, ec.LastAt = count, lastAt
		out = append(out, ec)
	}
	return out, nil
}
