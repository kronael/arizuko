package main

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/router"
	"github.com/kronael/arizuko/runed/runstore"
	"github.com/kronael/arizuko/store"
	"golang.org/x/sys/unix"
	"time"
)

func renameTestMap(t *testing.T) renameMap {
	t.Helper()
	mapping, err := newRenameMap("main", "sloth", []string{"main/main=sloth/discord"})
	if err != nil {
		t.Fatal(err)
	}
	return mapping
}

func TestGroupRenameOriginalCoordinates(t *testing.T) {
	mapping := renameTestMap(t)
	for before, want := range map[string]string{
		"main": "sloth", "main/main": "sloth/discord", "main/main/dev": "sloth/discord/dev",
		"main/trading": "sloth/trading", "main/main#observe": "sloth/discord#observe",
		"mainland": "mainland", "domain/main": "domain/main", "mainland/main": "mainland/main",
	} {
		if got := mapping.folder(before); got != want {
			t.Errorf("%q -> %q, want %q", before, got, want)
		}
	}
	for before, want := range map[string]string{
		"local:main": "local:main", "web:main/main": "web:sloth/discord",
		"hook:main/trading/deepdex": "hook:sloth/trading/deepdex", "discord:123/main": "discord:123/main",
		"agent:main": "agent:sloth", "telegram:main": "telegram:main",
	} {
		if got := mapping.identity(before); got != want {
			t.Errorf("%q -> %q, want %q", before, got, want)
		}
	}
	if _, err := newRenameMap("main", "main/sub", nil); err == nil {
		t.Fatal("overlapping map accepted")
	}
}

func TestGroupRenameClassifiedRows(t *testing.T) {
	mapping := renameTestMap(t)
	sep := store.KeySep
	fixtures := []struct {
		owner, entity, partition, sort, nextPartition, nextSort string
		attrs, want                                             map[string]string
	}{
		{"routd", store.Groups, "routd", "main/main", "routd", "sloth/discord", map[string]string{"added_at": "unchanged", "config": `{"model":"main-model"}`}, nil},
		{"routd", store.ACL, "routd", strings.Join([]string{"main", "mcp:*", "main/*", "", "", "allow"}, sep), "routd", strings.Join([]string{"main", "mcp:*", "sloth/*", "", "", "allow"}, sep), map[string]string{"granted_by": "agent:main", "grant_option": "1"}, nil},
		{"routd", store.ACLMembership, "routd", "discord:12" + sep + "main/main", "routd", "discord:12" + sep + "main/main", nil, nil},
		{"routd", store.Routes, "routd", "0000000001" + sep + "main/main#observe" + sep + "chat_jid=web:main/main verb=mention", "routd", "0000000001" + sep + "sloth/discord#observe" + sep + "chat_jid=web:sloth/discord verb=mention", map[string]string{"id": "33", "added_by": "main"}, nil},
		{"routd", store.Secrets, "routd", "folder" + sep + "main/*" + sep + "TOKEN", "routd", "folder" + sep + "sloth/*" + sep + "TOKEN", map[string]string{"value": "sealed-main-secret", "refresh_val": "sealed-refresh-main", "provider": "github", "scope_list": "repo"}, nil},
		{"routd", store.NetworkRules, "routd", "main/trading" + sep + "api.example.com", "routd", "sloth/trading" + sep + "api.example.com", nil, nil},
		{"routd", store.InstalledPackages, "routd", "main" + sep + "main-package", "routd", "sloth" + sep + "main-package", map[string]string{"revision": "main"}, nil},
		{"routd", store.Sessions, "routd", "main/main" + sep + "main-topic", "routd", "sloth/discord" + sep + "main-topic", map[string]string{"session_id": "main-session", "observed_cursor": "main-cursor", "parent_topic": "main-parent"}, nil},
		{"routd", store.Chats, "routd", "local:main", "routd", "local:main", map[string]string{"sticky_group": "main/main", "agent_cursor": "main-cursor"}, map[string]string{"sticky_group": "sloth/discord", "agent_cursor": "main-cursor"}},
		{"routd", store.ChatReplyState, "routd", "web:main/main" + sep + "topic", "routd", "web:sloth/discord" + sep + "topic", map[string]string{"engaged_folder": "main/main", "last_reply_id": "main"}, map[string]string{"engaged_folder": "sloth/discord", "last_reply_id": "main"}},
		{"routd", store.SystemMessages, "routd", "main" + sep + "instant", "routd", "sloth" + sep + "instant", map[string]string{"body": "main/trading event"}, nil},
		{"routd", store.TurnResults, "routd", "main/main" + sep + "main-turn", "routd", "sloth/discord" + sep + "main-turn", nil, nil},
		{"routd", store.GroupWatchers, "routd", "other" + sep + "main/main", "routd", "other" + sep + "sloth/discord", nil, nil},
		{"routd", store.Messages, "local:main", "instant" + sep + "main-message", "local:main", "instant" + sep + "main-message", map[string]string{"routed_to": "main/main", "sender": "main", "content": "main/main stays in history", "turn_id": "main-turn"}, map[string]string{"routed_to": "sloth/discord", "sender": "main", "content": "main/main stays in history", "turn_id": "main-turn"}},
		{"routd", store.MessageFolders, "local:main", "main-id", "local:main", "main-id", map[string]string{"chat_jid": "web:main", "sender": "main/main"}, map[string]string{"chat_jid": "web:sloth", "sender": "main/main"}},
		{"routd", store.MessageFolderCounts, "routd", "local:main", "routd", "local:main", map[string]string{"n": "17"}, nil},
		{"routd", store.MessageFolderCounts, "routd", "main/main", "routd", "sloth/discord", map[string]string{"n": "31211"}, nil},
		{"routd", store.MessageActivity, "routd", "main-id", "routd", "main-id", map[string]string{"chat_jid": "web:main", "preview": "main/main"}, map[string]string{"chat_jid": "web:sloth", "preview": "main/main"}},
		{"routd", store.MessageDelivery, "pending", "main-id", "pending", "main-id", map[string]string{"chat_jid": "main/main", "attempts": "3"}, map[string]string{"chat_jid": "sloth/discord", "attempts": "3"}},
		{"routd", store.ScheduledTasks, "routd", "main-task", "routd", "main-task", map[string]string{"owner": "main/trading", "chat_jid": "main/trading", "prompt": "main is mentioned", "next_run": "old-time"}, map[string]string{"owner": "sloth/trading", "chat_jid": "sloth/trading", "prompt": "main is mentioned", "next_run": "old-time"}},
		{"routd", store.TurnContexts, "routd", "main-turn", "routd", "main-turn", map[string]string{"folder": "main", "chat_jid": "web:main", "return_to": "main/trading", "trigger_sender": "agent:main", "state": "done", "trigger_msg_id": "main"}, map[string]string{"folder": "sloth", "chat_jid": "web:sloth", "return_to": "sloth/trading", "trigger_sender": "agent:sloth", "state": "done", "trigger_msg_id": "main"}},
		{"routd", store.PendingActions, "routd", "main-action", "routd", "main-action", map[string]string{"group_folder": "main", "caller_agent": "main", "args": `{"folder":"main/main","content":"main/main"}`}, map[string]string{"group_folder": "sloth", "caller_agent": "sloth", "args": `{"content":"main/main","folder":"sloth/discord"}`}},
		{"onbod", store.Onboarding, "onbod", "web:main", "onbod", "web:sloth", nil, nil},
		{"onbod", store.Invites, "onbod", "main-ref", "onbod", "main-ref", map[string]string{"target_glob": "main/*", "issued_by_sub": "agent:main"}, map[string]string{"target_glob": "sloth/*", "issued_by_sub": "agent:main"}},
		{"onbod", store.InviteRedemptions, "discord:123", "instant", "discord:123", "instant", map[string]string{"target_glob": "main/main/*"}, map[string]string{"target_glob": "sloth/discord/*"}},
		{"authd", "refresh_tokens", "authd", "main-hash", "authd", "main-hash", map[string]string{"sub": "main", "scope": "tasks:read,folder:main/*"}, map[string]string{"sub": "main", "scope": "tasks:read,folder:sloth/*"}},
		{"runed", runstore.SessionLog, "main/main", "main-session", "sloth/discord", "main-session", map[string]string{"session_id": "main-uuid", "result": "main/main text"}, nil},
		{"runed", runstore.CircuitBreaker, "main", "main", "sloth", "sloth", map[string]string{"failures": "5"}, nil},
		{"runed", runstore.Spawns, "spawn", "main-run", "spawn", "main-run", map[string]string{"folder": "main/main", "state": "exited", "container_name": "main-container"}, map[string]string{"folder": "sloth/discord", "state": "exited", "container_name": "main-container"}},
		{"discd", "outbound_dedup", "main-dedup", "main-dedup", "main-dedup", "main-dedup", map[string]string{"chat_jid": "main/main", "state": "sent", "platform_id": "main-id"}, map[string]string{"chat_jid": "sloth/discord", "state": "sent", "platform_id": "main-id"}},
		{"slakd", "inbound_pending", "inbound", "main-id", "inbound", "main-id", map[string]string{"payload": `{"chat_jid":"web:main","content":"main/main"}`, "attempts": "3"}, map[string]string{"payload": `{"chat_jid":"web:sloth","content":"main/main"}`, "attempts": "3"}},
		{"routd", audit.Entity, "audit", "main-id", "audit", "main-id", map[string]string{"folder": "main/main", "params": "main"}, nil},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.owner+"/"+fixture.entity, func(t *testing.T) {
			before := objstore.Item{Key: objstore.Key{Partition: fixture.partition, Sort: fixture.sort}, Attrs: fixture.attrs}
			after, err := mapping.row(fixture.owner, fixture.entity, before, "")
			if err != nil {
				t.Fatal(err)
			}
			want := fixture.want
			if want == nil {
				want = fixture.attrs
			}
			if after.Key.Partition != fixture.nextPartition || after.Key.Sort != fixture.nextSort || !reflect.DeepEqual(after.Attrs, want) {
				t.Fatalf("got %#v, want key %q/%q attrs %#v", after, fixture.nextPartition, fixture.nextSort, want)
			}
			if !reflect.DeepEqual(before.Attrs, fixture.attrs) {
				t.Fatal("source attrs mutated")
			}
		})
	}
	for _, entity := range []string{store.TurnContexts, runstore.Spawns} {
		if _, err := mapping.row("runed", entity, objstore.Item{Key: objstore.Key{Partition: "spawn", Sort: "live"}, Attrs: map[string]string{"folder": "main", "state": "running"}}, ""); err == nil {
			t.Fatal("active work accepted")
		}
	}
	if _, err := mapping.row("routd", store.Groups, objstore.Item{Key: objstore.Key{Partition: "routd", Sort: "main"}, Attrs: map[string]string{"config": `{"unclassified":"main/main"}`}}, ""); err == nil {
		t.Fatal("unclassified embedded reference accepted")
	}
}

