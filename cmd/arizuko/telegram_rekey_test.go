package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/router"
	"github.com/kronael/arizuko/runed/runstore"
	"github.com/kronael/arizuko/store"
)

func telegramFixture(t *testing.T) (renameInventory, map[string]objstore.Store) {
	t.Helper()
	inventory, drivers := renameFixture(t)
	drivers["teled"] = dyntest.Store(t, chanstore.Entities...)
	inventory.Owners["teled"] = map[string]map[string]int{}
	for _, entity := range chanstore.Entities {
		inventory.Owners["teled"][entity] = map[string]int{}
	}
	return inventory, drivers
}

func telegramTestAccounts() map[string]string {
	return map[string]string{"telegram": "111", "jynx": "222"}
}

func telegramTestPlan(t *testing.T, inventory renameInventory,
	drivers map[string]objstore.Store) renamePlan {
	t.Helper()
	plan, err := telegramRekeyPreflight(context.Background(), "fixture", "",
		telegramTestAccounts(), inventory, drivers)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func telegramItems(t *testing.T, driver objstore.Store, entity, partition string) []objstore.Item {
	t.Helper()
	items, err := driver.Query(context.Background(), objstore.Query{
		Entity: entity, Partition: partition})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func telegramMergedFixture(t *testing.T) (renameInventory, map[string]objstore.Store, []string) {
	t.Helper()
	inventory, drivers := telegramFixture(t)
	db := store.NewOver(drivers["routd"])
	keys := []string{}
	now := time.Now().UTC()
	for i, source := range []string{"", "telegram", "", "jynx", "", "telegram", "", "jynx", "", "telegram"} {
		msg := core.Message{ID: fmt.Sprintf("telegram:user/5:%d", i),
			Timestamp: now.Add(time.Duration(i) * time.Second), ChatJID: "telegram:user/5",
			Sender: "telegram:user/5", Content: "history telegram:user/5", RoutedTo: "atlas",
			Source: source, BotMsg: source == "", Verb: "message"}
		if msg.BotMsg {
			msg.Sender, msg.PlatformID = "atlas", fmt.Sprint(i)
		}
		if err := db.PutOutbound(context.Background(), msg, core.MessageStatusFailed); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, store.MessageCursor(msg))
	}
	if err := db.SetAgentCursor("telegram:user/5", keys[6]); err != nil {
		t.Fatal(err)
	}
	for entity, partition := range map[string]string{
		store.Messages: "telegram:user/5", store.Chats: "routd", store.Counters: "routd",
		store.MessageFolders: "atlas", store.MessageActivity: "routd",
		store.MessageDelivery: core.MessageStatusFailed, store.MessageFolderCounts: "routd",
	} {
		items := telegramItems(t, drivers["routd"], entity, partition)
		if len(items) > 0 {
			inventory.Owners["routd"][entity][partition] = len(items)
		}
	}
	for _, fixture := range []struct {
		owner, entity, sort string
		attrs               map[string]string
	}{
		{"routd", store.ChatProactive, "telegram:user/5", map[string]string{"fired_at": core.TS(now)}},
		{"routd", store.ChatReplyState, "telegram:user/5" + store.KeySep + "topic",
			map[string]string{"last_reply_id": "telegram:user/5:2"}},
		{"onbod", store.Onboarding, "telegram:user/5", map[string]string{"sender": "telegram:user/5", "chat_name": "person"}},
		{"routd", store.ScheduledTasks, "task",
			map[string]string{"chat_jid": "telegram:user/5", "trigger_sender": "telegram:user/5"}},
		{"routd", store.RouteTokens, "pairing", map[string]string{"jid": "telegram:user/5", "sender": "telegram:user/5"}},
		{"routd", store.ACLMembership, "telegram:user/5" + store.KeySep + "person",
			map[string]string{"created_at": "unchanged"}},
		{"routd", store.TurnContexts, "done",
			map[string]string{"chat_jid": "telegram:user/5", "return_to": "telegram:user/5", "state": store.TurnDone}},
		{"runed", runstore.Spawns, "run", map[string]string{"chat_jid": "telegram:user/5", "state": "exited"}},
		{"routd", store.Routes, "0000000030" + store.KeySep + "atlas" + store.KeySep + "room=user/5 verb=message",
			map[string]string{"id": "30"}},
		{"routd", store.ACL, strings.Join([]string{"telegram:user/5", "mcp:send", "**",
			"chat_jid=telegram:user/5", "jid=telegram:user/*", "allow"}, store.KeySep),
			map[string]string{"granted_by": "telegram:user/5"}},
	} {
		renameSeed(t, &inventory, drivers, fixture.owner, fixture.entity,
			objstore.Key{Partition: fixture.owner, Sort: fixture.sort}, fixture.attrs)
	}
	return inventory, drivers, keys
}

func TestTelegramRekeyMergedChatRollback(t *testing.T) {
	// Three base-account inputs and two jynx inputs split their replies and indexes; rollback restores every item image.
	inventory, drivers, keys := telegramMergedFixture(t)
	before := map[[3]string][]objstore.Item{}
	for owner, entities := range inventory.Owners {
		for entity, partitions := range entities {
			if entity == store.ScopeEntity {
				continue
			}
			for partition := range partitions {
				before[[3]string{owner, entity, partition}] = telegramItems(t, drivers[owner], entity, partition)
			}
		}
	}
	plan := telegramTestPlan(t, inventory, drivers)
	journal := filepath.Join(t.TempDir(), "journal")
	if err := renameExecute(context.Background(), journal, plan, drivers, false, false); err != nil {
		t.Fatal(err)
	}
	wantJIDs := []string{"telegram:111/user/5", "telegram:111/user/5", "telegram:111/user/5",
		"telegram:222/user/5", "telegram:222/user/5", "telegram:111/user/5", "telegram:111/user/5",
		"telegram:222/user/5", "telegram:222/user/5", "telegram:111/user/5"}
	if got := telegramItems(t, drivers["routd"], store.Messages, "telegram:user/5"); len(got) != 0 {
		t.Fatalf("old messages survived: %#v", got)
	}
	for jid, want := range map[string]int{"telegram:111/user/5": 6, "telegram:222/user/5": 4} {
		items := telegramItems(t, drivers["routd"], store.Messages, jid)
		if len(items) != want {
			t.Fatalf("%s: got %d messages, want %d", jid, len(items), want)
		}
		for _, item := range items {
			i := -1
			for j, key := range keys {
				if item.Key.Sort == key {
					i = j
				}
			}
			if i < 0 || wantJIDs[i] != jid {
				t.Fatalf("wrong message assignment: %#v", item)
			}
			original := before[[3]string{"routd", store.Messages, "telegram:user/5"}][i]
			if !maps.Equal(item.Attrs, original.Attrs) {
				t.Fatalf("message bytes changed: %#v", item)
			}
		}
		cursor, err := store.NewOver(drivers["routd"]).AgentCursor(jid)
		if err != nil || cursor != keys[6] {
			t.Fatalf("%s cursor: %q %v", jid, cursor, err)
		}
	}
	for entity, partition := range map[string]string{store.MessageFolders: "atlas",
		store.MessageActivity: "routd", store.MessageDelivery: core.MessageStatusFailed} {
		items := telegramItems(t, drivers["routd"], entity, partition)
		if len(items) != len(keys) {
			t.Fatalf("%s: %d index entries", entity, len(items))
		}
		for i, item := range items {
			want := before[[3]string{"routd", entity, partition}][i]
			want.Attrs = maps.Clone(want.Attrs)
			want.Attrs["chat_jid"] = wantJIDs[i]
			if !reflect.DeepEqual(item, want) {
				t.Fatalf("%s: wrong index: %#v want %#v", entity, item, want)
			}
		}
	}
	for _, entity := range []string{store.Chats, store.ChatProactive, store.ChatReplyState, store.Onboarding} {
		owner := "routd"
		if entity == store.Onboarding {
			owner = "onbod"
		}
		items := telegramItems(t, drivers[owner], entity, owner)
		if len(items) != 2 {
			t.Fatalf("%s: expected two copies: %#v", entity, items)
		}
		original := before[[3]string{owner, entity, owner}][0]
		for _, item := range items {
			if !maps.Equal(item.Attrs, original.Attrs) {
				t.Fatalf("%s: copied attributes changed: %#v", entity, item)
			}
			if item.Key.Sort != "telegram:111/user/5"+strings.TrimPrefix(original.Key.Sort, "telegram:user/5") &&
				item.Key.Sort != "telegram:222/user/5"+strings.TrimPrefix(original.Key.Sort, "telegram:user/5") {
				t.Fatalf("%s: wrong copy coordinate: %#v", entity, item)
			}
		}
	}
	for _, entity := range []string{store.RouteTokens, store.ACLMembership,
		store.TurnContexts, store.MessageFolderCounts, runstore.Spawns} {
		owner := "routd"
		if entity == runstore.Spawns {
			owner = "runed"
		}
		got := telegramItems(t, drivers[owner], entity, owner)
		if !reflect.DeepEqual(got, before[[3]string{owner, entity, owner}]) {
			t.Fatalf("historical or identity rows changed in %s: %#v", entity, got)
		}
	}
	tasks := telegramItems(t, drivers["routd"], store.ScheduledTasks, "routd")
	if tasks[0].Attrs["chat_jid"] != "telegram:111/user/5" {
		t.Fatalf("task did not follow newest inbound: %#v", tasks)
	}
	acl := telegramItems(t, drivers["routd"], store.ACL, "routd")
	wantACL := strings.Join([]string{"telegram:user/5", "mcp:send", "**",
		"chat_jid=telegram:*/user/5", "jid=telegram:*/user/*", "allow"}, store.KeySep)
	if len(acl) != 1 || acl[0].Key.Sort != wantACL {
		t.Fatalf("wrong ACL rewrite: %#v", acl)
	}
	routes := telegramItems(t, drivers["routd"], store.Routes, "routd")
	if len(routes) != 1 || !strings.HasSuffix(routes[0].Key.Sort, "room=*/user/5 verb=message") {
		t.Fatalf("wrong route rewrite: %#v", routes)
	}
	for _, jid := range []string{"telegram:111/user/5", "telegram:222/user/5"} {
		if !router.RouteMatches(core.Route{Match: "room=*/user/5 verb=message"},
			core.Message{ChatJID: jid, Verb: "message"}) {
			t.Fatalf("rewritten route does not match %s", jid)
		}
	}
	var recorded renamePlan
	if err := renameReadJSON(filepath.Join(journal, "plan.json"), &recorded); err != nil {
		t.Fatal(err)
	}
	if err := renameExecute(context.Background(), journal, recorded, drivers, false, true); err != nil {
		t.Fatal(err)
	}
	for coordinate, want := range before {
		got := telegramItems(t, drivers[coordinate[0]], coordinate[1], coordinate[2])
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("rollback changed bytes in %v: %s want %s", coordinate, gotJSON, wantJSON)
		}
	}
	for _, jid := range []string{"telegram:111/user/5", "telegram:222/user/5"} {
		if items := telegramItems(t, drivers["routd"], store.Messages, jid); len(items) != 0 {
			t.Fatalf("rollback left destination %s", jid)
		}
	}
}

