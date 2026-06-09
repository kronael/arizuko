package ipc

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// --- numArg helper ---

func TestNumArg(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		key  string
		want int
		ok   bool
	}{
		{"present float64", map[string]any{"n": float64(42)}, "n", 42, true},
		{"missing key", map[string]any{}, "n", 0, false},
		{"nil value", map[string]any{"n": nil}, "n", 0, false},
		{"wrong type string", map[string]any{"n": "42"}, "n", 0, false},
		{"zero value", map[string]any{"n": float64(0)}, "n", 0, true},
		{"negative", map[string]any{"n": float64(-5)}, "n", -5, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := numArg(c.args, c.key)
			if ok != c.ok {
				t.Errorf("ok = %v, want %v", ok, c.ok)
			}
			if got != c.want {
				t.Errorf("got = %d, want %d", got, c.want)
			}
		})
	}
}

// --- validHostname helper ---

func TestValidHostname(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"example.com", true},
		{"localhost", true},
		{"my-host.example.org", true},
		{"host:8080", true},
		{"", false},
		{strings.Repeat("a", 254), false},
		{"has space", false},
		{"has/slash", false},
		{"has@at", false},
	}
	for _, c := range cases {
		got := validHostname(c.in)
		if got != c.want {
			t.Errorf("validHostname(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// --- renderEnv / expandSecret / scrubResult ---

func TestRenderEnv(t *testing.T) {
	tmpl := map[string]string{
		"API_KEY": "{secret:MY_KEY}",
		"STATIC":  "no-substitution",
	}
	secrets := map[string]string{"MY_KEY": "secret123"}
	out := renderEnv(tmpl, secrets)
	// Convert to map for order-independent check.
	m := make(map[string]string)
	for _, kv := range out {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			m[parts[0]] = parts[1]
		}
	}
	if m["API_KEY"] != "secret123" {
		t.Errorf("API_KEY = %q, want secret123", m["API_KEY"])
	}
	if m["STATIC"] != "no-substitution" {
		t.Errorf("STATIC = %q, want no-substitution", m["STATIC"])
	}
}

func TestExpandSecret_MultiplePlaceholders(t *testing.T) {
	s := "prefix-{secret:K1}-middle-{secret:K2}-suffix"
	out := expandSecret(s, map[string]string{"K1": "aaa", "K2": "bbb"})
	if out != "prefix-aaa-middle-bbb-suffix" {
		t.Errorf("got %q", out)
	}
}

func TestScrubResult_ReplacesNonEmpty(t *testing.T) {
	b := []byte(`{"text":"the secret abc and more abc"}`)
	out := scrubResult(b, map[string]string{"TOK": "abc", "EMPTY": ""})
	s := string(out)
	if strings.Contains(s, "abc") {
		t.Errorf("raw secret still present: %q", s)
	}
	if !strings.Contains(s, "«redacted»") {
		t.Errorf("scrub marker missing: %q", s)
	}
	// Empty secret must NOT be replaced (would wipe the whole string).
	if strings.Contains(s, "«redacted»«redacted»") {
		t.Errorf("empty secret was scrubbed: %q", s)
	}
}

// --- decodePanePrompts ---

func TestDecodePanePrompts(t *testing.T) {
	valid := []any{
		map[string]any{"title": "Q1", "message": "What?"},
		map[string]any{"title": "Q2", "message": "How?"},
	}
	out, err := decodePanePrompts(valid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].Title != "Q1" || out[0].Message != "What?" {
		t.Errorf("first prompt = %+v", out[0])
	}
}

func TestDecodePanePrompts_Errors(t *testing.T) {
	cases := []struct {
		name string
		raw  any
	}{
		{"not array", "bad"},
		{"empty array", []any{}},
		{"too many", make([]any, 17)},
		{"non-object element", []any{"string-not-map"}},
		{"missing title", []any{map[string]any{"message": "m"}}},
		{"missing message", []any{map[string]any{"title": "t"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := decodePanePrompts(c.raw)
			if err == nil {
				t.Errorf("expected error for %q", c.name)
			}
		})
	}
}

// --- handleSubmitTurn direct coverage ---

func TestHandleSubmitTurn_MissingTurnID(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"
	gated := GatedFns{
		SubmitTurn: func(folder string, t TurnResult) error { return nil },
	}
	stop, err := ServeMCP(sock, gated, StoreFns{}, "world", nil, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	req := `{"jsonrpc":"2.0","id":2,"method":"submit_turn","params":{"status":"success"}}` + "\n"
	c.Write([]byte(req))
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var parsed struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Error == nil {
		t.Fatal("expected error for missing turn_id, got nil")
	}
	if !strings.Contains(parsed.Error.Message, "turn_id") {
		t.Errorf("error message = %q, want mention of turn_id", parsed.Error.Message)
	}
}

func TestHandleSubmitTurn_NilSubmitTurn(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"
	// SubmitTurn is nil — should return a config error.
	stop, err := ServeMCP(sock, GatedFns{}, StoreFns{}, "world", nil, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	req := `{"jsonrpc":"2.0","id":3,"method":"submit_turn","params":{"turn_id":"x","status":"success"}}` + "\n"
	c.Write([]byte(req))
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var parsed struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Error == nil {
		t.Fatal("expected error for nil SubmitTurn")
	}
	if !strings.Contains(parsed.Error.Message, "not configured") {
		t.Errorf("error = %q, want 'not configured'", parsed.Error.Message)
	}
}

func TestHandleSubmitTurn_ParseError(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"
	stop, err := ServeMCP(sock, GatedFns{}, StoreFns{}, "world", nil, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	// Invalid JSON.
	c.Write([]byte("not-json\n"))
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var parsed struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Error == nil {
		t.Fatal("expected parse error")
	}
	if parsed.Error.Code != -32700 {
		t.Errorf("error code = %d, want -32700 (parse error)", parsed.Error.Code)
	}
}

// --- inspect_messages access-denied for non-owner ---

func TestServeMCP_InspectMessages_AccessDenied(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"
	now := time.Now()

	called := 0
	db := StoreFns{
		MessagesBefore: func(jid string, before time.Time, limit int) ([]core.Message, error) {
			called++
			return []core.Message{{ID: "m1", Timestamp: now, Content: "secret"}}, nil
		},
		// tier-2 caller; the jid is NOT routed to their folder.
		JIDRoutedToFolder: func(jid, folder string) bool { return false },
	}
	stop, err := ServeMCP(sock, GatedFns{}, db, "world/a/b", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	_, errText := callTool(t, sock, "inspect_messages", map[string]any{
		"chat_jid": "telegram:foreign",
	})
	if !strings.Contains(errText, "access_denied") {
		t.Fatalf("expected access_denied, got %q", errText)
	}
	if called != 0 {
		t.Errorf("MessagesBefore should not be called on denial; called %d times", called)
	}
}

// --- fetch_history access-denied ---

func TestServeMCP_FetchHistory_AccessDenied(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	called := 0
	gated := GatedFns{
		FetchPlatformHistory: func(jid string, before time.Time, limit int) (PlatformHistory, error) {
			called++
			return PlatformHistory{Source: "platform"}, nil
		},
	}
	db := StoreFns{
		JIDRoutedToFolder: func(jid, folder string) bool { return false },
	}
	stop, err := ServeMCP(sock, gated, db, "world/a/b", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	_, errText := callTool(t, sock, "fetch_history", map[string]any{
		"chat_jid": "telegram:foreign",
	})
	if !strings.Contains(errText, "access_denied") {
		t.Fatalf("expected access_denied, got %q", errText)
	}
	if called != 0 {
		t.Errorf("FetchPlatformHistory should not be called on denial; called %d times", called)
	}
}

// --- fetch_history happy path ---

func TestServeMCP_FetchHistory_HappyPath(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"
	now := time.Now()

	gated := GatedFns{
		FetchPlatformHistory: func(jid string, before time.Time, limit int) (PlatformHistory, error) {
			return PlatformHistory{
				Source: "platform",
				Messages: []core.Message{
					{ID: "m1", ChatJID: jid, Content: "hi", Timestamp: now},
				},
			}, nil
		},
	}
	db := StoreFns{
		// tier-0 (root folder) bypasses JIDRoutedToFolder entirely.
	}
	stop, err := ServeMCP(sock, gated, db, "root", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	payload, errText := callTool(t, sock, "fetch_history", map[string]any{
		"chat_jid": "telegram:123",
	})
	if errText != "" {
		t.Fatalf("fetch_history error: %s", errText)
	}
	if payload["source"] != "platform" {
		t.Errorf("source = %v, want platform", payload["source"])
	}
	if payload["count"].(float64) != 1 {
		t.Errorf("count = %v, want 1", payload["count"])
	}
}

// --- schedule_task cron-expr branch ---

func TestServeMCP_ScheduleTask_InvalidCron(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	db := StoreFns{
		CreateTask:          func(t core.Task) error { return nil },
		DefaultFolderForJID: func(jid string) string { return "world" },
		ListTasks:           func(f string, r bool) []core.Task { return nil },
	}
	stop, err := ServeMCP(sock, GatedFns{}, db, "world", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	_, errText := callTool(t, sock, "schedule_task", map[string]any{
		"targetJid": "telegram:42",
		"prompt":    "do work",
		"cron":      "not-a-cron-expr",
	})
	if !strings.Contains(errText, "invalid cron") {
		t.Fatalf("expected invalid cron error, got %q", errText)
	}
}

// TestServeMCP_ScheduleTask_RFC3339OneShot verifies an RFC3339 cron value
// creates a one-shot task with an empty Cron field.
func TestServeMCP_ScheduleTask_RFC3339OneShot(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	var created []core.Task
	db := StoreFns{
		CreateTask: func(task core.Task) error {
			created = append(created, task)
			return nil
		},
		DefaultFolderForJID: func(jid string) string { return "world" },
		ListTasks:           func(f string, r bool) []core.Task { return nil },
	}
	stop, err := ServeMCP(sock, GatedFns{}, db, "world", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	payload, errText := callTool(t, sock, "schedule_task", map[string]any{
		"targetJid": "telegram:42",
		"prompt":    "one-shot ping",
		"cron":      future,
	})
	if errText != "" {
		t.Fatalf("schedule_task error: %s", errText)
	}
	if payload["taskId"] == nil {
		t.Fatalf("taskId missing from response: %v", payload)
	}
	if len(created) != 1 {
		t.Fatalf("expected 1 CreateTask call, got %d", len(created))
	}
	// One-shot: Cron field must be empty so timed marks it done after one firing.
	if created[0].Cron != "" {
		t.Errorf("one-shot task Cron = %q, want empty", created[0].Cron)
	}
	if created[0].NextRun == nil {
		t.Error("one-shot task NextRun must be set")
	}
}

// --- routeTargetWithin additional cases ---

func TestRouteTargetWithin_ExtraEdgeCases(t *testing.T) {
	cases := []struct {
		target, owner string
		want          bool
	}{
		{"folder:world/a", "world/a", true},
		{"folder:world/a/child", "world/a", true},
		{"folder:world/b", "world/a", false},
		{"daemon:x", "world/a", false},
		{"builtin:y", "world/a", false},
		// Plain path without prefix is also accepted by the switch default.
		{"world/a", "world/a", true},
		{"world/a/deep/nested", "world/a", true},
		{"world/ab", "world/a", false}, // must not prefix-match world/a in world/ab
	}
	for _, c := range cases {
		got := routeTargetWithin(c.target, c.owner)
		if got != c.want {
			t.Errorf("routeTargetWithin(%q, %q) = %v, want %v", c.target, c.owner, got, c.want)
		}
	}
}
