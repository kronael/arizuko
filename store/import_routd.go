package store

// import_routd.go is the target half of the move that carries routd's thirteen
// tables onto the entities they are now (spec 6/37 step 7).
//
// IT OWNS THE ITEM SHAPE AND NOTHING ELSE. The SOURCE half — the only half that
// knows the old column layout — is `arizuko migrate-routd`, which reads the
// legacy tables into the rows below. Two tools reach this: the in-place move on
// an already-split instance, and `arizuko migrate-split`, which carries the same
// thirteen out of the pre-split monolith. One target, so the two cannot disagree
// about what a row becomes.
//
// A ROW IS ITS COORDINATE AND ITS ATTRIBUTES, NOT A Go STRUCT. Every other
// import in this package takes typed rows; this one does not, and the reason is
// the failure it prevents. Six of the thirteen deliberately carry columns no Go
// struct in the tree names — the sealed secret and its four OAuth fields, a
// GitHub source's webhook secret, a token's `kind` — and a struct-shaped
// migration drops exactly those, silently, which is the shape of BUGS L265. A
// column list and an attribute map carry every one of them without naming any.
//
// WHAT IT DOES DECIDE is the two things a column cannot state: the COORDINATE,
// rendered by the same builder the daemon's own writer calls, and the WIDTH of
// every stamped instant. The column path wrote time.RFC3339 at 20 bytes and the
// object path writes core.TS at 30, and a stored instant is compared as TEXT —
// so a table carried over unchanged would hold two widths in one attribute and
// every comparison over it would answer by byte order rather than by time
// (BUGS L97). Normalising is this file's job, not the daemon's.

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// RoutdRow is one legacy row: the coordinate's fields in the resource's own
// order, and every other column as the attribute it becomes.
//
// An empty attribute VALUE is dropped rather than written, because absent is the
// only null this model has — a NULL column and an empty one are the same fact on
// the other side. A coordinate field may be empty (network_rules spells an
// instance-wide rule folder=""); the whole coordinate may not.
type RoutdRow struct {
	Key   []string
	Attrs map[string]string
}

// RoutdSnapshot is every legacy row, keyed by the entity it lands in.
type RoutdSnapshot map[string][]RoutdRow

// RoutdEntities is the order the thirteen are read, reported and written in.
// groups leads because an operator reads the report that way; nothing else
// depends on it, because an entity has no foreign key to satisfy.
var RoutdEntities = []string{
	Groups, ACL, ACLMembership, Routes, RouteTokens, ScheduledTasks,
	Secrets, PendingActions, NetworkRules, ProxydRoutes, WebRoutes,
	InstalledPackages, GitHubSources,
}

// routdKeyFields names each entity's coordinate, in order, so the source half
// can build its SELECT and this half can say which column was short. It is the
// SAME order the key builders below join in, and a test pins the two together.
var routdKeyFields = map[string][]string{
	Groups:            {"folder"},
	ACL:               {"principal", "action", "scope", "params", "predicate", "effect"},
	ACLMembership:     {"child", "parent"},
	Routes:            {"seq", "target", "match"},
	RouteTokens:       {"token_hash"},
	ScheduledTasks:    {"id"},
	Secrets:           {"scope_kind", "scope_id", "key"},
	PendingActions:    {"id"},
	NetworkRules:      {"folder", "target"},
	ProxydRoutes:      {"path"},
	WebRoutes:         {"path_prefix"},
	InstalledPackages: {"folder", "name"},
	GitHubSources:     {"source"},
}

// routdStamped names, per entity, the attributes whose width this move
// normalises to core.TS. They are exactly the resources' StampedFields — the
// instants the object path writes itself — so a value the column path left at
// 20 bytes would otherwise be compared against one this daemon writes at 30.
//
// An instant the resource does NOT stamp keeps the layout its own writer chose
// and is not in this list: scheduled_tasks.next_run is RFC3339 in the operator's
// timezone and is compared as a parsed instant, never as bytes.
var routdStamped = map[string][]string{
	Groups:            {"added_at", "updated_at"},
	ACL:               {"granted_at"},
	ACLMembership:     {"added_at"},
	RouteTokens:       {"created_at"},
	ScheduledTasks:    {"created_at"},
	Secrets:           {"created_at"},
	NetworkRules:      {"created_at"},
	WebRoutes:         {"created_at"},
	InstalledPackages: {"installed_at"},
	GitHubSources:     {"created_at"},
}

// RoutdCounts is how many items each entity received.
type RoutdCounts map[string]int

