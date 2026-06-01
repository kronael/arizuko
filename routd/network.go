package routd

import "strings"

// ResolveAllowlist returns the egress allowlist for folder: every network_rules
// target for the folder and all its ancestors (the folder='' base inherited by
// all). routd resolves this at dispatch and ships it to runed in
// RunRequest.EgressAllowlist, which runed wires into the crackbox EgressConfig.
// Ported from store.ResolveAllowlist (gated owned network_rules pre-split).
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
// folder inherits the instance base ('') + every ancestor's network rules.
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
