package store

// network.go holds every read and write of `network_rules`: one item per egress
// allowlist entry, addressed by (folder, target).
//
// THE PARTITION IS OwnerRoutd AND IT IS NOT A CHOICE THIS FILE MAKES.
// network_rules is a registered resreg resource, and an entity-backed
// resource's items live at its owner subsystem (resreg.SubsystemRoutd, which IS
// store.OwnerRoutd). A second spelling would put this file's items in one
// partition and the engine's read in another, and the failure is silent: a
// query on a partition nobody wrote to is a legal query that selects nothing.
//
// THE FOLDER LEADS THE COORDINATE, and that is what makes a subtree readable.
// `folder` is also the resource's purge scope, and a key predicate reaches a
// LEADING fragment of the key and nothing else — so every read below that
// selects one folder is a begins_with on `folder + KeySep`, exact because no
// value may carry the separator. Choosing `target` first would make each of
// them a whole-partition scan with a filter.
//
// THE STORED INSTANT IS core.TS, at 30 bytes. That is the width resreg's own
// entity path stamps a StampedField at (resreg/entity.go itemFor), and the two
// writers of these items have to agree byte for byte — an apply that stamped 30
// where this file wrote 20 would put two widths in one attribute, and a
// comparison over TEXT answers by byte order (BUGS L97). `arizuko migrate-routd`
// normalises the RFC3339 values the column path left behind.

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/egress/match"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a rule item carries. `folder` and `target` are NOT among them:
// they are the coordinate, and storing it twice is two facts that can disagree.
const (
	attrNetCreatedAt = "created_at"
	attrNetCreatedBy = "created_by"
)

type NetworkRule struct {
	Folder    string
	Target    string
	CreatedAt time.Time
	CreatedBy string
}

// networkRuleKey is the coordinate of one rule. The instance-wide rule spells
// its folder "" — the sentinel migration 0037 seeded — and that is a legal
// coordinate: the separator still divides the two parts, so the read splits it
// back the same way.
func networkRuleKey(folder, target string) (objstore.Key, error) {
	if target == "" {
		return objstore.Key{}, fmt.Errorf("network rule for %q carries no target, "+
			"which is half its coordinate", folder)
	}
	for name, v := range map[string]string{"folder": folder, "target": target} {
		if strings.Contains(v, KeySep) {
			return objstore.Key{}, fmt.Errorf("network rule %s=%q carries the separator "+
				"that divides the two fields of its coordinate", name, v)
		}
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: folder + KeySep + target}, nil
}

// networkRuleFrom reads one item back. A key that does not split into two parts
// is a LOUD fault rather than a half-filled rule: it names a row nobody wrote.
func networkRuleFrom(it objstore.Item) (NetworkRule, error) {
	folder, target, ok := strings.Cut(it.Key.Sort, KeySep)
	if !ok {
		return NetworkRule{}, fmt.Errorf("network rule %q does not split into "+
			"(folder, target)", it.Key.Sort)
	}
	r := NetworkRule{Folder: folder, Target: target, CreatedBy: it.Attrs[attrNetCreatedBy]}
	if ts := it.Attrs[attrNetCreatedAt]; ts != "" {
		r.CreatedAt, _ = time.Parse(core.TSLayout, ts)
	}
	return r, nil
}

// networkRulesIn reads the rules of ONE folder, in target order.
func (s *Store) networkRulesIn(ctx context.Context, folder string) ([]NetworkRule, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: NetworkRules, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.PrefixKey, Value: folder + KeySep},
	})
	if err != nil {
		return nil, fmt.Errorf("read network rules of %q: %w", folder, err)
	}
	return collectNetworkRules(items)
}

