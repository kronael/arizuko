package routd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// lastAck returns the text of the most recent ack/Send delivered to the chat.
func lastAck(t *testing.T, dl *recDeliverer) string {
	t.Helper()
	if len(dl.sends) == 0 {
		t.Fatal("no ack delivered")
	}
	return dl.sends[len(dl.sends)-1].text
}

// drive runs one inbound through the steer layer (the slash-command path is
// inside steer → handleCommand). Returns whether the message was consumed.
func steerOne(l *Loop, chatJID, folder, content string) bool {
	return l.steer(chatJID, core.Message{
		ChatJID: chatJID, Sender: "u", Content: content, Timestamp: time.Now().UTC(),
	}, folder)
}

// TestCmdChatID: /chatid acks the chat jid verbatim (gated cmdChatID).
func TestCmdChatID(t *testing.T) {
	_, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	if !steerOne(loop, "tg:42", "demo", "/chatid") {
		t.Fatal("/chatid not consumed")
	}
	if got := lastAck(t, dl); got != "tg:42" {
		t.Fatalf("/chatid ack=%q want tg:42", got)
	}
}

// TestCmdPing: /ping reports group, session prefix, active-run count, and group
// count in gated's exact format (gateway.cmdPing).
func TestCmdPing(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "demo"})
	_ = db.PutSession("demo", "", "sessionABCDEF")
	if !steerOne(loop, "web:demo", "demo", "/ping") {
		t.Fatal("/ping not consumed")
	}
	want := "pong\ngroup: demo\nsession: sessionA\nactive containers: 0\nregistered groups: 1"
	if got := lastAck(t, dl); got != want {
		t.Fatalf("/ping ack=%q want %q", got, want)
	}
}

// TestCmdPingNoSession: a folder without a session reports "session: none".
func TestCmdPingNoSession(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "demo"})
	_ = steerOne(loop, "web:demo", "demo", "/ping")
	if !strings.Contains(lastAck(t, dl), "session: none") {
		t.Fatalf("/ping ack=%q want session: none", lastAck(t, dl))
	}
}

// TestCmdStop: /stop with no live run acks gated's exact "no active" message.
// The default recRunner is not a RunStopper, so cmdStop reports no-active —
// identical to runed answering killed:false.
func TestCmdStop(t *testing.T) {
	_, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	if !steerOne(loop, "tg:1", "demo", "/stop") {
		t.Fatal("/stop not consumed")
	}
	if got := lastAck(t, dl); got != "No active container for this chat." {
		t.Fatalf("/stop ack=%q", got)
	}
}

// stopRunner is a Runner that also satisfies RunStopper: StopFolder records the
// asked folder and returns a canned outcome (the runed stop-by-folder RPC).
type stopRunner struct {
	recRunner
	folder string
	resp   runedv1.StopRunResponse
}

func (r *stopRunner) StopFolder(_ context.Context, folder string) (runedv1.StopRunResponse, error) {
	r.folder = folder
	return r.resp, nil
}

// TestCmdStopKillsViaRuned: /stop asks runed to kill the resolved folder's live
// run (POST /v1/runs/stop) and, on killed:true, acks gated's "Container stopped."
func TestCmdStopKillsViaRuned(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	sr := &stopRunner{resp: runedv1.StopRunResponse{Killed: true, RunID: "run_x"}}
	loop := NewLoop(db, sr, LoopConfig{})
	loop.StopQueue()
	dl := &recDeliverer{}
	loop.deliver = dl

	if !steerOne(loop, "tg:1", "demo", "/stop") {
		t.Fatal("/stop not consumed")
	}
	if sr.folder != "demo" {
		t.Fatalf("runed asked to stop folder %q want demo", sr.folder)
	}
	if got := lastAck(t, dl); got != "Container stopped." {
		t.Fatalf("/stop ack=%q want Container stopped.", got)
	}
}