func TestTelegramRekeyRefusals(t *testing.T) {
	key := objstore.Key{Partition: "routd", Sort: "named-row"}
	stamp := store.MsgStamp(core.TS(time.Now()), "unmapped")
	for _, fixture := range []struct {
		name, owner, entity string
		key                 objstore.Key
		attrs               map[string]string
		want                string
	}{
		{"source", "routd", store.Messages, objstore.Key{Partition: "telegram:user/5", Sort: stamp},
			map[string]string{"source": "missing"}, "source"},
		{"no-source", "routd", store.Messages, objstore.Key{Partition: "telegram:user/5", Sort: stamp},
			map[string]string{"sender": "telegram:user/5"}, "source"},
		{"pending", "routd", store.PendingActions, key,
			map[string]string{"args": `{"chat_jid":"telegram:user/5"}`}, "pending action"},
		{"live-turn", "routd", store.TurnContexts, key,
			map[string]string{"chat_jid": "telegram:user/5", "state": "running"}, "live turn"},
		{"retry-turn", "routd", store.TurnContexts, key,
			map[string]string{"return_to": "telegram:user/5", "state": "pending_retry"}, "live turn"},
		{"inbound-pending", "teled", chanstore.InboundPending, key,
			map[string]string{"payload": `{"chat_jid":"telegram:group/3"}`}, "not drained"},
		{"ack-mark", "teled", chanstore.AckMark, key, map[string]string{"platform_id": "3"}, "not drained"},
		{"unknown-field", "routd", store.Chats, key, map[string]string{"new_field": "telegram:user/5"}, "unknown field"},
		{"unknown-structured", "routd", store.Groups, key,
			map[string]string{"config": `{"chat_jid":"telegram:user/5"}`}, "unknown field"},
		{"unknown-key", "routd", store.Counters,
			objstore.Key{Partition: "routd", Sort: "telegram:user/5"}, nil, "unknown key"},
		{"orphan-index", "routd", store.MessageActivity, objstore.Key{Partition: "routd", Sort: stamp},
			map[string]string{"chat_jid": "telegram:user/5"}, "no message"},
		{"orphan-chat", "routd", store.Chats, objstore.Key{Partition: "routd", Sort: "telegram:user/7"}, nil, "no messages"},
		{"dedup-reference", "teled", chanstore.OutboundDedup, key,
			map[string]string{"chat_jid": "telegram:user/5"}, "unknown field"},
		{"route-verdict", "routd", store.Routes, objstore.Key{Partition: "routd",
			Sort: "0000000001" + store.KeySep + "atlas" + store.KeySep + "chat_jid=telegram:*/* verb=mention"},
			nil, "route verdict"},
		{"route-field", "routd", store.Routes, objstore.Key{Partition: "routd",
			Sort: "0000000001" + store.KeySep + "atlas" + store.KeySep + "unknown=telegram:user/5"},
			nil, "unknown route operand"},
		{"acl-field", "routd", store.ACL, objstore.Key{Partition: "routd", Sort: strings.Join(
			[]string{"human", "mcp:send", "**", "telegram:user/5", "", "allow"}, store.KeySep)},
			nil, "unknown ACL matcher"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			inventory, drivers := telegramFixture(t)
			renameSeed(t, &inventory, drivers, "routd", store.Messages,
				objstore.Key{Partition: "telegram:user/5", Sort: store.MsgStamp(core.TS(time.Now().Add(-time.Minute)), "input")},
				map[string]string{"source": "telegram", "sender": "telegram:user/5"})
			renameSeed(t, &inventory, drivers, fixture.owner, fixture.entity, fixture.key, fixture.attrs)
			counter := &renameFaultStore{Store: drivers[fixture.owner]}
			drivers[fixture.owner] = counter
			_, err := telegramRekeyPreflight(context.Background(), "fixture", "",
				telegramTestAccounts(), inventory, drivers)
			if err == nil || !strings.Contains(err.Error(), fixture.want) ||
				!strings.Contains(err.Error(), fixture.entity) ||
				!strings.Contains(err.Error(), fmt.Sprintf("%q", fixture.key.Sort)) {
				t.Fatalf("refusal must name row and reason: %v", err)
			}
			if counter.Calls != 0 {
				t.Fatal("preflight wrote")
			}
		})
	}
}

