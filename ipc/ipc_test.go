package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
)

func TestBuildMCPServer(t *testing.T) {
	gated := GatedFns{
		SendMessage:   func(jid, text string) (string, error) { return "", nil },
		SendDocument:  func(jid, path, fn, caption string) error { return nil },
		ClearSession:  func(f string) {},
		GetGroups:     func() map[string]core.Group { return nil },
		GroupsDir:     "/tmp/groups",
		WebDir:        "/tmp/web",
	}
	db := StoreFns{}
	// tier-0 gets all tools via ["*"] rules
	srv := buildMCPServer(gated, db, "world", []string{"*"})
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
}

func TestBuildMCPServer_NoTools(t *testing.T) {
	gated := GatedFns{
		SendMessage:   func(jid, text string) (string, error) { return "", nil },
		SendDocument:  func(jid, path, fn, caption string) error { return nil },
		ClearSession:  func(f string) {},
		GetGroups:     func() map[string]core.Group { return nil },
		GroupsDir:     "/tmp/groups",
		WebDir:        "/tmp/web",
	}
	db := StoreFns{}
	// empty rules → no tools registered (except get/set_grants for tier 0-1)
	srv := buildMCPServer(gated, db, "world", []string{})
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
}


func TestIsSelfDefault(t *testing.T) {
	cases := []struct {
		seq    int
		target string
		owner  string
		want   bool
	}{
		{0, "world/a", "world/a", true},
		{0, "folder:world/a", "world/a", true},
		{1, "world/a", "world/a", false},
		{0, "world/a/child", "world/a", false},
		{0, "world/b", "world/a", false},
	}
	for _, c := range cases {
		got := isSelfDefault(core.Route{Seq: c.seq, Target: c.target}, c.owner)
		if got != c.want {
			t.Errorf("isSelfDefault({Seq:%d,Target:%q},%q) = %v, want %v",
				c.seq, c.target, c.owner, got, c.want)
		}
	}
}

func TestRouteTargetWithin(t *testing.T) {
	cases := []struct {
		target, owner string
		want          bool
	}{
		{"world/a", "world/a", true},
		{"world/a/child", "world/a", true},
		{"folder:world/a/child", "world/a", true},
		{"world/b", "world/a", false},
		{"daemon:timed", "world/a", false},
		{"builtin:stop", "world/a", false},
	}
	for _, c := range cases {
		if got := routeTargetWithin(c.target, c.owner); got != c.want {
			t.Errorf("routeTargetWithin(%q, %q) = %v, want %v", c.target, c.owner, got, c.want)
		}
	}
}

func TestAllToolsRegistered(t *testing.T) {
	gated := GatedFns{
		SendMessage:         func(jid, text string) (string, error) { return "", nil },
		SendDocument:        func(jid, path, fn, caption string) error { return nil },
		ClearSession:        func(f string) {},
		GetGroups:           func() map[string]core.Group { return nil },
		EnqueueMessageCheck: func(jid string) {},
		InjectMessage:       func(j, c, s, n string) (string, error) { return "", nil },
		RegisterGroup:       func(j string, g core.Group) error { return nil },
		GroupsDir:           "/tmp/groups",
		WebDir:              "/tmp/web",
	}
	db := StoreFns{
		CreateTask:          func(t core.Task) error { return nil },
		GetTask:             func(id string) (core.Task, bool) { return core.Task{}, false },
		UpdateTaskStatus:    func(id, s string) error { return nil },
		DeleteTask:          func(id string) error { return nil },
		ListTasks:           func(f string, r bool) []core.Task { return nil },
		ListRoutes:          func(f string, r bool) []core.Route { return nil },
		SetRoutes:           func(f string, r []core.Route) error { return nil },
		AddRoute:            func(r core.Route) (int64, error) { return 0, nil },
		DeleteRoute:         func(id int64) error { return nil },
		GetRoute:            func(id int64) (core.Route, bool) { return core.Route{}, false },
		DefaultFolderForJID: func(j string) string { return "" },
		ListACL:             func(p string) []core.ACLRow { return nil },
	}

	// tier-0 with all rules — all tools should be present
	srv := buildMCPServer(gated, db, "world", []string{"*"})
	if srv == nil {
		t.Fatal("expected non-nil server")
	}

	// tier-3 with reply only — most tools absent
	srv2 := buildMCPServer(gated, db, "w/a/b/c", []string{"reply"})
	if srv2 == nil {
		t.Fatal("expected non-nil server for tier-3")
	}
}