// TestCmdStopNoActiveViaRuned: runed reporting killed:false renders gated's
// no-active text (the folder had no live spawn).
func TestCmdStopNoActiveViaRuned(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	sr := &stopRunner{resp: runedv1.StopRunResponse{Killed: false}}
	loop := NewLoop(db, sr, LoopConfig{})
	loop.StopQueue()
	dl := &recDeliverer{}
	loop.deliver = dl

	_ = steerOne(loop, "tg:1", "demo", "/stop")
	if got := lastAck(t, dl); got != "No active container for this chat." {
		t.Fatalf("/stop ack=%q want no-active", got)
	}
}

// TestCmdStatusRootOnly: /status from a root group (tier 0) reports the
// instance counts in gated's exact format (gateway.cmdStatus).
func TestCmdStatusRootOnly(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "demo"})
	if !steerOne(loop, "web:demo", "demo", "/status") {
		t.Fatal("/status not consumed")
	}
	want := "status\nchannels: 0\ngroups: 1\nactive containers: 0\nerrored chats: 0\nactive tasks: 0"
	if got := lastAck(t, dl); got != want {
		t.Fatalf("/status ack=%q want %q", got, want)
	}
}

// TestCmdStatusErroredCount: a chat flagged errored shows in /status counts.
func TestCmdStatusErroredCount(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "demo"})
	_ = db.MarkChatErrored("tg:99")
	_ = steerOne(loop, "web:demo", "demo", "/status")
	if !strings.Contains(lastAck(t, dl), "errored chats: 1") {
		t.Fatalf("/status ack=%q want errored chats: 1", lastAck(t, dl))
	}
}

// TestCmdStatusPermissionDenied: a child group (tier>0) is denied, matching
// gated's exact message.
func TestCmdStatusPermissionDenied(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "root/child"})
	_ = steerOne(loop, "web:root/child", "root/child", "/status")
	if got := lastAck(t, dl); got != "Permission denied: root only." {
		t.Fatalf("/status child ack=%q", got)
	}
}

// TestCmdRootUsage: a bare /root prompts usage (gateway.cmdRoot).
func TestCmdRootUsage(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "root/eng"})
	_ = steerOne(loop, "web:root/eng", "root/eng", "/root")
	if got := lastAck(t, dl); got != "Usage: /root <message>" {
		t.Fatalf("/root ack=%q", got)
	}
}

// TestCmdRootAlreadyRoot: /root from the root group itself short-circuits.
func TestCmdRootAlreadyRoot(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "root"})
	_ = steerOne(loop, "web:root", "root", "/root hi")
	if got := lastAck(t, dl); got != "Already in root group." {
		t.Fatalf("/root ack=%q", got)
	}
}

// TestCmdRootDelegates: /root from a child delegates the message up to the
// world-root group, appending a delegation row with the origin as return addr.
func TestCmdRootDelegates(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "root"})
	_ = db.PutGroup(core.Group{Folder: "root/eng"})
	if !steerOne(loop, "tg:7", "root/eng", "/root escalate this") {
		t.Fatal("/root not consumed")
	}
	msgs, _ := db.MessagesSince("root", "")
	if len(msgs) != 1 {
		t.Fatalf("delegation rows on root=%d want 1", len(msgs))
	}
	if msgs[0].Content != "escalate this" || msgs[0].ForwardedFrom != "tg:7" {
		t.Fatalf("delegated row=%+v want content/escalate from tg:7", msgs[0])
	}
}

// TestCmdRootMissingRoot: /root when the root group isn't registered acks the
// gated "not found" message.
func TestCmdRootMissingRoot(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "root/eng"})
	_ = steerOne(loop, "tg:7", "root/eng", "/root hi")
	if got := lastAck(t, dl); got != "Root group not found." {
		t.Fatalf("/root ack=%q", got)
	}
}

