package auth

import "testing"

// ValidateParams guards the gap between what paramsMatch READS and what the
// writer STORES. paramsMatch trims and skips leniently, so several specs mean
// something at read time that the stored bytes do not say — and the stored
// bytes are also the revoke key (routd revokeACLTx matches params exactly).
func TestValidateParams(t *testing.T) {
	for _, tc := range []struct {
		spec string
		ok   bool
		why  string
	}{
		{"", true, "the un-narrowed row"},
		{"repo=mine/*", true, "one clause"},
		{"repo=mine,branch=main", true, "two clauses"},
		{"repo", true, "a bare name means the argument must be present"},
		{"repo=a=b", true, "Cut takes the first =, so a value may carry one"},
		{"repo = mine", false, "read as repo=mine but stored with spaces, so the revoke key differs"},
		{" repo=mine", false, "leading space, same reason"},
		{"repo=a,repo=b", false, "clauses are ANDed, so this row can never match"},
		{"=mine", false, "no parameter name"},
		{"repo=a,,b=c", false, "an empty clause is silently skipped at read time"},
	} {
		err := ValidateParams(tc.spec)
		if tc.ok && err != nil {
			t.Errorf("ValidateParams(%q) = %v, want ok (%s)", tc.spec, err, tc.why)
		}
		if !tc.ok && err == nil {
			t.Errorf("ValidateParams(%q) = ok, want refused (%s)", tc.spec, tc.why)
		}
	}
}

// Every spec ValidateParams accepts must still be readable by the evaluator it
// is written for — the two are one grammar, and a validator that accepted a
// spec paramsMatch cannot match would be its own kind of silent row.
func TestValidateParams_AcceptedSpecsStillMatch(t *testing.T) {
	args := map[string]string{"repo": "mine/x", "branch": "main"}
	for _, spec := range []string{"", "repo=mine/*", "repo=mine/x,branch=main", "branch"} {
		if err := ValidateParams(spec); err != nil {
			t.Fatalf("precondition: ValidateParams(%q) = %v", spec, err)
		}
		if !paramsMatch(spec, args) {
			t.Errorf("paramsMatch(%q) = false; an accepted spec must be readable", spec)
		}
	}
}
