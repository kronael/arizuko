package routd

import (
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
)

func progressBroker(t *testing.T, dl *recDeliverer) (*DB, *Server, string) {
	t.Helper()
	dl.canThread = true
	db, srv := newProgressTurn(t, dl, "")
	if err := db.SetChatIsGroup(progressJID, true); err != nil {
		t.Fatal(err)
	}
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	bound := turnMCP{folder: "demo", chatJID: progressJID, turnID: "t1", trigger: "u1"}
	sock := t.TempDir() + "/gated.sock"
	stop, err := ipc.ServeMCP(sock, srv.buildRoutdFns(bound), srv.buildStoreFns(bound), "demo", true, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return db, srv, sock
}
