package routd

import (
	"context"
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

// TestRouteMissChatID: /chatid answers on an UNROUTED chat too — the exact
// case a new user needs it for (self-reporting their JID for onboarding).
// Steer never runs on a miss, so routeMiss carries the intercept; the miss
// still queues onboarding (the /chatid is a first contact like any other).
func TestRouteMissChatID(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	loop := NewLoop(db, nopRunner{}, LoopConfig{OnboardingEnabled: true})
	loop.StopQueue()
	fo := &fakeOnbod{}
	loop.SetOnbodClient(fo)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutMessage(core.Message{ID: "c1", ChatJID: "telegram:group/999", Sender: "u",
		Content: "/chatid", Timestamp: time.Now().UTC(), Verb: "message"})

	if _, err := loop.processGroupMessages("telegram:group/999"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if got := lastAck(t, dl); got != "telegram:group/999" {
		t.Fatalf("route-miss /chatid ack=%q want the jid", got)
	}
	if len(fo.onboarded) != 1 || fo.onboarded[0] != "telegram:group/999" {
		t.Fatalf("route-miss /chatid skipped onboarding: %v", fo.onboarded)
	}
	if db.GetAgentCursor("telegram:group/999") == "" {
		t.Fatal("route-miss /chatid did not advance cursor")
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

// TestCmdStatusOperatorOnly: /status from an operator (a `**` holder — here the
// steerOne sender "u" made a role:operator member) reports the instance counts
// in gated's exact format. Instance-wide visibility is an operator privilege,
// not a folder position.
func TestCmdStatusOperatorOnly(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "demo"})
	_ = db.AddMembership("u", "role:operator", "test")
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
	_ = db.AddMembership("u", "role:operator", "test")
	_ = db.MarkChatErrored("tg:99")
	_ = steerOne(loop, "web:demo", "demo", "/status")
	if !strings.Contains(lastAck(t, dl), "errored chats: 1") {
		t.Fatalf("/status ack=%q want errored chats: 1", lastAck(t, dl))
	}
}

// TestCmdStatusPermissionDenied: a non-operator (a bare top-level world is a
// tenant now, not root) is denied.
func TestCmdStatusPermissionDenied(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "root/child"})
	_ = steerOne(loop, "web:root/child", "root/child", "/status")
	if got := lastAck(t, dl); got != "Permission denied: operator only." {
		t.Fatalf("/status non-operator ack=%q", got)
	}
}

// TestCmdRootUsage: an operator's bare /root prompts usage (gateway.cmdRoot).
func TestCmdRootUsage(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "root/eng"})
	_ = db.AddMembership("u", "role:operator", "test")
	_ = steerOne(loop, "web:root/eng", "root/eng", "/root")
	if got := lastAck(t, dl); got != "Usage: /root <message>" {
		t.Fatalf("/root ack=%q", got)
	}
}

// TestCmdRootDeniedNonOperator: /root from a non-operator is denied outright —
// NO silent downgrade. Root is obtainable only by a `**` holder.
func TestCmdRootDeniedNonOperator(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "demo"})
	if !steerOne(loop, "web:demo", "demo", "/root do a thing") {
		t.Fatal("/root not consumed")
	}
	if got := lastAck(t, dl); !strings.Contains(got, "operator grant") {
		t.Fatalf("non-operator /root ack=%q want operator-denial", got)
	}
	// No message was re-injected — the turn was refused, not downgraded.
	if msgs, _ := db.MessagesSince("web:demo", ""); len(msgs) != 0 {
		t.Fatalf("non-operator /root re-injected %d messages, want 0", len(msgs))
	}
}

// TestCmdRootElevates: an operator's /root re-injects the instruction in place
// and registers the message for elevation, so the ensuing turn spawns root.
func TestCmdRootElevates(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "demo"})
	_ = db.AddMembership("u", "role:operator", "test")
	if !steerOne(loop, "web:demo", "demo", "/root escalate this") {
		t.Fatal("/root not consumed")
	}
	msgs, _ := db.MessagesSince("web:demo", "")
	if len(msgs) != 1 {
		t.Fatalf("re-injected rows=%d want 1", len(msgs))
	}
	if msgs[0].Content != "escalate this" {
		t.Fatalf("re-injected row=%+v want content=escalate this", msgs[0])
	}
	if _, ok := loop.pendingElevation.Load(msgs[0].ID); !ok {
		t.Fatal("re-injected message not registered for elevation")
	}
}

// TestWorkerSteersRootCommand: the queue worker path (processGroupMessages) —
// what adapter ingest enqueues into directly (server.go handleInbound) —
// intercepts /root itself. Before the fix steering lived only in the pollOnce
// backstop, so an ingest-enqueued "/root …" raced past it and ran as a plain
// UNELEVATED turn with the literal /root text (marinade atlas, 2026-07-16).
// The consumed command spawns no run; the re-injected instruction runs
// elevated on the next worker pass.
func TestWorkerSteersRootCommand(t *testing.T) {
	db, loop, rr := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "demo"})
	_ = db.AddMembership("u", "role:operator", "test")
	_ = db.PutMessage(core.Message{ID: "m1", ChatJID: "web:demo", Sender: "u",
		Content: "/root mint the token", Timestamp: time.Now().UTC()})

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process /root: %v", err)
	}
	if len(rr.runs) != 0 {
		t.Fatalf("raw /root reached a turn unsteered: %+v", rr.runs)
	}
	msgs, _ := db.MessagesSince("web:demo", db.GetAgentCursor("web:demo"))
	if len(msgs) != 1 || msgs[0].Content != "mint the token" {
		t.Fatalf("re-injected batch=%+v want one row with the /root arg", msgs)
	}
	if _, ok := loop.pendingElevation.Load(msgs[0].ID); !ok {
		t.Fatal("re-injected message not registered for elevation")
	}

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process injected: %v", err)
	}
	if len(rr.runs) != 1 || !rr.runs[0].elevated {
		t.Fatalf("injected instruction runs=%+v want exactly one ELEVATED run", rr.runs)
	}
}

