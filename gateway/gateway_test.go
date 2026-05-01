package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/router"
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
func (m *mockChannel) SendFile(_, _, _, _ string) error         { return nil }
func (m *mockChannel) Owns(jid string) bool {
	for _, j := range m.jids {
		if j == jid {
			return true
		}
	}
	return false
}

func (m *mockChannel) Send(jid, text, _, _, _ string) (string, error) {
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
		IpcDir:        filepath.Join(dir, "ipc"),
		GroupsDir:     dir,
	}
	gw := New(cfg, s)
	return gw, s
}

// setGroup is a test helper that registers a group in the DB.
func setGroup(gw *Gateway, jid string, g core.Group) {
	gw.store.PutGroup(g)
	match := "room=" + core.JidRoom(jid)
	gw.store.AddRoute(core.Route{Seq: 0, Match: match, Target: g.Folder})
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
		"/root", "/root hello world",
		"[Doc: f.pdf] /stop",
		"@root /ping",
		"[x] @y /new some arg",
		"/new@mybot",
		"/stop@some_bot_name",
		"/ping@BOT",
	}
	for _, s := range yes {
		if !isGatewayCommand(s) {
			t.Errorf("isGatewayCommand(%q) = false, want true", s)
		}
	}

	no := []string{
		"hello",
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
	setGroup(gw, "jid1", core.Group{Folder: "grp", Name: "Test"})

	cmds := []string{"/new", "/ping", "/chatid", "/stop", "/approve", "/reject"}
	for _, c := range cmds {
		msg := core.Message{ChatJID: "jid1", Content: c}
		grp, _ := gw.store.GroupByFolder("grp")
		if !gw.handleCommand(msg, grp) {
			t.Errorf("handleCommand(%q) = false, want true", c)
		}
	}
}

func TestHandleCommand_NonCommand(t *testing.T) {
	gw, _ := testGateway(t)
	setGroup(gw, "jid1", core.Group{Folder: "grp"})

	grp, _ := gw.store.GroupByFolder("grp")
	msg := core.Message{ChatJID: "jid1", Content: "hello world"}
	if gw.handleCommand(msg, grp) {
		t.Error("handleCommand returned true for non-command")
	}

	msg.Content = "/unknown"
	if gw.handleCommand(msg, grp) {
		t.Error("handleCommand returned true for unknown command")
	}
}

func TestCmdNew_ClearsSession(t *testing.T) {
	gw, s := testGateway(t)
	ch := &mockChannel{name: "test", jids: []string{"jid1"}}
	gw.AddChannel(ch)
	setGroup(gw, "jid1", core.Group{Folder: "grp", Name: "Test"})

	s.SetSession("grp", "", "sess-123")
	if id, ok := s.GetSession("grp", ""); !ok || id == "" {
		t.Fatal("session not set")
	}

	grp, _ := gw.store.GroupByFolder("grp")
	msg := core.Message{ChatJID: "jid1", Content: "/new"}
	gw.handleCommand(msg, grp)

	if id, _ := s.GetSession("grp", ""); id != "" {
		t.Error("session not cleared after /new")
	}
}

func TestCmdApproveReject_NotConfigured(t *testing.T) {
	for _, cmd := range []string{"/approve", "/reject"} {
		gw, _ := testGateway(t)
		ch := &mockChannel{name: "test", jids: []string{"jid1"}}
		gw.AddChannel(ch)
		setGroup(gw, "jid1", core.Group{Folder: "grp"})

		grp, _ := gw.store.GroupByFolder("grp")
		msg := core.Message{ChatJID: "jid1", Content: cmd}
		if !gw.handleCommand(msg, grp) {
			t.Fatalf("%s handled = false", cmd)
		}
		if got := ch.lastSent(); got != "HITL not configured" {
			t.Errorf("%s sent %q, want %q", cmd, got, "HITL not configured")
		}
	}
}

func TestCmdChatID_SendsJID(t *testing.T) {
	gw, _ := testGateway(t)
	ch := &mockChannel{name: "test", jids: []string{"jid1"}}
	gw.AddChannel(ch)
	setGroup(gw, "jid1", core.Group{Folder: "grp"})

	grp, _ := gw.store.GroupByFolder("grp")
	msg := core.Message{ChatJID: "jid1", Content: "/chatid"}
	gw.handleCommand(msg, grp)

	if got := ch.lastSent(); got != "jid1" {
		t.Errorf("chatid sent %q, want %q", got, "jid1")
	}
}

func TestCmdRoot_DelegatesToRoot(t *testing.T) {
	gw, s := testGateway(t)
	ch := &mockChannel{name: "test", jids: []string{"jid1"}}
	gw.AddChannel(ch)

	// Register root group and child group
	s.PutGroup(core.Group{Folder: "world", Name: "World"})
	setGroup(gw, "jid1", core.Group{Folder: "world/child", Name: "Child"})

	grp, _ := gw.store.GroupByFolder("world/child")

	// No arg → usage
	msg := core.Message{ChatJID: "jid1", Content: "/root"}
	gw.handleCommand(msg, grp)
	if got := ch.lastSent(); got != "Usage: /root <message>" {
		t.Errorf("empty arg sent %q, want usage", got)
	}

	// With arg → delegates to root
	msg.Content = "/root hello from child"
	gw.handleCommand(msg, grp)

	delegated, _ := s.MessagesSince("world", time.Time{}, "nobot")
	found := false
	for _, m := range delegated {
		if m.Content == "hello from child" && m.Sender == "delegate" {
			found = true
		}
	}
	if !found {
		t.Error("delegation message not found in root group")
	}
}

func TestCmdRoot_AlreadyRoot(t *testing.T) {
	gw, _ := testGateway(t)
	ch := &mockChannel{name: "test", jids: []string{"jid1"}}
	gw.AddChannel(ch)
	setGroup(gw, "jid1", core.Group{Folder: "world", Name: "World"})

	grp, _ := gw.store.GroupByFolder("world")
	msg := core.Message{ChatJID: "jid1", Content: "/root hello"}
	gw.handleCommand(msg, grp)

	if got := ch.lastSent(); got != "Already in root group." {
		t.Errorf("root group sent %q, want already-in-root", got)
	}
}