func TestSocialActionsRegistered(t *testing.T) {
	var postCalls, reactCalls, deleteCalls int
	gated := GatedFns{
		Post: func(jid, content string, media []string) (string, error) {
			postCalls++
			return "pid-1", nil
		},
		Like: func(jid, target, reaction string) error {
			reactCalls++
			return nil
		},
		Delete: func(jid, target string) error {
			deleteCalls++
			return nil
		},
		GroupsDir: "/tmp/groups",
		WebDir:    "/tmp/web",
	}
	// Rules permit all three actions for mastodon only. Tier-0 (folder="world").
	rules := []string{
		"post(jid=mastodon:*)",
		"like(jid=mastodon:*)",
		"delete(jid=mastodon:*)",
	}
	srv := buildMCPServer(gated, StoreFns{}, "world", rules)
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
	// With no matching rules, tools must not register at all (registerRaw early-returns).
	srv2 := buildMCPServer(gated, StoreFns{}, "w/a/b/c", []string{"reply"})
	if srv2 == nil {
		t.Fatal("expected non-nil server for tier-3 subset")
	}
}

func TestSendReply(t *testing.T) {
	gated := GatedFns{
		SendMessage:   func(jid, text string) (string, error) { return "", nil },
		SendDocument:  func(jid, path, fn, caption string) error { return nil },
		SendReply:     func(jid, text, rid string) (string, error) { return "", nil },
		GetGroups:     func() map[string]core.Group { return nil },
		GroupsDir:     "/tmp/groups",
		WebDir:        "/tmp/web",
	}
	srv := buildMCPServer(gated, StoreFns{}, "world", []string{"reply"})
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
}

func TestRefreshGroups(t *testing.T) {
	groups := map[string]core.Group{
		"world/a": {Folder: "world/a"},
	}
	gated := GatedFns{
		SendMessage:   func(jid, text string) (string, error) { return "", nil },
		SendDocument:  func(jid, path, fn, caption string) error { return nil },
		GetGroups:     func() map[string]core.Group { return groups },
		GroupsDir:     "/tmp/groups",
		WebDir:        "/tmp/web",
	}
	db := StoreFns{}
	// tier ≤ 2 gets refresh_groups
	srv := buildMCPServer(gated, db, "world/a", []string{"*"})
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
}

