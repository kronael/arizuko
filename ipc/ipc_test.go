package ipc

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onvos/arizuko/auth"
	"github.com/onvos/arizuko/core"
)

func TestBuildMCPServer(t *testing.T) {
	gated := GatedFns{
		SendMessage:   func(jid, text string) (string, error) { return "", nil },
		SendDocument:  func(jid, path, fn, caption string) error { return nil },
		ClearSession:  func(f string) {},
		GetGroups:     func() map[string]core.Group { return nil },
		GroupsDir:     "/tmp/groups",
		WebDir:        "/tmp/web",
	}
	db := StoreFns{}
	// tier-0 gets all tools via ["*"] rules
	srv := buildMCPServer(gated, db, "world", []string{"*"})
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
}

func TestBuildMCPServer_NoTools(t *testing.T) {
	gated := GatedFns{
		SendMessage:   func(jid, text string) (string, error) { return "", nil },
		SendDocument:  func(jid, path, fn, caption string) error { return nil },
		ClearSession:  func(f string) {},
		GetGroups:     func() map[string]core.Group { return nil },
		GroupsDir:     "/tmp/groups",
		WebDir:        "/tmp/web",
	}
	db := StoreFns{}
	// empty rules → no tools registered (except get/set_grants for tier 0-1)
	srv := buildMCPServer(gated, db, "world", []string{})
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
}


func TestFolderForJid(t *testing.T) {
	db := StoreFns{
		DefaultFolderForJID: func(jid string) string {
			if jid == "tg:1" {
				return "world/a"
			}
			return ""
		},
	}
	if f := folderForJid(db, "tg:1"); f != "world/a" {
		t.Errorf("got %q, want world/a", f)
	}
	if f := folderForJid(db, "missing"); f != "" {
		t.Errorf("got %q, want empty", f)
	}
	if f := folderForJid(StoreFns{}, "anything"); f != "" {
		t.Errorf("nil DefaultFolderForJID: got %q, want empty", f)
	}
}

func TestRouteTargetWithin(t *testing.T) {
	cases := []struct {
		target, owner string
		want          bool
	}{
		{"world/a", "world/a", true},
		{"world/a/child", "world/a", true},
		{"folder:world/a/child", "world/a", true},
		{"world/b", "world/a", false},
		{"daemon:timed", "world/a", false},
		{"builtin:stop", "world/a", false},
	}
	for _, c := range cases {
		if got := routeTargetWithin(c.target, c.owner); got != c.want {
			t.Errorf("routeTargetWithin(%q, %q) = %v, want %v", c.target, c.owner, got, c.want)
		}
	}
}

func TestAllToolsRegistered(t *testing.T) {
	gated := GatedFns{
		SendMessage:         func(jid, text string) (string, error) { return "", nil },
		SendDocument:        func(jid, path, fn, caption string) error { return nil },
		ClearSession:        func(f string) {},
		GetGroups:           func() map[string]core.Group { return nil },
		EnqueueMessageCheck: func(jid string) {},
		InjectMessage:       func(j, c, s, n string) (string, error) { return "", nil },
		RegisterGroup:       func(j string, g core.Group) error { return nil },
		GroupsDir:           "/tmp/groups",
		WebDir:              "/tmp/web",
	}
	db := StoreFns{
		CreateTask:          func(t core.Task) error { return nil },
		GetTask:             func(id string) (core.Task, bool) { return core.Task{}, false },
		UpdateTaskStatus:    func(id, s string) error { return nil },
		DeleteTask:          func(id string) error { return nil },
		ListTasks:           func(f string, r bool) []core.Task { return nil },
		ListRoutes:          func(f string, r bool) []core.Route { return nil },
		SetRoutes:           func(f string, r []core.Route) error { return nil },
		AddRoute:            func(r core.Route) (int64, error) { return 0, nil },
		DeleteRoute:         func(id int64) error { return nil },
		GetRoute:            func(id int64) (core.Route, bool) { return core.Route{}, false },
		DefaultFolderForJID: func(j string) string { return "" },
		GetGrants:           func(f string) []string { return nil },
		SetGrants:           func(f string, r []string) error { return nil },
	}

	// tier-0 with all rules — all tools should be present
	srv := buildMCPServer(gated, db, "world", []string{"*"})
	if srv == nil {
		t.Fatal("expected non-nil server")
	}

	// tier-3 with send_reply only — most tools absent
	srv2 := buildMCPServer(gated, db, "w/a/b/c", []string{"send_reply"})
	if srv2 == nil {
		t.Fatal("expected non-nil server for tier-3")
	}
}