func TestResolveGroup_Found(t *testing.T) {
	gw, _ := testGateway(t)
	setGroup(gw, "jid1", core.Group{Folder: "alpha", Name: "Alpha"})

	msg := core.Message{ChatJID: "jid1", Sender: "user", Verb: "message"}
	gr, ok := gw.resolveGroup(msg)
	if !ok {
		t.Fatal("resolveGroup returned false for routed message")
	}
	if gr.Folder != "alpha" {
		t.Errorf("folder = %q, want %q", gr.Folder, "alpha")
	}
}

func TestResolveGroup_NotFound(t *testing.T) {
	gw, _ := testGateway(t)
	setGroup(gw, "jid1", core.Group{Folder: "alpha"})

	msg := core.Message{ChatJID: "jid999", Verb: "message"}
	_, ok := gw.resolveGroup(msg)
	if ok {
		t.Error("resolveGroup returned true for unrouted message")
	}
}

func TestResolveGroup_BareFolder(t *testing.T) {
	gw, s := testGateway(t)
	s.PutGroup(core.Group{Folder: "beta", Name: "Beta"})

	msg := core.Message{ChatJID: "beta", Verb: "message"}
	gr, ok := gw.resolveGroup(msg)
	if !ok {
		t.Fatal("resolveGroup returned false for bare folder JID")
	}
	if gr.Name != "Beta" {
		t.Errorf("name = %q, want %q", gr.Name, "Beta")
	}
}

func TestResolveTarget_NoRoutes(t *testing.T) {
	gw, _ := testGateway(t)
	msg := core.Message{Content: "hello"}
	if got := gw.resolveTarget(msg, nil, "self"); got != "" {
		t.Errorf("resolveTarget = %q, want empty", got)
	}
}

func TestResolveTarget_MatchingRoute(t *testing.T) {
	gw, _ := testGateway(t)
	routes := []core.Route{
		{Match: "", Target: "other"},
	}
	msg := core.Message{Content: "hello"}
	got := gw.resolveTarget(msg, routes, "self")
	if got != "other" {
		t.Errorf("resolveTarget = %q, want %q", got, "other")
	}
}

func TestResolveTarget_SelfFolder(t *testing.T) {
	gw, _ := testGateway(t)
	routes := []core.Route{
		{Match: "", Target: "self"},
	}
	msg := core.Message{Content: "hello"}
	got := gw.resolveTarget(msg, routes, "self")
	if got != "" {
		t.Errorf("resolveTarget = %q, want empty (self)", got)
	}
}