func TestWorkspaceRel(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"/home/node/file.txt", "file.txt", false},
		{"/home/node/tmp/out.pdf", "tmp/out.pdf", false},
		{"/workspace/group/file.txt", "", true},               // no longer valid
		{"/workspace/media/img.png", "", true},
		{"/home/node", "", true},                              // exact prefix, no trailing slash
		{"~/tmp/out.txt", "", true},
		{"/tmp/file", "", true},
	}
	for _, c := range cases {
		got, err := workspaceRel(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("workspaceRel(%q): expected error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("workspaceRel(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("workspaceRel(%q): got %q, want %q", c.in, got, c.want)
		}
	}
}

// TestWorkToolsRegistered verifies set_work/get_work register per tier
// and build without error.
func TestWorkToolsRegistered(t *testing.T) {
	dir := t.TempDir()
	gated := GatedFns{GroupsDir: dir, WebDir: dir}
	// tier-0 with no rules: get_work always registers, set_work gated on tier
	if srv := buildMCPServer(gated, StoreFns{}, "world", nil); srv == nil {
		t.Fatal("tier-0 build failed")
	}
	// tier-3 at w/a/b/c: get_work registers, set_work does not
	if srv := buildMCPServer(gated, StoreFns{}, "w/a/b/c", nil); srv == nil {
		t.Fatal("tier-3 build failed")
	}

	// Verify work.md round-trip via filesystem (the path set_work writes)
	groupDir := filepath.Join(dir, "world")
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(groupDir, "work.md")
	if err := os.WriteFile(p, []byte("draft"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil || string(data) != "draft" {
		t.Errorf("round-trip failed: %q err=%v", data, err)
	}
}

func TestIdentityUsedInServer(t *testing.T) {
	id := auth.Resolve("world/parent/child")
	if id.Tier != 2 {
		t.Fatalf("got tier %d, want 2", id.Tier)
	}
	if id.World != "world" {
		t.Fatalf("got world %q, want world", id.World)
	}
}

func TestServeMCP_PeerCredAcceptsMatchingUID(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"
	stop, err := ServeMCP(sock, GatedFns{}, StoreFns{}, "test", nil, os.Getuid())
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	// Server should not close the conn immediately — it accepted the peer.
	c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	var buf [1]byte
	_, err = c.Read(buf[:])
	// Expect timeout (server is waiting for MCP input), not EOF.
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected read timeout, got err=%v", err)
	}
}

func TestServeMCP_SubmitTurn(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	var mu sync.Mutex
	var got []TurnResult
	gated := GatedFns{
		SubmitTurn: func(folder string, t TurnResult) error {
			mu.Lock()
			defer mu.Unlock()
			if folder != "world" {
				return errors.New("wrong folder: " + folder)
			}
			got = append(got, t)
			return nil
		},
	}

	stop, err := ServeMCP(sock, gated, StoreFns{}, "world", nil, 0)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "submit_turn",
		"params": map[string]any{
			"turn_id":    "msg-42",
			"session_id": "sess-1",
			"status":     "success",
			"result":     "hello",
		},
	}
	b, _ := json.Marshal(req)
	c.Write(append(b, '\n'))

	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var parsed struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Error != nil {
		t.Fatalf("submit_turn returned error: %s", parsed.Error.Message)
	}
	if parsed.Result["ok"] != true {
		t.Errorf("expected result.ok=true, got %+v", parsed.Result)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("expected 1 SubmitTurn call, got %d", len(got))
	}
	if got[0].TurnID != "msg-42" || got[0].SessionID != "sess-1" || got[0].Status != "success" || got[0].Result != "hello" {
		t.Errorf("payload mismatch: %+v", got[0])
	}
}

func TestServeMCP_SubmitTurnHiddenFromToolsList(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	stop, err := ServeMCP(sock, GatedFns{}, StoreFns{}, "world", []string{"*"}, 0)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
		"params":  map[string]any{},
	}
	b, _ := json.Marshal(req)
	c.Write(append(b, '\n'))

	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if strings.Contains(string(resp), "submit_turn") {
		t.Fatalf("tools/list leaked submit_turn:\n%s", resp)
	}
}

// callTool runs an MCP tools/call over the socket and returns the parsed
// payload of result.content[0].text (which is JSON for the get_thread tool).
func callTool(t *testing.T, sock, name string, args map[string]any) (map[string]any, string) {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
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
				Type string `json:"type"`
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
		return nil, text
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("unmarshal payload %q: %v", text, err)
	}
	return payload, ""
}

func TestServeMCP_GetThread_HappyPath(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"
	now := time.Now()
	called := 0
	db := StoreFns{
		MessagesByThread: func(jid, topic string, before time.Time, limit int) ([]core.Message, error) {
			called++
			if jid != "telegram:group/-100" || topic != "t1" {
				t.Errorf("MessagesByThread args: jid=%q topic=%q", jid, topic)
			}
			return []core.Message{
				{ID: "m1", ChatJID: jid, Sender: "u1", Content: "hi", Timestamp: now, Topic: topic},
			}, nil
		},
		JIDRoutedToFolder: func(jid, folder string) bool { return true },
	}
	stop, err := ServeMCP(sock, GatedFns{}, db, "world", []string{"*"}, 0)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	payload, errText := callTool(t, sock, "get_thread", map[string]any{
		"chat_jid": "telegram:group/-100",
		"topic":    "t1",
	})
	if errText != "" {
		t.Fatalf("get_thread error: %s", errText)
	}
	if called != 1 {
		t.Fatalf("MessagesByThread call count = %d, want 1", called)
	}
	if payload["count"].(float64) != 1 {
		t.Fatalf("count = %v, want 1", payload["count"])
	}
	if payload["source"] != "local-db" {
		t.Fatalf("source = %v, want local-db", payload["source"])
	}
}

func TestServeMCP_GetThread_CrossGroupDenied(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"
	called := 0
	db := StoreFns{
		MessagesByThread: func(jid, topic string, before time.Time, limit int) ([]core.Message, error) {
			called++
			return nil, nil
		},
		// Tier-2 (folder "world/a/b") asking about a jid routed to a different folder.
		JIDRoutedToFolder: func(jid, folder string) bool { return false },
	}
	stop, err := ServeMCP(sock, GatedFns{}, db, "world/a/b", []string{"*"}, 0)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	_, errText := callTool(t, sock, "get_thread", map[string]any{
		"chat_jid": "telegram:group/-999",
		"topic":    "t1",
	})
	if !strings.Contains(errText, "access_denied") {
		t.Fatalf("expected access_denied, got %q", errText)
	}
	if called != 0 {
		t.Fatalf("MessagesByThread should not be called on denial; got %d", called)
	}
}

