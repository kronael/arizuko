package routd

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
)

// callToolText drives one MCP tools/call over the socket and returns the raw
// content text (for tools whose payload is a bare JSON array, not an object —
// list_tasks marshals []core.Task directly, which callToolOverSock's map
// unmarshal can't decode). Second return is the error text when isError.
func callToolText(t *testing.T, sock, name string, args map[string]any) (string, string) {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial %s: %v", sock, err)
	}
	defer c.Close()
	req := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	}
	b, _ := json.Marshal(req)
	c.Write(append(b, '\n'))
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var parsed struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("unmarshal %q: %v", resp, err)
	}
	if parsed.Error != nil {
		t.Fatalf("rpc error: %s", parsed.Error.Message)
	}
	if len(parsed.Result.Content) == 0 {
		t.Fatalf("no content: %s", resp)
	}
	text := parsed.Result.Content[0].Text
	if parsed.Result.IsError {
		return "", text
	}
	return text, ""
}

// callToolArray drives a tool whose payload is a bare JSON array and returns it.
func callToolArray(t *testing.T, sock, name string, args map[string]any) ([]any, string) {
	t.Helper()
	text, errText := callToolText(t, sock, name, args)
	if errText != "" {
		return nil, errText
	}
	var arr []any
	if err := json.Unmarshal([]byte(text), &arr); err != nil {
		t.Fatalf("%s payload %q not a JSON array: %v", name, text, err)
	}
	return arr, ""
}

// seedTask seeds one task into routd's OWN routd.db — routd OWNS scheduled_tasks
// (migration 0009), so reads/writes go there, not a sibling messages.db. Uses
// the audit-free PutTaskRow (routd.db has no audit_log table).
func seedTask(t *testing.T, d *DB, id, owner, jid, prompt string) {
	t.Helper()
	if err := store.New(d.SQL()).PutTaskRow(core.Task{
		ID: id, Owner: owner, ChatJID: jid, Prompt: prompt,
		Cron: "0 9 * * *", Status: core.TaskActive, Created: time.Now(),
	}); err != nil {
		t.Fatalf("PutTaskRow %s: %v", id, err)
	}
}

// stubIdentity is a fixed IdentityResolver standing in for authd's
// GET /v1/identities/{sub} in the inspect_identity tests — routd snapshots
// identity over HTTP now (authd OWNS it, spec 5/9), never the sibling
// messages.db.
type stubIdentity struct {
	idn  ipc.Identity
	subs []string
	sub  string // the one sub that resolves; others are unclaimed
}

func (s stubIdentity) Resolve(sub string) (ipc.Identity, []string, bool) {
	if sub == s.sub {
		return s.idn, s.subs, true
	}
	return ipc.Identity{}, nil, false
}

