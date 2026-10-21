package tests

// Cross-language contract tests. arizuko is Go and ant is TypeScript, and every
// place a Go producer feeds a TS consumer (or the reverse) is a seam no compiler
// checks. Each seam below has already broken production once. The shape is the
// one tests/feat_continuity_test.go established: read the OTHER language's real
// source, never a copy that can go stale.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/kronael/arizuko/ipc"
)

// antSource reads one ant/src file, failing loudly rather than skipping — a
// contract test that skips when it cannot find the other side is a test that
// reports green on a deleted seam.
func antSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "ant", "src", name))
	if err != nil {
		t.Fatalf("read ant/src/%s: %v", name, err)
	}
	return string(b)
}

// antSubmitTurnFields returns the JSON keys ant declares in SubmitTurnPayload.
func antSubmitTurnFields(t *testing.T) []string {
	t.Helper()
	src := antSource(t, "mcp.ts")
	start := strings.Index(src, "export interface SubmitTurnPayload {")
	if start < 0 {
		t.Fatal("ant/src/mcp.ts no longer declares `export interface SubmitTurnPayload` — update this extractor to ant's current turn payload")
	}
	end := strings.Index(src[start:], "\n}")
	if end < 0 {
		t.Fatal("SubmitTurnPayload has no closing brace")
	}
	body := src[start : start+end]
	field := regexp.MustCompile(`(?m)^\s{2}([a-z_][a-z0-9_]*)\??:`)
	var out []string
	for _, m := range field.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatal("SubmitTurnPayload declares no fields — update this extractor")
	}
	sort.Strings(out)
	return out
}

// goTurnResultFields returns the JSON keys ipc.TurnResult accepts.
func goTurnResultFields(t *testing.T) []string {
	t.Helper()
	rt := reflect.TypeOf(ipc.TurnResult{})
	var out []string
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		out = append(out, strings.Split(tag, ",")[0])
	}
	sort.Strings(out)
	return out
}

// The turn payload is the one frame that carries an agent's answer out of the
// container. When the split dropped `result` on the Go side, every reply on
// every instance was silently empty for two days and every test stayed green —
// the frame arrived, so nothing looked broken. These two halves must name the
// same fields.
func TestSubmitTurnPayloadFieldsMatchAcrossLanguages(t *testing.T) {
	antFields := antSubmitTurnFields(t)
	goFields := goTurnResultFields(t)

	inGo := map[string]bool{}
	for _, f := range goFields {
		inGo[f] = true
	}
	for _, f := range antFields {
		if !inGo[f] {
			t.Errorf("ant sends %q in submit_turn and ipc.TurnResult has no json tag for it — "+
				"the value is discarded at the boundary with no error", f)
		}
	}
	inAnt := map[string]bool{}
	for _, f := range antFields {
		inAnt[f] = true
	}
	for _, f := range goFields {
		if !inAnt[f] {
			t.Errorf("ipc.TurnResult accepts %q and ant never sends it — either ant lost the "+
				"field or Go reads something nothing produces", f)
		}
	}
}

// Field names matching is not enough: a name can survive while the reader drops
// the value. Decode a payload with
// every field set and assert each one arrives non-zero.
func TestSubmitTurnDecodeKeepsEveryField(t *testing.T) {
	payload := map[string]any{
		"turn_id":    "t-1",
		"session_id": "11111111-2222-3333-4444-555555555555",
		"status":     "success",
		"error":      "",
		"timed_out":  true,
		"models": map[string]any{
			"opus": map[string]any{"input_tokens": 10, "output_tokens": 20},
		},
	}
	// `error` is empty on a success payload by construction; assert it on its own
	// below rather than weakening the success case.
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var got ipc.TurnResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("ipc.TurnResult cannot decode what ant sends: %v", err)
	}
	if got.TurnID == "" {
		t.Error("turn_id dropped in decode")
	}
	if got.SessionID == "" {
		t.Error("session_id dropped in decode — the next turn resumes blind")
	}
	if got.Status == "" {
		t.Error("status dropped in decode")
	}
	if !got.TimedOut {
		t.Error("timed_out dropped in decode — a timeout summary reads as a normal completion")
	}
	if len(got.Models) == 0 {
		t.Error("models dropped in decode — the turn costs nothing on the books")
	}

	var errCase ipc.TurnResult
	if err := json.Unmarshal([]byte(`{"turn_id":"t-2","status":"error","error":"boom"}`), &errCase); err != nil {
		t.Fatal(err)
	}
	if errCase.Error == "" {
		t.Error("error dropped in decode — a failed turn reports as silence")
	}
}
