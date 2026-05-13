package ipc

import (
	"strings"
	"sync"
	"testing"
)

// Spec 5/34: log_external_cost is the oracle-skill's hook for reporting
// non-Anthropic spend so the budget gate covers it. End-to-end:
// register the tool with a fake LogExternalCost recorder, call it via
// the MCP unix socket with realistic params, assert the row landed.
func TestLogExternalCost_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	var (
		mu       sync.Mutex
		captured []costCall
	)
	db := StoreFns{
		LogExternalCost: func(folder, provider, model string, in, out, cents int) error {
			mu.Lock()
			defer mu.Unlock()
			captured = append(captured, costCall{folder, provider, model, in, out, cents})
			return nil
		},
	}

	stop, err := ServeMCP(sock, GatedFns{}, db, "team", []string{"*"}, 0)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	_, errText := callTool(t, sock, "log_external_cost", map[string]any{
		"provider":      "openai",
		"model":         "gpt-5",
		"input_tokens":  1000,
		"output_tokens": 250,
		"cost_usd":      0.03,
	})
	if errText != "" {
		t.Fatalf("log_external_cost isError: %s", errText)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("captured rows = %d, want 1", len(captured))
	}
	row := captured[0]
	if row.folder != "team" || row.provider != "openai" || row.model != "gpt-5" {
		t.Errorf("identity mismatch: %+v", row)
	}
	if row.inputTok != 1000 || row.outputTok != 250 {
		t.Errorf("tokens: %+v", row)
	}
	if row.cents != 3 {
		t.Errorf("cents = %d, want 3 (0.03 USD × 100)", row.cents)
	}
}

func TestLogExternalCost_RejectsEmptyProvider(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"
	db := StoreFns{
		LogExternalCost: func(folder, provider, model string, in, out, cents int) error {
			return nil
		},
	}
	stop, err := ServeMCP(sock, GatedFns{}, db, "team", []string{"*"}, 0)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	_, errText := callTool(t, sock, "log_external_cost", map[string]any{
		"provider": "",
		"model":    "gpt-5",
		"cost_usd": 0.01,
	})
	if errText == "" || !strings.Contains(errText, "required") {
		t.Errorf("expected validation error, got %q", errText)
	}
}

// When db.LogExternalCost is nil the registration is skipped — gated
// deployments without the cost-caps wire-up don't expose the tool at
// all. Documented invariant; we leave a comment in the registration
// site rather than driving the unregistered-tool path through the MCP
// helper (which fatals on rpc-level errors).
//
// The other tests above prove behaviour when the function IS wired;
// the nil case is the opposite of registration, no exercise needed.

type costCall struct {
	folder, provider, model string
	inputTok, outputTok     int
	cents                   int
}
