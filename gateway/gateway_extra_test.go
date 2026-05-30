package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
)

// --- bug #1: SetAudit must survive the wireFns rebuild ---

// Run rebuilds gatedFns from scratch; the rebuild must re-apply the audit
// writer set via SetAudit, else per-tool-call audit emission goes silent.
func TestSetAudit_SurvivesWireFns(t *testing.T) {
	gw, _ := testGateway(t)
	aud := audit.New(audit.Config{Enabled: true, DataDir: t.TempDir(), MaxBytes: 1 << 20, RotateHours: 24})

	gw.SetAudit(aud)
	gw.wireFns() // same rebuild Run performs after AddChannel

	if gw.gatedFns.Audit == nil {
		t.Fatal("gatedFns.Audit is nil after wireFns; per-tool-call audit would be silent")
	}
	if gw.gatedFns.Audit != aud {
		t.Errorf("gatedFns.Audit = %p, want %p (the writer passed to SetAudit)", gw.gatedFns.Audit, aud)
	}
}

// --- bug #4: sendDocument honours SEND_DISABLED_CHANNELS ---

func TestSendDocument_ChannelSendDisabled(t *testing.T) {
	gw, _ := testGateway(t)
	ch := &testChannel{name: "tc", jids: []string{"telegram"}}
	gw.AddChannel(ch)
	gw.cfg.SendDisabledChannels = []string{"telegram"}

	if err := gw.sendDocument("telegram:12345", "/tmp/x.pdf", "x.pdf", "", "", ""); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if n := ch.getFiles(); n != 0 {
		t.Errorf("SendFile called %d times, want 0 (channel disabled)", n)
	}

	// Enabled channel still sends.
	gw.cfg.SendDisabledChannels = nil
	if err := gw.sendDocument("telegram:12345", "/tmp/x.pdf", "x.pdf", "", "", ""); err != nil {
		t.Errorf("unexpected error on enabled send: %v", err)
	}
	if n := ch.getFiles(); n != 1 {
		t.Errorf("SendFile called %d times, want 1 (channel enabled)", n)
	}
}

// --- bug #5: groupBySender keeps consecutive same-sender runs ---

func TestGroupBySender_ConsecutiveRuns(t *testing.T) {
	msgs := []core.Message{
		{ID: "1", Sender: "A"},
		{ID: "2", Sender: "B"},
		{ID: "3", Sender: "A"},
	}
	batches := groupBySender(msgs)
	if len(batches) != 3 {
		t.Fatalf("A,B,A → %d batches, want 3 (causal order preserved)", len(batches))
	}
	want := []string{"A", "B", "A"}
	for i, b := range batches {
		if len(b) != 1 || b[0].Sender != want[i] {
			t.Errorf("batch %d = %v, want single %q", i, b, want[i])
		}
	}

	// Adjacent same-sender messages coalesce into one batch.
	run := groupBySender([]core.Message{
		{ID: "1", Sender: "A"}, {ID: "2", Sender: "A"}, {ID: "3", Sender: "B"},
	})
	if len(run) != 2 || len(run[0]) != 2 || len(run[1]) != 1 {
		t.Errorf("A,A,B → %v, want [[A A] [B]]", run)
	}
}

// --- bug #6: groupByTopic keeps consecutive same-topic runs ---

func TestGroupByTopic_ConsecutiveRuns(t *testing.T) {
	msgs := []core.Message{
		{ID: "1", Topic: "#a"},
		{ID: "2", Topic: "#b"},
		{ID: "3", Topic: "#a"},
	}
	batches := groupByTopic(msgs)
	if len(batches) != 3 {
		t.Fatalf("#a,#b,#a → %d batches, want 3 (causal order preserved)", len(batches))
	}
	want := []string{"#a", "#b", "#a"}
	for i, b := range batches {
		if len(b) != 1 || b[0].Topic != want[i] {
			t.Errorf("batch %d = %v, want single %q", i, b, want[i])
		}
	}

	// Adjacent same-topic messages coalesce.
	run := groupByTopic([]core.Message{
		{ID: "1", Topic: "#a"}, {ID: "2", Topic: "#a"}, {ID: "3", Topic: "#b"},
	})
	if len(run) != 2 || len(run[0]) != 2 || len(run[1]) != 1 {
		t.Errorf("#a,#a,#b → %v, want [[a a] [b]]", run)
	}
}