func TestLoadState_LoadsGroups(t *testing.T) {
	gw, s := testGateway(t)
	s.PutGroup(core.Group{Folder: "alpha", Name: "Alpha"})
	s.PutGroup(core.Group{Folder: "beta", Name: "Beta"})
	s.AddRoute(core.Route{Match: "room=jid1", Target: "alpha"})
	s.AddRoute(core.Route{Match: "room=jid2", Target: "beta"})

	gw.loadState()

	groups := s.AllGroups()
	if len(groups) != 2 {
		t.Errorf("groups count = %d, want 2", len(groups))
	}
	if groups["alpha"].Name != "Alpha" {
		t.Error("group alpha not loaded correctly")
	}
	if folder := s.DefaultFolderForJID("jid1"); folder != "alpha" {
		t.Errorf("DefaultFolderForJID(jid1) = %q, want alpha", folder)
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
	prev := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s.SetAgentCursor("jid1", prev)

	gw.advanceAgentCursor("jid1", nil)

	got := s.GetAgentCursor("jid1")
	if got.IsZero() {
		t.Error("cursor reset on empty msgs")
	}
}

// Regression: pollOnce records steered timestamps so advanceAgentCursor
// can include them when the container completes. The cursor is NOT
// advanced during steer — only on container completion.
func TestPollOnce_SteerRecordsTimestamp(t *testing.T) {
	gw, s := testGateway(t)
	gw.cfg.MaxContainers = 2

	jid := "telegram:1"
	setGroup(gw, jid, core.Group{Folder: "grp", Name: "Group"})

	// Simulate an active container: pollOnce's steering branch only
	// fires when queue.SendMessages sees active=true + folder set.
	gw.queue.SetActiveForTest(jid, "fake-container-name", "grp")

	ts := time.Now().UTC()
	if err := s.PutMessage(core.Message{
		ID: "m1", ChatJID: jid, Sender: "user", Name: "User",
		Content: "hello", Timestamp: ts,
	}); err != nil {
		t.Fatal(err)
	}

	gw.pollOnce()

	// Cursor should NOT be advanced by steer alone.
	got := s.GetAgentCursor(jid)
	if !got.IsZero() {
		t.Errorf("cursor = %v, want zero (steer must not advance cursor)", got)
	}

	// steeredTs should be recorded.
	gw.mu.RLock()
	st, ok := gw.steeredTs[jid]
	gw.mu.RUnlock()
	if !ok || st.Before(ts) {
		t.Errorf("steeredTs = %v (ok=%v), want >= %v", st, ok, ts)
	}

	// advanceAgentCursor should incorporate the steered timestamp.
	earlier := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	gw.advanceAgentCursor(jid, []core.Message{{Timestamp: earlier}})
	got = s.GetAgentCursor(jid)
	if got.Before(ts) {
		t.Errorf("cursor after advance = %v, want >= %v (must include steered ts)", got, ts)
	}
}

// Regression: pollOnce must advance the agent cursor after handlePrefixLayer
// absorbs a message (real child delegation). Otherwise messages would be
// re-processed on every restart.
func TestPollOnce_PrefixRouteAdvancesCursor(t *testing.T) {
	gw, s := testGateway(t)
	gw.cfg.MaxContainers = 0 // queue tasks without running them

	jid := "telegram:1"
	setGroup(gw, jid, core.Group{Folder: "grp", Name: "Group"})
	// Child group must exist for the prefix-delegation path.
	s.PutGroup(core.Group{Folder: "grp/child", Name: "Child"})

	ts := time.Now().UTC()
	if err := s.PutMessage(core.Message{
		ID: "m1", ChatJID: jid, Sender: "user", Name: "User",
		Content: "@child hello", Timestamp: ts,
	}); err != nil {
		t.Fatal(err)
	}

	gw.pollOnce()

	got := s.GetAgentCursor(jid)
	if got.IsZero() || got.Before(ts) {
		t.Errorf("cursor = %v, want >= %v (prefix-route path must advance cursor)", got, ts)
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

func TestFindChannelForJID(t *testing.T) {
	gw, _ := testGateway(t)
	ch := &mockChannel{name: "tg", jids: []string{"jid1", "jid2"}}
	gw.AddChannel(ch)

	found := gw.findChannelForJID("jid1")
	if found == nil || found.Name() != "tg" {
		t.Error("findChannelForJID did not return owning channel")
	}

	if gw.findChannelForJID("jid999") != nil {
		t.Error("findChannelForJID returned non-nil for unknown jid")
	}
}

func TestFindChannelForJID_LatestSourceWins(t *testing.T) {
	gw, s := testGateway(t)
	ch1 := &mockChannel{name: "tg1", jids: []string{"telegram:100", "telegram:999"}}
	ch2 := &mockChannel{name: "tg2", jids: []string{"telegram:100"}}
	gw.AddChannel(ch1)
	gw.AddChannel(ch2)

	// No prior message: prefix fallback chooses first owner.
	if found := gw.findChannelForJID("telegram:100"); found == nil || found.Name() != "tg1" {
		t.Errorf("prefix fallback want tg1, got %v", found)
	}

	// Record an inbound message via tg2 → outbound should follow source.
	if err := s.PutMessage(core.Message{
		ID: "m1", ChatJID: "telegram:100", Sender: "u",
		Content: "hi", Timestamp: time.Now(), Source: "tg2",
	}); err != nil {
		t.Fatalf("PutMessage: %v", err)
	}
	if found := gw.findChannelForJID("telegram:100"); found == nil || found.Name() != "tg2" {
		t.Errorf("latest source want tg2, got %v", found)
	}

	// Unrecorded JID still resolves via owns().
	if found := gw.findChannelForJID("telegram:999"); found == nil || found.Name() != "tg1" {
		t.Errorf("unrecorded JID want tg1 via owns, got %v", found)
	}
}

func TestEmitSystemEvents_NewDay(t *testing.T) {
	gw, s := testGateway(t)
	grp := core.Group{Folder: "grp1", Name: "Test"}
	setGroup(gw, "jid1", grp)

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
	s.PutGroup(grp)

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

func TestPreviousSessionXML_Empty(t *testing.T) {
	if got := previousSessionXML(nil); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestPreviousSessionXML_WithRecord(t *testing.T) {
	now := time.Now()
	ended := now.Add(time.Minute)
	rec := core.SessionRecord{
		SessionID: "abc123def456",
		StartedAt: now,
		EndedAt:   &ended,
		MsgCount:  7,
		Result:    "ok",
	}
	got := previousSessionXML([]core.SessionRecord{rec})
	if !strings.Contains(got, "previous_session") {
		t.Errorf("expected previous_session tag, got %q", got)
	}
	if !strings.Contains(got, `msgs="7"`) {
		t.Errorf("expected msgs=7, got %q", got)
	}
	if !strings.Contains(got, `result="ok"`) {
		t.Errorf("expected result=ok, got %q", got)
	}
	if !strings.Contains(got, `"abc123de"`) {
		t.Errorf("expected truncated session id, got %q", got)
	}
}

func TestPreviousSessionXML_NoEndedAt(t *testing.T) {
	rec := core.SessionRecord{
		SessionID: "xyz",
		StartedAt: time.Now(),
		Result:    "ok",
	}
	got := previousSessionXML([]core.SessionRecord{rec})
	if !strings.Contains(got, "previous_session") {
		t.Errorf("expected previous_session tag, got %q", got)
	}
	if !strings.Contains(got, `ended=""`) {
		t.Errorf("expected empty ended, got %q", got)
	}
}

func TestParsePrefix(t *testing.T) {
	// Regression notes:
	//   "twitter content" — marinade atlas 2026-04-11 forwarded tweet misroute
	//   "hash mid-sentence" — symmetric with @ prefix
	cases := []struct {
		name, in, wantName, wantRest string
		wantOK                       bool
	}{
		{"@ at start", "@alice hello world", "alice", "hello world", true},
		{"@ mid-content ignored", "hello @alice world", "", "", false},
		{"twitter content ignored", "Solana's Napster Era Is Over\nbuffalu\n@buffalu__\n·\n7h", "", "", false},
		{"leading whitespace", "  @alice hello", "alice", "hello", true},
		{"# at start", "#topic rest of message", "topic", "rest of message", true},
		{"# mid-sentence ignored", "ask #general for help", "", "", false},
		{"none", "no prefix here", "", "", false},
		{"empty", "", "", "", false},
	}
	for _, c := range cases {
		name, rest, ok := parsePrefix(c.in)
		if ok != c.wantOK {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.wantOK)
			continue
		}
		if ok && (name != c.wantName || rest != c.wantRest) {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", c.name, name, rest, c.wantName, c.wantRest)
		}
	}
}

func TestExtFromMime(t *testing.T) {
	if got := extFromMime("image/jpeg", "photo.jpg"); got != ".jpg" {
		t.Errorf("extFromMime with filename = %q, want .jpg", got)
	}
	if got := extFromMime("image/jpeg", "photo.JPEG"); got != ".jpeg" {
		t.Errorf("extFromMime with uppercase ext = %q, want .jpeg", got)
	}

	if got := extFromMime("application/octet-stream", "noext"); got != ".bin" {
		t.Errorf("extFromMime bin fallback = %q, want .bin", got)
	}

	for _, m := range []string{"image/jpeg", "image/png", "audio/ogg", "audio/mpeg", "video/mp4"} {
		got := extFromMime(m, "")
		if got == "" {
			t.Errorf("extFromMime(%q, \"\") returned empty", m)
		}
		if got[0] != '.' {
			t.Errorf("extFromMime(%q, \"\") = %q, want leading dot", m, got)
		}
	}

	// Regression: WhatsApp photos arrive with mime=image/jpeg and no filename.
	// Go's mime.ExtensionsByType returns `.jfif` (Debian) or `.jpe` (musl)
	// first; Claude's Read tool only recognizes `.jpg`/`.jpeg`. Pin canonical
	// extensions for the common types.
	canonical := map[string]string{
		"image/jpeg": ".jpg",
		"image/png":  ".png",
		"image/gif":  ".gif",
		"image/webp": ".webp",
		"audio/ogg":  ".ogg",
	}
	for m, want := range canonical {
		if got := extFromMime(m, ""); got != want {
			t.Errorf("extFromMime(%q, \"\") = %q, want %q", m, got, want)
		}
	}
}

func TestEnrichAttachments_MediaDisabled(t *testing.T) {
	gw, _ := testGateway(t)

	msg := core.Message{
		ID:          "m1",
		Content:     "[Photo]",
		Attachments: `[{"mime":"image/jpeg","filename":"photo.jpg","url":"http://teled:9001/files/abc","size":1024}]`,
	}
	gw.enrichAttachments(context.Background(), &msg, "grp")

	if msg.Content != "[Photo]" {
		t.Errorf("content changed when MediaEnabled=false: %q", msg.Content)
	}
	if msg.Attachments == "" {
		t.Error("attachments should not be cleared when MediaEnabled=false")
	}
}

func TestEnrichAttachments_DownloadsFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("JFIF...fake image data"))
	}))
	defer srv.Close()

	gw, s := testGateway(t)
	gw.cfg.MediaEnabled = true
	gw.cfg.MediaMaxBytes = 10 * 1024 * 1024

	grp := core.Group{Folder: "grp", Name: "Test"}
	setGroup(gw, "jid1", grp)

	atts := `[{"mime":"image/jpeg","filename":"photo.jpg","url":"` + srv.URL + `/photo.jpg","size":22}]`
	msg := core.Message{
		ID:          "m-enrich",
		ChatJID:     "jid1",
		Sender:      "user",
		Content:     "[Photo]",
		Timestamp:   time.Now(),
		Attachments: atts,
	}
	s.PutMessage(msg)

	gw.enrichAttachments(context.Background(), &msg, "grp")

	if !strings.Contains(msg.Content, "<attachment") {
		t.Errorf("enriched content should contain attachment XML, got %q", msg.Content)
	}
	if !strings.Contains(msg.Content, "/home/node/media/") {
		t.Errorf("attachment path should be container-absolute (/home/node/media/...), got %q", msg.Content)
	}
	if msg.Attachments != "" {
		t.Errorf("attachments should be cleared after enrich, got %q", msg.Attachments)
	}

	msgs, _, _ := s.NewMessages([]string{"jid1"}, time.Time{}, "bot")
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "<attachment") {
		t.Errorf("DB content should contain attachment XML, got %q", msgs[0].Content)
	}
}