func TestTelegramRekeyRouteVerdicts(t *testing.T) {
	chats := map[string]telegramChat{"telegram:user/5": {JIDs: []string{"telegram:111/user/5", "telegram:222/user/5"}}}
	for _, raw := range []string{"chat_jid=telegram:user/5", "room=user/*",
		"platform=telegram room=user/5 verb=mention", "sender=telegram:user/5", "room=group/*"} {
		next := telegramRouteMatch(raw)
		if err := telegramRouteVerdicts(raw, next, chats); err != nil {
			t.Fatalf("safe route %q -> %q refused: %v", raw, next, err)
		}
	}
	for _, raw := range []string{"chat_jid=telegram:*/*", "room=*/*",
		"chat_jid=telegram:*/* verb=mention sender=telegram:user/5"} {
		if err := telegramRouteVerdicts(raw, telegramRouteMatch(raw), chats); err == nil {
			t.Fatalf("unsafe route %q accepted", raw)
		}
	}
}

func TestTelegramRekeyCreateResumeRollback(t *testing.T) {
	// An uncertain create resumes from either side of commit; its reverse removes only the copied row.
	for _, afterCommit := range []bool{false, true} {
		t.Run(fmt.Sprint(afterCommit), func(t *testing.T) {
			inventory, drivers := telegramFixture(t)
			item := objstore.Item{Key: objstore.Key{Partition: "routd", Sort: "telegram:222/user/5"},
				Attrs: map[string]string{"agent_cursor": store.MsgStamp(core.TS(time.Now()), "opaque-position")}}
			plan := renamePlan{Inventory: inventory, Rows: []renameRow{{Owner: "routd", Entity: store.Chats, After: item}}}
			journal := filepath.Join(t.TempDir(), "journal")
			fault := &renameFaultStore{Store: drivers["routd"], Fail: 1, After: afterCommit}
			drivers["routd"] = fault
			if err := renameExecute(context.Background(), journal, plan, drivers, false, false); err == nil {
				t.Fatal("interruption did not surface")
			}
			drivers["routd"] = fault.Store
			if err := renameExecute(context.Background(), journal, plan, drivers, true, false); err != nil {
				t.Fatal(err)
			}
			fault = &renameFaultStore{Store: drivers["routd"], Fail: 1, After: afterCommit}
			drivers["routd"] = fault
			if err := renameExecute(context.Background(), journal, plan, drivers, false, true); err == nil {
				t.Fatal("rollback interruption did not surface")
			}
			drivers["routd"] = fault.Store
			if err := renameExecute(context.Background(), journal, plan, drivers, false, true); err != nil {
				t.Fatal(err)
			}
			if items := telegramItems(t, drivers["routd"], store.Chats, "routd"); len(items) != 0 {
				t.Fatalf("create survived rollback: %#v", items)
			}
		})
	}
}

