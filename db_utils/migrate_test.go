package db_utils

import (
	"strings"
	"testing"
)

// A duplicate version number is the failure that motivated checkVersions:
// Migrate applies the first file of a repeated number, records that version,
// then skips the second because its version is no longer greater. Nothing
// errors, and the second file's DDL never runs (BUGS L4).
func TestCheckVersionsRejectsADuplicateNumber(t *testing.T) {
	err := checkVersions([]string{"0001-a.sql", "0002-b.sql", "0002-c.sql"})
	if err == nil {
		t.Fatal("want an error for two files numbered 0002, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "duplicates version 2") || !strings.Contains(got, "0002-c.sql") {
		t.Fatalf("error must name the duplicate and its number, got %q", got)
	}
}

func TestCheckVersionsRejectsAGap(t *testing.T) {
	err := checkVersions([]string{"0001-a.sql", "0003-c.sql"})
	if err == nil {
		t.Fatal("want an error for the missing 0002, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "must be 0002") {
		t.Fatalf("error must name the expected version, got %q", got)
	}
}

func TestCheckVersionsAcceptsADenseSet(t *testing.T) {
	if err := checkVersions([]string{"0001-a.sql", "0002-b.sql", "0003-c.sql"}); err != nil {
		t.Fatalf("dense 1..3 must pass, got %v", err)
	}
}

func TestCheckVersionsRejectsANonNumericPrefix(t *testing.T) {
	if err := checkVersions([]string{"init-a.sql"}); err == nil {
		t.Fatal("want an error for a non-numeric prefix, got nil")
	}
}