func TestEnrichAttachments_SkipsEmptyURL(t *testing.T) {
	gw, s := testGateway(t)
	gw.cfg.MediaEnabled = true

	grp := core.Group{Folder: "grp2", Name: "Test"}
	setGroup(gw, "jid2", grp)

	atts := `[{"mime":"image/jpeg","filename":"photo.jpg","url":"","size":0}]`
	msg := core.Message{
		ID: "m-nurl", ChatJID: "jid2", Sender: "user",
		Content: "[Photo]", Timestamp: time.Now(), Attachments: atts,
	}
	s.PutMessage(msg)

	gw.enrichAttachments(context.Background(), &msg, "grp2")

	if strings.Contains(msg.Content, "<attachment") {
		t.Error("should not add attachment XML when URL is empty")
	}
}

// --- delivery pipeline test helpers ---

type testChannel struct {
	name    string
	jids    []string
	mu      sync.Mutex
	sent    []testSentMsg
	sendErr error
	sendID  string
	counter int
}

type testSentMsg struct {
	jid      string
	text     string
	replyTo  string
	threadID string
}

func (c *testChannel) Name() string                    { return c.name }
func (c *testChannel) Connect(_ context.Context) error { return nil }
func (c *testChannel) Disconnect() error               { return nil }
func (c *testChannel) Typing(_ string, _ bool) error   { return nil }
func (c *testChannel) SendFile(_, _, _, _ string) error { return nil }
func (c *testChannel) Owns(jid string) bool {
	for _, j := range c.jids {
		if strings.HasPrefix(jid, j) {
			return true
		}
	}
	return false
}

func (c *testChannel) Send(jid, text, replyTo, threadID, _ string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sendErr != nil {
		return "", c.sendErr
	}
	c.sent = append(c.sent, testSentMsg{jid, text, replyTo, threadID})
	c.counter++
	if c.sendID != "" {
		return c.sendID, nil
	}
	return fmt.Sprintf("sent-%d", c.counter), nil
}

func (c *testChannel) getSent() []testSentMsg {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]testSentMsg, len(c.sent))
	copy(cp, c.sent)
	return cp
}

// --- delivery pipeline tests ---