// TestCmdInviteFederation: /invite is operator-gated (`**`), enforces the arg
// shape, then reports the onbod federation gap (routd cannot mint invites).
func TestCmdInviteFederation(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "root"})

	// non-operator → denied.
	_ = steerOne(loop, "web:root", "root", "/invite")
	if got := lastAck(t, dl); got != "Permission denied: operator only." {
		t.Fatalf("/invite non-operator ack=%q", got)
	}
	_ = db.AddMembership("u", "role:operator", "test")
	// bad arg → gated's usage message.
	_ = steerOne(loop, "web:root", "root", "/invite zero")
	if got := lastAck(t, dl); got != "Usage: /invite [max_uses]" {
		t.Fatalf("/invite bad-arg ack=%q", got)
	}
	// operator + ok → federation notice (not silently dropped).
	_ = steerOne(loop, "web:root", "root", "/invite")
	if !strings.Contains(lastAck(t, dl), "onbod") {
		t.Fatalf("/invite operator ack=%q want onbod federation note", lastAck(t, dl))
	}
}

// TestCmdGateFederation: /gate is operator-gated (`**`), enforces the subcommand
// shape, then reports the onbod federation gap.
func TestCmdGateFederation(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "root"})

	_ = steerOne(loop, "web:root", "root", "/gate list")
	if got := lastAck(t, dl); got != "Permission denied: operator only." {
		t.Fatalf("/gate non-operator ack=%q", got)
	}
	_ = db.AddMembership("u", "role:operator", "test")
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
	created   []string // target globs passed to CreateInvite/CreateInviteFull
	listed    []Invite // canned ListInvites result
	revoked   []string // tokens passed to RevokeInvite
	onboarded []string // jids passed to InsertOnboarding
	insertErr error    // forced InsertOnboarding failure (fail-loud tests)
	gates     []GateRow
	putCalls  []string // "gate=N"
	rmCalls   []string
	enCalls   []string // "gate=true|false"
}

func (f *fakeOnbod) CreateInvite(targetGlob string, maxUses int) (string, error) {
	f.created = append(f.created, targetGlob)
	return "tok-123", nil
}
func (f *fakeOnbod) CreateInviteFull(targetGlob, issuedBySub string, maxUses int, expiresAt *time.Time) (Invite, error) {
	f.created = append(f.created, targetGlob)
	return Invite{Token: "tok-123", TargetGlob: targetGlob, IssuedBySub: issuedBySub, MaxUses: maxUses, ExpiresAt: expiresAt}, nil
}
func (f *fakeOnbod) ListInvites(issuedBy string) ([]Invite, error) { return f.listed, nil }
func (f *fakeOnbod) RevokeInviteByRef(ref string) error {
	f.revoked = append(f.revoked, ref)
	return nil
}
func (f *fakeOnbod) InsertOnboarding(jid string) error {
	if f.insertErr != nil {
		return f.insertErr
	}
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
	_ = db.AddMembership("u", "role:operator", "test")

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
	_ = db.AddMembership("u", "role:operator", "test")

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

// TestDelegateToMissingFolderFailsLoudly: delegating to a folder that does not
// exist creates nothing and reports back into the originating chat, naming the
// missing folder AND register_group — routing consumed the message, so a silent
// drop would swallow it. Replaces TestSpawnOnDelegation + TestSpawnDeniedNoPrototype:
// no parent, no prototype/ dir and no config ever auto-creates a group.
func TestDelegateToMissingFolderFailsLoudly(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "root"})

	loop.delegate("root/ghost", "do the thing", "tg:1")

	if db.GroupExists("root/ghost") {
		t.Fatal("delegation auto-created a group; groups are created explicitly")
	}
	if routes, _ := db.Routes(); len(routes) != 0 {
		t.Fatalf("delegation added routes for a missing folder: %+v", routes)
	}
	if msgs, _ := db.MessagesSince("root/ghost", ""); len(msgs) != 0 {
		t.Fatalf("delegation row landed on a non-existent folder: %+v", msgs)
	}
	got := lastAck(t, dl)
	for _, want := range []string{"root/ghost", "register_group"} {
		if !strings.Contains(got, want) {
			t.Errorf("delegation failure ack = %q, must name %q so the agent knows what to do", got, want)
		}
	}
}

// TestDelegateToExistingChildStillWorks: the shape live instances already have —
// an ordinary child group with an ordinary room= route — routes and delegates
// exactly as before the auto-spawn removal.
func TestDelegateToExistingChildStillWorks(t *testing.T) {
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	loop.deliver = dl
	_ = db.PutGroup(core.Group{Folder: "rhias"})
	_ = db.PutGroup(core.Group{Folder: "rhias/nemo"})
	if _, err := db.AddRoute(core.Route{Seq: 0, Match: "room=" + core.JidRoom("tg:555"), Target: "rhias/nemo"}); err != nil {
		t.Fatal(err)
	}

	loop.delegate("rhias/nemo", "do the thing", "tg:555")

	msgs, _ := db.MessagesSince("rhias/nemo", "")
	if len(msgs) != 1 || msgs[0].Content != "do the thing" || msgs[0].ForwardedFrom != "tg:555" {
		t.Fatalf("delegation row=%+v want content=do the thing from tg:555", msgs)
	}
	if len(dl.sends) != 0 {
		t.Fatalf("a successful delegation must not ack an error: %+v", dl.sends)
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
