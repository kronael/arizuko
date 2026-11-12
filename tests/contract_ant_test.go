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
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

func TestSubmitStatusPayloadFieldsMatchAcrossLanguages(t *testing.T) {
	src := antSource(t, "mcp.ts")
	start := strings.Index(src, "export interface SubmitStatusPayload {")
	if start < 0 {
		t.Fatal("missing SubmitStatusPayload")
	}
	end := strings.Index(src[start:], "\n}")
	if end < 0 {
		t.Fatal("missing SubmitStatusPayload end")
	}
	field := regexp.MustCompile(`(?m)^\s{2}([a-z_][a-z0-9_]*)\??:`)
	var ant []string
	for _, match := range field.FindAllStringSubmatch(src[start:start+end], -1) {
		ant = append(ant, match[1])
	}
	var goFields []string
	typeOf := reflect.TypeOf(ipc.TurnStatus{})
	for i := 0; i < typeOf.NumField(); i++ {
		goFields = append(goFields, strings.Split(typeOf.Field(i).Tag.Get("json"), ",")[0])
	}
	sort.Strings(ant)
	sort.Strings(goFields)
	if !reflect.DeepEqual(ant, goFields) {
		t.Fatalf("ant fields %v; Go fields %v", ant, goFields)
	}
}

func TestTurnStatusAckFieldsMatchAcrossLanguages(t *testing.T) {
	src := antSource(t, "mcp.ts")
	start := strings.Index(src, "export interface TurnStatusAck {")
	if start < 0 {
		t.Fatal("missing TurnStatusAck")
	}
	end := strings.Index(src[start:], "\n}")
	if end < 0 {
		t.Fatal("missing TurnStatusAck end")
	}
	field := regexp.MustCompile(`(?m)^\s{2}([a-z_][a-z0-9_]*)\??:`)
	var ant []string
	for _, match := range field.FindAllStringSubmatch(src[start:start+end], -1) {
		ant = append(ant, match[1])
	}
	var goFields []string
	typeOf := reflect.TypeOf(ipc.TurnStatusAck{})
	for i := 0; i < typeOf.NumField(); i++ {
		goFields = append(goFields, strings.Split(typeOf.Field(i).Tag.Get("json"), ",")[0])
	}
	sort.Strings(ant)
	sort.Strings(goFields)
	if !reflect.DeepEqual(ant, goFields) {
		t.Fatalf("ant fields %v; Go fields %v", ant, goFields)
	}
}

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
		"turn_id":          "t-1",
		"session_id":       "11111111-2222-3333-4444-555555555555",
		"status":           "success",
		"error":            "",
		"timed_out":        true,
		"provider_failure": map[string]any{"reason": "rate_limit", "reset_at": "2026-11-08T13:00:00Z"},
		"harness":          "codex",
		"models": map[string]any{
			"gpt-5.5": map[string]any{"input": 10, "output": 20, "cache_read": 30, "cache_write": 40,
				"cost_cents": 50, "credits_spent": 1.5, "credits_balance": 2.5,
				"window_used_percent": 99.0, "plan_type": "pro"},
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
	if got.ProviderFailure == nil || got.ProviderFailure.Reason != "rate_limit" ||
		got.ProviderFailure.ResetAt != "2026-11-08T13:00:00Z" {
		t.Fatal("provider_failure dropped in decode")
	}
	if got.Harness != "codex" {
		t.Error("harness dropped in decode — the usage record cannot say who ran the turn")
	}
	u := got.Models["gpt-5.5"]
	if u.Input != 10 || u.Output != 20 || u.CacheRead != 30 || u.CacheWrite != 40 ||
		u.CostCents != 50 || u.CreditsSpent == nil || *u.CreditsSpent != 1.5 ||
		u.CreditsBalance == nil || *u.CreditsBalance != 2.5 ||
		u.WindowUsedPercent == nil || *u.WindowUsedPercent != 99 || u.PlanType != "pro" {
		t.Errorf("models decoded as %+v — a usage field ant sends is lost on the books", u)
	}

	var errCase ipc.TurnResult
	if err := json.Unmarshal([]byte(`{"turn_id":"t-2","status":"error","error":"boom"}`), &errCase); err != nil {
		t.Fatal(err)
	}
	if errCase.Error == "" {
		t.Error("error dropped in decode — a failed turn reports as silence")
	}
}

func TestProviderFailureFieldsMatchAcrossLanguages(t *testing.T) {
	src := antSource(t, "backend.ts")
	start := strings.Index(src, "export interface ProviderFailure {")
	if start < 0 {
		t.Fatal("missing ProviderFailure declaration")
	}
	end := strings.Index(src[start:], "\n}")
	if end < 0 {
		t.Fatal("missing ProviderFailure end")
	}
	fields := regexp.MustCompile(`(?m)^\s{2}([a-z_]+)(\??):`)
	ant := map[string]bool{}
	for _, match := range fields.FindAllStringSubmatch(src[start:start+end], -1) {
		ant[match[1]] = match[2] == "?"
	}
	got := map[string]bool{}
	typ := reflect.TypeOf(apiv1.ProviderFailure{})
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("json")
		got[strings.Split(tag, ",")[0]] = strings.Contains(tag, "omitempty")
	}
	if !reflect.DeepEqual(ant, got) {
		t.Fatalf("ProviderFailure declarations disagree: TS %v Go %v", ant, got)
	}
}

// ModelUsage is the per-turn usage record ant renders and routd stores
// (specs/6/39-gateway-owns-spend.md §"The turn usage record"). A field one side
// renames is dropped at the boundary with no error, and the turn reads as free.
func TestModelUsageFieldsMatchAcrossLanguages(t *testing.T) {
	src := antSource(t, "mcp.ts")
	start := strings.Index(src, "export interface ModelUsage {")
	if start < 0 {
		t.Fatal("missing ModelUsage declaration")
	}
	end := strings.Index(src[start:], "\n}")
	if end < 0 {
		t.Fatal("missing ModelUsage end")
	}
	fields := regexp.MustCompile(`(?m)^\s{2}([a-z_]+)(\??):`)
	ant := map[string]bool{}
	for _, match := range fields.FindAllStringSubmatch(src[start:start+end], -1) {
		ant[match[1]] = match[2] == "?"
	}
	got := map[string]bool{}
	typ := reflect.TypeOf(apiv1.ModelUsage{})
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("json")
		got[strings.Split(tag, ",")[0]] = strings.Contains(tag, "omitempty")
	}
	if !reflect.DeepEqual(ant, got) {
		t.Fatalf("ModelUsage declarations disagree: TS %v Go %v", ant, got)
	}
}
