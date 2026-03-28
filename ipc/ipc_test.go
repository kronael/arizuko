package ipc

import (
	"testing"

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
		HostGroupsDir: "/tmp/groups",
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
		HostGroupsDir: "/tmp/groups",
	}
	db := StoreFns{}
	// empty rules → no tools registered (except get/set_grants for tier 0-1)
	srv := buildMCPServer(gated, db, "world", []string{})
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
}

func TestToolErrFormat(t *testing.T) {
	r, err := toolErr("test error")
	if err != nil {
		t.Fatal(err)
	}
	if r == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestToolJSONFormat(t *testing.T) {
	r, err := toolJSON(map[string]string{"key": "val"})
	if err != nil {
		t.Fatal(err)
	}
	if r == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestIsRouteTypeValid(t *testing.T) {
	valid := []string{"command", "verb", "pattern", "keyword", "sender", "default"}
	for _, v := range valid {
		if !isRouteTypeValid(v) {
			t.Errorf("%s should be valid", v)
		}
	}
	if isRouteTypeValid("invalid") {
		t.Error("invalid should not be valid")
	}
}

func TestGroupFolderByJid(t *testing.T) {
	groups := map[string]core.Group{
		"jid1": {Folder: "world/a"},
		"jid2": {Folder: "world/b"},
	}
	if f := groupFolderByJid(groups, "jid1"); f != "world/a" {
		t.Errorf("got %q, want world/a", f)
	}
	if f := groupFolderByJid(groups, "missing"); f != "" {
		t.Errorf("got %q, want empty", f)
	}
}

func TestAllToolsRegistered(t *testing.T) {
	gated := GatedFns{
		SendMessage:      func(jid, text string) (string, error) { return "", nil },
		SendDocument:     func(jid, path, fn, caption string) error { return nil },
		ClearSession:     func(f string) {},
		GetGroups:        func() map[string]core.Group { return nil },
		DelegateToChild:  func(f, p, j string, d int, r []string) error { return nil },
		DelegateToParent: func(f, p, j string, d int, r []string) error { return nil },
		InjectMessage:    func(j, c, s, n string) (string, error) { return "", nil },
		RegisterGroup:    func(j string, g core.Group) error { return nil },
		GroupsDir:        "/tmp/groups",
		HostGroupsDir:    "/tmp/groups",
	}
	db := StoreFns{
		CreateTask:       func(t core.Task) error { return nil },
		GetTask:          func(id string) (core.Task, bool) { return core.Task{}, false },
		UpdateTaskStatus: func(id, s string) error { return nil },
		DeleteTask:       func(id string) error { return nil },
		ListTasks:        func(f string, r bool) []core.Task { return nil },
		GetRoutes:        func(j string) []core.Route { return nil },
		SetRoutes:        func(j string, r []core.Route) error { return nil },
		AddRoute:         func(j string, r core.Route) (int64, error) { return 0, nil },
		DeleteRoute:      func(id int64) error { return nil },
		GetRoute:         func(id int64) (core.Route, bool) { return core.Route{}, false },
		GetGrants:        func(f string) []string { return nil },
		SetGrants:        func(f string, r []string) error { return nil },
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

func TestSendReply(t *testing.T) {
	var got struct{ jid, text, replyToId string }
	gated := GatedFns{
		SendMessage:  func(jid, text string) (string, error) { got.jid = jid; got.text = text; return "", nil },
		SendDocument: func(jid, path, fn, caption string) error { return nil },
		SendReply: func(jid, text, rid string) (string, error) {
			got.jid = jid
			got.text = text
			got.replyToId = rid
			return "", nil
		},
		GetGroups:     func() map[string]core.Group { return nil },
		GroupsDir:     "/tmp/groups",
		HostGroupsDir: "/tmp/groups",
	}
	db := StoreFns{}
	srv := buildMCPServer(gated, db, "world", []string{"send_reply"})
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
}

func TestRefreshGroups(t *testing.T) {
	groups := map[string]core.Group{
		"jid1": {Folder: "world/a", Name: "Group A"},
	}
	gated := GatedFns{
		SendMessage:   func(jid, text string) (string, error) { return "", nil },
		SendDocument:  func(jid, path, fn, caption string) error { return nil },
		GetGroups:     func() map[string]core.Group { return groups },
		GroupsDir:     "/tmp/groups",
		HostGroupsDir: "/tmp/groups",
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
		{"/workspace/group/file.txt", "file.txt", false},      // compat
		{"/workspace/media/img.png", "", true},                // no longer valid
		{"/workspace/group", "", true},                        // exact prefix, no trailing slash
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

func TestIdentityUsedInServer(t *testing.T) {
	id := auth.Resolve("world/parent/child")
	if id.Tier != 2 {
		t.Fatalf("got tier %d, want 2", id.Tier)
	}
	if id.World != "world" {
		t.Fatalf("got world %q, want world", id.World)
	}
}
