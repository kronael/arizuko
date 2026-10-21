package routd

// The inversion: a world folder may reach every tool inside its own subtree, and
// a human sees every one of those calls before it runs. The old shape was the
// opposite — a group that was never granted a tool simply could not use it, and
// the refusal was invisible to the person, so a trading world could not create a
// cron and nobody could tell why.
//
// Two rows carry it (onbod writeWorld) and seventeen carry the exception
// (store.SeedRoutdFloor): the messaging verbs and the always-mounted reads skip
// approval, so the agent still answers a message without asking anyone.

import (
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
)

// worldFolder gives `folder` exactly what onbod's writeWorld gives a new world:
// the member edge, the blanket tool reach and the blanket hold, all scoped to
// its own subtree. The rows are spelled here the way that writer spells them.
func worldFolder(t *testing.T, d *DB, folder string) {
	t.Helper()
	if err := d.PutGroup(core.Group{Folder: folder}); err != nil {
		t.Fatalf("create %s: %v", folder, err)
	}
	if err := addMembership(d, "folder:"+folder, store.RoleMember, "test"); err != nil {
		t.Fatalf("bind %s to role:member: %v", folder, err)
	}
	for _, action := range []string{"mcp:*", store.HoldPrefix + "*"} {
		if err := addACLRow(d, core.ACLRow{
			Principal: "folder:" + folder, Action: action,
			Scope: folder + "/**", Effect: store.EffectAllow, GrantedBy: "onbod",
		}); err != nil {
			t.Fatalf("grant %s to %s: %v", action, folder, err)
		}
	}
}

// TestWorld_HoldsWhatTheOwnerShouldSeeAndRunsTheRest is the whole inversion in
// one test. `schedule_task` is a tool nothing grants a plain folder: the world
// reaches it, the call waits, the owner approves in chat, and it runs in the turn
// that asked. `reply` is the same world's ordinary work and never waits.
func TestWorld_HoldsWhatTheOwnerShouldSeeAndRunsTheRest(t *testing.T) {
	waitForVerdict(t)

	d := memDB(t)
	dl := &recDeliverer{}
	srv := &Server{db: d, deliver: dl}
	worldFolder(t, d, "atlas")
	// The world's creator, exactly as onbod grants them.
	addACL(t, d, "owner", "admin", "atlas/**", store.EffectAllow)

	gate := srv.holdGate(turnMCP{folder: "atlas", chatJID: "tg:1", turnID: "t1"})

	out := make(chan bool, 1)
	go func() {
		_, held := gate("schedule_task", map[string]any{"cron": "0 9 * * *", "prompt": "check the desk"})
		out <- held
	}()
	id := awaitHeldID(t, d)
	// The gate records the row and THEN delivers the notice, so waiting for the
	// row is not waiting for the notice. awaitNotice takes the deliverer's lock,
	// which is what makes the read below safe as well as non-empty.
	awaitNotice(t, dl)

	// The person is told, in the chat, with the id they answer on.
	sends := dl.sentMsgs()
	if len(sends) != 1 || !containsAll(sends[0].text, "schedule_task", id) ||
		sends[0].options[0].Data != "/approve "+id {
		t.Fatalf("the owner was not asked: %+v", sends)
	}

	// The owner answers with the ordinary chat command.
	loop := &Loop{db: d, deliver: dl}
	loop.cmdResolveHold("tg:1", "atlas", id, "owner", PendingApproved, false)

	// held=false is the gate telling ipc to run the call.
	if <-out {
		t.Fatal("the approved call must run in the turn that asked")
	}
	if row, _ := pendingActionRow(d, id); row.Status != PendingApproved {
		t.Fatalf("status = %q, want approved", row.Status)
	}

	// And the world's ordinary work is never held: the floor's skip rows reach
	// this folder through its role:member edge.
	for _, verb := range []string{"reply", "send", "set_work", "inspect_routing"} {
		if _, held := gate(verb, map[string]any{"text": "hi"}); held {
			t.Errorf("%s waited for a human; the skip floor must cover it", verb)
		}
	}
	rows, err := d.ListPendingActions("atlas", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("the turn recorded %d held calls, want only schedule_task", len(rows))
	}
}

// TestWorld_ReachDoesNotCrossIntoASibling — the world's blanket grant is scoped
// to its own subtree, so it authorizes nothing in another tenant's folder.
func TestWorld_ReachDoesNotCrossIntoASibling(t *testing.T) {
	d := memDB(t)
	worldFolder(t, d, "atlas")
	if d.Authorize("folder:atlas", "borg/desk", "mcp:schedule_task", nil) {
		t.Fatal("a world's blanket tool grant reached another tenant's folder")
	}
	if !d.Authorize("folder:atlas", "atlas/desk", "mcp:schedule_task", nil) {
		t.Fatal("a world must reach every tool inside its own subtree")
	}
}

func containsAll(s string, want ...string) bool {
	for _, w := range want {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}

// TestWorld_HoldsOnlyWhatAPersonShouldSee walks the tools a world turn actually
// mounts and puts each on one side of the line. A read the agent needs every turn
// must never wait for a human. Messaging and ending the turn's own conversation
// also skip holds. Other writes wait under the blanket hold.
func TestWorld_HoldsOnlyWhatAPersonShouldSee(t *testing.T) {
	d := memDB(t)
	worldFolder(t, d, "atlas")
	srv := NewServer(d, nil, nil, nil, 0, "")
	sock := groupfolder.IpcSocket(t.TempDir())
	stop, err := ipc.ServeMCP(sock, srv.buildRoutdFns(turnMCP{folder: "atlas"}),
		srv.buildStoreFns(turnMCP{folder: "atlas"}), "atlas", false, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(2 * time.Second)
	for !fileExists(sock) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	// Reads, messages, and stopping the turn's own conversation.
	free := map[string]bool{}
	for _, n := range []string{
		"reply", "send", "send_file", "send_voice", "post", "forward", "quote",
		"repost", "like", "dislike", "edit", "delete", "disengage",
		"inspect_routing", "inspect_tasks", "inspect_session", "inspect_identity",
		"inspect_messages", "find_messages", "get_thread", "fetch_history",
		"get_work", "set_work",
	} {
		free[n] = true
	}

	for name := range listToolNames(t, sock) {
		held := d.CheckHold("folder:atlas", "atlas", name, nil)
		switch {
		case free[name] && held:
			t.Errorf("%s waits for a human; a world cannot read or answer without asking", name)
		case !free[name] && !held:
			t.Errorf("%s runs unwatched; a world's writes are what the blanket hold is for", name)
		}
	}
}
