package store

// routes.go holds every read and write of `routes`: one item per routing rule,
// addressed by (seq, target, match).
//
// THE PARTITION IS OwnerRoutd AND IT IS NOT A CHOICE THIS FILE MAKES. `routes`
// is a registered resreg resource, and an entity-backed resource's items live
// at its owner subsystem (resreg.SubsystemRoutd, which IS store.OwnerRoutd).
//
// `seq` LEADS THE COORDINATE AND IS PADDED. A sort key is compared as TEXT, so
// an unpadded number puts 10 before 2 and a table whose whole meaning is "lower
// seq wins, first match" would read back in an order contradicting itself. The
// padding is the same rendering resreg declares on the same field
// (resreg/codec.go), because both of them write these items.
//
// THE READ ORDER IS (seq, id) AND NOT THE COORDINATE'S. Every live instance
// carries almost every rule at seq 0 — 58 of 58 on krons — so among equal seq
// the order that decides which rule fires is the INSERTION order, which is what
// `ORDER BY seq, id` gave. Sorting by the coordinate instead would re-rank those
// 58 rules alphabetically and change which one answers a chat. The read sorts
// after it reads.
//
// `id` IS MINTED, because an entity has no AUTOINCREMENT. It is the handle
// delete_route(id) addresses — a live agent wire contract — and two concurrent
// add_route calls reading `max(id) + 1` would hand the same handle to two
// rules, after which the delete names both. store.Counters is the
// compare-and-swap that makes the mint one winner (entities.go).

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
)

// ErrWebJIDRouted is returned by AddRoute when the operator tries to
// register a chat_jid=web:* predicate. Web JIDs are 1:1 with groups —
// the gateway addresses web:<folder> directly via GroupByFolder and
// never consults the route table. The fix is to create the group, not
// add a route.
var ErrWebJIDRouted = fmt.Errorf(
	"web: JIDs are 1:1 with groups; route table does not apply. " +
		"Create the group via setup_group instead.",
)

// The attributes a route item carries. `seq`, `target` and `match` are NOT among
// them: they are the coordinate.
const (
	attrRouteID       = "id"
	attrRouteObsMsgs  = "observe_window_messages"
	attrRouteObsChars = "observe_window_chars"
	attrRouteAddedBy  = "added_by"
	attrRouteAddedVia = "added_via"
)

// routeSeqWidth is the width `seq` is padded to inside the coordinate. It is
// resreg's own padWidth, and a test pins the two together: a key written at one
// width and read at another sorts into the wrong place instead of failing.
const routeSeqWidth = 10

// routeKey is the coordinate of one rule.
//
// A negative or over-wide seq is REFUSED rather than padded: "-1" sorts before
// "0" and then before "-2", so the padding inverts the order it exists to
// preserve. A value carrying the separator is refused for the reason every
// composite coordinate refuses one — two rules would address one item.
func routeKey(r core.Route) (objstore.Key, error) {
	seq, err := padSeq(r.Seq)
	if err != nil {
		return objstore.Key{}, err
	}
	if r.Target == "" {
		return objstore.Key{}, fmt.Errorf("route carries no target, which is part of its "+
			"coordinate (seq=%d match=%q)", r.Seq, r.Match)
	}
	for name, v := range map[string]string{"target": r.Target, "match": r.Match} {
		if strings.Contains(v, KeySep) {
			return objstore.Key{}, fmt.Errorf("route %s=%q carries the byte that separates "+
				"the three fields of its coordinate", name, v)
		}
	}
	return objstore.Key{
		Partition: OwnerRoutd,
		Sort:      seq + KeySep + r.Target + KeySep + r.Match,
	}, nil
}

// padSeq renders one seq at the declared width.
func padSeq(seq int) (string, error) {
	if seq < 0 {
		return "", fmt.Errorf("route seq %d is negative, and the coordinate pads to sort in "+
			"numeric order, which padding a negative does not do", seq)
	}
	text := strconv.Itoa(seq)
	if len(text) > routeSeqWidth {
		return "", fmt.Errorf("route seq %d is wider than the %d digits the coordinate pads "+
			"to, so it would sort below every shorter value", seq, routeSeqWidth)
	}
	return strings.Repeat("0", routeSeqWidth-len(text)) + text, nil
}