func TestServeMCP_PeerCredRejectsWrongUID(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"
	// Set expectedUID to a value we can't possibly be.
	wrong := os.Getuid() + 100000
	stop, err := ServeMCP(sock, GatedFns{}, StoreFns{}, "test", nil, wrong)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	// Server should close the conn after reading peer cred.
	c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var buf [1]byte
	_, err = c.Read(buf[:])
	// Expect EOF/closed, not timeout.
	if err == nil {
		t.Fatalf("expected conn to be closed, got nil err")
	}
	if strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected conn closed, got timeout: %v", err)
	}
}

// Spec 5/G — engage/disengage roundtrip with authz. Caller folder
// must own the conversation (EngagedFolder match OR JID default route).
func TestServeMCP_Engagement_Authz(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	var stored time.Time
	var storedFolder string
	db := StoreFns{
		SetEngagement: func(jid, topic, folder string, until time.Time) error {
			stored = until
			storedFolder = folder
			return nil
		},
		EngagedFolder: func(jid, topic string) string {
			switch jid {
			case "tg:1":
				return "world"
			case "tg:9":
				return "someone-else"
			}
			return ""
		},
		JIDRoutedToFolder: func(jid, folder string) bool {
			return jid == "tg:2" && folder == "world"
		},
	}
	gated := GatedFns{EngagementTTL: 10 * time.Minute}
	stop, err := ServeMCP(sock, gated, db, "world", []string{"*"}, 0)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	// Owned via EngagedFolder match → engage succeeds.
	_, errText := callTool(t, sock, "engage", map[string]any{"jid": "tg:1"})
	if errText != "" {
		t.Fatalf("engage (own folder via EngagedFolder): %s", errText)
	}
	if stored.IsZero() {
		t.Fatal("expected non-zero engaged_until")
	}

	// Owned via JID default route → engage succeeds.
	stored = time.Time{}
	_, errText = callTool(t, sock, "engage", map[string]any{"jid": "tg:2"})
	if errText != "" {
		t.Fatalf("engage (own folder via route): %s", errText)
	}
	if stored.IsZero() {
		t.Fatal("expected non-zero engaged_until")
	}

	// Active engagement owned by another folder → engage denied.
	_, errText = callTool(t, sock, "engage", map[string]any{"jid": "tg:9"})
	if errText == "" {
		t.Fatal("expected denial for foreign-owned jid")
	}
	if !strings.Contains(errText, "owned by") {
		t.Fatalf("unexpected denial message: %s", errText)
	}

	// All engage writes claim engaged_folder = caller (fix 5).
	if storedFolder != "world" {
		t.Fatalf("engaged_folder = %q, want world", storedFolder)
	}

	// disengage on owned jid writes zero time.
	stored = time.Now().Add(time.Minute) // sentinel non-zero
	_, errText = callTool(t, sock, "disengage", map[string]any{"jid": "tg:1"})
	if errText != "" {
		t.Fatalf("disengage: %s", errText)
	}
	if !stored.IsZero() {
		t.Fatalf("disengage should pass zero time, got %v", stored)
	}

	// disengage on foreign-owned jid denied.
	_, errText = callTool(t, sock, "disengage", map[string]any{"jid": "tg:9"})
	if errText == "" {
		t.Fatal("expected disengage denial")
	}
}

// Spec 5/G fix 5 — fresh chat (no current engagement, no default route) is
// engageable by any caller. Lets autonomous turns bootstrap conversations
// without a pre-existing route.
func TestServeMCP_Engagement_FreshChatAuthz(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	var stored time.Time
	var storedFolder string
	db := StoreFns{
		SetEngagement: func(jid, topic, folder string, until time.Time) error {
			stored = until
			storedFolder = folder
			return nil
		},
		EngagedFolder:     func(jid, topic string) string { return "" }, // no prior engagement
		JIDRoutedToFolder: func(jid, folder string) bool { return false },
	}
	gated := GatedFns{EngagementTTL: 10 * time.Minute}
	stop, err := ServeMCP(sock, gated, db, "world", []string{"*"}, 0)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	_, errText := callTool(t, sock, "engage", map[string]any{"jid": "tg:fresh"})
	if errText != "" {
		t.Fatalf("engage on fresh chat: %s", errText)
	}
	if stored.IsZero() {
		t.Fatal("expected non-zero engaged_until on fresh chat")
	}
	if storedFolder != "world" {
		t.Fatalf("engaged_folder = %q, want world", storedFolder)
	}
}