// routdKey renders one row's coordinate through the SAME builder the entity's
// own daemon writer calls, so a migrated item lands where a live write would
// put it. A dispatch, not a second declaration: each case calls the one key
// function that entity already has.
func routdKey(entity string, key []string) (objstore.Key, error) {
	want := routdKeyFields[entity]
	if len(want) == 0 {
		return objstore.Key{}, fmt.Errorf("%s is not one of routd's thirteen entities", entity)
	}
	if len(key) != len(want) {
		return objstore.Key{}, fmt.Errorf("%s row carries %d coordinate fields and its "+
			"coordinate (%s) has %d", entity, len(key), strings.Join(want, ","), len(want))
	}
	switch entity {
	case Groups:
		return groupKey(key[0])
	case ACL:
		return aclKey(core.ACLRow{Principal: key[0], Action: key[1], Scope: key[2],
			Params: key[3], Predicate: key[4], Effect: key[5]})
	case ACLMembership:
		return membershipKey(key[0], key[1])
	case Routes:
		seq, err := strconv.Atoi(key[0])
		if err != nil {
			return objstore.Key{}, fmt.Errorf("route seq %q is not a number: %w", key[0], err)
		}
		return routeKey(core.Route{Seq: seq, Target: key[1], Match: key[2]})
	case RouteTokens:
		// The digest arrives already hex-encoded: the column is a BLOB and the
		// coordinate is text, so the reader encodes it where it reads it.
		if key[0] == "" {
			return objstore.Key{}, fmt.Errorf("route token row carries no token_hash, " +
				"which is its coordinate")
		}
		return objstore.Key{Partition: OwnerRoutd, Sort: key[0]}, nil
	case ScheduledTasks:
		return taskKey(key[0])
	case Secrets:
		return secretKey(SecretScope(key[0]), key[1], key[2])
	case PendingActions:
		return PendingActionKey(key[0])
	case NetworkRules:
		return networkRuleKey(key[0], key[1])
	case ProxydRoutes:
		return proxydRouteKey(key[0])
	case WebRoutes:
		return WebRouteKey(key[0])
	case InstalledPackages:
		return InstalledPackageKey(key[0], key[1])
	case GitHubSources:
		return githubSourceKey(key[0])
	}
	return objstore.Key{}, fmt.Errorf("%s has no coordinate builder", entity)
}

// sqliteDatetime is the layout `datetime('now')` writes: UTC, a space where
// RFC3339 has a T, and no zone at all. Several migrations and one writer used
// it, so it is on disk on every live instance.
const sqliteDatetime = "2006-01-02 15:04:05"

// instantLayouts is every shape these columns hold, MEASURED on all three live
// instances 2026-09-09 rather than assumed:
//
//	20 bytes ending Z   time.RFC3339           every instance, every column
//	19 bytes with a space  datetime('now')     acl.granted_at, acl_membership.added_at,
//	                                           groups.added_at, network_rules.created_at,
//	                                           web_routes.created_at
//	28-30 bytes ending Z  RFC3339Nano/core.TS  groups.added_at, route_tokens.created_at,
//	                                           acl.granted_at, web_routes.created_at
//
// FOUR WIDTHS IN ONE COLUMN is what a TEXT comparison over it was answering
// against, which is BUGS L97 read off live data rather than argued: 19 sorts
// before 20 sorts before 28, so `granted_at <` ordered by width first and by
// time second. Normalising is why this move exists at all.
var instantLayouts = []string{core.TSLayout, time.RFC3339Nano, time.RFC3339, sqliteDatetime}

// normaliseInstant re-renders one stored instant at core.TS's width.
//
// A value already at the target width passes through its own parse unchanged.
// Anything none of the layouts reads is a LOUD fault: an instant nobody can read
// is one this move would carry into a comparison that answers by byte order.
func normaliseInstant(entity, attr, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	for _, layout := range instantLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return core.TS(t), nil
		}
	}
	return "", fmt.Errorf("%s.%s is %q, which is not an instant this move can read; "+
		"a stored instant is compared as TEXT and a value of another shape answers by "+
		"byte order rather than by time", entity, attr, raw)
}

// routdWrite renders one legacy row as the item it becomes.
func routdWrite(entity string, r RoutdRow) (objstore.Write, error) {
	key, err := routdKey(entity, r.Key)
	if err != nil {
		return objstore.Write{}, err
	}
	attrs := make(map[string]string, len(r.Attrs))
	for name, v := range r.Attrs {
		if v == "" {
			continue // absent is the only null this model has
		}
		attrs[name] = v
	}
	for _, attr := range routdStamped[entity] {
		text, err := normaliseInstant(entity, attr, attrs[attr])
		if err != nil {
			return objstore.Write{}, err
		}
		if text == "" {
			delete(attrs, attr)
			continue
		}
		attrs[attr] = text
	}
	return objstore.Write{Op: objstore.Put, Entity: entity, Key: key, Attrs: attrs}, nil
}

