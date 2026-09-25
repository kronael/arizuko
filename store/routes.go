package store

// routes.go holds every read and write of `routes`: one item per routing rule,
// addressed by (seq, target, match).
//
// The partition is OwnerRoutd because resreg's entity path writes these items
// too (resreg.SubsystemRoutd IS store.OwnerRoutd). `seq` leads the coordinate
// and is PADDED: a sort key is compared as TEXT, and an unpadded 10 sorts
// before 2. The padding is resreg's own rendering of the field
// (resreg/codec.go).
//
// THE READ ORDER IS (seq, id) AND NOT THE COORDINATE'S: 58 of 58 rules on
// krons sit at seq 0, so among equal seq the INSERTION order decides which
// rule fires. The read sorts after it reads.
//
// `id` IS MINTED, because an entity has no AUTOINCREMENT. It is the handle
// delete_route(id) addresses, a live agent wire contract, and two concurrent
// add_route calls reading `max(id) + 1` would hand one handle to two rules.
// store.Counters is the compare-and-swap that makes the mint one winner
// (entities.go).

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
)

// ErrWebJIDRouted is returned by AddRoute when the operator tries to register
// a chat_jid=web:* predicate. Web JIDs are 1:1 with groups: the gateway
// addresses web:<folder> directly and never consults the route table. The fix
// is to create the group, not add a route.
var ErrWebJIDRouted = fmt.Errorf(
	"web: JIDs are 1:1 with groups; route table does not apply. " +
		"Create the group via setup_group instead.",
)

// The attributes a route item carries. `seq`, `target` and `match` are the
// coordinate.
const (
	attrRouteID       = "id"
	attrRouteObsMsgs  = "observe_window_messages"
	attrRouteObsChars = "observe_window_chars"
	attrRouteAddedBy  = "added_by"
	attrRouteAddedVia = "added_via"
)

// routeSeqWidth is the width `seq` is padded to inside the coordinate: resreg's
// own padWidth, and a test pins the two together.
const routeSeqWidth = 10

// routeKey is the coordinate of one rule. A negative or over-wide seq is
// REFUSED rather than padded: "-1" sorts before "0" and then before "-2". A
// value carrying the separator is refused: two rules would address one item.
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

// routeFrom reads one item back. A key that does not split into three, or
// whose seq is not a number, is a LOUD fault: the router would dispatch on a
// rule nobody wrote.
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

// attrInt reads one numeric attribute. Absent is zero.
func attrInt(it objstore.Item, name string) int {
	n, err := strconv.Atoi(it.Attrs[name])
	if err != nil {
		return 0
	}
	return n
}

// routeAttrs renders the attributes one rule carries. A zero window is left
// OFF: a stored 0 would read as a cap of nothing rather than no cap. `id` is
// always written; the mint has already chosen it.
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

// routeItems reads a selection of the partition and turns it into rules, in
// the order the router resolves them. An unreadable item fails the whole read:
// a routing table read half-way dispatches to the wrong folder.
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
// written in (file header).
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
// ERROR where AllRoutes answers with a nil table: onbod offers to route a JID
// it finds in no rule, so a table it merely failed to read must not read as
// "this chat goes nowhere".
func (s *Store) RouteTable() ([]core.Route, error) {
	return s.allRoutes(context.Background())
}

// routeByID finds the item one id names. The id is an ATTRIBUTE, so this reads
// the table and compares.
func (s *Store) routeByID(ctx context.Context, id int64) (objstore.Item, core.Route, bool, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{Entity: Routes, Partition: OwnerRoutd})
	if err != nil {
		return objstore.Item{}, core.Route{}, false, fmt.Errorf("read routes: %w", err)
	}
	// EVERY item is read, not just up to the first match. An id is a handle the
	// operator and the agent both address a rule by, and it is NOT unique in every
	// live table: sloth carries six ids twice, one sloth rule against one
	// marinade rule, because the 2026-09-18 merge joined two partitions each
	// minted from its own counter (BUGS `L364`, `L335`).
	//
	// Returning the first match deleted the WRONG RULE and said it had deleted the
	// right one: `route rm 37` destroyed `main/main ← room=group/5069669817
	// verb=mention` and left 37 in place, because items arrive in sort-key order
	// and the lower `seq` came first. So an ambiguous id is refused, naming both
	// coordinates. A loud refusal costs an operator one command; the silent
	// version cost a live routing rule.
	var found objstore.Item
	var route core.Route
	ok := false
	for _, it := range items {
		r, err := routeFrom(it)
		if err != nil {
			return objstore.Item{}, core.Route{}, false, err
		}
		if r.ID != id {
			continue
		}
		if ok {
			return objstore.Item{}, core.Route{}, false, fmt.Errorf(
				"route id %d names two rules (%q and %q); it is a duplicate this table "+
					"must not hold, so nothing is changed — address the rule by its "+
					"(seq, target, match) coordinate and re-mint the ids (BUGS L364)",
				id, found.Key.Sort, it.Key.Sort)
		}
		found, route, ok = it, r, true
	}
	return found, route, ok, nil
}

