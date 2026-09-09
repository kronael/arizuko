package routd

// network.go is routd's read face onto `network_rules`. The rules are objstore
// items whose coordinate is (folder, target) and whose reads live in
// store/network.go, so both bodies below are one call each: a second reader here
// would be a second spelling of a format store owns, and the two would disagree
// about what a folder inherits.

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

// ResolveAllowlist returns the egress allowlist for folder: every network_rules
// target for the folder and all its ancestors (the folder="" base inherited by
// all), de-duplicated and sorted. routd resolves this at dispatch and ships it to
// runed in RunRequest.EgressAllowlist, which runed bakes into this spawn's own
// proxy.
func (d *DB) ResolveAllowlist(folder string) ([]string, error) {
	return d.store().ResolveAllowlist(folder)
}