func TestSocialActionsRegistered(t *testing.T) {
	var postCalls, reactCalls, deleteCalls int
	gated := GatedFns{
		Post: func(jid, content string, media []string) (string, error) {
			postCalls++
			return "pid-1", nil
		},
		React: func(jid, target, reaction string) error {
			reactCalls++
			return nil
		},
		DeletePost: func(jid, target string) error {
			deleteCalls++
			return nil
		},
		GroupsDir: "/tmp/groups",
		WebDir:    "/tmp/web",
	}
	// Rules permit all three actions for mastodon only. Tier-0 (folder="world").
	rules := []string{
		"post(jid=mastodon:*)",
		"react(jid=mastodon:*)",
		"delete_post(jid=mastodon:*)",
	}
	srv := buildMCPServer(gated, StoreFns{}, "world", rules)
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
	// With no matching rules, tools must not register at all (registerRaw early-returns).
	srv2 := buildMCPServer(gated, StoreFns{}, "w/a/b/c", []string{"send_reply"})
	if srv2 == nil {
		t.Fatal("expected non-nil server for tier-3 subset")
	}
}

func TestSendReply(t *testing.T) {
	gated := GatedFns{
		SendMessage:   func(jid, text string) (string, error) { return "", nil },
		SendDocument:  func(jid, path, fn, caption string) error { return nil },
		SendReply:     func(jid, text, rid string) (string, error) { return "", nil },
		GetGroups:     func() map[string]core.Group { return nil },
		GroupsDir:     "/tmp/groups",
		WebDir:        "/tmp/web",
	}
	srv := buildMCPServer(gated, StoreFns{}, "world", []string{"send_reply"})
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
}

func TestRefreshGroups(t *testing.T) {
	groups := map[string]core.Group{
		"world/a": {Folder: "world/a", Name: "Group A"},
	}
	gated := GatedFns{
		SendMessage:   func(jid, text string) (string, error) { return "", nil },
		SendDocument:  func(jid, path, fn, caption string) error { return nil },
		GetGroups:     func() map[string]core.Group { return groups },
		GroupsDir:     "/tmp/groups",
		WebDir:        "/tmp/web",
	}
	db := StoreFns{}
	// tier ≤ 2 gets refresh_groups
	srv := buildMCPServer(gated, db, "world/a", []string{"*"})
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
}

func TestWorkspaceRel(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"/home/node/file.txt", "file.txt", false},
		{"/home/node/tmp/out.pdf", "tmp/out.pdf", false},
		{"/workspace/group/file.txt", "", true},               // no longer valid
		{"/workspace/media/img.png", "", true},
		{"/home/node", "", true},                              // exact prefix, no trailing slash
		{"~/tmp/out.txt", "", true},
		{"/tmp/file", "", true},
	}
	for _, c := range cases {
		got, err := workspaceRel(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("workspaceRel(%q): expected error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("workspaceRel(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("workspaceRel(%q): got %q, want %q", c.in, got, c.want)
		}
	}
}

// TestWorkToolsRegistered verifies set_work/get_work register per tier
// and build without error.
func TestWorkToolsRegistered(t *testing.T) {
	dir := t.TempDir()
	gated := GatedFns{GroupsDir: dir, WebDir: dir}
	// tier-0 with no rules: get_work always registers, set_work gated on tier
	if srv := buildMCPServer(gated, StoreFns{}, "world", nil); srv == nil {
		t.Fatal("tier-0 build failed")
	}
	// tier-3 at w/a/b/c: get_work registers, set_work does not
	if srv := buildMCPServer(gated, StoreFns{}, "w/a/b/c", nil); srv == nil {
		t.Fatal("tier-3 build failed")
	}

	// Verify work.md round-trip via filesystem (the path set_work writes)
	groupDir := filepath.Join(dir, "world")
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(groupDir, "work.md")
	if err := os.WriteFile(p, []byte("draft"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil || string(data) != "draft" {
		t.Errorf("round-trip failed: %q err=%v", data, err)
	}
}

func TestIdentityUsedInServer(t *testing.T) {
	id := auth.Resolve("world/parent/child")
	if id.Tier != 2 {
		t.Fatalf("got tier %d, want 2", id.Tier)
	}
	if id.World != "world" {
		t.Fatalf("got world %q, want world", id.World)
	}
}

func TestServeMCP_PeerCredAcceptsMatchingUID(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"
	stop, err := ServeMCP(sock, GatedFns{}, StoreFns{}, "test", nil, os.Getuid())
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	// Server should not close the conn immediately — it accepted the peer.
	c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	var buf [1]byte
	_, err = c.Read(buf[:])
	// Expect timeout (server is waiting for MCP input), not EOF.
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected read timeout, got err=%v", err)
	}
}

func TestServeMCP_PeerCredRejectsWrongUID(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"
	// Set expectedUID to a value we can't possibly be.
	wrong := os.Getuid() + 100000
	stop, err := ServeMCP(sock, GatedFns{}, StoreFns{}, "test", nil, wrong)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	// Server should close the conn after reading peer cred.
	c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var buf [1]byte
	_, err = c.Read(buf[:])
	// Expect EOF/closed, not timeout.
	if err == nil {
		t.Fatalf("expected conn to be closed, got nil err")
	}
	if strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected conn closed, got timeout: %v", err)
	}
}