// CheckRoutdSnapshot renders every row and reports what would be written,
// WITHOUT writing. It runs on a real run too, so a row the coordinate cannot
// address is named before the run that would stop half way through.
//
// A coordinate two rows share is a fault here rather than a silent overwrite:
// the last write would win and the row the operator still sees in the old table
// would be gone with no error to read.
func CheckRoutdSnapshot(snap RoutdSnapshot) (RoutdCounts, error) {
	counts := RoutdCounts{}
	for _, entity := range RoutdEntities {
		rows := snap[entity]
		seen := make(map[string]bool, len(rows))
		for _, r := range rows {
			w, err := routdWrite(entity, r)
			if err != nil {
				return nil, err
			}
			if seen[w.Key.Sort] {
				return nil, fmt.Errorf("%s carries two rows at %s=%q, so one would "+
					"overwrite the other", entity,
					strings.Join(routdKeyFields[entity], ","), w.Key.Sort)
			}
			seen[w.Key.Sort] = true
		}
		counts[entity] = len(rows)
	}
	for entity := range snap {
		if !slices.Contains(RoutdEntities, entity) {
			return nil, fmt.Errorf("the snapshot carries %q, which is not one of routd's "+
				"thirteen entities", entity)
		}
	}
	return counts, nil
}

// ImportRoutd writes the snapshot as items, and answers how many landed per
// entity.
//
// The write is an unconditional Put: this runs against a database whose old
// tables have just been set aside, so every coordinate is this row's own. It is
// chunked because a batch carries at most MaxBatchActions and one entity's rows
// can exceed it; each chunk commits on its own, so a run that dies leaves work a
// re-run finishes rather than a half-applied transaction.
//
// The route-id counter is set to the largest id carried over. Without it the
// next add_route would mint 1 and hand a live rule's handle to a second rule,
// after which delete_route(id) names both.
func (s *Store) ImportRoutd(ctx context.Context, snap RoutdSnapshot) (RoutdCounts, error) {
	counts, err := CheckRoutdSnapshot(snap)
	if err != nil {
		return nil, err
	}
	for _, entity := range RoutdEntities {
		writes := make([]objstore.Write, 0, len(snap[entity]))
		for _, r := range snap[entity] {
			w, err := routdWrite(entity, r)
			if err != nil {
				return nil, err
			}
			writes = append(writes, w)
		}
		if _, err := s.applyInChunks(ctx, "import "+entity, writes); err != nil {
			return nil, err
		}
	}
	if err := s.carryRouteIDs(ctx, snap[Routes]); err != nil {
		return nil, err
	}
	return counts, nil
}

// carryRouteIDs moves the route-id counter past every id the snapshot carries.
//
// The counter is seeded at zero when the entity is created (ensureCounters), and
// a migrated table's ids start at 1 — so without this the mint hands out a
// handle a live rule already holds. It is an Increment rather than a Put,
// because Increment is the one write this package has for a counter and it
// carries the compare-and-swap that makes two callers safe.
func (s *Store) carryRouteIDs(ctx context.Context, rows []RoutdRow) error {
	largest := 0
	for _, r := range rows {
		n, err := strconv.Atoi(r.Attrs["id"])
		if err != nil {
			continue
		}
		largest = max(largest, n)
	}
	if largest == 0 {
		return nil
	}
	key := objstore.Key{Partition: OwnerRoutd, Sort: CounterRouteID}
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Counters, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: key.Sort},
	})
	if err != nil {
		return fmt.Errorf("read the route id counter: %w", err)
	}
	if len(items) == 0 {
		return fmt.Errorf("the route id counter is missing; EnsureEntities seeds it, and " +
			"an Increment against a missing item refuses the whole batch it rides in")
	}
	at, err := strconv.Atoi(items[0].Attrs[counterAttr])
	if err != nil {
		return fmt.Errorf("the route id counter holds %q, which is not a number",
			items[0].Attrs[counterAttr])
	}
	if at >= largest {
		return nil
	}
	_, committed, err := s.incrementEntity(ctx, Counters, key, counterAttr, largest-at, nil, nil)
	if err != nil {
		return fmt.Errorf("carry the route id counter to %d: %w", largest, err)
	}
	if !committed {
		return fmt.Errorf("the route id counter refused its own increment")
	}
	return nil
}