func TestMakeOutputCallback_SendsReply(t *testing.T) {
	gw, s := testGateway(t)
	ch := &testChannel{name: "tc", jids: []string{"jid1"}}
	gw.AddChannel(ch)
	setGroup(gw, "jid1", core.Group{Folder: "grp", Name: "Test"})

	cb, hadOutput := gw.makeOutputCallback(ch, "jid1", "", "msg-1", "grp")
	cb("Hello from agent", "")

	if !*hadOutput {
		t.Error("hadOutput should be true")
	}
	sent := ch.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent count = %d, want 1", len(sent))
	}
	if sent[0].text != "Hello from agent" {
		t.Errorf("sent text = %q, want %q", sent[0].text, "Hello from agent")
	}
	if sent[0].replyTo != "msg-1" {
		t.Errorf("replyTo = %q, want %q", sent[0].replyTo, "msg-1")
	}

	replyID := s.GetLastReplyID("jid1", "")
	if replyID == "" {
		t.Error("last reply ID not stored in DB")
	}
}

func TestMakeOutputCallback_SendError(t *testing.T) {
	gw, _ := testGateway(t)
	ch := &testChannel{name: "tc", jids: []string{"jid1"}, sendErr: errors.New("network down")}
	gw.AddChannel(ch)
	setGroup(gw, "jid1", core.Group{Folder: "grp", Name: "Test"})

	cb, hadOutput := gw.makeOutputCallback(ch, "jid1", "", "msg-1", "grp")
	cb("Error test", "")

	if !*hadOutput {
		t.Error("hadOutput should be true even on send error")
	}
	sent := ch.getSent()
	if len(sent) != 0 {
		t.Errorf("sent count = %d, want 0 (error path)", len(sent))
	}
}

func TestMakeOutputCallback_EmptySentID(t *testing.T) {
	gw, s := testGateway(t)
	ch := &testChannel{name: "tc", jids: []string{"jid1"}}
	gw.AddChannel(ch)
	gw.cfg.SendDisabledChannels = []string{"jid1"}
	setGroup(gw, "jid1", core.Group{Folder: "grp", Name: "Test"})

	cb, hadOutput := gw.makeOutputCallback(ch, "jid1", "", "msg-1", "grp")
	cb("Suppressed message", "")

	if !*hadOutput {
		t.Error("hadOutput should be true even when send suppressed")
	}
	msgs, _, _ := s.NewMessages([]string{"jid1"}, time.Time{}, "bot")
	for _, m := range msgs {
		if m.Content == "Suppressed message" {
			t.Error("suppressed message should not be stored")
		}
	}
}

func TestMakeOutputCallback_StripsThinksAndStatus(t *testing.T) {
	gw, _ := testGateway(t)
	ch := &testChannel{name: "tc", jids: []string{"jid1"}}
	gw.AddChannel(ch)
	setGroup(gw, "jid1", core.Group{Folder: "grp", Name: "Test"})

	cb, hadOutput := gw.makeOutputCallback(ch, "jid1", "", "msg-1", "grp")
	cb("<think>internal thought</think>Visible reply<status>Working on it</status>", "")

	if !*hadOutput {
		t.Error("hadOutput should be true")
	}
	sent := ch.getSent()
	if len(sent) != 2 {
		t.Fatalf("sent count = %d, want 2 (status + reply)", len(sent))
	}
	if sent[0].text != "⏳ Working on it" {
		t.Errorf("status text = %q, want %q", sent[0].text, "⏳ Working on it")
	}
	if sent[1].text != "Visible reply" {
		t.Errorf("reply text = %q, want %q", sent[1].text, "Visible reply")
	}
}

// SEND_DISABLED_GROUPS mute mode (ebb3225): outbound persists to the
// messages table but the channel.Send call is skipped. hadOutput must
// still flip so the gateway sees the agent produced something.
func TestMakeOutputCallback_MutedGroup(t *testing.T) {
	gw, s := testGateway(t)
	gw.cfg.SendDisabledGroups = []string{"grp"}
	ch := &testChannel{name: "tc", jids: []string{"telegram"}}
	gw.AddChannel(ch)
	setGroup(gw, "telegram:12345", core.Group{Folder: "grp", Name: "Test"})

	cb, hadOutput := gw.makeOutputCallback(ch, "telegram:12345", "", "msg-1", "grp")
	cb("hello world", "")

	if got := len(ch.getSent()); got != 0 {
		t.Errorf("muted group: channel.Send called %d times, want 0", got)
	}
	if !*hadOutput {
		t.Error("hadOutput should flip even when group is muted")
	}

	rows, err := s.MessagesAll("telegram:12345", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("muted group: persisted row count = %d, want 1", len(rows))
	}
	m := rows[0]
	if m.Content != "hello world" {
		t.Errorf("Content = %q, want %q", m.Content, "hello world")
	}
	if !m.BotMsg {
		t.Error("BotMsg should be true for outbound")
	}
	if m.Sender != "grp" {
		t.Errorf("Sender = %q, want %q", m.Sender, "grp")
	}
	if m.RoutedTo != "telegram:12345" {
		t.Errorf("RoutedTo = %q, want %q", m.RoutedTo, "telegram:12345")
	}
}

func TestMakeOutputCallback_ThreadID(t *testing.T) {
	gw, _ := testGateway(t)
	ch := &testChannel{name: "tc", jids: []string{"jid1"}}
	gw.AddChannel(ch)
	setGroup(gw, "jid1", core.Group{Folder: "grp", Name: "Test"})

	cb, _ := gw.makeOutputCallback(ch, "jid1", "#general", "msg-1", "grp")
	cb("Threaded reply", "")

	sent := ch.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent count = %d, want 1", len(sent))
	}
	if sent[0].threadID != "#general" {
		t.Errorf("threadID = %q, want %q", sent[0].threadID, "#general")
	}
}