func (s *Store) GetRoute(id int64) (core.Route, bool) {
	r, ok, err := s.RouteByID(id)
	if err != nil {
		return core.Route{}, false
	}
	return r, ok
}

// RouteByID answers the rule one id names, and SEPARATES "no such rule" from
// "the table could not be read": an operator told 404 about a route that still
// exists recreates it, and then two rules answer one chat.
func (s *Store) RouteByID(id int64) (core.Route, bool, error) {
	_, r, ok, err := s.routeByID(context.Background(), id)
	return r, ok, err
}

// putRoute writes one rule create-if-absent, minting an id when the caller
// carries none. It answers the id the rule now holds, whether it minted one or
// found the rule already there: what an idempotent add owes its caller.
func (s *Store) putRoute(ctx context.Context, r core.Route, addedBy, addedVia string) (int64, error) {
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
	created, err := s.TryWriteEntities(ctx, nil, w)
	if err != nil {
		return 0, err
	}
	if created {
		return r.ID, nil
	}
	// A second id for one rule is a handle that deletes somebody else's rule.
	return s.routeIDAt(ctx, key.Sort)
}

// routeCreateWrite renders "this rule exists" as a write that CANNOT REFUSE:
// every attribute is an Init, so a rule already there is left as it is. For a
// caller buffering several rules beside a write whose own refusal is an answer
// (AddGitHubSource).
func routeCreateWrite(r core.Route, addedBy, addedVia string) (objstore.Write, error) {
	key, err := routeKey(r)
	if err != nil {
		return objstore.Write{}, err
	}
	return objstore.Write{
		Op: objstore.Merge, Entity: Routes, Key: key,
		Init: routeAttrs(r, addedBy, addedVia),
	}, nil
}

func (s *Store) AddRoute(r core.Route) (int64, error) {
	if MatchesWebJID(r.Match) {
		return 0, ErrWebJIDRouted
	}
	ctx := context.Background()
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
	actor, actorSub, surface := s.auditIdentity()
	created, err := s.tryWriteAudited(func() ([]objstore.Write, audit.Event, error) {
		w := objstore.Write{
			Op: objstore.Put, Entity: Routes, Key: key, Attrs: routeAttrs(r, "", ""),
			Cond: objstore.Cond{Item: objstore.ItemAbsent},
		}
		return []objstore.Write{w}, audit.Event{
			Category: audit.CategoryMutation,
			Action:   "route.create",
			Actor:    actor,
			ActorSub: actorSub,
			Surface:  surface,
			Resource: fmt.Sprintf("routes/%d", r.ID),
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
	if created {
		return r.ID, nil
	}
	// The rule is already there and the id it carries is the caller's handle.
	// Nothing was created, so nothing is recorded.
	return s.routeIDAt(ctx, key.Sort)
}

// routeIDAt answers the id the rule at sort already carries, for the two
// writers that get there by having their own create REFUSED: a rule that is
// not there is a contradiction and says so.
func (s *Store) routeIDAt(ctx context.Context, sort string) (int64, error) {
	id, ok, err := s.routeAt(ctx, sort)
	if err != nil || !ok {
		return 0, fmt.Errorf("route %q refused its own create and is not there", sort)
	}
	return id, nil
}

// routeAt answers the id the rule at one coordinate holds, and whether a rule
// is there at all. The buffered add asks BEFORE it buffers anything: a write
// that has not been sent has no verdict to read back.
func (s *Store) routeAt(ctx context.Context, sort string) (int64, bool, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Routes, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: sort},
	})
	if err != nil || len(items) == 0 {
		return 0, false, err
	}
	return int64(attrInt(items[0], attrRouteID)), true, nil
}

// PutRouteRow is the audit-free twin of AddRoute, for a caller with no audit
// context of its own.
func (s *Store) PutRouteRow(r core.Route) (int64, error) {
	if MatchesWebJID(r.Match) {
		return 0, ErrWebJIDRouted
	}
	return s.putRoute(context.Background(), r, "", "")
}

