package store

import (
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
)

// BUGS A4, the sharp half: individual scan failures used to be SKIPPED
// (`if err == nil { out = append(...) }`), so a row that would not scan simply
// vanished from the policy. That is not a degradation, it is an ESCALATION —
// drop a DENY row and the allow rows above it survive, so the caller is granted
// something an operator explicitly forbade.
//
// Forced by widening a column's stored type: `grant_option` scans into a bool,
// so a text value there fails that row's scan and nothing else.
func TestACLReadRefusesToReturnAPartialPolicy(t *testing.T) {
	s := openMem(t)
	rows := []core.ACLRow{
		{Principal: "google:alice", Action: "*", Scope: "atlas/**", Effect: "allow"},
		{Principal: "google:alice", Action: "mcp:send", Scope: "atlas/secret", Effect: "deny"},
	}
	for _, r := range rows {
		if err := s.AddACLRow(r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ACLRowsFor([]string{"google:alice"})
	if err != nil || len(got) != 2 {
		t.Fatalf("fixture: rows=%d err=%v, want 2 rows", len(got), err)
	}

	// Corrupt ONLY the deny row's grant_option so its scan fails.
	if _, err := s.db.Exec(
		`UPDATE acl SET grant_option = 'not-a-bool' WHERE effect = 'deny'`); err != nil {
		t.Fatal(err)
	}

	got, err = s.ACLRowsFor([]string{"google:alice"})
	if err == nil {
		t.Fatalf("a row that would not scan was silently dropped: got %d rows, no error", len(got))
	}
	if len(got) != 0 {
		t.Errorf("partial policy returned alongside the error: %+v", got)
	}
	if !strings.Contains(err.Error(), "ACLRowsFor") {
		t.Errorf("error should name the failing reader, got: %v", err)
	}
}