// Regression for marinade 2026-04-11 21:39:09 "no channel for jid" 18s
// after telegram registered. processSenderBatch captured deliverCh=nil
// when the adapter hadn't re-registered yet on gated startup; the
// callback never re-resolved and every send silently failed even after
// the channel came online. Fix: late-bind ch in sendOnce.
func TestMakeOutputCallback_LateBindsChannel(t *testing.T) {
	gw, _ := testGateway(t)
	setGroup(gw, "jid1", core.Group{Folder: "grp", Name: "Test"})

	// Build the callback BEFORE the channel registers. This mirrors
	// processSenderBatch running during the startup window where the
	// adapter HTTP POST /v1/channels/register hasn't arrived yet.
	cb, hadOutput := gw.makeOutputCallback(nil, "jid1", "", "msg-1", "grp")

	// Channel registers after the callback was built (startup race).
	ch := &testChannel{name: "tc", jids: []string{"jid1"}}
	gw.AddChannel(ch)

	// Agent produces output. The send should succeed via late-bind.
	cb("Hello after registration", "")

	if !*hadOutput {
		t.Error("hadOutput should be true")
	}
	sent := ch.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent count = %d, want 1 (late-bind failed)", len(sent))
	}
	if sent[0].text != "Hello after registration" {
		t.Errorf("sent text = %q, want %q", sent[0].text, "Hello after registration")
	}
}

func TestSendMessageReply_NoChannel(t *testing.T) {
	gw, _ := testGateway(t)

	_, err := gw.sendMessageReply("unknown-jid", "hello", "", "")
	if err == nil {
		t.Error("expected error for unknown JID")
	}
	if !strings.Contains(err.Error(), "no channel") {
		t.Errorf("error = %q, want 'no channel' message", err.Error())
	}
}

func TestSendMessageReply_ChannelSendDisabled(t *testing.T) {
	gw, _ := testGateway(t)
	ch := &testChannel{name: "tc", jids: []string{"telegram"}}
	gw.AddChannel(ch)
	gw.cfg.SendDisabledChannels = []string{"telegram"}

	id, err := gw.sendMessageReply("telegram:12345", "hello", "", "")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if id != "" {
		t.Errorf("sentID = %q, want empty", id)
	}
	sent := ch.getSent()
	if len(sent) != 0 {
		t.Error("message should not have been sent to disabled channel")
	}
}

func TestFormatOutbound_ThinkOnlyProducesEmpty(t *testing.T) {
	cases := []string{
		"<think>only thoughts</think>",
		"<think>first</think><think>second</think>",
		"<think>nested <think>deep</think> thought</think>",
		"  <think>whitespace around</think>  ",
	}
	for _, tc := range cases {
		got := router.FormatOutbound(tc)
		if got != "" {
			t.Errorf("FormatOutbound(%q) = %q, want empty", tc, got)
		}
	}
}

func TestCheckMigrationVersion(t *testing.T) {
	gw, s := testGateway(t)

	// Set HostAppDir so the source MIGRATION_VERSION can be found
	gw.cfg.HostAppDir = t.TempDir()
	srcDir := filepath.Join(gw.cfg.HostAppDir, "ant", "skills", "self")
	os.MkdirAll(srcDir, 0o755)
	os.WriteFile(filepath.Join(srcDir, "MIGRATION_VERSION"), []byte("55\n"), 0o644)

	// Create root group with old version
	s.PutGroup(core.Group{Folder: "myworld", Name: "MyWorld"})
	groupSkillDir := filepath.Join(gw.cfg.GroupsDir, "myworld", ".claude", "skills", "self")
	os.MkdirAll(groupSkillDir, 0o755)
	os.WriteFile(filepath.Join(groupSkillDir, "MIGRATION_VERSION"), []byte("54\n"), 0o644)

	// Create child group (should be skipped)
	s.PutGroup(core.Group{Folder: "myworld/child", Name: "Child"})

	gw.checkMigrationVersion()

	// Should have injected a message into myworld
	msgs, _ := s.MessagesSince("myworld", time.Time{}, "nobot")
	found := false
	for _, m := range msgs {
		if strings.Contains(m.Content, "System update") && m.Sender == "system" {
			found = true
		}
	}
	if !found {
		t.Error("expected auto-migration message in myworld")
	}

	// Child group should NOT have a migration message
	childMsgs, _ := s.MessagesSince("myworld/child", time.Time{}, "nobot")
	for _, m := range childMsgs {
		if strings.Contains(m.Content, "System update") {
			t.Error("child group should not get auto-migration message")
		}
	}
}

func TestCheckMigrationVersion_UpToDate(t *testing.T) {
	gw, s := testGateway(t)
	gw.cfg.HostAppDir = t.TempDir()
	srcDir := filepath.Join(gw.cfg.HostAppDir, "ant", "skills", "self")
	os.MkdirAll(srcDir, 0o755)
	os.WriteFile(filepath.Join(srcDir, "MIGRATION_VERSION"), []byte("55\n"), 0o644)

	s.PutGroup(core.Group{Folder: "uptodate", Name: "UpToDate"})
	groupSkillDir := filepath.Join(gw.cfg.GroupsDir, "uptodate", ".claude", "skills", "self")
	os.MkdirAll(groupSkillDir, 0o755)
	os.WriteFile(filepath.Join(groupSkillDir, "MIGRATION_VERSION"), []byte("55\n"), 0o644)

	gw.checkMigrationVersion()

	msgs, _ := s.MessagesSince("uptodate", time.Time{}, "nobot")
	for _, m := range msgs {
		if strings.Contains(m.Content, "System update") {
			t.Error("should not trigger migration when up to date")
		}
	}
}

func TestCheckMigrationVersion_NoVersionFile(t *testing.T) {
	gw, s := testGateway(t)
	gw.cfg.HostAppDir = t.TempDir()
	srcDir := filepath.Join(gw.cfg.HostAppDir, "ant", "skills", "self")
	os.MkdirAll(srcDir, 0o755)
	os.WriteFile(filepath.Join(srcDir, "MIGRATION_VERSION"), []byte("10\n"), 0o644)

	s.PutGroup(core.Group{Folder: "fresh", Name: "Fresh"})
	os.MkdirAll(filepath.Join(gw.cfg.GroupsDir, "fresh", ".claude", "skills", "self"), 0o755)

	gw.checkMigrationVersion()

	msgs, _ := s.MessagesSince("fresh", time.Time{}, "nobot")
	found := false
	for _, m := range msgs {
		if strings.Contains(m.Content, "System update") && m.Sender == "system" {
			found = true
		}
	}
	if !found {
		t.Error("expected migration message for group with no version file")
	}
}

