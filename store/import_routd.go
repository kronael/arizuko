package store

// import_routd.go is the target half of the move that carries routd's thirteen
// tables onto entities (spec 6/37 step 7). NOTHING REACHES IT NOW: neither
// migration tool is a subcommand any more and no production caller remains
// (BUGS L348).
//
// A row is its coordinate and an attribute map, NOT a Go struct: six of the
// thirteen carry columns no struct in the tree names (the sealed secret and its
// four OAuth fields, a GitHub source's webhook secret, a token's `kind`), and a
// struct-shaped migration drops exactly those, silently (BUGS L265).
//
// It decides the COORDINATE, through the same builder the daemon's own writer
// calls, and the WIDTH of every stamped instant: the column path wrote
// time.RFC3339 at 20 bytes and the object path writes core.TS at 30, and a
// stored instant is compared as TEXT (BUGS L97).

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
// order, and every other column as the attribute it becomes. An empty
// attribute VALUE is dropped, because absent is the only null this model has.
// A coordinate field may be empty (network_rules spells an instance-wide rule
// folder=""); the whole coordinate may not.
type RoutdRow struct {
	Key   []string
	Attrs map[string]string
}

// RoutdSnapshot is every legacy row, keyed by the entity it lands in.
type RoutdSnapshot map[string][]RoutdRow

// RoutdEntities is the order the thirteen are read, reported and written in.
// Nothing depends on it: an entity has no foreign key to satisfy.
var RoutdEntities = []string{
	Groups, ACL, ACLMembership, Routes, RouteTokens, ScheduledTasks,
	Secrets, PendingActions, NetworkRules, ProxydRoutes, WebRoutes,
	InstalledPackages, GitHubSources,
}

// routdKeyFields names each entity's coordinate, in order, so the source half
// can build its SELECT and this half can say which column was short. It is the
// SAME order the key builders join in, and a test pins the two together.
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

// routdStamped names, per entity, the attributes this move normalises to
// core.TS: exactly the resources' StampedFields. An instant the resource does
// NOT stamp keeps its own writer's layout and is not here:
// scheduled_tasks.next_run is RFC3339 in the operator's timezone and is
// compared as a parsed instant, never as bytes.
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
// put it.
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
		// coordinate is text.
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
// RFC3339 has a T, and no zone. It is on disk on every live instance.
const sqliteDatetime = "2006-01-02 15:04:05"

// instantLayouts is every shape these columns hold, MEASURED on all three live
// instances 2026-09-09: 20-byte RFC3339 in every column; 19-byte
// datetime('now') in acl, acl_membership, groups, network_rules and
// web_routes; 28-30 byte RFC3339Nano/core.TS in groups, route_tokens, acl and
// web_routes. Four widths in one column: 19 sorts before 20 before 28, so
// `granted_at <` ordered by width first (BUGS L97).
var instantLayouts = []string{core.TSLayout, time.RFC3339Nano, time.RFC3339, sqliteDatetime}

// normaliseInstant re-renders one stored instant at core.TS's width. Anything
// none of the layouts reads is a LOUD fault: an instant nobody can read is one
// this move would carry into a comparison that answers by byte order.
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
// WITHOUT writing, so a row the coordinate cannot address is named before the
// run that would stop half way through. A coordinate two rows share is a
// fault, never a silent overwrite.
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

// ImportRoutd writes the snapshot as items and answers how many landed per
// entity. The write is an unconditional Put against a database whose old
// tables have just been set aside; each chunk commits on its own, so a run
// that dies leaves work a re-run finishes. The route-id counter is set to the
// largest id carried over, or the next add_route would hand a live rule's
// handle to a second rule.
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
// An Increment rather than a Put, because Increment is the one counter write
// this package has and it carries the compare-and-swap.
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