// --- bug #7: spawn/register propagate AddRoute errors ---

func TestRegisterGroupIPC_AddRouteError(t *testing.T) {
	gw, s := testGateway(t)
	// Drop the routes table so PutGroup succeeds but AddRoute fails.
	if _, err := s.DB().Exec("DROP TABLE routes"); err != nil {
		t.Fatalf("drop routes: %v", err)
	}
	err := gw.registerGroupIPC("telegram:777", core.Group{Folder: "world/x", AddedAt: time.Now()})
	if err == nil {
		t.Fatal("registerGroupIPC swallowed AddRoute error; group would be orphaned")
	}
	if !strings.Contains(err.Error(), "add route") {
		t.Errorf("error = %q, want 'add route' context", err.Error())
	}
	// The group row must be rolled back — no route-less orphan left behind.
	if _, ok := s.GroupByFolder("world/x"); ok {
		t.Fatal("AddRoute failure left an orphan group row (no rollback)")
	}
}

func TestSpawnFromPrototype_AddRouteError(t *testing.T) {
	dir := t.TempDir()
	gw, s := testGateway(t)
	gw.cfg.GroupsDir = dir
	gw.folders.GroupsDir = dir

	parentFolder := "main"
	protoDir := filepath.Join(dir, parentFolder, "prototype")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(protoDir, "CLAUDE.md"), []byte("# proto"), 0o644)
	s.PutGroup(core.Group{Folder: parentFolder, AddedAt: time.Now(), Config: core.GroupConfig{MaxChildren: 5}})

	if _, err := s.DB().Exec("DROP TABLE routes"); err != nil {
		t.Fatalf("drop routes: %v", err)
	}
	_, err := gw.spawnFromPrototype(parentFolder, "telegram:888")
	if err == nil {
		t.Fatal("spawnFromPrototype swallowed AddRoute error; child would be orphaned")
	}
	if !strings.Contains(err.Error(), "add route") {
		t.Errorf("error = %q, want 'add route' context", err.Error())
	}
	// The child group row must be rolled back — no un-respawnable orphan.
	if _, ok := s.GroupByFolder(spawnFolderName(parentFolder, "telegram:888")); ok {
		t.Fatal("AddRoute failure left an orphan child group row (no rollback)")
	}
}

// --- bug #8: observeWindow uses the first matching route override ---

func TestObserveWindow_FirstRouteWins(t *testing.T) {
	gw, s := testGateway(t)
	s.PutGroup(core.Group{Folder: "grp", AddedAt: time.Now()})
	// Two routes target "grp" with different overrides; first inserted wins.
	if _, err := s.AddRoute(core.Route{Seq: 0, Match: "room=a", Target: "grp", ObserveWindowMessages: 5, ObserveWindowChars: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRoute(core.Route{Seq: 0, Match: "room=b", Target: "grp", ObserveWindowMessages: 9, ObserveWindowChars: 900}); err != nil {
		t.Fatal(err)
	}
	n, c := gw.observeWindow("grp")
	if n != 5 || c != 100 {
		t.Errorf("observeWindow = (%d, %d), want (5, 100) from the first matching route", n, c)
	}
}

// --- bug #9: sendVoice validates text before hitting TTS ---

func TestSendVoice_RejectsEmptyAndOversize(t *testing.T) {
	dir := t.TempDir()
	gw, _ := testGateway(t)
	gw.cfg.ProjectRoot = dir
	gw.cfg.TTSEnabled = true
	gw.cfg.TTSURL = "http://127.0.0.1:0" // must never be reached
	gw.cfg.TTSVoice = "v"
	ch := &testChannel{name: "tc", jids: []string{"telegram"}}
	gw.AddChannel(ch)

	if _, err := gw.sendVoice("telegram:1", "", "", "", ""); err == nil {
		t.Error("empty text should be rejected before TTS")
	}
	if _, err := gw.sendVoice("telegram:1", strings.Repeat("a", 6000), "", "", ""); err == nil {
		t.Error("6000-char text should be rejected before TTS")
	}
	if ch.getVoices() != 0 {
		t.Error("SendVoice channel call should not fire on invalid text")
	}
}

// --- bug #2 & #3: HTTP helpers (downloadFile truncation, transcribeOnce body close) ---

func TestDownloadFile_OversizeErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.bin")
	err := downloadFile(context.Background(), srv.URL, dest, "", 10)
	if err == nil {
		t.Fatal("downloadFile accepted oversize body; would silently truncate")
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Error("oversize download left a partial file behind")
	}
}

func TestDownloadFile_AtLimitSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(strings.Repeat("x", 10)))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "ok.bin")
	if err := downloadFile(context.Background(), srv.URL, dest, "", 10); err != nil {
		t.Fatalf("body exactly at limit should succeed: %v", err)
	}
	data, _ := os.ReadFile(dest)
	if len(data) != 10 {
		t.Errorf("wrote %d bytes, want 10", len(data))
	}
}

