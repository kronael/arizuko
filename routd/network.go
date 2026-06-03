package routd

import (
	"strings"
	"time"
)

// NetworkRule is one explicit egress allowlist row (routd-local; mirrors
// store.NetworkRule).
type NetworkRule struct {
	Folder    string
	Target    string
	CreatedBy string
}

// AddNetworkRule appends one egress allowlist target for folder (idempotent).
// folder="" is the instance-wide base. Mirrors store.AddNetworkRule.
func (d *DB) AddNetworkRule(folder, target, by string) error {
	_, err := d.db.Exec(
		`INSERT OR IGNORE INTO network_rules (folder, target, created_at, created_by)
		 VALUES (?, ?, ?, ?)`,
		folder, target, time.Now().UTC().Format(time.RFC3339), by)
	return err
}

// RemoveNetworkRule drops one egress allowlist target for folder. No error if absent.
func (d *DB) RemoveNetworkRule(folder, target string) error {
	_, err := d.db.Exec(`DELETE FROM network_rules WHERE folder = ? AND target = ?`, folder, target)
	return err
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
	for _, p := range strings.Split(folder, "/") {
		if cur == "" {
			cur = p
		} else {
			cur += "/" + p
		}
		out = append(out, cur)
	}
	return out
}
