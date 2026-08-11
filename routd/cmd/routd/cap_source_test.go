package main

import (
	"os"
	"strings"
	"testing"
)

// The concurrency ceiling has TWO readers: routd's queue paces the work and
// runed admits it. While routd's MaxRuns was unwired it held a hardcoded 5, so
// an operator who raised MAX_CONCURRENT_CONTAINERS moved runed's cap and not
// routd's (BUGS F71).
//
// This reads SOURCE, because a value-level test structurally cannot see the
// defect: with the env var unset both sides resolve to 5 and agree by accident.
// The same reasoning as resreg's TestResourceName_NoStringLiteral.
const capEnvVar = "MAX_CONCURRENT_CONTAINERS"

func TestConcurrencyCapReadsOneEnvVar(t *testing.T) {
	routdMain, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	want := `MaxRuns: intOr("` + capEnvVar + `"`
	if !strings.Contains(string(routdMain), want) {
		t.Fatalf("routd does not wire its queue cap to %s — an operator raising it "+
			"would move runed's ceiling and leave routd's hardcoded default", capEnvVar)
	}

	runedCfg, err := os.ReadFile("../../../core/config.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runedCfg), capEnvVar) {
		t.Fatalf("%s is not the name core.LoadConfig reads; the two caps have drifted apart", capEnvVar)
	}
}