func renameFixture(t *testing.T) (renameInventory, map[string]objstore.Store) {
	t.Helper()
	inventory := renameInventory{Instance: "fixture", Owners: map[string]map[string]map[string]int{}}
	drivers := map[string]objstore.Store{}
	for _, owner := range []string{"routd", "runed", "onbod", "authd", "discd"} {
		entities, err := renameEntities(owner)
		if err != nil {
			t.Fatal(err)
		}
		driver := dyntest.Store(t, entities...)
		drivers[owner] = driver
		inventory.Owners[owner] = map[string]map[string]int{}
		for _, entity := range entities {
			inventory.Owners[owner][entity] = map[string]int{}
		}
		if owner == "routd" || owner == "onbod" {
			if err := objstore.EnsureScope(context.Background(), driver, store.OwnerScope(owner)); err != nil {
				t.Fatal(err)
			}
			inventory.Owners[owner][store.ScopeEntity]["resreg"] = 1
		}
	}
	return inventory, drivers
}

func renameSeed(t *testing.T, inventory *renameInventory, drivers map[string]objstore.Store, owner, entity string, key objstore.Key, attrs map[string]string) {
	t.Helper()
	ok, err := drivers[owner].GuardedBatch(context.Background(), nil, []objstore.Write{{Op: objstore.Put, Entity: entity, Key: key, Attrs: attrs}})
	if err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	inventory.Owners[owner][entity][key.Partition]++
}

type renameFaultStore struct {
	objstore.Store
	Calls, Fail, Queries int
	After                bool
}

func (driver *renameFaultStore) Query(ctx context.Context, query objstore.Query) ([]objstore.Item, error) {
	driver.Queries++
	return driver.Store.Query(ctx, query)
}

func (driver *renameFaultStore) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	driver.Calls++
	if driver.Calls == driver.Fail && !driver.After {
		return false, errors.New("fixture interrupted before commit")
	}
	ok, err := driver.Store.GuardedBatch(ctx, checks, writes)
	if driver.Calls == driver.Fail && driver.After {
		return false, errors.New("fixture interrupted after commit")
	}
	return ok, err
}

