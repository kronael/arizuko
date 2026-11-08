package routd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

func TestMentionRouteReadAccess(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	const parent = "discord:123/456"
	const topic = "789"
	const folder = "atlas/support"
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + parent + " verb=mention", Target: folder}, {Match: "chat_jid=" + parent, Target: "atlas"}})
	srv := NewServer(db, nil, &recDeliverer{history: []byte(`{"source":"platform","messages":[]}`)}, nil, 0, "")
	bound := turnMCP{folder: folder, chatJID: parent, topic: topic}
	fns := srv.buildStoreFns(bound)
	if !fns.JIDRoutedToFolder(parent, folder) {
		t.Error("exact bound chat must pass own-chat exception")
	}
	if fns.JIDRoutedToFolder(parent, "atlas/other") {
		t.Error("unrouted folder passed")
	}
	// A different turn still has access through the mention route.
	other := srv.buildStoreFns(turnMCP{folder: folder, chatJID: "discord:123/other"})
	if !other.JIDRoutedToFolder(parent, folder) {
		t.Error("mention route did not admit parent")
	}
	ipcDir := filepath.Join(t.TempDir(), "ipc")
	stop, err := srv.ServeTurnMCP(bound, ipcDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	sock := groupfolder.IpcSocket(ipcDir)
	routeDir := filepath.Join(t.TempDir(), "ipc")
	stopRoute, err := srv.ServeTurnMCP(turnMCP{folder: folder, chatJID: "discord:123/other"}, routeDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopRoute)
	routeSock := groupfolder.IpcSocket(routeDir)
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"get_thread", map[string]any{"chat_jid": parent, "topic": topic}},
		{"fetch_history", map[string]any{"chat_jid": parent}},
		{"inspect_messages", map[string]any{"chat_jid": parent}},
		{"inspect_routing", map[string]any{"jid": parent}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := callToolOverSock(t, routeSock, tc.name, tc.args); got != "" {
				t.Errorf("read denied: %s", got)
			}
		})
	}
	if _, got := callToolOverSock(t, sock, "fetch_history", map[string]any{"chat_jid": "discord:123/789"}); got != "" {
		t.Errorf("thread read denied: %s", got)
	}
	if _, got := callToolOverSock(t, sock, "fetch_history", map[string]any{"chat_jid": "discord:123/unrouted"}); !strings.Contains(got, "access_denied") {
		t.Errorf("unrouted chat: %q", got)
	}
}
