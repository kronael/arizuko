package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

type mockChannel struct {
	name string
	jids []string
	mu   sync.Mutex
	sent []sentMsg
}

type sentMsg struct {
	jid  string
	text string
}

func (m *mockChannel) Name() string                             { return m.name }
func (m *mockChannel) Connect(_ context.Context) error          { return nil }
func (m *mockChannel) Disconnect() error                        { return nil }
func (m *mockChannel) Typing(_ string, _ bool) error            { return nil }
func (m *mockChannel) SendFile(_, _, _ string) error            { return nil }
func (m *mockChannel) Owns(jid string) bool {
	for _, j := range m.jids {
		if j == jid {
			return true
		}
	}
	return false
}

func (m *mockChannel) Send(jid, text, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentMsg{jid, text})
	return "", nil
}

func (m *mockChannel) lastSent() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		return ""
	}
	return m.sent[len(m.sent)-1].text
}

func testGateway(t *testing.T) (*Gateway, *store.Store) {
	t.Helper()
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	dir := t.TempDir()
	cfg := &core.Config{
		Name:          "test",
		MaxContainers: 2,
		DataDir:       dir,
		GroupsDir:     dir,
	}
	gw := New(cfg, s)
	return gw, s
}

func TestCmdText(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello", "hello"},
		{"/stop", "/stop"},
		{"[Doc: file.pdf] /stop", "/stop"},
		{"[Image] @root /new", "/new"},
		{"@root /ping", "/ping"},
		{"[Media] plain text", "plain text"},
		{"  [x] @y /chatid  ", "/chatid"},
		{"@only", ""},
		{"[bracket only]", ""},
	}
	for _, tc := range cases {
		got := cmdText(tc.in)
		if got != tc.want {
			t.Errorf("cmdText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsGatewayCommand(t *testing.T) {
	yes := []string{
		"/new", "/New", "/NEW",
		"/ping", "/chatid", "/stop",
		"[Doc: f.pdf] /stop",
		"@root /ping",
		"[x] @y /new some arg",
	}
	for _, s := range yes {
		if !isGatewayCommand(s) {
			t.Errorf("isGatewayCommand(%q) = false, want true", s)
		}
	}

	no := []string{
		"hello",
		"/approve",
		"/foo",
		"/newbie",
		"@root plain text",
		"",
	}
	for _, s := range no {
		if isGatewayCommand(s) {
			t.Errorf("isGatewayCommand(%q) = true, want false", s)
		}
	}
}

func TestHandleCommand_RecognizedCommands(t *testing.T) {
	gw, _ := testGateway(t)
	ch := &mockChannel{name: "test", jids: []string{"jid1"}}
	gw.AddChannel(ch)
	gw.groups["jid1"] = core.Group{Folder: "grp", Name: "Test"}

	cmds := []string{"/new", "/ping", "/chatid", "/stop"}
	for _, c := range cmds {
		msg := core.Message{ChatJID: "jid1", Content: c}
		grp := gw.groups["jid1"]
		if !gw.handleCommand(msg, grp) {
			t.Errorf("handleCommand(%q) = false, want true", c)
		}
	}
}

func TestHandleCommand_NonCommand(t *testing.T) {
	gw, _ := testGateway(t)
	gw.groups["jid1"] = core.Group{Folder: "grp"}

	msg := core.Message{ChatJID: "jid1", Content: "hello world"}
	if gw.handleCommand(msg, gw.groups["jid1"]) {
		t.Error("handleCommand returned true for non-command")
	}

	msg.Content = "/unknown"
	if gw.handleCommand(msg, gw.groups["jid1"]) {
		t.Error("handleCommand returned true for unknown command")
	}
}

func TestCmdNew_ClearsSession(t *testing.T) {
	gw, s := testGateway(t)
	ch := &mockChannel{name: "test", jids: []string{"jid1"}}
	gw.AddChannel(ch)
	gw.groups["jid1"] = core.Group{Folder: "grp", Name: "Test"}

	s.SetSession("grp", "", "sess-123")
	if id, ok := s.GetSession("grp", ""); !ok || id == "" {
		t.Fatal("session not set")
	}

	msg := core.Message{ChatJID: "jid1", Content: "/new"}
	gw.handleCommand(msg, gw.groups["jid1"])

	if id, _ := s.GetSession("grp", ""); id != "" {
		t.Error("session not cleared after /new")
	}
}

func TestCmdChatID_SendsJID(t *testing.T) {
	gw, _ := testGateway(t)
	ch := &mockChannel{name: "test", jids: []string{"jid1"}}
	gw.AddChannel(ch)
	gw.groups["jid1"] = core.Group{Folder: "grp"}

	msg := core.Message{ChatJID: "jid1", Content: "/chatid"}
	gw.handleCommand(msg, gw.groups["jid1"])

	if got := ch.lastSent(); got != "jid1" {
		t.Errorf("chatid sent %q, want %q", got, "jid1")
	}
}

func TestGroupForJid_Found(t *testing.T) {
	gw, _ := testGateway(t)
	gw.groups["jid1"] = core.Group{Folder: "alpha", Name: "Alpha"}

	gr, ok := gw.groupForJid("jid1")
	if !ok {
		t.Fatal("groupForJid returned false for known JID")
	}
	if gr.Folder != "alpha" {
		t.Errorf("folder = %q, want %q", gr.Folder, "alpha")
	}
}

func TestGroupForJid_NotFound(t *testing.T) {
	gw, _ := testGateway(t)
	gw.groups["jid1"] = core.Group{Folder: "alpha"}

	_, ok := gw.groupForJid("jid999")
	if ok {
		t.Error("groupForJid returned true for unknown JID")
	}
}

func TestGroupForJid_LocalPrefix(t *testing.T) {
	gw, _ := testGateway(t)
	gw.groups["jid1"] = core.Group{Folder: "beta", Name: "Beta"}

	gr, ok := gw.groupForJid("local:beta")
	if !ok {
		t.Fatal("groupForJid returned false for local: prefix")
	}
	if gr.Name != "Beta" {
		t.Errorf("name = %q, want %q", gr.Name, "Beta")
	}
}

func TestResolveTarget_NoRoutes(t *testing.T) {
	msg := core.Message{Content: "hello"}
	if got := resolveTarget(msg, nil, "self"); got != "" {
		t.Errorf("resolveTarget = %q, want empty", got)
	}
}

func TestResolveTarget_MatchingRoute(t *testing.T) {
	routes := []core.Route{
		{Type: "default", Target: "other"},
	}
	msg := core.Message{Content: "hello"}
	got := resolveTarget(msg, routes, "self")
	if got != "other" {
		t.Errorf("resolveTarget = %q, want %q", got, "other")
	}
}

func TestResolveTarget_SelfFolder(t *testing.T) {
	routes := []core.Route{
		{Type: "default", Target: "self"},
	}
	msg := core.Message{Content: "hello"}
	got := resolveTarget(msg, routes, "self")
	if got != "" {
		t.Errorf("resolveTarget = %q, want empty (self)", got)
	}
}

func TestLoadState_LoadsGroups(t *testing.T) {
	gw, s := testGateway(t)
	s.PutGroup("jid1", core.Group{Folder: "alpha", Name: "Alpha"})
	s.PutGroup("jid2", core.Group{Folder: "beta", Name: "Beta"})

	gw.loadState()

	if len(gw.groups) != 2 {
		t.Errorf("groups count = %d, want 2", len(gw.groups))
	}
	if gw.groups["jid1"].Folder != "alpha" {
		t.Error("group jid1 not loaded correctly")
	}
}

func TestLoadState_MigratesOldCursors(t *testing.T) {
	gw, s := testGateway(t)
	s.PutGroup("jid1", core.Group{Folder: "grp"})

	ts := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)
	old := map[string]string{
		"jid1": ts.Format(time.RFC3339Nano),
	}
	raw, _ := json.Marshal(old)
	s.SetState("last_agent_timestamp", string(raw))

	gw.loadState()

	got := s.GetAgentCursor("jid1")
	if got.IsZero() {
		t.Fatal("agent cursor not migrated")
	}
	if !got.Equal(ts) {
		t.Errorf("cursor = %v, want %v", got, ts)
	}
	if s.GetState("last_agent_timestamp") != "" {
		t.Error("old cursor key not cleared")
	}
}

func TestSaveState_PersistsTimestamp(t *testing.T) {
	gw, s := testGateway(t)
	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	gw.lastTimestamp = ts

	gw.saveState()

	raw := s.GetState("last_timestamp")
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatalf("parse saved timestamp: %v", err)
	}
	if !parsed.Equal(ts) {
		t.Errorf("saved = %v, want %v", parsed, ts)
	}
}