// TestCmdInviteFederation: /invite enforces the tier-0 gate + arg shape like
// gated, then reports the onbod federation gap (routd cannot mint invites).
func TestCmdInviteFederation(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "root"})
	_ = db.PutGroup(core.Group{Folder: "root/eng"})

	// child group → denied, gated's exact message.
	_ = steerOne(loop, "web:root/eng", "root/eng", "/invite")
	if got := lastAck(t, dl); got != "Permission denied: root group only." {
		t.Fatalf("/invite child ack=%q", got)
	}
	// bad arg → gated's usage message.
	_ = steerOne(loop, "web:root", "root", "/invite zero")
	if got := lastAck(t, dl); got != "Usage: /invite [max_uses]" {
		t.Fatalf("/invite bad-arg ack=%q", got)
	}
	// root + ok → federation notice (not silently dropped).
	_ = steerOne(loop, "web:root", "root", "/invite")
	if !strings.Contains(lastAck(t, dl), "onbod") {
		t.Fatalf("/invite root ack=%q want onbod federation note", lastAck(t, dl))
	}
}

// TestCmdGateFederation: /gate enforces the tier-0 gate + subcommand shape like
// gated, then reports the onbod federation gap.
func TestCmdGateFederation(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "root"})
	_ = db.PutGroup(core.Group{Folder: "root/eng"})

	_ = steerOne(loop, "web:root/eng", "root/eng", "/gate list")
	if got := lastAck(t, dl); got != "Permission denied: root only." {
		t.Fatalf("/gate child ack=%q", got)
	}
	_ = steerOne(loop, "web:root", "root", "/gate bogus")
	if got := lastAck(t, dl); got != "Usage: /gate [list|add|rm|enable|disable]" {
		t.Fatalf("/gate bad-subcmd ack=%q", got)
	}
	_ = steerOne(loop, "web:root", "root", "/gate list")
	if !strings.Contains(lastAck(t, dl), "onbod") {
		t.Fatalf("/gate list ack=%q want onbod federation note", lastAck(t, dl))
	}
}

// fakeOnbod records calls and returns canned results for the /invite + /gate
// federation tests (the production OnbodClient is httpOnbod against onbod).
type fakeOnbod struct {
	created   []string // target globs passed to CreateInvite
	onboarded []string // jids passed to InsertOnboarding
	gates     []GateRow
	putCalls  []string // "gate=N"
	rmCalls   []string
	enCalls   []string // "gate=true|false"
}

func (f *fakeOnbod) CreateInvite(targetGlob string, maxUses int) (string, error) {
	f.created = append(f.created, targetGlob)
	return "tok-123", nil
}
func (f *fakeOnbod) InsertOnboarding(jid string) error {
	f.onboarded = append(f.onboarded, jid)
	return nil
}
func (f *fakeOnbod) ListGates() ([]GateRow, error) { return f.gates, nil }
func (f *fakeOnbod) PutGate(gate string, n int) error {
	f.putCalls = append(f.putCalls, gate)
	return nil
}
func (f *fakeOnbod) DeleteGate(gate string) error {
	f.rmCalls = append(f.rmCalls, gate)
	return nil
}
func (f *fakeOnbod) EnableGate(gate string, enabled bool) error {
	f.enCalls = append(f.enCalls, gate)
	return nil
}

// TestCmdInviteFederated: with an onbod client wired, /invite mints via onbod
// and acks the token (no stub text). The target glob is "<root>/" so the
// redeemer picks a username under root.
func TestCmdInviteFederated(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	fo := &fakeOnbod{}
	loop.SetOnbodClient(fo)
	_ = db.PutGroup(core.Group{Folder: "root"})

	_ = steerOne(loop, "web:root", "root", "/invite 5")
	if len(fo.created) != 1 || fo.created[0] != "root/" {
		t.Fatalf("CreateInvite calls = %v, want [root/]", fo.created)
	}
	if got := lastAck(t, dl); !strings.Contains(got, "tok-123") || strings.Contains(got, "onbod") {
		t.Fatalf("/invite ack=%q want the token, not a federation stub", got)
	}
}

