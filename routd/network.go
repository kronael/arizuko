package routd

// network.go is routd's read face onto `network_rules`. The reads live in
// store/network.go: a second reader here would be a second spelling of a
// format store owns, and the two would disagree about what a folder inherits.

// NetworkRule is one explicit egress allowlist row. JSON tags match the shape
// network_list's `own` array returns to the agent.
type NetworkRule struct {
	Folder    string `json:"folder"`
	Target    string `json:"target"`
	CreatedBy string `json:"created_by,omitempty"`
}

// ListNetworkRules returns the explicit rules for folder only (not the resolved
// ancestry — use ResolveAllowlist for the inherited set), in target order.
func (d *DB) ListNetworkRules(folder string) ([]NetworkRule, error) {
	rules, err := d.store().ListNetworkRules(folder)
	if err != nil {
		return nil, err
	}
	out := make([]NetworkRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, NetworkRule{Folder: r.Folder, Target: r.Target, CreatedBy: r.CreatedBy})
	}
	return out, nil
}

// ResolveAllowlist returns the egress allowlist for folder: every target for
// the folder and all its ancestors (the folder="" base), de-duplicated and
// sorted. Shipped to runed in RunRequest.EgressAllowlist at dispatch.
func (d *DB) ResolveAllowlist(folder string) ([]string, error) {
	return d.store().ResolveAllowlist(folder)
}
