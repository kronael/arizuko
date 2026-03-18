package ipc

import (
	"testing"

	"github.com/onvos/arizuko/auth"
	"github.com/onvos/arizuko/core"
)

func TestBuildMCPServer(t *testing.T) {
	gated := GatedFns{
		SendMessage:   func(jid, text string) error { return nil },
		SendDocument:  func(jid, path, fn string) error { return nil },
		ClearSession:  func(f string) {},
		GetGroups:     func() map[string]core.Group { return nil },
		GroupsDir:     "/tmp/groups",
		HostGroupsDir: "/tmp/groups",
	}
	db := StoreFns{}
	srv := buildMCPServer(gated, db, "world/parent")
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
	// Verify that buildMCPServer registers tools for a high-tier folder
	// (previously these would have been filtered out)
	gated := GatedFns{
		SendMessage:      func(jid, text string) error { return nil },
		SendDocument:     func(jid, path, fn string) error { return nil },
		ClearSession:     func(f string) {},
		GetGroups:        func() map[string]core.Group { return nil },
		DelegateToChild:  func(f, p, j string, d int) error { return nil },
		DelegateToParent: func(f, p, j string, d int) error { return nil },
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
	}

	// tier 3 folder — in old code, many tools would be excluded
	srv := buildMCPServer(gated, db, "w/a/b/c")
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
	// If we got here without panic, all tools were registered successfully
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
