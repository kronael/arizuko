package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/egress/match"
)

type NetworkRule struct {
	Folder    string
	Target    string
	CreatedAt time.Time
	CreatedBy string
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
func (s *Store) AddNetworkRule(folder, target, by string) error {
	if target != "*" {
		if err := match.ValidEntry(target); err != nil {
			return fmt.Errorf("network rule: %w", err)
		}
		target = match.NormalizeEntry(target)
	}
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		_, err := tx.Exec(
			`INSERT OR IGNORE INTO network_rules (folder, target, created_at, created_by)
			 VALUES (?, ?, ?, ?)`,
			folder, target, time.Now().UTC().Format(time.RFC3339), by,
		)
		actor := by
		if actor == "" {
			actor = "system"
		}
		return audit.Event{
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
		}, err
	})
}

// RemoveNetworkRule normalizes target the same way AddNetworkRule stores it,
// so a rule added as *.example.com is removable by the same syntax.
func (s *Store) RemoveNetworkRule(folder, target string) error {
	if target != "*" {
		target = match.NormalizeEntry(target)
	}
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		_, err := tx.Exec(
			`DELETE FROM network_rules WHERE folder = ? AND target = ?`,
			folder, target,
		)
		return audit.Event{
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
		}, err
	})
}

func (s *Store) ListNetworkRules(folder string) ([]NetworkRule, error) {
	rows, err := s.db.Query(
		`SELECT folder, target, created_at, created_by
		 FROM network_rules WHERE folder = ?
		 ORDER BY target`,
		folder,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNetworkRules(rows)
}

func (s *Store) AllNetworkRules() ([]NetworkRule, error) {
	rows, err := s.db.Query(
		`SELECT folder, target, created_at, created_by
		 FROM network_rules ORDER BY folder, target`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNetworkRules(rows)
}

func (s *Store) ResolveAllowlist(folder string) ([]string, error) {
	folders := folderAncestry(folder)
	placeholders := strings.Repeat("?,", len(folders))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(folders))
	for i, f := range folders {
		args[i] = f
	}
	rows, err := s.db.Query(
		`SELECT DISTINCT target FROM network_rules
		 WHERE folder IN (`+placeholders+`)
		 ORDER BY target`,
		args...,
	)
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

func scanNetworkRules(rows *sql.Rows) ([]NetworkRule, error) {
	var out []NetworkRule
	for rows.Next() {
		var r NetworkRule
		var ts string
		if err := rows.Scan(&r.Folder, &r.Target, &ts, &r.CreatedBy); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, r)
	}
	return out, rows.Err()
}