func TestRecoverPendingMessages(t *testing.T) {
	gw, s := testGateway(t)
	gw.cfg.MaxContainers = 10

	var mu sync.Mutex
	recovered := map[string]bool{}
	gw.queue.SetProcessMessagesFn(func(jid string) (bool, error) {
		mu.Lock()
		recovered[jid] = true
		mu.Unlock()
		return true, nil
	})
	gw.queue.SetHasPendingFn(func(jid string) bool {
		mu.Lock()
		defer mu.Unlock()
		return !recovered[jid]
	})

	s.PutMessage(core.Message{
		ID: "m1", ChatJID: "telegram:-100", Sender: "user",
		Content: "hello", Timestamp: time.Now(),
	})
	s.PutMessage(core.Message{
		ID: "m2", ChatJID: "discord:200", Sender: "user",
		Content: "hi", Timestamp: time.Now(),
	})
	s.PutMessage(core.Message{
		ID: "m3", ChatJID: "mygroup", Sender: "system",
		Content: "task", Timestamp: time.Now(),
	})

	gw.recoverPendingMessages()

	want := []string{"telegram:-100", "discord:200", "mygroup"}
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		done := true
		for _, jid := range want {
			if !recovered[jid] {
				done = false
				break
			}
		}
		mu.Unlock()
		if done {
			break
		}
		select {
		case <-deadline:
			mu.Lock()
			defer mu.Unlock()
			for _, jid := range want {
				if !recovered[jid] {
					t.Errorf("%s not recovered", jid)
				}
			}
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Errored messages are included in recovery (retry semantics, not quarantine).
func TestRecoverPendingMessages_IncludesErroredMessage(t *testing.T) {
	gw, s := testGateway(t)
	gw.cfg.MaxContainers = 10

	var mu sync.Mutex
	recovered := map[string]bool{}
	gw.queue.SetProcessMessagesFn(func(jid string) (bool, error) {
		mu.Lock()
		recovered[jid] = true
		mu.Unlock()
		return true, nil
	})

	jid := "telegram:-100"
	now := time.Now()
	s.PutMessage(core.Message{
		ID: "m1", ChatJID: jid, Sender: "user",
		Content: "failed-last-time", Timestamp: now,
	})
	if err := s.MarkMessagesErrored([]string{"m1"}); err != nil {
		t.Fatal(err)
	}

	if !s.HasPendingMessages(jid, gw.cfg.Name) {
		t.Fatal("expected pending (errored row must remain visible)")
	}

	msgs, _ := s.MessagesSince(jid, time.Time{}, gw.cfg.Name)
	if len(msgs) != 1 || !msgs[0].Errored {
		t.Fatalf("expected errored-tagged message, got %+v", msgs)
	}

	gw.recoverPendingMessages()

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		done := recovered[jid]
		mu.Unlock()
		if done {
			return
		}
		select {
		case <-deadline:
			t.Error("chat with errored-only messages should still be recovered")
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// MarkMessagesErrored must keep rows visible to all three read paths
// (annotate/retry semantics, not filter). Scanned rows carry Errored=true
// so the prompt formatter can tag them for the agent.
func TestMarkMessagesErrored_VisibleToReads(t *testing.T) {
	gw, s := testGateway(t)

	jid := "telegram:1"
	now := time.Now()
	s.PutMessage(core.Message{ID: "m1", ChatJID: jid, Sender: "u", Content: "poison", Timestamp: now})
	if err := s.MarkMessagesErrored([]string{"m1"}); err != nil {
		t.Fatal(err)
	}

	if !s.HasPendingMessages(jid, gw.cfg.Name) {
		t.Error("HasPendingMessages should return true for errored rows (retry semantics)")
	}
	msgs, _, _ := s.NewMessages([]string{jid}, time.Time{}, gw.cfg.Name)
	if len(msgs) != 1 || !msgs[0].Errored {
		t.Errorf("NewMessages should return errored row tagged: %+v", msgs)
	}
	since, _ := s.MessagesSince(jid, time.Time{}, gw.cfg.Name)
	if len(since) != 1 || !since[0].Errored {
		t.Errorf("MessagesSince should return errored row tagged: %+v", since)
	}
}

func TestEnrichAttachments_VoiceTranscription(t *testing.T) {
	// Mock file server
	fileSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/ogg")
		w.Write([]byte("OggS...fake audio"))
	}))
	defer fileSrv.Close()

	// Mock whisper server — records Content-Type from request
	var gotContentType string
	whisperSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		io.ReadAll(r.Body)
		json.NewEncoder(w).Encode(map[string]string{"text": "hello world"})
	}))
	defer whisperSrv.Close()

	gw, s := testGateway(t)
	gw.cfg.MediaEnabled = true
	gw.cfg.MediaMaxBytes = 10 * 1024 * 1024
	gw.cfg.VoiceEnabled = true
	gw.cfg.WhisperURL = whisperSrv.URL
	gw.cfg.WhisperModel = "turbo"

	grp := core.Group{Folder: "grp", Name: "Test"}
	setGroup(gw, "jid1", grp)

	atts := `[{"mime":"audio/ogg","filename":"voice.ogg","url":"` + fileSrv.URL + `/voice.ogg","size":17}]`
	msg := core.Message{
		ID: "m-voice", ChatJID: "jid1", Sender: "user",
		Content: "[Voice]", Timestamp: time.Now(), Attachments: atts,
	}
	s.PutMessage(msg)

	gw.enrichAttachments(context.Background(), &msg, "grp")

	if gotContentType != "audio/ogg" {
		t.Errorf("whisper Content-Type = %q, want %q", gotContentType, "audio/ogg")
	}
	if !strings.Contains(msg.Content, `transcript="hello world"`) {
		t.Errorf("expected transcript in content, got %q", msg.Content)
	}
	if !strings.Contains(msg.Content, "<attachment") {
		t.Errorf("expected attachment XML, got %q", msg.Content)
	}
}