func TestTelegramAccounts(t *testing.T) {
	for _, values := range [][]string{nil, {"telegram"}, {"=111"}, {"telegram=0"},
		{"telegram=-1"}, {"telegram=01"}, {"telegram=abc"}, {"telegram=111", "telegram=222"},
		{"telegram=111", "jynx=111"}} {
		if _, err := telegramAccounts(values); err == nil {
			t.Fatalf("bad accounts accepted: %q", values)
		}
	}
	got, err := telegramAccounts([]string{"jynx=222", "telegram=111"})
	if err != nil || !maps.Equal(got, telegramTestAccounts()) {
		t.Fatalf("accounts: %v %v", got, err)
	}
}

func TestTelegramRekeyCreateGuards(t *testing.T) {
	inventory, drivers := telegramFixture(t)
	item := objstore.Item{Key: objstore.Key{Partition: "routd", Sort: "telegram:222/user/5"},
		Attrs: map[string]string{"agent_cursor": store.MsgStamp(core.TS(time.Now()), "original")}}
	renameSeed(t, &inventory, drivers, "routd", store.Chats, item.Key, item.Attrs)
	row := renameRow{Owner: "routd", Entity: store.Chats, After: item}
	if err := renameApplyBatch(context.Background(), []renameRow{row}, drivers, false); err == nil {
		t.Fatal("create overwrote existing row")
	}
	changed := maps.Clone(item.Attrs)
	changed["agent_cursor"] = store.MsgStamp(core.TS(time.Now()), "drift")
	renameSeed(t, &inventory, drivers, "routd", store.Chats, item.Key, changed)
	if err := renameApplyBatch(context.Background(), []renameRow{row}, drivers, true); err == nil {
		t.Fatal("rollback deleted a changed copy")
	}
}

