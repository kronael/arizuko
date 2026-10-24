package main

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/router"
	"github.com/kronael/arizuko/store"
)

func TestTelegramRekeyForeignRoutes(t *testing.T) {
	for _, raw := range []string{"platform=web  room=user/5", "room=user/5 sender=person"} {
		t.Run(raw, func(t *testing.T) {
			inventory, drivers := telegramFixture(t)
			key := objstore.Key{Partition: "routd", Sort: strings.Join(
				[]string{"0000000030", "atlas", raw}, store.KeySep)}
			renameSeed(t, &inventory, drivers, "routd", store.Routes, key, map[string]string{"id": "30"})
			renameSeed(t, &inventory, drivers, "routd", store.Chats,
				objstore.Key{Partition: "routd", Sort: "web:user/5"}, map[string]string{"name": "web"})
			before := telegramItems(t, drivers["routd"], store.Routes, "routd")
			plan := telegramTestPlan(t, inventory, drivers)
			if err := renameExecute(context.Background(), filepath.Join(t.TempDir(), "journal"),
				plan, drivers, false, false); err != nil {
				t.Fatal(err)
			}
			after := telegramItems(t, drivers["routd"], store.Routes, "routd")
			if !reflect.DeepEqual(before, after) {
				t.Errorf("foreign route bytes changed: %#v -> %#v", before, after)
			}
			parts := strings.Split(after[0].Key.Sort, store.KeySep)
			if !router.RouteMatches(core.Route{Match: parts[2]},
				core.Message{ChatJID: "web:user/5", Sender: "person"}) {
				t.Error("web:user/5 lost its route")
			}
		})
	}
}

func TestTelegramRekeyAllChatVerdicts(t *testing.T) {
	for _, fixture := range []struct {
		name, jid, entity string
	}{
		{"foreign-match", "web:user/5", store.Chats},
		{"foreign-new-match", "web:111/user/5", store.Chats},
		{"foreign-message-only", "web:user/5", store.Messages},
		{"scoped-telegram", "telegram:333/user/5", store.Chats},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			inventory, drivers := telegramFixture(t)
			stamp := store.MsgStamp("2026-10-24T00:00:00.000000000Z", "input")
			renameSeed(t, &inventory, drivers, "routd", store.Messages,
				objstore.Key{Partition: "telegram:user/5", Sort: stamp}, map[string]string{"source": "telegram"})
			chatKey := objstore.Key{Partition: "routd", Sort: fixture.jid}
			if fixture.entity == store.Messages {
				chatKey = objstore.Key{Partition: fixture.jid, Sort: stamp}
			}
			renameSeed(t, &inventory, drivers, "routd", fixture.entity, chatKey, map[string]string{"name": "other"})
			key := objstore.Key{Partition: "routd", Sort: strings.Join(
				[]string{"0000000030", "atlas", "room=user/5 verb=mention sender=person"}, store.KeySep)}
			renameSeed(t, &inventory, drivers, "routd", store.Routes, key, map[string]string{"id": "30"})
			counter := &renameFaultStore{Store: drivers["routd"]}
			drivers["routd"] = counter
			_, err := telegramRekeyPreflight(context.Background(), "fixture", "",
				telegramTestAccounts(), inventory, drivers)
			if err == nil || !strings.Contains(err.Error(), "route verdict") ||
				!strings.Contains(err.Error(), fmt.Sprintf("%q", key.Sort)) {
				t.Fatalf("unsafe rewrite must refuse and name route: %v", err)
			}
			if counter.Calls != 0 {
				t.Fatal("refused preflight wrote")
			}
		})
	}
}
