package routd

import (
	"strings"
)

// NetworkRule is one explicit egress allowlist row. JSON tags match the shape
// network_list's `own` array returns to the agent.
type NetworkRule struct {
	Folder    string `json:"folder"`
	Target    string `json:"target"`
	CreatedBy string `json:"created_by,omitempty"`
}

// ListNetworkRules returns the explicit rules for folder only (not the resolved
// ancestry — use ResolveAllowlist for the inherited set).
func (d *DB) ListNetworkRules(folder string) ([]NetworkRule, error) {
	rows, err := d.db.Query(
		`SELECT folder, target, created_by FROM network_rules WHERE folder = ? ORDER BY target`,
		folder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NetworkRule
	for rows.Next() {
		var r NetworkRule
		if err := rows.Scan(&r.Folder, &r.Target, &r.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ResolveAllowlist returns the egress allowlist for folder: every network_rules
// target for the folder and all its ancestors (the folder=” base inherited by
// all). routd resolves this at dispatch and ships it to runed in
// RunRequest.EgressAllowlist, which runed wires into the crackbox EgressConfig.
func (d *DB) ResolveAllowlist(folder string) ([]string, error) {
	folders := folderAncestry(folder)
	ph := strings.TrimSuffix(strings.Repeat("?,", len(folders)), ",")
	args := make([]any, len(folders))
	for i, f := range folders {
		args[i] = f
	}
	rows, err := d.db.Query(
		`SELECT DISTINCT target FROM network_rules WHERE folder IN (`+ph+`) ORDER BY target`,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// folderAncestry returns "", then each ancestor path down to folder, so a
// folder inherits the instance base (”) + every ancestor's network rules.
func folderAncestry(folder string) []string {
	out := []string{""}
	if folder == "" {
		return out
	}
	cur := ""
	for p := range strings.SplitSeq(folder, "/") {
		if cur == "" {
			cur = p
		} else {
			cur += "/" + p
		}
		out = append(out, cur)
	}
	return out
}