func TestGroupRenameResumeRollback(t *testing.T) {
	for _, afterCommit := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[afterCommit], func(t *testing.T) {
			inventory, drivers := renameFixture(t)
			beforeRows := []renameRow{}
			for i := 0; i < 81; i++ {
				folder := "main/" + strings.Repeat("x", i+1)
				key := objstore.Key{Partition: "routd", Sort: folder}
				attrs := map[string]string{"added_at": "unchanged", "config": `{"model":"haiku"}`}
				renameSeed(t, &inventory, drivers, "routd", store.Groups, key, attrs)
				beforeRows = append(beforeRows, renameRow{Owner: "routd", Entity: store.Groups, Before: objstore.Item{Key: key, Attrs: maps.Clone(attrs)}})
			}
			sep := store.KeySep
			for _, target := range []string{"main/main", "main/trading"} {
				renameSeed(t, &inventory, drivers, "routd", store.Routes, objstore.Key{Partition: "routd", Sort: "1000000000" + sep + target + sep + "room=discord:123"}, map[string]string{"id": "33"})
			}
			counter := &renameFaultStore{Store: drivers["routd"]}
			drivers["routd"] = counter
			plan, err := renamePreflight(context.Background(), "fixture", t.TempDir(), renameTestMap(t), inventory, drivers)
			if err != nil {
				t.Fatal(err)
			}
			if counter.Queries > 4 {
				t.Fatalf("preflight made %d queries for two partitions and a marker", counter.Queries)
			}
			preflightQueries := counter.Queries
			journal := filepath.Join(t.TempDir(), "journal")
			fault := &renameFaultStore{Store: drivers["routd"], Fail: 2, After: afterCommit}
			drivers["routd"] = fault
			if err = renameExecute(context.Background(), journal, plan, drivers, false, false); err == nil {
				t.Fatal("interruption did not surface")
			}
			drivers["routd"] = fault.Store
			if err = renameExecute(context.Background(), journal, plan, drivers, true, false); err != nil {
				t.Fatal(err)
			}
			if used := counter.Queries - preflightQueries; used > 85 {
				t.Fatalf("resume/execution made %d reads; only uncertain batch and final partitions need reads", used)
			}
			items, err := drivers["routd"].Query(context.Background(), objstore.Query{Entity: store.Groups, Partition: "routd"})
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 81 {
				t.Fatalf("group count %d", len(items))
			}
			for _, item := range items {
				if !strings.HasPrefix(item.Key.Sort, "sloth/") {
					t.Fatal("old coordinate survived")
				}
			}
			routes, err := drivers["routd"].Query(context.Background(), objstore.Query{Entity: store.Routes, Partition: "routd"})
			if err != nil || len(routes) != 2 {
				t.Fatalf("duplicate route IDs lost: %v %#v", err, routes)
			}
			if err = renameExecute(context.Background(), journal, plan, drivers, false, true); err != nil {
				t.Fatal(err)
			}
			for _, row := range beforeRows {
				_, matches, err := renameRowState(context.Background(), drivers[row.Owner], row.Entity, row.Before)
				if err != nil || !matches {
					t.Fatalf("rollback lost image: %v", err)
				}
			}
			if info, err := os.Stat(filepath.Join(journal, "plan.json")); err != nil || info.Mode().Perm() != 0o600 {
				t.Fatal("journal not private")
			}
		})
	}
}

func TestGroupRenameCollisionAndCountsWriteNothing(t *testing.T) {
	inventory, drivers := renameFixture(t)
	for _, folder := range []string{"main", "sloth"} {
		renameSeed(t, &inventory, drivers, "routd", store.Groups, objstore.Key{Partition: "routd", Sort: folder}, map[string]string{"added_at": "unchanged"})
	}
	fault := &renameFaultStore{Store: drivers["routd"]}
	drivers["routd"] = fault
	if _, err := renamePreflight(context.Background(), "fixture", t.TempDir(), renameTestMap(t), inventory, drivers); err == nil {
		t.Fatal("destination collision accepted")
	}
	if fault.Calls != 0 {
		t.Fatal("preflight wrote")
	}
	inventory.Owners["routd"][store.Groups]["routd"] = 3
	if _, err := renamePreflight(context.Background(), "fixture", t.TempDir(), renameTestMap(t), inventory, drivers); err == nil {
		t.Fatal("count mismatch accepted")
	}
	if fault.Calls != 0 {
		t.Fatal("count preflight wrote")
	}
}

func TestGroupRenameFilesMergeAndRollback(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"groups/main/main/.hidden":      "main historical bytes",
		"groups/main/trading/state":     "opaque main state",
		"groups/main/unregistered/data": "main bytes",
		"web/pub/main/main/page.html":   `<a href="/pub/main/trading/">trade</a>`,
		"web/pub/sloth/index.html":      "keep existing landing page",
		"web/pub/other/links.html":      `<a href="/pub/main/main/page.html">chat</a>`,
	}
	for path, data := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../main/.hidden", filepath.Join(root, "groups/main/trading/chat")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../trading/state", filepath.Join(root, "groups/main/main/state")); err != nil {
		t.Fatal(err)
	}
	operations, err := renamePlanFiles(root, renameTestMap(t))
	if err != nil {
		t.Fatal(err)
	}
	plan := renamePlan{Files: operations}
	journal := filepath.Join(t.TempDir(), "journal")
	if err = renameExecute(context.Background(), journal, plan, nil, false, false); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"groups/sloth/discord/.hidden", "groups/sloth/trading/state", "groups/sloth/unregistered/data"} {
		if _, err = os.Stat(filepath.Join(root, path)); err != nil {
			t.Fatal(err)
		}
	}
	if target, err := os.Readlink(filepath.Join(root, "groups/sloth/discord/state")); err != nil || target != "../trading/state" {
		t.Fatalf("link %q: %v", target, err)
	}
	if target, err := os.Readlink(filepath.Join(root, "groups/sloth/trading/chat")); err != nil || target != "../discord/.hidden" {
		t.Fatalf("mapped relative link %q: %v", target, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "web/pub/other/links.html"))
	if err != nil || !strings.Contains(string(data), "/pub/sloth/discord/page.html") {
		t.Fatalf("web references: %q %v", data, err)
	}
	if err = renameExecute(context.Background(), journal, plan, nil, false, true); err != nil {
		t.Fatal(err)
	}
	for path, want := range files {
		got, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", path, got, err)
		}
	}
}