func TestAdvanceAgentCursor(t *testing.T) {
	gw, s := testGateway(t)
	s.PutGroup("jid1", core.Group{Folder: "grp"})

	t1 := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 15, 11, 0, 0, 0, time.UTC)
	msgs := []core.Message{
		{Timestamp: t1},
		{Timestamp: t2},
	}

	gw.advanceAgentCursor("jid1", msgs)

	got := s.GetAgentCursor("jid1")
	if !got.Equal(t2) {
		t.Errorf("cursor = %v, want %v", got, t2)
	}
}

func TestAdvanceAgentCursor_Empty(t *testing.T) {
	gw, s := testGateway(t)
	s.PutGroup("jid1", core.Group{Folder: "grp"})
	prev := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s.SetAgentCursor("jid1", prev)

	gw.advanceAgentCursor("jid1", nil)

	got := s.GetAgentCursor("jid1")
	if got.IsZero() {
		t.Error("cursor reset on empty msgs")
	}
}

func TestAddRemoveChannel(t *testing.T) {
	gw, _ := testGateway(t)
	ch1 := &mockChannel{name: "ch1", jids: []string{"j1"}}
	ch2 := &mockChannel{name: "ch2", jids: []string{"j2"}}

	gw.AddChannel(ch1)
	gw.AddChannel(ch2)
	if len(gw.channels) != 2 {
		t.Fatalf("channels = %d, want 2", len(gw.channels))
	}

	gw.RemoveChannel("ch1")
	if len(gw.channels) != 1 {
		t.Fatalf("channels after remove = %d, want 1", len(gw.channels))
	}
	if gw.channels[0].Name() != "ch2" {
		t.Error("wrong channel removed")
	}
}

