package auth

import (
	"net/http/httptest"
	"testing"
)

// These tests pin the CURRENT behavior of stampES256Identity on the ES256-direct
// path (the cutover-soak header rewrite). They are documentation-of-intent, not
// assertions that the behavior is correct: bugs.md flags this function as a
// FLIP-BLOCKER (operator -> empty groups -> requireFolder lockout; prefixed sub
// passed through verbatim for onbod gate-matching). NO behavior change here — if
// the FLIP-BLOCKER is resolved, these expectations move with it.

// (a) Operator: an `arz/folder` claim of "**" is stamped as the SOLE
// X-User-Groups entry, verbatim. Whether "**" downstream means "operator: all
// folders" (correct) or is treated as a literal folder prefix by MatchGroups is
// the open intent question — here we only assert what the stamp emits.
func TestStampES256_OperatorFolderClaim(t *testing.T) {
	r := httptest.NewRequest("GET", "/x", nil)
	stampES256Identity(r, Subject{Sub: "u_op", Extra: map[string]string{"arz/folder": "**"}})

	if got := r.Header.Get("X-User-Sub"); got != "u_op" {
		t.Errorf("X-User-Sub = %q, want u_op", got)
	}
	// CURRENT: the single arz/folder claim becomes the lone groups entry.
	if got := r.Header.Get("X-User-Groups"); got != `["**"]` {
		t.Errorf("X-User-Groups = %q, want [\"**\"] (current stamp behavior)", got)
	}
}

// An operator with NO arz/folder claim gets an EMPTY groups list — the
// FLIP-BLOCKER lockout shape (requireFolder sees no grants). Asserted as
// current behavior, not endorsed.
func TestStampES256_NoFolderClaim_EmptyGroups(t *testing.T) {
	r := httptest.NewRequest("GET", "/x", nil)
	stampES256Identity(r, Subject{Sub: "u_op"})

	if got := r.Header.Get("X-User-Groups"); got != `[]` {
		t.Errorf("X-User-Groups = %q, want [] (current: empty when no arz/folder)", got)
	}
}

// (b) A prefixed sub (github:/google:) is stamped into X-User-Sub VERBATIM —
// the prefix is neither stripped nor rewritten to a `user:`-form. onbod gate
// matching and dashd per-user secrets read this header; the open question is
// whether the prefix should survive (gate matching) or be normalized.
func TestStampES256_PrefixedSub_PassthroughVerbatim(t *testing.T) {
	for _, sub := range []string{"github:42", "google:alice@x"} {
		r := httptest.NewRequest("GET", "/x", nil)
		stampES256Identity(r, Subject{Sub: sub, Extra: map[string]string{"arz/folder": "solo/inbox"}})
		if got := r.Header.Get("X-User-Sub"); got != sub {
			t.Errorf("X-User-Sub = %q, want %q (current: verbatim passthrough)", got, sub)
		}
		if got := r.Header.Get("X-User-Groups"); got != `["solo/inbox"]` {
			t.Errorf("X-User-Groups = %q, want [\"solo/inbox\"]", got)
		}
	}
}
