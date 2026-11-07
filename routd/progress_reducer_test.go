package routd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/ipc"
)

func TestProgress_MergedSnapshots(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := newProgressTurn(t, dl, "")
	claude := `{"hook_event_name":"PostToolUse","tool_name":"TodoWrite","session_id":"s1","tool_use_id":"c1","tool_input":{"todos":[{"content":"Build","status":"pending"}]}}`
	codex := `{"hook_event_name":"PostToolUse","tool_name":"update_plan","session_id":"s2","tool_use_id":"c2","tool_input":{"plan":[{"step":"Test","status":"in_progress"}]},"tool_response":"Plan updated"}`
	for _, status := range []ipc.TurnStatus{{TurnID: "t1", Source: "claude", Snapshot: json.RawMessage(claude)}, {TurnID: "t1", Source: "codex", Snapshot: json.RawMessage(codex)}} {
		if _, err := srv.submitStatus(status); err != nil {
			t.Fatal(err)
		}
	}
	srv.flushProgress(time.Now().Add(progressInterval))
	rows := statusRows(t, db, progressJID)
	if len(dl.sends) != 1 || len(dl.edits) != 1 || len(rows) != 1 || dl.edits[0].platformID != rows[0].PlatformID || !strings.Contains(rows[0].Content, "☐ Build\n⏳ Test") {
		t.Fatalf("sends=%+v edits=%+v rows=%+v", dl.sends, dl.edits, rows)
	}
	replacement := `{"hook_event_name":"PostToolUse","tool_name":"TodoWrite","session_id":"s1","tool_use_id":"c3","tool_input":{"todos":[{"content":"Ship","status":"completed"}]}}`
	if _, err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1", Source: "claude", Snapshot: json.RawMessage(replacement)}); err != nil {
		t.Fatal(err)
	}
	srv.flushProgress(time.Now().Add(2 * progressInterval))
	rows = statusRows(t, db, progressJID)
	if !strings.Contains(rows[0].Content, "☑ Ship\n⏳ Test") {
		t.Fatalf("replacement: %+v", rows)
	}
}

func TestProgress_ReducerLifecycle(t *testing.T) {
	db, _ := newProgressTurn(t, &recDeliverer{}, "")
	progress := &turnProgress{sources: make(map[string][]progressItem), seen: make(map[string]bool), calls: make(map[string]bool)}
	db.progress.Store("t1", progress)
	apply := func(source, raw string) (string, bool) {
		t.Helper()
		return progress.reduceProgress(ipc.TurnStatus{TurnID: "t1", Source: source, Snapshot: json.RawMessage(raw)})
	}
	claude := `{"hook_event_name":"PostToolUse","tool_name":"TaskCreate","session_id":"s","tool_use_id":"1","tool_input":{"subject":"Build","activeForm":"Building"},"tool_response":{"task":{"id":"1","subject":"Build"}}}`
	if text, ok := apply("claude", claude); !ok || text != "☐ Build" {
		t.Fatalf("create: %q %v", text, ok)
	}
	if _, ok := apply("claude", claude); ok {
		t.Fatal("duplicate published")
	}
	update := `{"hook_event_name":"PostToolUse","tool_name":"TaskUpdate","session_id":"s","tool_use_id":"2","tool_input":{"taskId":"1","status":"in_progress"},"tool_response":{"success":true,"taskId":"1"}}`
	if text, ok := apply("claude", update); !ok || text != "⏳ Building" {
		t.Fatalf("update: %q %v", text, ok)
	}
	list := `{"hook_event_name":"PostToolUse","tool_name":"TaskList","session_id":"s","tool_use_id":"3","tool_input":{},"tool_response":{"tasks":[{"id":"2","subject":"Test","status":"pending"}]}}`
	if text, ok := apply("claude", list); !ok || text != "⏳ Building\n☐ Test" {
		t.Fatalf("list: %q %v", text, ok)
	}
	get := `{"hook_event_name":"PostToolUse","tool_name":"TaskGet","session_id":"s","tool_use_id":"get","tool_input":{"taskId":"3"},"tool_response":{"task":{"id":"3","subject":"Ship","status":"completed"}}}`
	if text, ok := apply("claude", get); !ok || text != "⏳ Building\n☐ Test\n☑ Ship" {
		t.Fatalf("get: %q %v", text, ok)
	}
	failed := `{"hook_event_name":"PostToolUse","tool_name":"TaskUpdate","session_id":"s","tool_use_id":"fail","tool_input":{"taskId":"3","status":"deleted"},"tool_response":{"success":false}}`
	if _, ok := apply("claude", failed); ok {
		t.Fatal("failed update published")
	}
	deleted := `{"hook_event_name":"PostToolUse","tool_name":"TaskUpdate","session_id":"s","tool_use_id":"delete","tool_input":{"taskId":"3","status":"deleted"},"tool_response":{"success":true,"taskId":"3"}}`
	if text, ok := apply("claude", deleted); !ok || text != "⏳ Building\n☐ Test" {
		t.Fatalf("delete: %q %v", text, ok)
	}
	bad := `{"hook_event_name":"PostToolUse","tool_name":"TodoWrite","session_id":"s","tool_use_id":"4","tool_input":{"todos":[{"content":"bad","status":"pending","activeForm":null}]}}`
	if _, ok := apply("claude", bad); ok {
		t.Fatal("malformed snapshot published")
	}
	for _, tc := range []struct{ name, item string }{
		{"missing label", `{"status":"pending"}`},
		{"missing status", `{"content":"bad"}`},
		{"invalid status", `{"content":"bad","status":"invalid"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := json.Marshal(progress.sources)
			raw := `{"hook_event_name":"PostToolUse","tool_name":"TodoWrite","session_id":"s","tool_use_id":"` + tc.name + `","tool_input":{"todos":[` + tc.item + `]}}`
			_, ok := apply("claude", raw)
			after, _ := json.Marshal(progress.sources)
			if ok || string(after) != string(before) {
				t.Fatalf("malformed snapshot changed progress: %s", after)
			}
		})
	}
	child := `{"hook_event_name":"PostToolUse","tool_name":"TodoWrite","agent_id":"child","session_id":"s","tool_use_id":"5","tool_input":{"todos":[]}}`
	if _, ok := apply("claude", child); ok {
		t.Fatal("child snapshot published")
	}
	if _, ok := progress.reduceProgress(ipc.TurnStatus{TurnID: "t1", Text: "fallback"}); ok {
		t.Fatal("fallback replaced checklist")
	}
	clear := `{"hook_event_name":"PostToolUse","tool_name":"TodoWrite","session_id":"s","tool_use_id":"6","tool_input":{"todos":[]}}`
	if text, ok := apply("claude", clear); !ok || text != "No remaining tasks." {
		t.Fatalf("clear: %q %v", text, ok)
	}
	finishTurn(db, nil, "t1", endDone)
	if _, ok := db.progress.Load("t1"); ok {
		t.Fatal("state survived end")
	}
}