// transcribeOnce must not leak the response body on a non-200; a server
// that blocks on a second connection (because the first body wasn't drained
// /closed and the client has MaxConnsPerHost=… not set, but Body.Close frees
// the conn for reuse) would hang. We assert the call returns "" promptly and
// that a follow-up request to the same server succeeds (connection reused).
func TestTranscribeOnce_Non200ClosesBody(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "boom", 500)
	}))
	defer srv.Close()

	f := filepath.Join(t.TempDir(), "a.ogg")
	os.WriteFile(f, []byte("audio"), 0o644)

	if got := transcribeOnce(context.Background(), srv.URL, "m", f, "", "audio/ogg"); got != "" {
		t.Errorf("transcribeOnce on 500 = %q, want empty", got)
	}
	// A second call must complete (body from the first was closed → conn
	// returned to the pool, no leak/hang).
	if got := transcribeOnce(context.Background(), srv.URL, "m", f, "", "audio/ogg"); got != "" {
		t.Errorf("second transcribeOnce = %q, want empty", got)
	}
	if calls != 2 {
		t.Errorf("server saw %d calls, want 2", calls)
	}
}

// --- setCurrentTurn / clearCurrentTurn / currentTriggerSender / currentTurnTopic ---

func TestCurrentTurn_SetClearAccessors(t *testing.T) {
	gw, _ := testGateway(t)

	// Before set: both return empty.
	if got := gw.currentTriggerSender("grp"); got != "" {
		t.Errorf("before set: triggerSender = %q, want empty", got)
	}
	if got := gw.currentTurnTopic("grp"); got != "" {
		t.Errorf("before set: topic = %q, want empty", got)
	}

	gw.setCurrentTurn("grp", "telegram:user/42", "#deploy")

	if got := gw.currentTriggerSender("grp"); got != "telegram:user/42" {
		t.Errorf("after set: triggerSender = %q, want telegram:user/42", got)
	}
	if got := gw.currentTurnTopic("grp"); got != "#deploy" {
		t.Errorf("after set: topic = %q, want #deploy", got)
	}

	gw.clearCurrentTurn("grp")

	if got := gw.currentTriggerSender("grp"); got != "" {
		t.Errorf("after clear: triggerSender = %q, want empty", got)
	}
	if got := gw.currentTurnTopic("grp"); got != "" {
		t.Errorf("after clear: topic = %q, want empty", got)
	}
}

// Different folders must not see each other's turn state.
func TestCurrentTurn_IsolatedByFolder(t *testing.T) {
	gw, _ := testGateway(t)

	gw.setCurrentTurn("grp1", "sender-A", "#topic-A")
	gw.setCurrentTurn("grp2", "sender-B", "#topic-B")

	if got := gw.currentTriggerSender("grp1"); got != "sender-A" {
		t.Errorf("grp1 triggerSender = %q, want sender-A", got)
	}
	if got := gw.currentTriggerSender("grp2"); got != "sender-B" {
		t.Errorf("grp2 triggerSender = %q, want sender-B", got)
	}
	if got := gw.currentTurnTopic("grp1"); got != "#topic-A" {
		t.Errorf("grp1 topic = %q, want #topic-A", got)
	}
	if got := gw.currentTurnTopic("grp2"); got != "#topic-B" {
		t.Errorf("grp2 topic = %q, want #topic-B", got)
	}

	// Clearing grp1 must not affect grp2.
	gw.clearCurrentTurn("grp1")
	if got := gw.currentTriggerSender("grp2"); got != "sender-B" {
		t.Errorf("grp2 after clearing grp1: triggerSender = %q, want sender-B", got)
	}
}

