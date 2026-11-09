package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestConcurrencyCapReadsOneEnvVar(t *testing.T) {
	for _, path := range []string{"main.go", "../../../runed/cmd/runed/main.go"} {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`MaxAgents:\s*cfg\.MaxAgents\b`).Match(source) {
			t.Fatalf("%s does not use the configured agent cap", path)
		}
	}

	runedCfg, err := os.ReadFile("../../../core/config.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runedCfg), `EnvInt("MAX_CONCURRENT_AGENTS", 5)`) {
		t.Fatal("core.LoadConfig does not read MAX_CONCURRENT_AGENTS")
	}
}
