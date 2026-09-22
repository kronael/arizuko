package store

import (
	"context"

	"github.com/kronael/arizuko/audit"
)

// LogCLIAudit records a mutating CLI operation as one audit_log row (spec 5/I).
// args is the space-joined argument list, redacted by the caller.
func (s *Store) LogCLIAudit(osUser, command, args string) error {
	_, err := audit.EmitTo(context.Background(), s.Obj(), audit.Event{
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