// routeFrom reads one item back. A key that does not split into three parts, or
// whose seq is not a number, is a LOUD fault: half a coordinate names a rule
// nobody wrote, and the router would dispatch on it.
func routeFrom(it objstore.Item) (core.Route, error) {
	parts := strings.SplitN(it.Key.Sort, KeySep, 3)
	if len(parts) != 3 {
		return core.Route{}, fmt.Errorf("route item %q splits into %d parts and its "+
			"coordinate (seq,target,match) has 3", it.Key.Sort, len(parts))
	}
	seq, err := strconv.Atoi(parts[0])
	if err != nil {
		return core.Route{}, fmt.Errorf("route item %q carries seq %q, which is not a "+
			"number: %w", it.Key.Sort, parts[0], err)
	}
	return core.Route{
		Seq:                   seq,
		Target:                parts[1],
		Match:                 parts[2],
		ID:                    int64(attrInt(it, attrRouteID)),
		ObserveWindowMessages: attrInt(it, attrRouteObsMsgs),
		ObserveWindowChars:    attrInt(it, attrRouteObsChars),
	}, nil
}

// attrInt reads one numeric attribute. Absent is zero, which is the only null
// this model has and exactly what `COALESCE(col, 0)` answered.
func attrInt(it objstore.Item, name string) int {
	n, err := strconv.Atoi(it.Attrs[name])
	if err != nil {
		return 0
	}
	return n
}

// routeAttrs renders the attributes one rule carries.
//
// A zero window is left OFF, because absent is the only null and a stored 0
// would read as a cap of nothing rather than as no cap. `id` is always written:
// it is the rule's handle and the mint has already chosen it.
func routeAttrs(r core.Route, addedBy, addedVia string) map[string]string {
	attrs := map[string]string{attrRouteID: strconv.FormatInt(r.ID, 10)}
	if r.ObserveWindowMessages != 0 {
		attrs[attrRouteObsMsgs] = strconv.Itoa(r.ObserveWindowMessages)
	}
	if r.ObserveWindowChars != 0 {
		attrs[attrRouteObsChars] = strconv.Itoa(r.ObserveWindowChars)
	}
	setAttr(attrs, attrRouteAddedBy, addedBy)
	setAttr(attrs, attrRouteAddedVia, addedVia)
	return attrs
}

// routeItems reads a selection of the partition and turns it into rules, in the
// order the router resolves them: seq first, then the id that records which was
// written first. An unreadable item fails the whole read — a routing table read
// half-way is a table that dispatches to the wrong folder.
func (s *Store) routeItems(ctx context.Context, q objstore.Query) ([]core.Route, error) {
	items, err := s.Obj().Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("read routes: %w", err)
	}
	out := make([]core.Route, 0, len(items))
	for _, it := range items {
		r, err := routeFrom(it)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	routeOrder(out)
	return out, nil
}

// routeOrder is `ORDER BY seq, id`: precedence, then the order the rules were
// written in. See the file header on why the coordinate's own order is not it.
func routeOrder(rs []core.Route) {
	slices.SortFunc(rs, func(a, b core.Route) int {
		if a.Seq != b.Seq {
			return a.Seq - b.Seq
		}
		return int(a.ID - b.ID)
	})
}

// allRoutes is the whole table.
func (s *Store) allRoutes(ctx context.Context) ([]core.Route, error) {
	return s.routeItems(ctx, objstore.Query{Entity: Routes, Partition: OwnerRoutd})
}

func (s *Store) AllRoutes() []core.Route {
	rs, err := s.RouteTable()
	if err != nil {
		return nil
	}
	return rs
}

// RouteTable is the whole table in the order the router resolves it, and an
// ERROR where AllRoutes answers with a nil table.
//
// The difference decides an act: onbod offers to route a JID it finds in no
// rule, so a table it merely failed to read must not read as "this chat goes
// nowhere" — that offer writes a second rule for a chat that already had one.
func (s *Store) RouteTable() ([]core.Route, error) {
	return s.allRoutes(context.Background())
}

