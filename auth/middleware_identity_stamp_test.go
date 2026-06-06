package auth

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// These tests pin the POST-FLIP behavior of stampES256Identity on the
// ES256-direct path (the cutover identity stamp — bugs.md FLIP-BLOCKER,
// resolved here). Post-flip a backend re-verifies the forwarded Bearer and
// stamps its own X-User-* headers; the JWT carries only the bare sub (prefixed
// with `user:` in the claim) and a narrow arz/folder claim, so the FULL grant
// set must come from the DB (the injected Grants resolver), not the token.

func groupsHeader(t *testing.T, sub Subject, grants Grants) []string {
	t.Helper()
	r := httptest.NewRequest("GET", "/x", nil)
	stampES256Identity(r, sub, grants)
	var gs []string
	if err := json.Unmarshal([]byte(r.Header.Get("X-User-Groups")), &gs); err != nil {
		t.Fatalf("X-User-Groups not valid JSON: %q (%v)", r.Header.Get("X-User-Groups"), err)
	}
	return gs
}

// (a) Operator: a `**` DB grant survives the stamp, regardless of the token's
// arz/folder claim — the resolver, not the claim, defines X-User-Groups.
func TestStampES256_Operator_FullGrantsFromDB(t *testing.T) {
	grants := func(sub string) []string { return []string{"**"} }
	gs := groupsHeader(t, Subject{Sub: "user:u_op", Extra: map[string]string{"arz/folder": "solo/inbox"}}, grants)
	if len(gs) != 1 || gs[0] != "**" {
		t.Fatalf("operator X-User-Groups = %v, want [**] from DB resolver", gs)
	}
	if !MatchGroups(gs, "corp/eng/sre") {
		t.Fatal("operator ** must pass requireFolder for any folder")
	}
}

// (d) A token whose arz/folder claim is a single narrow folder does NOT shrink
// the stamped grant set below the DB grants — all of X,Y,Z stay present.
func TestStampES256_MultiFolder_NotShrunkByNarrowClaim(t *testing.T) {
	want := []string{"corp/eng/x", "corp/eng/y", "corp/eng/z"}
	grants := func(sub string) []string { return want }
	gs := groupsHeader(t, Subject{Sub: "user:u_dev", Extra: map[string]string{"arz/folder": "corp/eng/x"}}, grants)
	if len(gs) != 3 {
		t.Fatalf("X-User-Groups = %v, want all three DB folders, not the narrow claim", gs)
	}
	for _, f := range want {
		if !MatchGroups(gs, f) {
			t.Fatalf("folder %q missing from stamped grants %v", f, gs)
		}
	}
}

// (c) The bare sub is stamped: the JWT `user:` prefix is stripped so onbod
// gate matching keyed on the provider sub (github:/google:) fires and DB
// lookups (also bare-keyed) agree.
func TestStampES256_StripsUserPrefix_BareSub(t *testing.T) {
	for in, want := range map[string]string{
		"user:github:abc":     "github:abc",
		"user:google:abc@x.y": "google:abc@x.y",
		"github:42":           "github:42", // already bare → unchanged
	} {
		var gotBare string
		grants := func(sub string) []string { gotBare = sub; return nil }
		r := httptest.NewRequest("GET", "/x", nil)
		stampES256Identity(r, Subject{Sub: in}, grants)
		if got := r.Header.Get("X-User-Sub"); got != want {
			t.Errorf("X-User-Sub = %q, want bare %q", got, want)
		}
		if gotBare != want {
			t.Errorf("resolver keyed on %q, want bare %q", gotBare, want)
		}
	}
}

// Generic / standalone soak (nil resolver): X-User-Groups falls back to the
// token's arz/folder claim as the lone entry; absent claim → empty list.
func TestStampES256_NilResolver_FallsBackToFolderClaim(t *testing.T) {
	gs := groupsHeader(t, Subject{Sub: "user:u_7", Extra: map[string]string{"arz/folder": "atlas/main"}}, nil)
	if len(gs) != 1 || gs[0] != "atlas/main" {
		t.Fatalf("nil-resolver fallback = %v, want [atlas/main]", gs)
	}
	if got := groupsHeader(t, Subject{Sub: "user:u_7"}, nil); len(got) != 0 {
		t.Fatalf("nil-resolver, no claim = %v, want empty", got)
	}
}