func TestGroupRenameOwnerSchemaCoverage(t *testing.T) {
	mapping := renameTestMap(t)
	for _, owner := range []string{"routd", "onbod", "authd", "runed", "discd", "slakd", "teled", "gitd", "whapd", "emaid"} {
		entities, err := renameEntities(owner)
		if err != nil {
			t.Fatal(err)
		}
		for _, entity := range entities {
			key := objstore.Key{Partition: "unrelated", Sort: strings.Join([]string{"unrelated", "unrelated", "unrelated", "unrelated", "unrelated", "unrelated"}, store.KeySep)}
			_, err = mapping.row(owner, entity, objstore.Item{Key: key}, "")
			if err != nil && strings.Contains(err.Error(), "unclassified coordinate") {
				t.Fatal(err)
			}
		}
	}
	if _, err := mapping.row("routd", "unknown", objstore.Item{}, ""); err == nil {
		t.Fatal("unknown schema accepted")
	}
}

func TestGroupRenameStructuredMountAndUserScope(t *testing.T) {
	root := t.TempDir()
	mapping := renameTestMap(t)
	raw := `{"Mounts":[{"Host":"` + root + `/groups/main/main/share","Container":"/home/node","RO":false}]}`
	before := objstore.Item{Key: objstore.Key{Partition: "routd", Sort: "main"}, Attrs: map[string]string{"container_config": raw}}
	after, err := mapping.row("routd", store.Groups, before, root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(after.Attrs["container_config"], root+"/groups/sloth/discord/share") {
		t.Fatal("mount stayed at old coordinate")
	}
	raw = `{"scope_kind":"user","scope_id":"main","key":"TOKEN"}`
	next, err := mapping.structured(raw, root)
	if err != nil || next != raw {
		t.Fatalf("user scope changed: %q %v", next, err)
	}
}

func TestGroupRenameTornJournalAndPartialStaging(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.env")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	operation := renameFileImage("edit", path, "", info)
	operation.Before = []byte("before")
	operation.After = []byte("after")
	plan := renamePlan{Files: []renameFile{operation}}
	journal := filepath.Join(t.TempDir(), "journal")
	if err = os.Mkdir(journal, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err = renameDurableFile(filepath.Join(journal, "plan.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	progress, err := os.OpenFile(filepath.Join(journal, "progress.jl"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err = renameRecord(progress, renameProgress{Step: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err = progress.Write([]byte(`{"Step":0,"Done":`)); err != nil {
		t.Fatal(err)
	}
	if err = progress.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path+".arizuko-rename", []byte("part"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = renameExecute(context.Background(), journal, plan, nil, true, false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "after" {
		t.Fatalf("%q %v", got, err)
	}
	if err = renameExecute(context.Background(), journal, plan, nil, false, true); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil || string(got) != "before" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestGroupRenameFilesystemCollisionWriteNothing(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"web/pub/main/page.html", "web/pub/sloth/page.html"} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(path), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := renamePlanFiles(root, renameTestMap(t)); err == nil {
		t.Fatal("leaf collision accepted")
	}
	for _, path := range []string{"web/pub/main/page.html", "web/pub/sloth/page.html"} {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(data) != path {
			t.Fatal("collision changed files")
		}
	}
}

func TestGroupRenameDirectoryMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "groups/main")
	if err := os.MkdirAll(filepath.Join(path, "main"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o751); err != nil {
		t.Fatal(err)
	}
	if err := unix.Setxattr(path, "user.rename", []byte("preserve"), 0); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := renamePlanFiles(root, renameTestMap(t))
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(t.TempDir(), "journal")
	plan := renamePlan{Files: operations}
	if err = renameExecute(context.Background(), journal, plan, nil, false, false); err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(root, "groups/sloth")
	attrs, err := renameXattrs(next)
	if err != nil || string(attrs["user.rename"]) != "preserve" {
		t.Fatal("destination lost xattrs")
	}
	if err = renameExecute(context.Background(), journal, plan, nil, false, true); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	attrs, err = renameXattrs(path)
	if err != nil || string(attrs["user.rename"]) != "preserve" {
		t.Fatal("rollback lost xattrs")
	}
	if before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("directory metadata changed: %#v %#v", before, after)
	}
}

func TestGroupRenameHumanSenderAndBotAttestation(t *testing.T) {
	db := routdStore(t)
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	for _, bot := range []bool{false, true} {
		id := "human"
		if bot {
			id = "bot"
		}
		message := core.Message{ID: id, Timestamp: now, ChatJID: "web:main", Sender: "main", Name: "main", Content: "main/main", RoutedTo: "main/main", BotMsg: bot, ReplyToSender: "main"}
		if err := db.PutMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	mapping := renameTestMap(t)
	for entity, partition := range map[string]string{store.Messages: "web:main", store.MessageFolders: "main/main", store.MessageActivity: "routd"} {
		items, err := db.Obj().Query(context.Background(), objstore.Query{Entity: entity, Partition: partition})
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 2 {
			t.Fatalf("%s count %d", entity, len(items))
		}
		for _, before := range items {
			after, err := mapping.row("routd", entity, before, "")
			if err != nil {
				t.Fatal(err)
			}
			want := "main"
			if before.Attrs["is_bot_message"] == "1" {
				want = "sloth"
			}
			if after.Attrs["sender"] != want {
				t.Errorf("%s bot=%q sender=%q, want %q", entity, before.Attrs["is_bot_message"], after.Attrs["sender"], want)
			}
			if after.Attrs["sender_name"] != before.Attrs["sender_name"] || after.Attrs["reply_to_sender"] != before.Attrs["reply_to_sender"] {
				t.Error("display labels changed")
			}
		}
	}
}

func TestGroupRenameAuthSubjectsKeepHumanNames(t *testing.T) {
	mapping := renameTestMap(t)
	before := objstore.Item{Key: objstore.Key{Partition: "authd", Sort: "main"}, Attrs: map[string]string{"sub": "main", "principal": "main", "name": "main"}}
	after, err := mapping.row("authd", "auth_users", before, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("human identity changed: %#v", after)
	}
	before.Attrs["sub"] = "agent:main/main"
	after, err = mapping.row("authd", "refresh_tokens", before, "")
	if err != nil {
		t.Fatal(err)
	}
	if after.Attrs["sub"] != "agent:sloth/discord" {
		t.Fatalf("folder subject stayed old: %#v", after)
	}
}

func TestGroupRenameNetworkCreatorIsHistorical(t *testing.T) {
	before := objstore.Item{Key: objstore.Key{Partition: store.OwnerRoutd, Sort: "main/trading" + store.KeySep + "api.example.com"}, Attrs: map[string]string{"created_by": "main", "created_at": "unchanged"}}
	after, err := renameTestMap(t).row(store.OwnerRoutd, store.NetworkRules, before, "")
	if err != nil {
		t.Fatal(err)
	}
	if after.Key.Sort != "sloth/trading"+store.KeySep+"api.example.com" || !reflect.DeepEqual(after.Attrs, before.Attrs) {
		t.Fatalf("ownership or provenance incorrect: %#v", after)
	}
}

func TestAstraInterruptedFileRollback(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "groups/main/.mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"url":"/pub/main/main/"}`), 0600); err != nil {
		t.Fatal(err)
	}
	ops, err := renamePlanFiles(root, renameTestMap(t))
	if err != nil {
		t.Fatal(err)
	}
	plan := renamePlan{DataDir: root, Files: ops}
	journal := filepath.Join(t.TempDir(), "journal")
	if err = os.Mkdir(journal, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err = renameDurableFile(filepath.Join(journal, "plan.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	progress, err := os.OpenFile(filepath.Join(journal, "progress.jl"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	reached := false
	for i, op := range ops {
		if err = renameRecord(progress, renameProgress{Step: i}); err != nil {
			t.Fatal(err)
		}
		if op.Operation == "edit" {
			if err = os.WriteFile(op.Source+".arizuko-rename", []byte("partial"), 0600); err != nil {
				t.Fatal(err)
			}
			reached = true
			break
		}
		if err = renameApplyFile(op, false); err != nil {
			t.Fatal(err)
		}
		if err = renameRecord(progress, renameProgress{Step: i, Done: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err = progress.Close(); err != nil {
		t.Fatal(err)
	}
	if !reached {
		t.Fatal("fixture missed edit")
	}
	if err = renameExecute(context.Background(), journal, plan, nil, false, true); err != nil {
		t.Fatalf("rollback after interrupted staging: %v", err)
	}
}

func TestAstraPublicMarkdown(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "web/pub/main/trading/theses/README.md")
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("Visit /pub/main/trading/theses/"), 0600); err != nil {
		t.Fatal(err)
	}
	ops, err := renamePlanFiles(root, renameTestMap(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = renameExecute(context.Background(), filepath.Join(t.TempDir(), "journal"), renamePlan{Files: ops}, nil, false, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "web/pub/sloth/trading/theses/README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "/pub/main/") {
		t.Fatalf("operational public URL remained: %s", data)
	}
}

func TestAstraACLHumanPrincipal(t *testing.T) {
	mapping := renameTestMap(t)
	item := objstore.Item{Key: objstore.Key{Partition: "routd", Sort: strings.Join([]string{"main", "admin", "main/**", "", "", "allow"}, store.KeySep)}}
	next, err := mapping.row("routd", store.ACL, item, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Split(next.Key.Sort, store.KeySep)[0] != "main" {
		t.Fatalf("human ACL principal changed to %q", strings.Split(next.Key.Sort, store.KeySep)[0])
	}
}

func TestGroupRenameMembershipUsesFolderPrincipals(t *testing.T) {
	mapping := renameTestMap(t)
	before := objstore.Item{Key: objstore.Key{Partition: store.OwnerRoutd, Sort: "main" + store.KeySep + "folder:main/main"}}
	after, err := mapping.row(store.OwnerRoutd, store.ACLMembership, before, "")
	if err != nil {
		t.Fatal(err)
	}
	if after.Key.Sort != "main"+store.KeySep+"folder:sloth/discord" {
		t.Fatalf("membership endpoints changed incorrectly: %#v", after)
	}
	raw := `{"principal":"main","child":"main","parent":"folder:main/main","group_folder":"main"}`
	next, err := mapping.structured(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if next != `{"child":"main","group_folder":"sloth","parent":"folder:sloth/discord","principal":"main"}` {
		t.Fatalf("pending principal coordinates changed incorrectly: %s", next)
	}
}

func TestAstraACLParamsSchema(t *testing.T) {
	mapping := renameTestMap(t)
	item := objstore.Item{Key: objstore.Key{Partition: "routd", Sort: strings.Join([]string{"folder:main", "mcp:send", "main/**", "folder=main/main", "", "allow"}, store.KeySep)}}
	_, err := mapping.row("routd", store.ACL, item, "")
	if err != nil {
		t.Fatalf("valid ACL parameter grammar refused: %v", err)
	}
}

func TestGroupRenameACLMatcherKeepsAuthorization(t *testing.T) {
	db := routdStore(t)
	grant := core.ACLRow{Principal: auth.FolderPrincipal("main"), Action: "mcp:send", Scope: "main/**", Params: "folder=main/main,chat_jid=web:main/main", Predicate: "sub=main", Effect: store.EffectAllow}
	if err := db.PutACLRow(grant); err != nil {
		t.Fatal(err)
	}
	caller := auth.Caller{Route: "main", Claims: map[string]string{"sub": "main"}}
	params := map[string]string{"folder": "main/main", "chat_jid": "web:main/main"}
	if !auth.Authorize(db, caller, "mcp:send", "main/main", params) {
		t.Fatal("fixture permission missing")
	}
	items, err := db.Obj().Query(context.Background(), objstore.Query{Entity: store.ACL, Partition: store.OwnerRoutd})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, before := range items {
		if !strings.Contains(before.Key.Sort, "folder=main/main") {
			continue
		}
		after, err := renameTestMap(t).row(store.OwnerRoutd, store.ACL, before, "")
		if err != nil {
			t.Fatal(err)
		}
		if err = renameApplyBatch(context.Background(), []renameRow{{Owner: store.OwnerRoutd, Entity: store.ACL, Before: before, After: after}}, map[string]objstore.Store{store.OwnerRoutd: db.Obj()}, false); err != nil {
			t.Fatal(err)
		}
		found = true
	}
	if !found {
		t.Fatal("fixture grant absent")
	}
	caller.Route = "sloth"
	params["folder"] = "sloth/discord"
	params["chat_jid"] = "web:sloth/discord"
	if !auth.Authorize(db, caller, "mcp:send", "sloth/discord", params) {
		t.Fatal("permission changed after rename")
	}
	params["folder"] = "sloth/trading"
	if auth.Authorize(db, caller, "mcp:send", "sloth/trading", params) {
		t.Fatal("matcher expanded to a sibling")
	}
}

func TestAstraPlannedDestinationCollision(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"groups/main/main/page.html", "groups/main/discord/page.html"} {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ops, err := renamePlanFiles(root, renameTestMap(t))
	if err == nil {
		t.Fatalf("preflight accepted %d operations with two sources targeting groups/sloth/discord", len(ops))
	}
}

func TestAstraSymlinkMetadata(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "outside")
	if err := os.Symlink(filepath.Join(root, "groups/main/main/file"), source); err != nil {
		t.Fatal(err)
	}
	times := []unix.Timespec{unix.NsecToTimespec(1_000_000_000), unix.NsecToTimespec(1_000_000_000)}
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, source, times, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := renamePlanFiles(root, renameTestMap(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = renameExecute(context.Background(), filepath.Join(t.TempDir(), "journal"), renamePlan{Files: ops}, nil, false, false); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("symlink mtime changed: %v -> %v", before.ModTime(), after.ModTime())
	}
}

func TestGroupRenameRejectsJournalSymlinkInsideData(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(alias, "journal")
	if err := renameExecute(context.Background(), journal, renamePlan{DataDir: root}, nil, false, false); err == nil {
		t.Fatal("journal inside data directory through a symlink was accepted")
	}
}

func TestGroupRenameUnrelatedPendingWork(t *testing.T) {
	mapping := renameTestMap(t)
	for _, entity := range []string{store.TurnContexts, runstore.Spawns} {
		before := objstore.Item{Key: objstore.Key{Partition: "routd", Sort: "unrelated"}, Attrs: map[string]string{"folder": "atlas", "state": "pending_retry"}}
		after, err := mapping.row("routd", entity, before, "")
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("unrelated %s changed or refused: %#v %v", entity, after, err)
		}
		before.Attrs["return_to"] = "main/main"
		if _, err := mapping.row("routd", entity, before, ""); err == nil {
			t.Fatalf("affected pending %s accepted", entity)
		}
	}
}

func TestGroupRenameStructuredNumbers(t *testing.T) {
	for _, literal := range []string{"9007199254740993", "1.234567890123456789e+45"} {
		next, err := renameTestMap(t).structured(`{"folder":"main/main","id":`+literal+`}`, "")
		if err != nil || !strings.Contains(next, `"id":`+literal) || !strings.Contains(next, "sloth/discord") {
			t.Fatalf("numeric ID %s changed: %s %v", literal, next, err)
		}
	}
	for _, raw := range []string{`{"folder":"main"} {}`, `{"folder":"main"} junk`} {
		if _, err := renameTestMap(t).structured(raw, ""); err == nil {
			t.Fatal("trailing JSON accepted")
		}
	}
}

func TestGroupRenameLegacyLocalFeedsAndRouting(t *testing.T) {
	ctx := context.Background()
	inventory, drivers := renameFixture(t)
	mapping := renameTestMap(t)
	for _, folder := range []string{"main", "main/main"} {
		renameSeed(t, &inventory, drivers, "routd", store.Groups, objstore.Key{Partition: "routd", Sort: folder}, map[string]string{"added_at": "unchanged"})
	}
	for _, jid := range []string{"local:main", "local:sloth"} {
		renameSeed(t, &inventory, drivers, "routd", store.Chats, objstore.Key{Partition: "routd", Sort: jid}, map[string]string{"sticky_group": "main/main", "agent_cursor": "2026-10-14T00:00:00.000000000Z" + store.KeySep + jid})
		renameSeed(t, &inventory, drivers, "routd", store.Messages, objstore.Key{Partition: jid, Sort: "2026-10-14T00:00:00.000000000Z" + store.KeySep + "opaque-id"}, map[string]string{"routed_to": "main/main", "sender": "main", "content": "main history"})
		renameSeed(t, &inventory, drivers, "routd", store.MessageFolders, objstore.Key{Partition: jid, Sort: "2026-10-14T00:00:00.000000000Z" + store.KeySep + "opaque-id"}, map[string]string{"chat_jid": jid, "sender": "main"})
		renameSeed(t, &inventory, drivers, "routd", store.MessageFolderCounts, objstore.Key{Partition: "routd", Sort: jid}, map[string]string{"n": "17"})
	}
	renameSeed(t, &inventory, drivers, "routd", store.Routes, objstore.Key{Partition: "routd", Sort: strings.Join([]string{"0000000016", "main/main", "room=main"}, store.KeySep)}, map[string]string{"id": "16"})
	db := store.NewOver(drivers["routd"])
	if rt := router.ResolveRouteTarget(core.Message{ChatJID: "local:main"}, db.AllRoutes()); rt.Folder != "main/main" {
		t.Fatalf("fixture route: %#v", rt)
	}
	plan, err := renamePreflight(ctx, "fixture", "", mapping, inventory, drivers)
	if err != nil {
		t.Fatal(err)
	}
	if err = renameExecute(ctx, filepath.Join(t.TempDir(), "journal"), plan, drivers, false, false); err != nil {
		t.Fatal(err)
	}
	if rt := router.ResolveRouteTarget(core.Message{ChatJID: "local:main"}, db.AllRoutes()); rt.Folder != "sloth/discord" {
		t.Fatalf("legacy route lost: %#v", rt)
	}
	bare := mapping.identity("main")
	if bare != "sloth" {
		t.Fatalf("bare folder: %q", bare)
	}
	if group, ok := db.GroupByFolder(bare); !ok || group.Folder != "sloth" {
		t.Fatalf("renamed direct folder absent: %#v %v", group, ok)
	}
	for _, jid := range []string{"local:main", "local:sloth"} {
		rows, err := drivers["routd"].Query(ctx, objstore.Query{Entity: store.Chats, Partition: "routd", Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: jid}})
		if err != nil || len(rows) != 1 || rows[0].Attrs["agent_cursor"] != "2026-10-14T00:00:00.000000000Z"+store.KeySep+jid || rows[0].Attrs["sticky_group"] != "sloth/discord" {
			t.Fatalf("chat merged or cursor changed: %#v %v", rows, err)
		}
		rows, err = drivers["routd"].Query(ctx, objstore.Query{Entity: store.Messages, Partition: jid})
		if err != nil || len(rows) != 1 || rows[0].Attrs["routed_to"] != "sloth/discord" || rows[0].Attrs["sender"] != "main" || rows[0].Attrs["content"] != "main history" {
			t.Fatalf("feed history changed: %#v %v", rows, err)
		}
		rows, err = drivers["routd"].Query(ctx, objstore.Query{Entity: store.MessageFolders, Partition: jid})
		if err != nil || len(rows) != 1 || rows[0].Attrs["chat_jid"] != jid {
			t.Fatalf("feed index changed: %#v %v", rows, err)
		}
		rows, err = drivers["routd"].Query(ctx, objstore.Query{Entity: store.MessageFolderCounts, Partition: "routd", Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: jid}})
		if err != nil || len(rows) != 1 || rows[0].Attrs["n"] != "17" {
			t.Fatalf("feed count changed: %#v %v", rows, err)
		}
	}
	for before, want := range map[string]string{"room=main": "room=main", "platform=local room=main": "platform=local room=main", "platform=web room=main": "platform=web room=sloth", "platform=hook room=main/main": "platform=hook room=sloth/main", "chat_jid=local:main": "chat_jid=local:main", "chat_jid=web:main/main": "chat_jid=web:sloth/discord", "sender=main": "sender=main"} {
		got, err := mapping.match(before)
		if err != nil || got != want {
			t.Fatalf("route %q became %q want %q: %v", before, got, want, err)
		}
	}
}

func TestGroupRenameACLScopeDecisions(t *testing.T) {
	ctx := context.Background()
	inventory, drivers := renameFixture(t)
	for _, folder := range []string{"main", "main/main", "main/trading", "other/main", "other/trading", "mainland"} {
		renameSeed(t, &inventory, drivers, "routd", store.Groups, objstore.Key{Partition: "routd", Sort: folder}, map[string]string{"added_at": "unchanged"})
	}
	source := drivers["routd"]
	for _, fixture := range []struct {
		scope  string
		refuse bool
	}{
		{"m*", true}, {"main*", true}, {"*/main", true}, {"main/m*", true}, {"s*", true}, {"main/d*", true},
		{"**", false}, {"*", false}, {"main/**", false}, {"main/*", false}, {"other/**", false}, {"main", false}, {"main/main", false},
	} {
		t.Run(fixture.scope, func(t *testing.T) {
			key := objstore.Key{Partition: "routd", Sort: strings.Join([]string{"role:member", "mcp:send", fixture.scope, "", "", "allow"}, store.KeySep)}
			renameSeed(t, &inventory, drivers, "routd", store.ACL, key, nil)
			counter := &renameFaultStore{Store: source}
			drivers["routd"] = counter
			_, err := renamePreflight(ctx, "fixture", "", renameTestMap(t), inventory, drivers)
			if fixture.refuse && (err == nil || !strings.Contains(err.Error(), "ACL scope")) {
				t.Errorf("unsafe scope %q accepted or wrong refusal: %v", fixture.scope, err)
			}
			if !fixture.refuse && err != nil {
				t.Errorf("safe scope %q refused: %v", fixture.scope, err)
			}
			if counter.Calls != 0 {
				t.Errorf("preflight made %d writes", counter.Calls)
			}
			if counter.Queries != 3 {
				t.Errorf("ACL/group/marker inventory needs three reads, got %d", counter.Queries)
			}
			drivers["routd"] = source
			ok, err := source.GuardedBatch(ctx, nil, []objstore.Write{{Op: objstore.Delete, Entity: store.ACL, Key: key}})
			if err != nil || !ok {
				t.Fatalf("fixture cleanup: %v %v", ok, err)
			}
			delete(inventory.Owners["routd"][store.ACL], "routd")
		})
	}
}

func TestAstraInterruptedRecoveryRollback(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "forward", true: "reverse"}[reverse], func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "groups/main/.mcp.json")
			if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(`{"url":"/pub/main/main/"}`), 0600); err != nil {
				t.Fatal(err)
			}
			ops, err := renamePlanFiles(root, renameTestMap(t))
			if err != nil {
				t.Fatal(err)
			}
			plan := renamePlan{DataDir: root, Files: ops}
			journal := filepath.Join(t.TempDir(), "journal")
			if reverse {
				if err = renameExecute(context.Background(), journal, plan, nil, false, false); err != nil {
					t.Fatal(err)
				}
			} else {
				if err = os.Mkdir(journal, 0700); err != nil {
					t.Fatal(err)
				}
				data, err := json.Marshal(plan)
				if err != nil {
					t.Fatal(err)
				}
				if err = renameDurableFile(filepath.Join(journal, "plan.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			progress, err := os.OpenFile(filepath.Join(journal, "progress.jl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				t.Fatal(err)
			}
			editStep := -1
			for i, op := range ops {
				if reverse && op.Operation != "edit" {
					continue
				}
				if err = renameRecord(progress, renameProgress{Step: i, Reverse: reverse}); err != nil {
					t.Fatal(err)
				}
				if op.Operation == "edit" {
					if err = os.WriteFile(op.Source+".arizuko-rename", []byte("partial"), 0600); err != nil {
						t.Fatal(err)
					}
					editStep = i
					break
				}
				if err = renameApplyFile(op, false); err != nil {
					t.Fatal(err)
				}
				if err = renameRecord(progress, renameProgress{Step: i, Done: true}); err != nil {
					t.Fatal(err)
				}
			}
			if err = progress.Close(); err != nil {
				t.Fatal(err)
			}
			if editStep < 0 {
				t.Fatal("fixture missed edit")
			}
			stat, err := os.Stat(filepath.Join(journal, "progress.jl"))
			if err != nil {
				t.Fatal(err)
			}
			record, err := json.Marshal(renameProgress{Step: editStep, Reverse: !reverse, Done: true})
			if err != nil {
				t.Fatal(err)
			}
			var oldLimit unix.Rlimit
			if err = unix.Getrlimit(unix.RLIMIT_FSIZE, &oldLimit); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &oldLimit); err != nil {
					t.Error(err)
				}
			})
			signals := make(chan os.Signal, 1)
			signal.Notify(signals, syscall.SIGXFSZ)
			defer signal.Stop(signals)
			limit := oldLimit
			limit.Cur = uint64(stat.Size()) + uint64(len(record)) + 1
			if err = unix.Setrlimit(unix.RLIMIT_FSIZE, &limit); err != nil {
				t.Fatal(err)
			}
			interrupted := renameExecute(context.Background(), journal, plan, nil, !reverse, reverse)
			if err = unix.Setrlimit(unix.RLIMIT_FSIZE, &oldLimit); err != nil {
				t.Fatal(err)
			}
			if interrupted == nil {
				t.Fatal("second recovery interruption did not happen")
			}
			t.Logf("recovery returned at the next intent: %v", interrupted)
			if _, err = os.Lstat(ops[editStep].Source + ".arizuko-rename"); !os.IsNotExist(err) {
				t.Errorf("recovered done record left staging: %v", err)
			}
			if err = renameExecute(context.Background(), journal, plan, nil, reverse, !reverse); err != nil {
				t.Fatalf("change direction after second crash: %v", err)
			}
		})
	}
}

func TestAstraWildcardACLParamsPreflight(t *testing.T) {
	ctx := context.Background()
	inventory, drivers := renameFixture(t)
	mapping := renameTestMap(t)
	renameSeed(t, &inventory, drivers, "routd", store.Groups, objstore.Key{Partition: "routd", Sort: "main"}, map[string]string{"added_at": "unchanged"})
	key := objstore.Key{Partition: "routd", Sort: strings.Join([]string{"human", "mcp:send", "**", "folder=m*", "", "allow"}, store.KeySep)}
	renameSeed(t, &inventory, drivers, "routd", store.ACL, key, nil)
	db := store.NewOver(drivers["routd"])
	caller := auth.Caller{Principal: "human"}
	if !auth.Authorize(db, caller, "mcp:send", "main", map[string]string{"folder": "main"}) {
		t.Fatal("fixture was not authorized")
	}
	plan, err := renamePreflight(ctx, "fixture", t.TempDir(), mapping, inventory, drivers)
	if err != nil {
		return
	}
	if err = renameExecute(ctx, filepath.Join(t.TempDir(), "journal"), plan, drivers, false, false); err != nil {
		t.Fatal(err)
	}
	if !auth.Authorize(db, caller, "mcp:send", "sloth", map[string]string{"folder": "sloth"}) {
		t.Fatal("preflight accepted folder=m* but rename revoked existing send permission")
	}
}

func TestGroupRenameACLExactCoordinateMatchers(t *testing.T) {
	for _, fixture := range []struct {
		params, predicate  string
		refuse, authorized bool
	}{
		{"folder=m*", "", true, true}, {"", "folder=m*", true, true}, {"folder=s*", "", true, false}, {"", "folder=s*", true, false},
		{"folder=main/*", "", true, false}, {"folder=*", "", true, true},
		{"folder=main", "", false, true}, {"", "folder=main", false, true}, {"", "", false, true},
		{"sub=m*", "", false, true}, {"", "sub=m*", false, true},
	} {
		t.Run(fixture.params+"|"+fixture.predicate, func(t *testing.T) {
			ctx := context.Background()
			inventory, drivers := renameFixture(t)
			renameSeed(t, &inventory, drivers, "routd", store.Groups, objstore.Key{Partition: "routd", Sort: "main"}, map[string]string{"added_at": "unchanged"})
			key := objstore.Key{Partition: "routd", Sort: strings.Join([]string{"human", "mcp:send", "**", fixture.params, fixture.predicate, "allow"}, store.KeySep)}
			renameSeed(t, &inventory, drivers, "routd", store.ACL, key, nil)
			db := store.NewOver(drivers["routd"])
			caller := auth.Caller{Principal: "human", Claims: map[string]string{"folder": "main", "sub": "main"}}
			params := map[string]string{"folder": "main", "sub": "main"}
			if got := auth.Authorize(db, caller, "mcp:send", "main", params); got != fixture.authorized {
				t.Fatalf("native preimage authorization: %v want %v", got, fixture.authorized)
			}
			counter := &renameFaultStore{Store: drivers["routd"]}
			drivers["routd"] = counter
			plan, err := renamePreflight(ctx, "fixture", "", renameTestMap(t), inventory, drivers)
			if counter.Calls != 0 {
				t.Fatalf("preflight wrote %d batches", counter.Calls)
			}
			if fixture.refuse {
				if err == nil || !strings.Contains(err.Error(), "coordinate ACL matcher") {
					t.Fatalf("non-exact coordinate matcher accepted or wrong refusal: %v", err)
				}
				if _, ok := db.GroupByFolder("main"); !ok {
					t.Fatal("preflight changed group")
				}
				return
			}
			if err != nil {
				t.Fatalf("supported matcher refused: %v", err)
			}
			if err = renameExecute(ctx, filepath.Join(t.TempDir(), "journal"), plan, drivers, false, false); err != nil {
				t.Fatal(err)
			}
			caller.Claims["folder"] = "sloth"
			params["folder"] = "sloth"
			if got := auth.Authorize(db, caller, "mcp:send", "sloth", params); got != fixture.authorized {
				t.Fatalf("native renamed authorization: %v want %v", got, fixture.authorized)
			}
		})
	}
}

func TestGroupRenameImmutableImagesOutsideSettingsScan(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "images/rootfs-a35045d89e3e/app/node_modules/typescript/lib/_tsc.js")
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteString("/pub/main/main/ immutable image"); err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(5 << 20); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	ops, err := renamePlanFiles(root, renameTestMap(t))
	if err != nil || len(ops) != 0 {
		t.Fatalf("immutable image entered rewrite scan: %#v %v", ops, err)
	}
	control := filepath.Join(root, "env/settings.json")
	if err = os.MkdirAll(filepath.Dir(control), 0750); err != nil {
		t.Fatal(err)
	}
	file, err = os.Create(control)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(5 << 20); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = renamePlanFiles(root, renameTestMap(t)); err == nil || !strings.Contains(err.Error(), control) {
		t.Fatalf("oversized operational settings accepted: %v", err)
	}
}

