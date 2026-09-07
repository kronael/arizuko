package store

// The read every store test uses to check what a mutation recorded. It goes
// through audit's own Query, which is the method every daemon answers
// GET /v1/audit with, so a test asserts about what an operator can read back.

import (
	"testing"

	"github.com/kronael/arizuko/audit"
)

// auditRows reads back every recorded row, newest first.
func auditRows(t *testing.T, s *Store) []audit.Row {
	t.Helper()
	rows, err := audit.Query(t.Context(), s.Obj(), audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatalf("read the audit trail: %v", err)
	}
	return rows
}

// auditRowsFor reads back every recorded row carrying one action.
func auditRowsFor(t *testing.T, s *Store, action string) []audit.Row {
	t.Helper()
	var out []audit.Row
	for _, r := range auditRows(t, s) {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}