// routeByID finds the item one id names. The id is an ATTRIBUTE, not a leading
// fragment of the key, so this reads the table and compares — which is what an
// unindexed `WHERE id = ?` over 58 rows was.
func (s *Store) routeByID(ctx context.Context, id int64) (objstore.Item, core.Route, bool, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{Entity: Routes, Partition: OwnerRoutd})
	if err != nil {
		return objstore.Item{}, core.Route{}, false, fmt.Errorf("read routes: %w", err)
	}
	for _, it := range items {
		r, err := routeFrom(it)
		if err != nil {
			return objstore.Item{}, core.Route{}, false, err
		}
		if r.ID == id {
			return it, r, true, nil
		}
	}
	return objstore.Item{}, core.Route{}, false, nil
}

func (s *Store) GetRoute(id int64) (core.Route, bool) {
	r, ok, err := s.RouteByID(id)
	if err != nil {
		return core.Route{}, false
	}
	return r, ok
}

// RouteByID answers the rule one id names, and SEPARATES "no such rule" from
// "the table could not be read" — the distinction dashd's route pages gate on,
// because an operator told 404 about a route that still exists goes and
// recreates it, and then two rules answer one chat.
func (s *Store) RouteByID(id int64) (core.Route, bool, error) {
	_, r, ok, err := s.routeByID(context.Background(), id)
	return r, ok, err
}

// putRoute writes one rule create-if-absent, minting an id when the caller
// carries none. It answers the id the rule now holds, whether it minted one or
// found the rule already there — which is what an idempotent add owes its
// caller, and what LastInsertId gave.
func (s *Store) putRoute(ctx context.Context, tx *sql.Tx, r core.Route, addedBy, addedVia string) (int64, error) {
	key, err := routeKey(r)
	if err != nil {
		return 0, err
	}
	if r.ID == 0 {
		n, err := s.nextNumber(ctx, OwnerRoutd, CounterRouteID)
		if err != nil {
			return 0, fmt.Errorf("mint a route id: %w", err)
		}
		r.ID = int64(n)
	}
	w := objstore.Write{
		Op: objstore.Put, Entity: Routes, Key: key, Attrs: routeAttrs(r, addedBy, addedVia),
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}
	var created bool
	if tx != nil {
		created, err = s.WriteEntitiesInTx(ctx, tx, nil, w)
	} else {
		created, err = s.WriteEntities(ctx, nil, w)
	}
	if err != nil {
		return 0, err
	}
	if created {
		return r.ID, nil
	}
	// The rule is already there, so the coordinate names it and the id it
	// already carries is the handle its caller has to be given: a second id for
	// one rule is a handle that deletes somebody else's rule.
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Routes, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: key.Sort},
	})
	if err != nil || len(items) == 0 {
		return 0, fmt.Errorf("route %q refused its own create and is not there", key.Sort)
	}
	return int64(attrInt(items[0], attrRouteID)), nil
}

func (s *Store) AddRoute(r core.Route) (int64, error) {
	if MatchesWebJID(r.Match) {
		return 0, ErrWebJIDRouted
	}
	ctx := context.Background()
	var id int64
	err := s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		var err error
		id, err = s.putRoute(ctx, tx, r, "", "")
		if err != nil {
			return audit.Event{}, err
		}
		actor, actorSub, surface := s.auditIdentity()
		return audit.Event{
			Category: audit.CategoryMutation,
			Action:   "route.create",
			Actor:    actor,
			ActorSub: actorSub,
			Surface:  surface,
			Resource: fmt.Sprintf("routes/%d", id),
			Folder:   r.Target,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"match":  r.Match,
				"target": r.Target,
				"seq":    r.Seq,
			},
		}, nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// PutRouteRow writes a route idempotently WITHOUT emitting an audit_log row —
// the audit-free twin of AddRoute, for a caller with no audit context of its
// own.
func (s *Store) PutRouteRow(r core.Route) (int64, error) {
	if MatchesWebJID(r.Match) {
		return 0, ErrWebJIDRouted
	}
	return s.putRoute(context.Background(), nil, r, "", "")
}

