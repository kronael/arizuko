package routd

import (
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

// TestServeTurnMCP_RevokeAppliesToNextCallInSameTurn is the regression guard for
// the property the whole no-baked-permissions design rests on: authorization is
// read from the DB on EVERY tool call, so revoking a grant takes effect on the
// turn's next call — no waiting for the turn or any token to expire.
//
// The socket stays up across both calls: same turn, same connection-less socket,
// same agent. Only the acl table changes between them.
//
// Note which half of the gate catches it. toolGrant's first half tests `rules`,
// a SNAPSHOT taken by deriveFolderGrants when the socket was bound; the second
// half calls db.Authorize, which hits SQLite live. A mid-turn deny is invisible
// to the snapshot, so if anyone ever caches the live half per turn, this test is
// what fails. The asymmetry is deliberate: revocation is immediate, elevation is
// not (a mid-turn allow still needs the next turn to register the tool).
func TestServeTurnMCP_RevokeAppliesToNextCallInSameTurn(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	const folder = "w/a/b/c"
	const jid = "slack:team/channel/c1"
	// PutGroup binds the folder to role:member (the 4/R messaging floor), which
	// is what makes reply visible — magnitude is role membership, not depth.
	if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
		t.Fatal(err)
	}
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: folder}})
	if _, err := db.PutTurnContext("t1", folder, "", jid, "u1", ""); err != nil {
		t.Fatal(err)
	}

	deliver := &recDeliverer{pid: "pid-x"}
	srv := NewServer(db, nil, deliver, nil, 0, "")
	ipcDir := filepath.Join(t.TempDir(), "ipc", folder)
	stop, err := srv.ServeTurnMCP(
		turnMCP{folder: folder, chatJID: jid, turnID: "t1", trigger: "u1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	defer stop()
	sock := groupfolder.IpcSocket(ipcDir)

	// 1. Before revocation: the role:member floor permits reply.
	if _, errText := callToolOverSock(t, sock, "reply",
		map[string]any{"chatJid": jid, "text": "first"}); errText != "" {
		t.Fatalf("reply should be permitted by the role:member floor, got: %s", errText)
	}
	if len(deliver.sends) != 1 {
		t.Fatalf("deliver.sends=%d want 1 after the permitted call", len(deliver.sends))
	}

	// 2. Operator revokes mid-turn. The socket is untouched and still bound.
	addACL(t, db, "folder:"+folder, "mcp:reply", folder, "deny")

	// 3. The very next call on that same live socket must be denied.
	if _, errText := callToolOverSock(t, sock, "reply",
		map[string]any{"chatJid": jid, "text": "second"}); errText == "" {
		t.Fatal("reply must be denied on the next call after the deny row was added")
	}
	if len(deliver.sends) != 1 {
		t.Fatalf("deliver.sends=%d want 1 — the revoked call must not have delivered", len(deliver.sends))
	}
}