func TestEnrichAttachments_VoiceDisabled(t *testing.T) {
	fileSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OggS...fake audio"))
	}))
	defer fileSrv.Close()

	gw, s := testGateway(t)
	gw.cfg.MediaEnabled = true
	gw.cfg.MediaMaxBytes = 10 * 1024 * 1024
	gw.cfg.VoiceEnabled = false

	grp := core.Group{Folder: "grp", Name: "Test"}
	setGroup(gw, "jid1", grp)

	atts := `[{"mime":"audio/ogg","filename":"voice.ogg","url":"` + fileSrv.URL + `/voice.ogg","size":17}]`
	msg := core.Message{
		ID: "m-novoice", ChatJID: "jid1", Sender: "user",
		Content: "[Voice]", Timestamp: time.Now(), Attachments: atts,
	}
	s.PutMessage(msg)

	gw.enrichAttachments(context.Background(), &msg, "grp")

	if strings.Contains(msg.Content, "transcript") {
		t.Errorf("expected no transcript when VoiceEnabled=false, got %q", msg.Content)
	}
	if !strings.Contains(msg.Content, "<attachment") {
		t.Errorf("expected attachment XML even with voice disabled, got %q", msg.Content)
	}
}

func TestEnrichAttachments_Base64Decode(t *testing.T) {
	gw, s := testGateway(t)
	gw.cfg.MediaEnabled = true
	gw.cfg.MediaMaxBytes = 10 * 1024 * 1024

	grp := core.Group{Folder: "grp", Name: "Test"}
	setGroup(gw, "jid1", grp)

	raw := []byte("fake-image-bytes")
	b64 := base64.StdEncoding.EncodeToString(raw)
	atts := `[{"mime":"image/png","filename":"shot.png","data":"` + b64 + `","size":16}]`
	msg := core.Message{
		ID: "m-b64", ChatJID: "jid1", Sender: "user",
		Content: "[Image]", Timestamp: time.Now(), Attachments: atts,
	}
	s.PutMessage(msg)

	gw.enrichAttachments(context.Background(), &msg, "grp")

	if !strings.Contains(msg.Content, "<attachment") {
		t.Errorf("expected attachment XML for base64 data, got %q", msg.Content)
	}
	if !strings.Contains(msg.Content, "shot.png") {
		t.Errorf("expected filename in XML, got %q", msg.Content)
	}
}

func TestWhisperTranscribe_MultiLanguage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := r.URL.Query().Get("language")
		text := "transcript-" + lang
		if lang == "" {
			text = "transcript-default"
		}
		json.NewEncoder(w).Encode(map[string]string{"text": text})
	}))
	defer srv.Close()

	dir := t.TempDir()
	f := filepath.Join(dir, "audio.ogg")
	os.WriteFile(f, []byte("fake"), 0o644)

	got := whisperTranscribe(context.Background(), srv.URL, "turbo", f, "audio/ogg", []string{"en", "es"})
	if !strings.Contains(got, "transcript-en") {
		t.Errorf("missing en transcript, got %q", got)
	}
	if !strings.Contains(got, "transcript-es") {
		t.Errorf("missing es transcript, got %q", got)
	}
	parts := strings.Split(got, "\n")
	if len(parts) != 2 {
		t.Errorf("expected 2 parts joined by newline, got %d: %q", len(parts), got)
	}
}

func TestWhisperTranscribe_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	dir := t.TempDir()
	f := filepath.Join(dir, "audio.ogg")
	os.WriteFile(f, []byte("fake"), 0o644)

	got := whisperTranscribe(context.Background(), srv.URL, "turbo", f, "audio/ogg", nil)
	if got != "" {
		t.Errorf("expected empty on server error, got %q", got)
	}
}

func TestReadWhisperLanguages(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".whisper-language"), []byte("en\nes\n"), 0o644)

	langs := readWhisperLanguages(dir)
	if len(langs) != 2 || langs[0] != "en" || langs[1] != "es" {
		t.Errorf("readWhisperLanguages = %v, want [en es]", langs)
	}
}

func TestReadWhisperLanguages_Missing(t *testing.T) {
	dir := t.TempDir()
	langs := readWhisperLanguages(dir)
	if langs != nil {
		t.Errorf("readWhisperLanguages = %v, want nil", langs)
	}
}

// Circuit-breaker hard reset: errored messages for the chat are deleted
// and the group's session is cleared so the next inbound starts fresh.
func TestOnCircuitBreakerOpen_PrunesAndResetsSession(t *testing.T) {
	gw, s := testGateway(t)
	jid := "telegram:42"
	setGroup(gw, jid, core.Group{Folder: "grp", Name: "G"})
	s.SetSession("grp", "", "sess-abc")

	now := time.Now()
	s.PutMessage(core.Message{ID: "m1", ChatJID: jid, Sender: "u", Content: "boom", Timestamp: now})
	s.PutMessage(core.Message{ID: "m2", ChatJID: jid, Sender: "u", Content: "fresh", Timestamp: now.Add(time.Second)})
	if err := s.MarkMessagesErrored([]string{"m1"}); err != nil {
		t.Fatal(err)
	}

	gw.onCircuitBreakerOpen(jid, fmt.Errorf("too many failures"))

	msgs, _ := s.MessagesSince(jid, time.Time{}, gw.cfg.Name)
	if len(msgs) != 1 || msgs[0].ID != "m2" {
		t.Errorf("after prune want only m2 remaining, got %+v", msgs)
	}
	if id, _ := s.GetSession("grp", ""); id != "" {
		t.Errorf("session should be cleared, got %q", id)
	}
}