func TestTelegramRekeyPreflightGuards(t *testing.T) {
	for _, name := range []string{"missing-owner", "missing-table", "unknown-table", "count",
		"held-marker", "no-marker", "collision", "outside-inventory", "missing-fallback"} {
		t.Run(name, func(t *testing.T) {
			inventory, drivers := telegramFixture(t)
			key := objstore.Key{Partition: "telegram:user/5", Sort: store.MsgStamp(core.TS(time.Now()), "input")}
			attrs := map[string]string{"source": "telegram"}
			accounts := telegramTestAccounts()
			if name == "missing-fallback" {
				attrs = map[string]string{"is_bot_message": "1"}
				delete(accounts, "telegram")
			}
			renameSeed(t, &inventory, drivers, "routd", store.Messages, key, attrs)
			want := ""
			switch name {
			case "missing-owner":
				delete(inventory.Owners, "teled")
				want = "missing owner teled"
			case "missing-table":
				delete(inventory.Owners["teled"], chanstore.AckMark)
				want = "missing inventory table teled.ack_marks"
			case "unknown-table":
				inventory.Owners["teled"]["unexpected"] = map[string]int{}
				want = "unclassified table teled.unexpected"
			case "count":
				inventory.Owners["routd"][store.Messages][key.Partition]++
				want = "count changed"
			case "held-marker", "no-marker":
				renameSeed(t, &inventory, drivers, "routd", store.Routes,
					objstore.Key{Partition: "routd", Sort: "0000000001" + store.KeySep + "atlas" + store.KeySep + "room=user/5"}, nil)
				marker := map[string]string{"version": "1", "holder": "operator"}
				want = "replace is in flight"
				if name == "no-marker" {
					marker = map[string]string{}
					want = "no cold version marker"
				}
				ok, err := drivers["routd"].GuardedBatch(context.Background(), nil,
					[]objstore.Write{{Op: objstore.Put, Entity: store.ScopeEntity,
						Key: store.OwnerScope("routd").Key, Attrs: marker}})
				if err != nil || !ok {
					t.Fatalf("marker fixture: %v %v", ok, err)
				}
			case "collision", "outside-inventory":
				dest := objstore.Key{Partition: "telegram:111/user/5", Sort: key.Sort}
				renameSeed(t, &inventory, drivers, "routd", store.Messages, dest, attrs)
				want = "destination row exists"
				if name == "outside-inventory" {
					delete(inventory.Owners["routd"][store.Messages], dest.Partition)
					want = "outside inventory"
				}
			case "missing-fallback":
				want = `source "telegram" has no --account mapping`
			}
			counter := &renameFaultStore{Store: drivers["routd"]}
			drivers["routd"] = counter
			_, err := telegramRekeyPreflight(context.Background(), "fixture", "", accounts, inventory, drivers)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("wanted %q, got %v", want, err)
			}
			if counter.Calls != 0 {
				t.Fatal("preflight wrote")
			}
		})
	}
}