// TestCmdGateFederated: with an onbod client wired, /gate list/add/rm/enable
// reach onbod and ack the real result (no stub text).
func TestCmdGateFederated(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	fo := &fakeOnbod{gates: []GateRow{{Gate: "*", LimitPerDay: 10, Enabled: true}}}
	loop.SetOnbodClient(fo)
	_ = db.PutGroup(core.Group{Folder: "root"})

	_ = steerOne(loop, "web:root", "root", "/gate list")
	if got := lastAck(t, dl); !strings.Contains(got, "* 10/day enabled") {
		t.Fatalf("/gate list ack=%q want the gate row", got)
	}
	_ = steerOne(loop, "web:root", "root", "/gate add github:org=acme 25")
	if len(fo.putCalls) != 1 || fo.putCalls[0] != "github:org=acme" {
		t.Fatalf("PutGate calls = %v", fo.putCalls)
	}
	_ = steerOne(loop, "web:root", "root", "/gate disable github:org=acme")
	if len(fo.enCalls) != 1 {
		t.Fatalf("EnableGate calls = %v", fo.enCalls)
	}
	_ = steerOne(loop, "web:root", "root", "/gate rm github:org=acme")
	if len(fo.rmCalls) != 1 {
		t.Fatalf("DeleteGate calls = %v", fo.rmCalls)
	}
	// unknown subcommand still gets the usage line, even with onbod wired.
	_ = steerOne(loop, "web:root", "root", "/gate bogus")
	if got := lastAck(t, dl); got != "Usage: /gate [list|add|rm|enable|disable]" {
		t.Fatalf("/gate bogus ack=%q", got)
	}
}

// TestCmdApproveReject: /approve and /reject both ack "HITL not configured".
func TestCmdApproveReject(t *testing.T) {
	_, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	for _, cmd := range []string{"/approve", "/reject"} {
		if !steerOne(loop, "tg:1", "demo", cmd) {
			t.Fatalf("%s not consumed", cmd)
		}
		if got := lastAck(t, dl); got != "HITL not configured" {
			t.Fatalf("%s ack=%q", cmd, got)
		}
	}
}

// TestSpawnOnDelegation: delegating to an unknown child whose parent exists
// with a prototype/ dir spawns the child (group + room route) and lands the
// delegation row on it (port of gateway.delegateViaMessage spawn-on-delegation).
func TestSpawnOnDelegation(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	groups := t.TempDir()
	loop := NewLoop(db, &recRunner{}, LoopConfig{GroupsDir: groups})
	loop.StopQueue()

	// parent group with a prototype/ dir on disk + unlimited children.
	_ = db.PutGroup(core.Group{Folder: "root", Config: core.GroupConfig{MaxChildren: -1}})
	proto := filepath.Join(groups, "root", "prototype")
	if err := os.MkdirAll(proto, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proto, "PERSONA.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	childJID := "tg:555"
	childFolder := spawnFolderName("root", childJID)
	loop.delegate(childFolder, "do the thing", childJID)

	if !db.GroupExists(childFolder) {
		t.Fatalf("child group %q not spawned", childFolder)
	}
	// prototype was copied into the child dir.
	if _, err := os.Stat(filepath.Join(groups, childFolder, "PERSONA.md")); err != nil {
		t.Fatalf("prototype not copied: %v", err)
	}
	// a room-matched route now targets the child.
	routes, _ := db.Routes()
	var routed bool
	for _, r := range routes {
		if r.Target == childFolder && r.Match == "room="+core.JidRoom(childJID) {
			routed = true
		}
	}
	if !routed {
		t.Fatalf("no room route for spawned child: %+v", routes)
	}
	// the delegation row landed on the child with the return address.
	msgs, _ := db.MessagesSince(childFolder, "")
	if len(msgs) != 1 || msgs[0].Content != "do the thing" || msgs[0].ForwardedFrom != childJID {
		t.Fatalf("delegation row=%+v want content=do the thing from %s", msgs, childJID)
	}
}

// TestSpawnDeniedNoPrototype: an unknown target whose parent has no prototype/
// dir is NOT spawned and drops (no group, no route) — routd never invents a
// container/group out of thin air.
func TestSpawnDeniedNoPrototype(t *testing.T) {
	db, loop, _ := recLoop(t)
	_ = db.PutGroup(core.Group{Folder: "root"})
	loop.delegate("root/ghost", "hi", "tg:1")
	if db.GroupExists("root/ghost") {
		t.Fatal("child spawned without a prototype dir")
	}
}

// TestBudgetGateRefusesOverCap: a folder at/over its daily cost cap refuses the
// turn pre-spawn — no run dispatched, a channel-visible refusal delivered, and
// the cursor advances past the batch (gated budgetGate parity).
func TestBudgetGateRefusesOverCap(t *testing.T) {
	db, loop, rr := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	loop.costCapsEnabled = true
	_ = db.PutGroup(core.Group{Folder: "demo"})
	// cap = 100 cents; spend already 150 cents today.
	if _, err := db.SQL().Exec("UPDATE groups SET cost_cap_cents_per_day=100 WHERE folder='demo'"); err != nil {
		t.Fatal(err)
	}
	_ = db.PutCost("demo", "t-prior", "", "claude", 0, 0, 150)
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "web:demo", Sender: "u",
		Content: "expensive question", Timestamp: time.Now().UTC()})

	had, err := loop.processGroupMessages("web:demo")
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(rr.runs) != 0 {
		t.Fatalf("over-cap turn dispatched a run: %+v", rr.runs)
	}
	if !had {
		t.Fatal("over-cap refusal should report hadOutput=true (consumed)")
	}
	want := "Budget reached for today (channel spent 150 of 100 cents). Resumes at 00:00 UTC."
	if got := lastAck(t, dl); got != want {
		t.Fatalf("refusal=%q want %q", got, want)
	}
	if db.GetAgentCursor("web:demo") == "" {
		t.Fatal("refused turn did not advance cursor")
	}
}

