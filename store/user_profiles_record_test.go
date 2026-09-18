package store

import (
	"context"
	"testing"
)

// The admission path used to RENAME a profile nothing ever created, and the
// refusal was tolerated, so every person admitted stayed nameless on every
// operator surface (BUGS L269). RecordPerson creates it, and renames it when the
// person comes back.
func TestRecordPerson_CreatesThenRenames(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	const sub = "github:42"

	if err := s.RecordPerson(ctx, sub, "nikol", "Nikol"); err != nil {
		t.Fatalf("first admission: %v", err)
	}
	name, known, err := s.Username(sub)
	if err != nil {
		t.Fatal(err)
	}
	if !known || name != "nikol" {
		t.Fatalf("Username = (%q, %v), want (nikol, true) — the profile was never created", name, known)
	}

	// The same person comes back with a different name.
	if err := s.RecordPerson(ctx, sub, "nikol2", "Nikol"); err != nil {
		t.Fatalf("second admission: %v", err)
	}
	name, known, err = s.Username(sub)
	if err != nil {
		t.Fatal(err)
	}
	if !known || name != "nikol2" {
		t.Fatalf("Username = (%q, %v), want (nikol2, true)", name, known)
	}
}