// Spec 5/G fix 2 — recordOutbound writes last_reply for every conversational
// outbound but skips the engagement bump when the turn's trigger sender is
// timed-* (scheduled / autonomous broadcasts must not extend engagement).
func TestRecordOutbound_TimedSkipsEngagement(t *testing.T) {
	var lastReplyWrites, bumps int
	var triggerLookup string
	mkDB := func(triggerSender string) StoreFns {
		return StoreFns{
			SetLastReply: func(jid, topic, replyID, folder string) error {
				lastReplyWrites++
				return nil
			},
			BumpEngagement: func(jid, topic, folder string, until time.Time) error {
				bumps++
				return nil
			},
			CurrentTriggerSender: func(folder string) string {
				triggerLookup = folder
				return triggerSender
			},
		}
	}
	gated := GatedFns{EngagementTTL: 10 * time.Minute}

	// Human-triggered turn: bump fires.
	db := mkDB("user:42")
	recordOutbound(gated, db, "tg:1", "hi", "plat-1", "world")
	if lastReplyWrites != 1 || bumps != 1 {
		t.Fatalf("user turn: writes=%d bumps=%d, want 1/1", lastReplyWrites, bumps)
	}
	if triggerLookup != "world" {
		t.Fatalf("CurrentTriggerSender called with %q, want world", triggerLookup)
	}

	// Timed-triggered turn: bump skipped, last_reply still written.
	lastReplyWrites, bumps, triggerLookup = 0, 0, ""
	db = mkDB("timed-isolated-abc")
	recordOutbound(gated, db, "tg:1", "hi", "plat-2", "world")
	if lastReplyWrites != 1 {
		t.Fatalf("timed turn: SetLastReply writes=%d, want 1", lastReplyWrites)
	}
	if bumps != 0 {
		t.Fatalf("timed turn: bumps=%d, want 0 (timed-* must not engage)", bumps)
	}

	// Empty platformID (failed send): neither write fires.
	lastReplyWrites, bumps = 0, 0
	db = mkDB("user:42")
	recordOutbound(gated, db, "tg:1", "hi", "", "world")
	if lastReplyWrites != 0 || bumps != 0 {
		t.Fatalf("empty platformID: writes=%d bumps=%d, want 0/0", lastReplyWrites, bumps)
	}
}

func TestRouteTokens_IssueTierMatrix(t *testing.T) {
	var issued []string
	gated := GatedFns{
		IssueRouteToken: func(kind, owner, target, src, suffix string) (RouteTokenInfo, error) {
			issued = append(issued, kind+":"+owner+"→"+target)
			jid := "web:" + target
			if kind == "hook" {
				jid = "hook:" + target + "/" + src
			}
			return RouteTokenInfo{RawToken: "tok", JID: jid, OwnerFolder: owner}, nil
		},
		ListRouteTokens:  func(owner string) []RouteTokenInfo { return nil },
		RevokeRouteToken: func(jid, owner string) (bool, error) { return true, nil },
	}
	// Use grants "*" so the action tools are registered.
	rules := []string{"*"}

	// Tier 1 at "acme": can mint for self + descendants, not siblings.
	srv := buildMCPServer(gated, StoreFns{}, "acme", rules)
	if srv == nil {
		t.Fatal("nil server")
	}

	// Tier 2 at "acme/eng": cannot mint for "acme" (parent) or sibling "acme/ops".
	srv2 := buildMCPServer(gated, StoreFns{}, "acme/eng", rules)
	if srv2 == nil {
		t.Fatal("nil server")
	}

	// Tier 3+ at "a/b/c/d": no mint (cannot register tool — registerRaw still
	// registers since matching rules exist, but handler returns unauthorized).
	srv3 := buildMCPServer(gated, StoreFns{}, "a/b/c/d", rules)
	if srv3 == nil {
		t.Fatal("nil server")
	}
}
