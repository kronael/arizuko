package ipc

import (
	"testing"
	"time"

	"github.com/onvos/arizuko/core"
)

// TestInspectToolsRegistered verifies the inspect_* family registers at
// every tier without panicking. Tier-gate semantics are exercised via
// the underlying store.ErroredChats / ListTasks (isRoot flag) in
// store/inspect_test.go; this test pins the MCP wiring.
func TestInspectToolsRegistered(t *testing.T) {
	now := time.Now()
	db := StoreFns{
		ListRoutes:          func(f string, r bool) []core.Route { return nil },
		ListTasks:           func(f string, r bool) []core.Task { return nil },
		DefaultFolderForJID: func(j string) string { return "world/a" },
		JIDRoutedToFolder:   func(j, f string) bool { return f == "world/a" },
		GetTask:             func(id string) (core.Task, bool) { return core.Task{Owner: "world/a"}, true },
		ErroredChats: func(f string, r bool) []ErroredChat {
			if r {
				return []ErroredChat{{ChatJID: "tg:1", Count: 2, LastAt: now, RoutedTo: "world/a"}}
			}
			if f == "world/a" {
				return []ErroredChat{{ChatJID: "tg:1", Count: 2, LastAt: now, RoutedTo: "world/a"}}
			}
			return nil
		},
		TaskRunLogs: func(id string, n int) []TaskRunLog {
			return []TaskRunLog{{ID: 1, TaskID: id, RunAt: now, Status: "ok"}}
		},
		RecentSessions: func(f string, n int) []core.SessionRecord {
			return []core.SessionRecord{{ID: 1, Folder: f, SessionID: "s1", StartedAt: now}}
		},
		GetSession: func(f, topic string) (string, bool) { return "s1", true },
	}
	gated := GatedFns{GroupsDir: "/tmp/groups", WebDir: "/tmp/web"}

	// tier-0 (root) — sees all
	if srv := buildMCPServer(gated, db, "world", []string{"*"}); srv == nil {
		t.Fatal("tier-0 server nil")
	}
	// tier-2 — scoped to own folder
	if srv := buildMCPServer(gated, db, "world/a/b", []string{"*"}); srv == nil {
		t.Fatal("tier-2 server nil")
	}
}