// DeleteRoute removes a rule by id and records it. Returns rows affected (0 →
// not found, and no audit row: a delete that matched nothing is not a
// mutation).
func (s *Store) DeleteRoute(id int64) (int64, error) {
	ctx := context.Background()
	it, r, ok, err := s.routeByID(ctx, id)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	actor, actorSub, surface := s.auditIdentity()
	err = s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		if _, err := s.WriteEntitiesInTx(ctx, tx, nil, objstore.Write{
			Op: objstore.Delete, Entity: Routes, Key: it.Key,
		}); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{
			Category: audit.CategoryMutation,
			Action:   "route.delete",
			Actor:    actor,
			ActorSub: actorSub,
			Surface:  surface,
			Resource: fmt.Sprintf("routes/%d", id),
			Folder:   core.ParseRouteTarget(r.Target).Folder,
			Outcome:  audit.OutcomeOK,
		}, nil
	})
	if err != nil {
		return 0, err
	}
	return 1, nil
}

// UpdateRoute rewrites a rule by id and records it. Returns rows affected
// (0 → not found, and no audit row).
//
// An edit that changes seq, target or match changes the rule's COORDINATE, so
// the write is a delete and a put in ONE batch: the item does not move, it is
// replaced. The id is carried across, because it is the handle the caller and
// every agent already hold.
func (s *Store) UpdateRoute(id int64, r core.Route) (int64, error) {
	if MatchesWebJID(r.Match) {
		return 0, ErrWebJIDRouted
	}
	ctx := context.Background()
	it, _, ok, err := s.routeByID(ctx, id)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	r.ID = id
	key, err := routeKey(r)
	if err != nil {
		return 0, err
	}
	writes := []objstore.Write{{
		Op: objstore.Put, Entity: Routes, Key: key, Attrs: routeAttrs(r, "", ""),
	}}
	if key != it.Key {
		// The delete goes FIRST so the two writes name two different items: a
		// batch may name each item once (objstore.ValidateBatch).
		writes = append([]objstore.Write{{
			Op: objstore.Delete, Entity: Routes, Key: it.Key,
		}}, writes...)
	}
	actor, actorSub, surface := s.auditIdentity()
	err = s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		if _, err := s.WriteEntitiesInTx(ctx, tx, nil, writes...); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{
			Category: audit.CategoryMutation,
			Action:   "route.update",
			Actor:    actor,
			ActorSub: actorSub,
			Surface:  surface,
			Resource: fmt.Sprintf("routes/%d", id),
			Folder:   r.Target,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"match":  r.Match,
				"target": r.Target,
				"seq":    r.Seq,
			},
		}, nil
	})
	if err != nil {
		return 0, err
	}
	return 1, nil
}

// MatchesWebJID reports whether a route's Match expression includes a
// chat_jid predicate that begins with "web:". Such routes can never
// fire — the gateway addresses web JIDs directly — and silently mask
// the operator's setup error. Exported because routd's resreg face writes
// `routes` on its own tx and must refuse the same rule (BUGS L57).
func MatchesWebJID(match string) bool {
	for f := range strings.FieldsSeq(match) {
		k, pat, ok := strings.Cut(f, "=")
		if !ok || k != "chat_jid" {
			continue
		}
		if strings.HasPrefix(pat, "web:") {
			return true
		}
	}
	return false
}

// ListRoutes returns the rules whose target is inside folder's subtree, or
// every rule for a root caller.
//
// The target is the SECOND field of the coordinate, so containment is a
// comparison and not a key predicate: the read takes the table and filters. The
// predicate itself is groupfolder's, and it is the same one the SQL half
// rendered — a folder matches itself, its children under "/" and its topics
// under "#" (spec 6/40 §1).
func (s *Store) ListRoutes(folder string, isRoot bool) []core.Route {
	all, err := s.allRoutes(context.Background())
	if err != nil {
		return nil
	}
	if isRoot || folder == "" {
		return all
	}
	out := make([]core.Route, 0, len(all))
	for _, r := range all {
		if groupfolder.ContainsSep(folder, r.Target, "/") ||
			groupfolder.ContainsSep(folder, r.Target, "#") {
			out = append(out, r)
		}
	}
	return out
}