// TestBudgetGateAllowsUnderCap: a folder under its cap dispatches normally.
func TestBudgetGateAllowsUnderCap(t *testing.T) {
	db, loop, rr := recLoop(t)
	loop.costCapsEnabled = true
	_ = db.PutGroup(core.Group{Folder: "demo"})
	if _, err := db.SQL().Exec("UPDATE groups SET cost_cap_cents_per_day=1000 WHERE folder='demo'"); err != nil {
		t.Fatal(err)
	}
	_ = db.PutCost("demo", "t-prior", "", "claude", 0, 0, 150)
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "web:demo", Sender: "u",
		Content: "cheap question", Timestamp: time.Now().UTC()})

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(rr.runs) != 1 {
		t.Fatalf("under-cap turn dispatched %d runs want 1", len(rr.runs))
	}
}

// TestBudgetGateZeroCapUncapped: a folder with cap=0 (the default) is uncapped
// regardless of spend — the turn dispatches.
func TestBudgetGateZeroCapUncapped(t *testing.T) {
	db, loop, rr := recLoop(t)
	loop.costCapsEnabled = true
	_ = db.PutGroup(core.Group{Folder: "demo"}) // cap defaults to 0
	_ = db.PutCost("demo", "t-prior", "", "claude", 0, 0, 9999)
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "web:demo", Sender: "u",
		Content: "q", Timestamp: time.Now().UTC()})

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(rr.runs) != 1 {
		t.Fatalf("zero-cap folder gated (runs=%d want 1)", len(rr.runs))
	}
}

// TestBudgetGateDisabledBypasses: costCapsEnabled=false bypasses the gate even
// when over cap (the operator escape hatch).
func TestBudgetGateDisabledBypasses(t *testing.T) {
	db, loop, rr := recLoop(t)
	loop.costCapsEnabled = false
	_ = db.PutGroup(core.Group{Folder: "demo"})
	if _, err := db.SQL().Exec("UPDATE groups SET cost_cap_cents_per_day=10 WHERE folder='demo'"); err != nil {
		t.Fatal(err)
	}
	_ = db.PutCost("demo", "t-prior", "", "claude", 0, 0, 9999)
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "web:demo", Sender: "u",
		Content: "q", Timestamp: time.Now().UTC()})

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(rr.runs) != 1 {
		t.Fatalf("disabled gate still refused (runs=%d want 1)", len(rr.runs))
	}
}