// TestInspectIdentity_ViaAuthd: inspect_identity resolves a claimed sub to its
// identity + linked subs through the authd resolver (SetIdentityResolver), NOT
// the sibling messages.db. An unclaimed sub returns {identity:null, subs:[]}.
func TestInspectIdentity_ViaAuthd(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
	srv.SetIdentityResolver(stubIdentity{
		idn:  ipc.Identity{ID: "idn-alice", Name: "alice"},
		subs: []string{"tg:42", "discord:7"},
		sub:  "tg:42",
	})
	ipcDir := filepath.Join(t.TempDir(), "ipc", "demo")
	stop, err := srv.ServeTurnMCP(turnMCP{folder: "demo", turnID: "t1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	defer stop()
	sock := groupfolder.IpcSocket(ipcDir)

	payload, errText := callToolOverSock(t, sock, "inspect_identity", map[string]any{"sub": "tg:42"})
	if errText != "" {
		t.Fatalf("inspect_identity error: %s", errText)
	}
	id, ok := payload["identity"].(map[string]any)
	if !ok || id["name"] != "alice" {
		t.Fatalf("inspect_identity identity=%v want name=alice", payload["identity"])
	}
	subs, ok := payload["subs"].([]any)
	if !ok || len(subs) != 2 {
		t.Fatalf("inspect_identity subs=%v want 2", payload["subs"])
	}

	// Unclaimed sub → {identity:null, subs:[]} (not an error).
	payload, errText = callToolOverSock(t, sock, "inspect_identity", map[string]any{"sub": "tg:999"})
	if errText != "" {
		t.Fatalf("inspect_identity unclaimed error: %s", errText)
	}
	if payload["identity"] != nil {
		t.Fatalf("unclaimed sub identity=%v want null", payload["identity"])
	}
}

// TestInspectIdentity_NoResolver: with no resolver wired (no AUTHD_URL) the sub
// is unclaimed; the tool answers {identity:null, subs:[]} rather than erroring.
func TestInspectIdentity_NoResolver(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
	ipcDir := filepath.Join(t.TempDir(), "ipc", "demo")
	stop, err := srv.ServeTurnMCP(turnMCP{folder: "demo", turnID: "t1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	defer stop()
	payload, errText := callToolOverSock(t, groupfolder.IpcSocket(ipcDir), "inspect_identity",
		map[string]any{"sub": "tg:42"})
	if errText != "" {
		t.Fatalf("inspect_identity error: %s", errText)
	}
	if payload["identity"] != nil {
		t.Fatalf("no-resolver identity=%v want null", payload["identity"])
	}
}

// TestInspectIdentity_NoResolverUnclaimed proves identity is federated to authd:
// with no IdentityResolver wired, inspect_identity answers unclaimed — routd
// never reads identity from a local DB (authd OWNS it, served over HTTP). routd
// opens NO sibling messages.db, so there is no cross-DB claim to surface.
func TestInspectIdentity_NoResolverUnclaimed(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
	// No resolver wired → the sub stays unclaimed.
	ipcDir := filepath.Join(t.TempDir(), "ipc", "demo")
	stop, err := srv.ServeTurnMCP(turnMCP{folder: "demo", turnID: "t1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	defer stop()
	payload, errText := callToolOverSock(t, groupfolder.IpcSocket(ipcDir), "inspect_identity",
		map[string]any{"sub": "tg:42"})
	if errText != "" {
		t.Fatalf("inspect_identity error: %s", errText)
	}
	if payload["identity"] != nil {
		t.Fatalf("no resolver wired surfaced identity=%v want null (identity reads authd, not a local DB)", payload["identity"])
	}
}

// TestInspectSession_Federated: inspect_session returns the routd.db session_id
// plus recent session_log rows FEDERATED from runed's GET /v1/sessions/recent
// (here a SessionResolver stub). No cross-DB runed.db read — runed owns
// session_log and serves it over HTTP.
func TestInspectSession_Federated(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
	srv.SetSessionResolver(&stubSessions{rows: []core.SessionRecord{{
		ID: 1, Folder: "demo", SessionID: "sess-1",
		StartedAt: time.Now(), Result: "ok", MsgCount: 5,
	}}})
	ipcDir := filepath.Join(t.TempDir(), "ipc", "demo")
	stop, err := srv.ServeTurnMCP(turnMCP{folder: "demo", turnID: "t1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	defer stop()
	payload, errText := callToolOverSock(t, groupfolder.IpcSocket(ipcDir), "inspect_session",
		map[string]any{})
	if errText != "" {
		t.Fatalf("inspect_session error: %s", errText)
	}
	recent, ok := payload["recent"].([]any)
	if !ok || len(recent) != 1 {
		t.Fatalf("inspect_session recent=%v want 1 row", payload["recent"])
	}
}

// TestInspectSession_NilResolver: no SessionResolver wired → recent[] is
// empty/null, no error. The tool registers (GetSession backs it from routd.db)
// and answers.
func TestInspectSession_NilResolver(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
	ipcDir := filepath.Join(t.TempDir(), "ipc", "demo")
	stop, err := srv.ServeTurnMCP(turnMCP{folder: "demo", turnID: "t1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	defer stop()
	payload, errText := callToolOverSock(t, groupfolder.IpcSocket(ipcDir), "inspect_session",
		map[string]any{})
	if errText != "" {
		t.Fatalf("inspect_session error: %s", errText)
	}
	if r, ok := payload["recent"].([]any); ok && len(r) != 0 {
		t.Fatalf("nil-runed recent=%v want empty", payload["recent"])
	}
}

// TestListTasks_OwnDB: list_tasks returns scheduled_tasks read from routd's OWN
// routd.db (routd OWNS the table — migration 0009). ListTasks was nil in routd
// → dark.
func TestListTasks_OwnDB(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	seedTask(t, db, "task-1", "demo", "slack:T/C/U", "daily standup")

	srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
	ipcDir := filepath.Join(t.TempDir(), "ipc", "demo")
	// folder "demo" is tier 0 (root) → sees every task (owner filter empty).
	stop, err := srv.ServeTurnMCP(turnMCP{folder: "demo", turnID: "t1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	defer stop()
	sock := groupfolder.IpcSocket(ipcDir)

	arr, errText := callToolArray(t, sock, "list_tasks", map[string]any{})
	if errText != "" {
		t.Fatalf("list_tasks error: %s", errText)
	}
	if len(arr) != 1 {
		t.Fatalf("list_tasks=%v want 1 task", arr)
	}

	// inspect_tasks also reads tasks + per-task run logs from routd's own db.
	if _, err := db.SQL().Exec(
		`INSERT INTO task_run_logs (task_id, run_at, duration_ms, status)
		 VALUES (?, ?, ?, ?)`, "task-1", time.Now().Format(time.RFC3339), 12, "ok"); err != nil {
		t.Fatalf("seed task_run_logs: %v", err)
	}
	payload, errText := callToolOverSock(t, sock, "inspect_tasks", map[string]any{"task_id": "task-1"})
	if errText != "" {
		t.Fatalf("inspect_tasks error: %s", errText)
	}
	if tl, ok := payload["tasks"].([]any); !ok || len(tl) != 1 {
		t.Fatalf("inspect_tasks tasks=%v want 1", payload["tasks"])
	}
	if rl, ok := payload["runs"].([]any); !ok || len(rl) != 1 {
		t.Fatalf("inspect_tasks runs=%v want 1 run log", payload["runs"])
	}
}

// TestFetchHistory_AdapterRows: fetch_history proxies to the owning adapter
// (Deliverer.FetchHistory), decodes the HistoryResponse, and returns the rows.
// GatedFns.FetchPlatformHistory was nil in routd → the tool was dark.
func TestFetchHistory_AdapterRows(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	const jid = "slack:team/channel/c1"
	if _, err := db.PutTurnContext("t1", "demo", "", jid, "u1", ""); err != nil {
		t.Fatal(err)
	}

	// Canned adapter HistoryResponse (chanlib shape: messages with int ts).
	history := []byte(`{"source":"platform","messages":[
		{"id":"m1","chat_jid":"slack:team/channel/c1","sender":"u1","content":"hi","timestamp":1700000000},
		{"id":"m2","chat_jid":"slack:team/channel/c1","sender":"u2","content":"yo","timestamp":1700000060}]}`)
	deliver := &recDeliverer{pid: "pid-x", history: history}
	srv := NewServer(db, nil, deliver, nil, 0, "")
	ipcDir := filepath.Join(t.TempDir(), "ipc", "demo")
	stop, err := srv.ServeTurnMCP(
		turnMCP{folder: "demo", chatJID: jid, turnID: "t1", trigger: "u1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	defer stop()

	payload, errText := callToolOverSock(t, groupfolder.IpcSocket(ipcDir), "fetch_history",
		map[string]any{"chat_jid": jid})
	if errText != "" {
		t.Fatalf("fetch_history error: %s", errText)
	}
	if payload["source"] != "platform" {
		t.Fatalf("fetch_history source=%v want platform", payload["source"])
	}
	// count comes back as a JSON number (float64); messages is the rendered
	// <messages> XML string router.FormatMessages produces.
	if c, _ := payload["count"].(float64); c != 2 {
		t.Fatalf("fetch_history count=%v want 2", payload["count"])
	}
	// Rows are cached into routd.db (dedup by id), the sole-appender path.
	if n := countMsgs(t, db, jid); n != 2 {
		t.Fatalf("cached rows=%d want 2", n)
	}
}

// TestFetchHistory_AdapterFailFallsBackToCache: when the adapter errors,
// fetch_history falls back to source:"cache" with the local routd.db rows.
func TestFetchHistory_AdapterFailFallsBackToCache(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	const jid = "slack:team/channel/c1"
	if _, err := db.PutTurnContext("t1", "demo", "", jid, "u1", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.PutMessage(core.Message{
		ID: "cached-1", ChatJID: jid, Sender: "u1", Content: "old",
		Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed cache row: %v", err)
	}

	deliver := &recDeliverer{historyErr: errSend}
	srv := NewServer(db, nil, deliver, nil, 0, "")
	ipcDir := filepath.Join(t.TempDir(), "ipc", "demo")
	stop, err := srv.ServeTurnMCP(
		turnMCP{folder: "demo", chatJID: jid, turnID: "t1", trigger: "u1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	defer stop()
	payload, errText := callToolOverSock(t, groupfolder.IpcSocket(ipcDir), "fetch_history",
		map[string]any{"chat_jid": jid})
	if errText != "" {
		t.Fatalf("fetch_history error: %s", errText)
	}
	if payload["source"] != "cache" {
		t.Fatalf("fetch_history source=%v want cache (adapter failed)", payload["source"])
	}
	if c, _ := payload["count"].(float64); c != 1 {
		t.Fatalf("fetch_history cache count=%v want 1", payload["count"])
	}
}

func countMsgs(t *testing.T, db *DB, jid string) int {
	t.Helper()
	var n int
	db.SQL().QueryRow("SELECT COUNT(*) FROM messages WHERE chat_jid=?", jid).Scan(&n)
	return n
}

// TestListTasks_NilSibling: no messages.db → list_tasks answers an empty array,
// not an error.
func TestListTasks_NilSibling(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
	ipcDir := filepath.Join(t.TempDir(), "ipc", "demo")
	stop, err := srv.ServeTurnMCP(turnMCP{folder: "demo", turnID: "t1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	defer stop()
	arr, errText := callToolArray(t, groupfolder.IpcSocket(ipcDir), "list_tasks", map[string]any{})
	if errText != "" {
		t.Fatalf("list_tasks error: %s", errText)
	}
	if len(arr) != 0 {
		t.Fatalf("nil-sibling list_tasks=%v want empty", arr)
	}
}

// TestTaskWriteTools_WriteOwnDB: the WRITE task tools (schedule_task,
// pause_task, cancel_task) now write routd's OWN routd.db (routd OWNS
// scheduled_tasks — migration 0009). No errTaskFederation: schedule_task
// round-trips via list_tasks; pause flips status; cancel removes the row.
func TestTaskWriteTools_WriteOwnDB(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// schedule_task resolves targetJid → folder; route it so it reaches CreateTask.
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	const jid = "slack:team/channel/c1"
	seedTask(t, db, "task-1", "demo", jid, "existing") // pause/cancel target

	srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
	ipcDir := filepath.Join(t.TempDir(), "ipc", "demo")
	stop, err := srv.ServeTurnMCP(turnMCP{folder: "demo", turnID: "t1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	defer stop()
	sock := groupfolder.IpcSocket(ipcDir)

	// schedule_task writes a NEW task into routd.db (no federation error).
	res, errText := callToolOverSock(t, sock, "schedule_task",
		map[string]any{"targetJid": jid, "prompt": "ping", "cron": "60000"})
	if errText != "" {
		t.Fatalf("schedule_task error: %s", errText)
	}
	newID, _ := res["taskId"].(string)
	if newID == "" {
		t.Fatalf("schedule_task returned no taskId: %v", res)
	}
	// Round-trip: list_tasks now shows both the seeded + scheduled task.
	arr, errText := callToolArray(t, sock, "list_tasks", map[string]any{})
	if errText != "" {
		t.Fatalf("list_tasks error: %s", errText)
	}
	if len(arr) != 2 {
		t.Fatalf("list_tasks=%v want 2 (seeded + scheduled)", arr)
	}

	// pause_task flips the seeded task to paused in routd.db.
	if _, errText := callToolOverSock(t, sock, "pause_task", map[string]any{"taskId": "task-1"}); errText != "" {
		t.Fatalf("pause_task error: %s", errText)
	}
	if got, _ := db.SiblingGetTask("task-1"); got.Status != core.TaskPaused {
		t.Fatalf("pause_task: status=%q want paused", got.Status)
	}

	// cancel_task removes the seeded task from routd.db.
	if _, errText := callToolOverSock(t, sock, "cancel_task", map[string]any{"taskId": "task-1"}); errText != "" {
		t.Fatalf("cancel_task error: %s", errText)
	}
	if _, ok := db.SiblingGetTask("task-1"); ok {
		t.Fatal("cancel_task: task-1 still present after cancel")
	}
	if tasks := db.SiblingTasks("demo", true); len(tasks) != 1 || tasks[0].ID != newID {
		t.Fatalf("after cancel, want only the scheduled task %q, got %+v", newID, tasks)
	}
}