func TestTelegramRekeyNewestInboundTask(t *testing.T) {
	inventory, drivers := telegramFixture(t)
	now := time.Now()
	for i, source := range []string{"telegram", "jynx", ""} {
		attrs := map[string]string{"source": source}
		if source == "" {
			attrs = map[string]string{"is_bot_message": "1"}
		}
		renameSeed(t, &inventory, drivers, "routd", store.Messages,
			objstore.Key{Partition: "telegram:user/5",
				Sort: store.MsgStamp(core.TS(now.Add(time.Duration(i)*time.Second)), fmt.Sprint(i))}, attrs)
	}
	renameSeed(t, &inventory, drivers, "routd", store.ScheduledTasks,
		objstore.Key{Partition: "routd", Sort: "task"},
		map[string]string{"chat_jid": "telegram:user/5", "trigger_sender": "telegram:user/5"})
	plan := telegramTestPlan(t, inventory, drivers)
	journal := filepath.Join(t.TempDir(), "journal")
	if err := renameExecute(context.Background(), journal, plan, drivers, false, false); err != nil {
		t.Fatal(err)
	}
	items := telegramItems(t, drivers["routd"], store.ScheduledTasks, "routd")
	if len(items) != 1 || items[0].Attrs["chat_jid"] != "telegram:222/user/5" ||
		items[0].Attrs["trigger_sender"] != "telegram:user/5" {
		t.Fatalf("task lost the newest inbound account or trigger sender: %#v", items)
	}
}

func TestTelegramRekeyPreservedIdentities(t *testing.T) {
	for _, entity := range []string{store.TurnContexts, store.PendingActions} {
		before := objstore.Item{Key: objstore.Key{Partition: "routd", Sort: "unrelated"},
			Attrs: map[string]string{"chat_jid": "telegram:group/3", "state": "running",
				"sender": "telegram:user/5", "trigger_sender": "telegram:user/5",
				"content": "telegram:user/5"}}
		items, err := telegramRekeyRow(renameRow{Owner: "routd", Entity: entity, Before: before}, nil)
		if err != nil || len(items) != 1 || !reflect.DeepEqual(items[0], before) {
			t.Fatalf("%s: sender identity changed or refused: %#v %v", entity, items, err)
		}
	}
}
