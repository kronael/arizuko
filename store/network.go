package store

// network.go holds every read and write of `network_rules`: one item per egress
// allowlist entry, addressed by (folder, target).
//
// The partition is OwnerRoutd because resreg's entity path writes these items
// too (resreg.SubsystemRoutd IS store.OwnerRoutd); a second spelling selects
// nothing, silently. The folder leads the coordinate: it is also the purge
// scope, and every one-folder read is a begins_with on `folder + KeySep`.
// `created_at` is core.TS at 30 bytes, the width resreg's entity path stamps
// (BUGS L97).

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/egress/match"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a rule item carries. `folder` and `target` are the
// coordinate.
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
// its folder "", a legal coordinate: the separator still divides the parts.
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

// networkRuleFrom reads one item back. A key that does not split into two is a
// LOUD fault: it names a row nobody wrote.
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

// AddNetworkRule validates target where the operator writes it: a rule that
// can never match (a URL, a host:port, an IP) is refused with the reason, not
// stored to read as protection (spec 6/25 1b). `*` is a folder's allow-all
// marker, the operator CLI's to write; the agent's network_allow rejects it.
// Targets are stored normalized (lowercase, `*.` folded onto its apex), so a
// rule added as *.example.com is removed as example.com. The write is
// create-if-absent: re-adding a rule must not restamp who added it or when.
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
		// A refusal is the host already being allowed, which is the state the
		// caller asked for.
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
// every ancestor, including the instance-wide "" folder. One read per ancestor
// rather than one of the whole entity; the union is de-duplicated and sorted
// here.
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

// AddNetworkRuleInto buffers one rule on the caller's write set, for routd's
// agent-facing `network_allow`: the rule rides the batch that carries
// resreg.invoke's single audit_log row. Every attribute is Init and it is a
// Merge rather than a guarded Put, so it has nothing to refuse: allowing a host
// twice is the state the caller asked for, and a refusal would take the audit
// row down with it. It validates and normalizes through the same match grammar
// as AddNetworkRule, so either writer stores the same string.
func (s *Store) AddNetworkRuleInto(ws *WriteSet, folder, target, by string) error {
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
	born := map[string]string{attrNetCreatedAt: core.NowTS()}
	setAttr(born, attrNetCreatedBy, by)
	ws.Add(objstore.Write{
		Op: objstore.Merge, Entity: NetworkRules, Key: key, Init: born,
	})
	return nil
}

// RemoveNetworkRuleInto buffers one rule's removal on the caller's write set. A
// rule that is not there is not an error: closing egress that is already
// closed is the outcome the caller asked for.
func (s *Store) RemoveNetworkRuleInto(ws *WriteSet, folder, target string) error {
	if target != "*" {
		target = match.NormalizeEntry(target)
	}
	key, err := networkRuleKey(folder, target)
	if err != nil {
		return err
	}
	ws.Add(objstore.Write{Op: objstore.Delete, Entity: NetworkRules, Key: key})
	return nil
}