// DeleteRoute removes a rule by id and records it. Returns rows affected (0 is
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
	err = s.writeAudited(func() ([]objstore.Write, audit.Event, error) {
		w := objstore.Write{Op: objstore.Delete, Entity: Routes, Key: it.Key}
		return []objstore.Write{w}, audit.Event{
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

// UpdateRoute rewrites a rule by id and records it. Returns rows affected (0
// is not found, and no audit row). An edit that changes seq, target or match
// changes the rule's COORDINATE, so the write is a delete and a put in ONE
// batch; the id is carried across, because it is the handle every agent holds.
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
	err = s.writeAudited(func() ([]objstore.Write, audit.Event, error) {
		return writes, audit.Event{
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

// MatchesWebJID reports whether a route's Match expression includes a chat_jid
// predicate that begins with "web:". Such routes can never fire and silently
// mask the operator's setup error. Exported because routd's resreg face writes
// `routes` itself and must refuse the same rule (BUGS L57).
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
// every rule for a root caller. The target is the SECOND field of the
// coordinate, so containment is a filter: a folder matches itself, its
// children under "/" and its topics under "#" (spec 6/40 §1).
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

// PutRouteInto buffers one rule's creation on the caller's write set and
// answers the id the rule holds, for routd's `/v1/routes` face, where AddRoute
// would record the act twice.
//
// THE RULE IS LOOKED UP BEFORE THE WRITE: an add is idempotent, and a buffered
// write has no verdict to read back. A rule another caller creates between
// that read and the batch REFUSES the guarded create: loud, rather than an id
// no row carries. The id is minted BEFORE anything is buffered (nextNumber).
func (s *Store) PutRouteInto(ctx context.Context, ws *WriteSet, r core.Route,
	addedBy, addedVia string, refused error) (int64, error) {
	if MatchesWebJID(r.Match) {
		return 0, ErrWebJIDRouted
	}
	key, err := routeKey(r)
	if err != nil {
		return 0, err
	}
	held, there, err := s.routeAt(ctx, key.Sort)
	if err != nil {
		return 0, err
	}
	if there {
		return held, nil
	}
	if r.ID == 0 {
		n, nerr := s.nextNumber(ctx, OwnerRoutd, CounterRouteID)
		if nerr != nil {
			return 0, fmt.Errorf("mint a route id: %w", nerr)
		}
		r.ID = int64(n)
	}
	if err := ws.AddGuarded(refused, objstore.Write{
		Op: objstore.Put, Entity: Routes, Key: key, Attrs: routeAttrs(r, addedBy, addedVia),
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}); err != nil {
		return 0, err
	}
	return r.ID, nil
}

// DeleteRouteInto buffers the removal of one rule by id and reports whether a
// rule was there to remove. The delete conditions on the item still being
// there, and refused is what a rule another caller removed in between means.
func (s *Store) DeleteRouteInto(ctx context.Context, ws *WriteSet, id int64, refused error) (bool, error) {
	it, _, ok, err := s.routeByID(ctx, id)
	if err != nil || !ok {
		return false, err
	}
	return true, ws.AddGuarded(refused, objstore.Write{
		Op: objstore.Delete, Entity: Routes, Key: it.Key,
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	})
}

// ReplaceFolderRoutesInto buffers the replacement of the rules whose target is
// `folder` or under it with `routes`: routd's set_routes. An empty folder
// replaces the whole table.
//
// EVERY REPLACEMENT RULE IS MINTED A NEW id: set_routes replaces rules, and
// carrying a replaced rule's handle across would hand `delete_route(id)` an
// identity nobody wrote. A replacement whose coordinate an existing rule holds
// is an unconditional Put and that rule is NOT deleted: a batch may name each
// item once. THE WHOLE REPLACEMENT IS ONE BATCH, because a routing table half
// replaced is neither table; past objstore.MaxBatchActions it is REFUSED
// naming the count, a loud answer to an operator who can split the call. The
// largest live instance holds 58 rules (2026-09-07).
func (s *Store) ReplaceFolderRoutesInto(ctx context.Context, ws *WriteSet, folder string,
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
			// The same containment ListRoutes reads with.
			if !groupfolder.ContainsSep(folder, r.Target, "/") &&
				!groupfolder.ContainsSep(folder, r.Target, "#") {
				continue
			}
		}
		writes = append(writes, objstore.Write{Op: objstore.Delete, Entity: Routes, Key: it.Key})
	}
	ws.Add(append(writes, puts...)...)
	return nil
}