func TestFindChannel(t *testing.T) {
	gw, _ := testGateway(t)
	ch := &mockChannel{name: "tg", jids: []string{"jid1", "jid2"}}
	gw.AddChannel(ch)

	found := gw.findChannel("jid1")
	if found == nil || found.Name() != "tg" {
		t.Error("findChannel did not return owning channel")
	}

	if gw.findChannel("jid999") != nil {
		t.Error("findChannel returned non-nil for unknown jid")
	}
}

func TestEmitSystemEvents_NewDay(t *testing.T) {
	gw, s := testGateway(t)
	grp := core.Group{Folder: "grp1", Name: "Test"}
	s.PutGroup("jid1", grp)
	gw.groups["jid1"] = grp

	yesterday := time.Now().AddDate(0, 0, -1)
	s.SetAgentCursor("jid1", yesterday)
	s.SetSession("grp1", "", "sess-1")

	gw.emitSystemEvents(grp, "jid1")

	out := s.FlushSysMsgs("grp1")
	if out == "" {
		t.Fatal("no system events emitted")
	}
	if !strings.Contains(out, "new_day") {
		t.Errorf("expected new_day event, got: %s", out)
	}
}

func TestEmitSystemEvents_NewSession(t *testing.T) {
	gw, s := testGateway(t)
	grp := core.Group{Folder: "grp2", Name: "Test"}
	gw.groups["jid2"] = grp

	s.SetAgentCursor("jid2", time.Now())

	gw.emitSystemEvents(grp, "jid2")

	out := s.FlushSysMsgs("grp2")
	if out == "" {
		t.Fatal("no system events emitted")
	}
	if !strings.Contains(out, "new_session") {
		t.Errorf("expected new_session event, got: %s", out)
	}
}