func TestGroupRenameCanonicalJIDOwnership(t *testing.T) {
	mapping := renameTestMap(t)
	for before, want := range map[string]string{
		groupfolder.WebJID("main/main"):             groupfolder.WebJID("sloth/discord"),
		groupfolder.HookJID("main", "main"):         groupfolder.HookJID("sloth", "main"),
		groupfolder.HookJID("main/main", "main"):    groupfolder.HookJID("sloth/discord", "main"),
		groupfolder.HookJID("main/trading", "main"): groupfolder.HookJID("sloth/trading", "main"),
	} {
		if got := mapping.identity(before); got != want {
			t.Errorf("canonical JID %q became %q, want %q", before, got, want)
		}
	}
}

func TestGroupRenameHookRoomRetainsSource(t *testing.T) {
	before := groupfolder.HookJID("main", "main")
	next := groupfolder.HookJID("sloth", "main")
	original := "platform=hook room=" + core.JidRoom(before)
	mapped, err := renameTestMap(t).match(original)
	if err != nil {
		t.Fatal(err)
	}
	if !router.RouteMatches(core.Route{Match: original}, core.Message{ChatJID: before}) {
		t.Fatal("native hook fixture did not match")
	}
	if !router.RouteMatches(core.Route{Match: mapped}, core.Message{ChatJID: next}) {
		t.Fatalf("hook source room lost: %q became %q, cannot match %q", original, mapped, next)
	}
}
