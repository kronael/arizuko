package store

import (
	"context"

	"github.com/kronael/arizuko/audit"
)

// LogCLIAudit records a mutating CLI operation as one audit_log row.
// args is the space-joined argument list; values are redacted by the
// caller before passing in. Spec 5/I + audit/PLAN.md.
//
// The legacy cli_audit table is not written to; existing rows remain
// for backfill. New callers should prefer audit.Emit / audit.EmitDB
// for richer Event fields (turn_id, source_ip, ...).
func (s *Store) LogCLIAudit(osUser, command, args string) error {
	_, err := audit.EmitDB(context.Background(), s.db, audit.Event{
		Category: audit.CategoryMutation,
		Action:   "cli." + command,
		Actor:    "cli:" + osUser,
		Surface:  audit.SurfaceCLI,
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"args": args,
		},
	})
	return err
}
