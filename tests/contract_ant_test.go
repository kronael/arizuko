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
	"github.com/kronael/arizuko/router"
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
// the value, which is exactly what happened to `result`. Decode a payload with
// every field set and assert each one arrives non-zero.
func TestSubmitTurnDecodeKeepsEveryField(t *testing.T) {
	payload := map[string]any{
		"turn_id":    "t-1",
		"session_id": "11111111-2222-3333-4444-555555555555",
		"status":     "success",
		"result":     "the answer the user waits for",
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
	if got.Result == "" {
		t.Error("result dropped in decode — the user gets an empty reply and nothing logs an error")
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

// antStatusRE compiles ant's own <status> pattern out of index.ts.
func antStatusRE(t *testing.T) *regexp.Regexp {
	t.Helper()
	src := antSource(t, "index.ts")
	m := regexp.MustCompile(`(?m)^const STATUS_RE = /(.+)/(\w*);`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("ant/src/index.ts no longer declares `const STATUS_RE = /…/;` — update this extractor to ant's current status grammar")
	}
	pattern := strings.ReplaceAll(m[1], `[\s\S]`, `(?s).`)
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("ant's STATUS_RE %q does not compile as a Go regexp: %v", pattern, err)
	}
	return re
}

// <status> is the one grammar parsed in BOTH languages: ant forwards each block
// mid-turn so the user sees progress, and routd strips the same blocks out of
// the final reply so they are not delivered twice. Two parsers of one format
// drift silently — the user then sees a progress notice repeated as the answer,
// or loses it entirely. Both must agree on every input.
func TestStatusGrammarAgreesAcrossLanguages(t *testing.T) {
	antRE := antStatusRE(t)
	for _, tc := range []struct {
		name, in string
		want     []string
	}{
		{"one block", "before <status>working</status> after", []string{"working"}},
		{"two blocks", "<status>a</status>mid<status>b</status>", []string{"a", "b"}},
		{"multiline", "<status>line one\nline two</status>", []string{"line one\nline two"}},
		{"none", "plain prose with no markup", nil},
		{"unclosed", "<status>never closed", nil},
		{"empty", "<status></status>", nil},
		{"whitespace only", "<status>   </status>", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, goGot := router.ExtractStatusBlocks(tc.in)

			var antGot []string
			for _, m := range antRE.FindAllStringSubmatch(tc.in, -1) {
				if s := strings.TrimSpace(m[1]); s != "" {
					antGot = append(antGot, s)
				}
			}

			if !reflect.DeepEqual(goGot, antGot) {
				t.Errorf("the two <status> parsers disagree on %q:\n  go  (router.ExtractStatusBlocks) = %q\n  ant (index.ts STATUS_RE)       = %q",
					tc.in, goGot, antGot)
			}
			if !reflect.DeepEqual(goGot, tc.want) {
				t.Errorf("ExtractStatusBlocks(%q) = %q, want %q", tc.in, goGot, tc.want)
			}
		})
	}
}
