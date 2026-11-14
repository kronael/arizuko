package routd

import (
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
)

func TestMutedFolderMCPText(t *testing.T) {
	t.Skip("L870: folder mute does not reach MCP text sends")
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	const jid = "slack:team/channel/c1"
	if _, err := db.PutTurnContext("t1", "demo", "", jid, "u1", ""); err != nil {
		t.Fatal(err)
	}
	deliver := &recDeliverer{pid: "pid"}
	srv := NewServer(db, nil, deliver, nil, 0, "")
	srv.SetDisabledGroups([]string{"demo"})
	bound := turnMCP{folder: "demo", chatJID: jid, turnID: "t1", trigger: "u1"}
	sock := t.TempDir() + "/mcp.sock"
	stop, err := ipc.ServeMCP(sock, srv.buildRoutdFns(bound), srv.buildStoreFns(bound), "demo", true, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	_, errText := callToolOverSock(t, sock, "send", map[string]any{"chatJid": jid, "text": "muted"})
	if errText != "" {
		t.Fatal(errText)
	}
	if len(deliver.sends) != 0 {
		t.Fatalf("muted folder delivered %d messages", len(deliver.sends))
	}
	if n := countBots(t, db, jid); n != 1 {
		t.Fatalf("stored %d messages", n)
	}
}