// clearCurrentTurn on an absent folder must not panic.
func TestCurrentTurn_ClearAbsent_NoPanic(t *testing.T) {
	gw, _ := testGateway(t)
	// Must not panic.
	gw.clearCurrentTurn("does-not-exist")
}

//--- lookupCommand with Slack backslash prefix ---

func TestLookupCommand_BackslashAlias(t *testing.T) {
	// Slack sometimes delivers /new as \new.
	cmd, _ := lookupCommand(`\new`)
	if cmd == nil {
		t.Fatal("lookupCommand(\\new) returned nil, expected /new")
	}
	if cmd.name != "/new" {
		t.Errorf("cmd.name = %q, want /new", cmd.name)
	}
}

// --- cmdStatus tier-0 gate ---

func TestCmdStatus_TierGate(t *testing.T) {
	// /status is root-only. A tier-2 caller must receive "Permission denied".
	gw, _ := testGateway(t)
	ch := &mockChannel{name: "tc", jids: []string{"jid1"}}
	gw.AddChannel(ch)
	setGroup(gw, "jid1", core.Group{Folder: "world/a/b"})

	grp, _ := gw.store.GroupByFolder("world/a/b")
	msg := core.Message{ChatJID: "jid1", Content: "/status"}
	if !gw.handleCommand(msg, grp) {
		t.Fatal("handleCommand returned false for /status")
	}
	if got := ch.lastSent(); got != "Permission denied: root only." {
		t.Errorf("sent = %q, want permission denied", got)
	}
}

// --- cmdPing reports session and group info ---

func TestCmdPing_ReportsGroupFolder(t *testing.T) {
	gw, s := testGateway(t)
	ch := &mockChannel{name: "tc", jids: []string{"jid1"}}
	gw.AddChannel(ch)
	setGroup(gw, "jid1", core.Group{Folder: "world/child"})
	s.SetSession("world/child", "", "ping-session-id")

	grp, _ := gw.store.GroupByFolder("world/child")
	msg := core.Message{ChatJID: "jid1", Content: "/ping"}
	if !gw.handleCommand(msg, grp) {
		t.Fatal("handleCommand returned false for /ping")
	}
	reply := ch.lastSent()
	if reply == "" {
		t.Fatal("no reply from /ping")
	}
	if !containsAll(reply, "pong", "world/child", "ping-ses") {
		t.Errorf("ping reply missing expected fields: %q", reply)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if len(s) < len(p) {
			return false
		}
		found := false
		for i := 0; i+len(p) <= len(s); i++ {
			if s[i:i+len(p)] == p {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// --- cmdNew with topic ---

func TestCmdNew_WithTopic_ClearsTopicSession(t *testing.T) {
	gw, s := testGateway(t)
	ch := &mockChannel{name: "tc", jids: []string{"jid1"}}
	gw.AddChannel(ch)
	setGroup(gw, "jid1", core.Group{Folder: "grp"})

	// Seed a topic session.
	s.SetSession("grp", "#deploy", "topic-sess-1")

	grp, _ := gw.store.GroupByFolder("grp")
	msg := core.Message{ChatJID: "jid1", Content: "/new #deploy"}
	gw.handleCommand(msg, grp)

	if id, _ := s.GetSession("grp", "#deploy"); id != "" {
		t.Errorf("topic session not cleared: %q", id)
	}
}

//--- resolveGroup returns false for own-group JID not in DB ---

func TestResolveGroup_UnregisteredBareFolder(t *testing.T) {
	gw, _ := testGateway(t)
	// "ghost" folder is NOT in the DB.
	msg := core.Message{ChatJID: "ghost", Verb: "message"}
	if _, ok := gw.resolveGroup(msg); ok {
		t.Error("resolveGroup returned true for unregistered bare-folder JID")
	}
}

// --- saveState + loadState round-trip ---

func TestSaveAndLoadState_RoundTrip(t *testing.T) {
	gw, _ := testGateway(t)
	want := time.Date(2026, 1, 15, 9, 30, 0, 123456789, time.UTC)
	gw.lastTimestamp = want
	gw.saveState()

	// Reset and reload.
	gw.lastTimestamp = time.Time{}
	gw.loadState()

	if !gw.lastTimestamp.Equal(want) {
		t.Errorf("loaded = %v, want %v", gw.lastTimestamp, want)
	}
}
