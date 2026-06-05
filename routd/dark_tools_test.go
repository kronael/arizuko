package routd

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
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

// attachRunedSibling attaches an in-memory runed.db (just the session_log table
// runed owns) as routd's RO runedDB handle, so the inspect_session "recent"
// hint can read it cross-DB. The DB name is unique per call (cache=shared is
// process-wide, which would cross-contaminate between tests).
func attachRunedSibling(t *testing.T, d *DB) *sql.DB {
	t.Helper()
	h, err := sql.Open("sqlite", "file:runedsib_"+randHex(8)+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open runed sibling: %v", err)
	}
	if _, err := h.Exec(`CREATE TABLE session_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT, group_folder TEXT NOT NULL,
		session_id TEXT NOT NULL, started_at TEXT NOT NULL, ended_at TEXT,
		result TEXT, error TEXT, message_count INTEGER)`); err != nil {
		t.Fatalf("create session_log: %v", err)
	}
	d.runedDB = h
	t.Cleanup(func() { h.Close() })
	return h
}

func seedTask(t *testing.T, s *store.Store, id, owner, jid, prompt string) {
	t.Helper()
	if err := s.CreateTask(core.Task{
		ID: id, Owner: owner, ChatJID: jid, Prompt: prompt,
		Cron: "0 9 * * *", Status: core.TaskActive, Created: time.Now(),
	}); err != nil {
		t.Fatalf("CreateTask %s: %v", id, err)
	}
}

// TestInspectIdentity_SiblingRead: inspect_identity resolves a claimed sub to
// its identity + sibling subs by reading identities/identity_claims in the
// sibling messages.db (gated's store). StoreFns.GetIdentityForSub was nil in
// routd → the tool was dark.
func TestInspectIdentity_SiblingRead(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := attachACLSibling(t, db)

	idn, err := s.CreateIdentity("alice")
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}
	if err := s.LinkSub(idn.ID, "tg:42"); err != nil {
		t.Fatalf("LinkSub tg:42: %v", err)
	}
	if err := s.LinkSub(idn.ID, "discord:7"); err != nil {
		t.Fatalf("LinkSub discord:7: %v", err)
	}

	srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
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

// TestInspectIdentity_NilSibling: with no messages.db the sub is unclaimed; the
// tool still answers {identity:null, subs:[]} rather than erroring.
func TestInspectIdentity_NilSibling(t *testing.T) {
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
		t.Fatalf("nil-sibling identity=%v want null", payload["identity"])
	}
}

// TestInspectSession_SiblingRead: inspect_session returns the routd.db
// session_id plus recent session_log rows read RO from runed.db. RecentSessions
// was nil in routd → the recent[] hint was dark.
func TestInspectSession_SiblingRead(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	rdb := attachRunedSibling(t, db)
	if _, err := rdb.Exec(
		`INSERT INTO session_log (group_folder, session_id, started_at, result, message_count)
		 VALUES (?, ?, ?, ?, ?)`,
		"demo", "sess-1", time.Now().Format(time.RFC3339), "ok", 5); err != nil {
		t.Fatalf("seed session_log: %v", err)
	}

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
	recent, ok := payload["recent"].([]any)
	if !ok || len(recent) != 1 {
		t.Fatalf("inspect_session recent=%v want 1 row", payload["recent"])
	}
}

// TestInspectSession_NilRunedSibling: no runed.db → recent[] is empty/null, no
// error. The tool registers (GetSession backs it from routd.db) and answers.
func TestInspectSession_NilRunedSibling(t *testing.T) {
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

// TestListTasks_SiblingRead: list_tasks returns scheduled_tasks read RO from
// the sibling messages.db (timed's table). ListTasks was nil in routd → dark.
func TestListTasks_SiblingRead(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := attachACLSibling(t, db)
	seedTask(t, s, "task-1", "demo", "slack:T/C/U", "daily standup")

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

	// inspect_tasks also reads tasks + per-task run logs from the sibling.
	if _, err := s.DB().Exec(
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

// TestTaskWriteTools_FederationPending: the WRITE task tools (schedule_task,
// pause_task, cancel_task) must NOT write timed's sibling table. They return a
// clear federation-pending error instead of faking a cross-DB write.
func TestTaskWriteTools_FederationPending(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := attachACLSibling(t, db)
	// schedule_task resolves targetJid → folder; route it so it reaches the
	// CreateTask federation guard rather than "target group not registered".
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	const jid = "slack:team/channel/c1"
	seedTask(t, s, "task-1", "demo", jid, "existing") // so pause/cancel find a row

	srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
	ipcDir := filepath.Join(t.TempDir(), "ipc", "demo")
	stop, err := srv.ServeTurnMCP(turnMCP{folder: "demo", turnID: "t1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	defer stop()
	sock := groupfolder.IpcSocket(ipcDir)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"schedule_task", map[string]any{"targetJid": jid, "prompt": "ping", "cron": "60000"}},
		{"pause_task", map[string]any{"taskId": "task-1"}},
		{"cancel_task", map[string]any{"taskId": "task-1"}},
	} {
		_, errText := callToolOverSock(t, sock, tc.tool, tc.args)
		if errText == "" {
			t.Fatalf("%s should error (federation pending), got success", tc.tool)
		}
		if !strings.Contains(errText, "federation pending") {
			t.Fatalf("%s error=%q want 'federation pending'", tc.tool, errText)
		}
	}

	// The sibling task row is untouched: no cross-DB write happened.
	if tasks := db.SiblingTasks("demo", true); len(tasks) != 1 || tasks[0].ID != "task-1" {
		t.Fatalf("sibling tasks mutated by write tools: %+v", tasks)
	}
}