func collectNetworkRules(items []objstore.Item) ([]NetworkRule, error) {
	out := make([]NetworkRule, 0, len(items))
	for _, it := range items {
		r, err := networkRuleFrom(it)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// AddNetworkRule validates target where the operator writes it — a rule that
// can never match (a URL, a host:port, an IP) is refused with the reason, not
// stored to read as protection (spec 6/25 1b). `*` is the one non-host row: a
// folder's allow-all marker, which the spawn path translates into the egress
// binary's explicit --allow-all mode. Writing it opens all egress, so it is
// the operator CLI's to write; the agent's network_allow rejects it before
// reaching the store. Targets are stored normalized (lowercase, `*.` glob
// folded onto its apex), so a rule added as *.example.com is removed as
// example.com.
//
// The write is create-if-absent, which is what `INSERT OR IGNORE` said: adding
// a rule that is already there must not restamp who added it or when.
func (s *Store) AddNetworkRule(folder, target, by string) error {
	if target != "*" {
		if err := match.ValidEntry(target); err != nil {
			return fmt.Errorf("network rule: %w", err)
		}
		target = match.NormalizeEntry(target)
	}
	key, err := networkRuleKey(folder, target)
	if err != nil {
		return err
	}
	attrs := map[string]string{attrNetCreatedAt: core.NowTS()}
	setAttr(attrs, attrNetCreatedBy, by)
	_, err = s.tryWriteAudited(func() ([]objstore.Write, audit.Event, error) {
		// A refusal is the host already being allowed, which is the state the caller
		// asked for, and keeps the instant and the author of the first grant.
		w := objstore.Write{
			Op: objstore.Put, Entity: NetworkRules, Key: key, Attrs: attrs,
			Cond: objstore.Cond{Item: objstore.ItemAbsent},
		}
		actor := by
		if actor == "" {
			actor = "system"
		}
		return []objstore.Write{w}, audit.Event{
			Category: audit.CategoryNetwork,
			Action:   "egress.allow.add",
			Actor:    actor,
			ActorSub: by,
			Surface:  audit.SurfaceGateway,
			Resource: "network_rules/" + folder + "/" + target,
			Folder:   folder,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"target": target,
			},
		}, nil
	})
	return err
}

// RemoveNetworkRule normalizes target the same way AddNetworkRule stores it,
// so a rule added as *.example.com is removable by the same syntax.
func (s *Store) RemoveNetworkRule(folder, target string) error {
	if target != "*" {
		target = match.NormalizeEntry(target)
	}
	key, err := networkRuleKey(folder, target)
	if err != nil {
		return err
	}
	return s.writeAudited(func() ([]objstore.Write, audit.Event, error) {
		w := objstore.Write{Op: objstore.Delete, Entity: NetworkRules, Key: key}
		return []objstore.Write{w}, audit.Event{
			Category: audit.CategoryNetwork,
			Action:   "egress.allow.remove",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "network_rules/" + folder + "/" + target,
			Folder:   folder,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"target": target,
			},
		}, nil
	})
}

func (s *Store) ListNetworkRules(folder string) ([]NetworkRule, error) {
	return s.networkRulesIn(context.Background(), folder)
}

func (s *Store) AllNetworkRules() ([]NetworkRule, error) {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: NetworkRules, Partition: OwnerRoutd,
	})
	if err != nil {
		return nil, fmt.Errorf("read network rules: %w", err)
	}
	return collectNetworkRules(items)
}

// ResolveAllowlist is every host folder may reach: its own rules and those of
// every ancestor, including the instance-wide "" folder.
//
// One read per ancestor rather than one read of the whole entity: a folder is
// at most a handful deep, each read is an exact begins_with on the coordinate,
// and a whole-partition read would examine every folder's rules to answer about
// one. The union is de-duplicated and sorted here, which is what `SELECT
// DISTINCT … ORDER BY target` did.
func (s *Store) ResolveAllowlist(folder string) ([]string, error) {
	ctx := context.Background()
	seen := map[string]bool{}
	for _, f := range folderAncestry(folder) {
		rules, err := s.networkRulesIn(ctx, f)
		if err != nil {
			return nil, err
		}
		for _, r := range rules {
			seen[r.Target] = true
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	slices.Sort(out)
	return out, nil
}

func folderAncestry(folder string) []string {
	out := []string{""}
	if folder == "" {
		return out
	}
	parts := strings.Split(folder, "/")
	cur := ""
	for _, p := range parts {
		if cur == "" {
			cur = p
		} else {
			cur = cur + "/" + p
		}
		out = append(out, cur)
	}
	return out
}

// AddNetworkRuleTx appends one rule create-if-absent on a transaction the CALLER
// owns, for routd's agent-facing `network_allow`: the rule and resreg.invoke's
// single audit_log row commit together, and AddNetworkRule would open a second
// transaction on the same database and record the act twice.
//
// It validates and normalizes through the same match grammar AddNetworkRule
// does, so a rule reaching the table through either writer is the same string:
// one added as *.example.com is removable as example.com whichever surface wrote
// it.
func (s *Store) AddNetworkRuleTx(ctx context.Context, tx *sql.Tx, folder, target, by string) error {
	if target != "*" {
		if err := match.ValidEntry(target); err != nil {
			return fmt.Errorf("network rule: %w", err)
		}
		target = match.NormalizeEntry(target)
	}
	key, err := networkRuleKey(folder, target)
	if err != nil {
		return err
	}
	attrs := map[string]string{attrNetCreatedAt: core.NowTS()}
	setAttr(attrs, attrNetCreatedBy, by)
	// A refusal is the host already being allowed, which is the state the caller
	// asked for, and keeps the instant and the author of the first grant.
	_, err = s.TryWriteEntitiesInTx(ctx, tx, nil, objstore.Write{
		Op: objstore.Put, Entity: NetworkRules, Key: key, Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
	return err
}

// RemoveNetworkRuleTx drops one rule on a transaction the caller owns. A rule
// that is not there is not an error — closing egress that is already closed is
// the outcome the caller asked for.
func (s *Store) RemoveNetworkRuleTx(ctx context.Context, tx *sql.Tx, folder, target string) error {
	if target != "*" {
		target = match.NormalizeEntry(target)
	}
	key, err := networkRuleKey(folder, target)
	if err != nil {
		return err
	}
	err = s.WriteEntitiesInTx(ctx, tx, nil, objstore.Write{
		Op: objstore.Delete, Entity: NetworkRules, Key: key,
	})
	return err
}
