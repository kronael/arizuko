package routd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/groupfolder"
)

// TestBuildGatedFnsSendReply: SendReply is deliver-only — it fans the text out
// to the Deliverer and returns the platform id, matching gated's SendReply. It
// does NOT persist the bot row; that is the ipc tool layer's recordOutbound (not
// invoked when the closure is called directly). The full socket path including
// the persist is covered by TestServeTurnMCP_ReplyOverSocket.
func TestBuildGatedFnsSendReply(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	deliver := &recDeliverer{pid: "pid-9"}
	srv := NewServer(db, nil, deliver, nil, 0, "")
	if _, err := db.PutTurnContext("t1", "demo", "", "tg:42", "u1", ""); err != nil {
		t.Fatal(err)
	}

	fns := srv.buildGatedFns(turnMCP{folder: "demo", topic: "", chatJID: "tg:42", turnID: "t1", trigger: "u1"})
	pid, err := fns.SendReply("tg:42", "answer", "")
	if err != nil {
		t.Fatalf("SendReply: %v", err)
	}
	if pid != "pid-9" {
		t.Fatalf("platform id=%q want pid-9", pid)
	}
	if len(deliver.sends) != 1 || deliver.sends[0].text != "answer" {
		t.Fatalf("deliver.sends=%+v want one 'answer'", deliver.sends)
	}
}

// TestServeTurnMCPSocketLifecycle: ServeTurnMCP binds the group MCP socket in a
// temp ipc dir; the returned stop removes it.
func TestServeTurnMCPSocketLifecycle(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")

	// Non-existent nested dir: routd serves before runed mkdirs, so ServeTurnMCP
	// must create the parent itself (else net.Listen fails on a fresh folder).
	ipcDir := filepath.Join(t.TempDir(), "ipc", "demo")
	stop, err := srv.ServeTurnMCP(turnMCP{folder: "demo", turnID: "t1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	sock := groupfolder.IpcSocket(ipcDir)
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket not bound at %s: %v", sock, err)
	}
	stop()
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket %s still present after stop (err=%v)", filepath.Base(sock), err)
	}
}