// PutRouteTx writes one rule create-if-absent on a transaction the CALLER owns
// and answers the id the rule holds, for routd's `/v1/routes` face: the rule and
// resreg.invoke's single audit_log row commit together, and AddRoute would open
// a second transaction on the same database and record the act twice.
//
// The id is minted BEFORE anything is written to tx. The counter is a
// compare-and-swap of its own (entities.go nextNumber), so it runs outside the
// caller's transaction — and a transaction already holding the engine's write
// lock would block its own mint.
func (s *Store) PutRouteTx(ctx context.Context, tx *sql.Tx, r core.Route, addedBy, addedVia string) (int64, error) {
	if MatchesWebJID(r.Match) {
		return 0, ErrWebJIDRouted
	}
	return s.putRoute(ctx, tx, r, addedBy, addedVia)
}

// DeleteRouteTx removes one rule by id on a transaction the caller owns and
// reports whether a rule went. The id is an attribute, so the item is found by
// reading the table; the delete then conditions on the item still being there,
// so a rule another caller removed in between answers false rather than OK.
func (s *Store) DeleteRouteTx(ctx context.Context, tx *sql.Tx, id int64) (bool, error) {
	it, _, ok, err := s.routeByID(ctx, id)
	if err != nil || !ok {
		return false, err
	}
	return s.WriteEntitiesInTx(ctx, tx, nil, objstore.Write{
		Op: objstore.Delete, Entity: Routes, Key: it.Key,
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	})
}

// ReplaceFolderRoutesTx replaces the rules whose target is `folder` or under it
// with `routes`, on a transaction the CALLER owns — routd's set_routes. An empty
// folder replaces the whole table, which is the operator/root shape.
//
// EVERY REPLACEMENT RULE IS MINTED A NEW id. set_routes does not edit rules, it
// replaces them, so a survivor genuinely was created by this call and carrying a
// replaced rule's handle across would hand `delete_route(id)` an identity nobody
// wrote. The mints all run BEFORE the first write to tx, because the counter is
// its own compare-and-swap outside the transaction and a transaction holding the
// write lock would block it.
//
// A replacement whose coordinate an existing rule already holds is written as an
// unconditional Put and that rule is NOT deleted: a batch may name each item
// once (objstore.ValidateBatch), and a Put replaces the whole item anyway.
func (s *Store) ReplaceFolderRoutesTx(ctx context.Context, tx *sql.Tx, folder string,
	routes []core.Route, addedBy, addedVia string) error {
	puts := make([]objstore.Write, 0, len(routes))
	replaced := make(map[string]bool, len(routes))
	for _, r := range routes {
		if MatchesWebJID(r.Match) {
			return ErrWebJIDRouted
		}
		n, err := s.nextNumber(ctx, OwnerRoutd, CounterRouteID)
		if err != nil {
			return fmt.Errorf("mint a route id: %w", err)
		}
		r.ID = int64(n)
		key, err := routeKey(r)
		if err != nil {
			return err
		}
		if replaced[key.Sort] {
			return fmt.Errorf("route set carries seq=%d target=%q match=%q twice; "+
				"two rules would address one item", r.Seq, r.Target, r.Match)
		}
		replaced[key.Sort] = true
		puts = append(puts, objstore.Write{
			Op: objstore.Put, Entity: Routes, Key: key, Attrs: routeAttrs(r, addedBy, addedVia),
		})
	}
	items, err := s.Obj().Query(ctx, objstore.Query{Entity: Routes, Partition: OwnerRoutd})
	if err != nil {
		return fmt.Errorf("read routes: %w", err)
	}
	writes := make([]objstore.Write, 0, len(items)+len(puts))
	for _, it := range items {
		if replaced[it.Key.Sort] {
			continue
		}
		if folder != "" {
			r, err := routeFrom(it)
			if err != nil {
				return err
			}
			// The same containment ListRoutes reads with: a target is the folder
			// itself, a child under "/" or a topic under "#".
			if !groupfolder.ContainsSep(folder, r.Target, "/") &&
				!groupfolder.ContainsSep(folder, r.Target, "#") {
				continue
			}
		}
		writes = append(writes, objstore.Write{Op: objstore.Delete, Entity: Routes, Key: it.Key})
	}
	writes = append(writes, puts...)
	_, err = applyChunks(ctx, "replace the routes of "+folder, writes,
		func(ctx context.Context, chunk []objstore.Write) (bool, error) {
			return s.WriteEntitiesInTx(ctx, tx, nil, chunk...)
		})
	return err
}
