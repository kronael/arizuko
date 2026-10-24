package main

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

func TestTelegramRekeySenderPolicy(t *testing.T) {
	for _, matcher := range []string{"params", "predicate"} {
		t.Run(matcher, func(t *testing.T) {
			inventory, drivers := telegramFixture(t)
			db := store.NewOver(drivers["routd"])
			caller := auth.Caller{Principal: "telegram:user/5",
				Claims: map[string]string{"sender": "telegram:user/5"}}
			params := map[string]string{"sender": "telegram:user/5"}
			rows := []core.ACLRow{
				{Principal: caller.Principal, Action: "mcp:inject_message", Scope: "atlas", Effect: "allow"},
				{Principal: caller.Principal, Action: "mcp:inject_message", Scope: "atlas", Effect: "deny"},
				{Principal: caller.Principal, Action: "hold:mcp:inject_message", Scope: "atlas", Effect: "allow"},
			}
			for i := 1; i < len(rows); i++ {
				if matcher == "params" {
					rows[i].Params = "sender=telegram:user/5"
				} else {
					rows[i].Predicate = "sender=telegram:user/5"
				}
			}
			for _, row := range rows {
				if err := db.AddACLRow(row); err != nil {
					t.Fatal(err)
				}
			}
			inventory.Owners["routd"][store.ACL]["routd"] = len(rows)
			check := func() {
				t.Helper()
				if auth.Authorize(db, caller, "mcp:inject_message", "atlas", params) {
					t.Error("sender deny stopped applying")
				}
				if !auth.CheckHold(db, caller, "inject_message", "atlas", params) {
					t.Error("sender approval hold stopped applying")
				}
			}
			check()
			plan := telegramTestPlan(t, inventory, drivers)
			if err := renameExecute(context.Background(), filepath.Join(t.TempDir(), "journal"),
				plan, drivers, false, false); err != nil {
				t.Fatal(err)
			}
			check()
		})
	}
}

func TestTelegramRekeyMatcherRoles(t *testing.T) {
	for _, field := range []string{"sender", "trigger_sender", "principal", "sub", "child", "parent"} {
		t.Run(field, func(t *testing.T) {
			key := objstore.Key{Partition: "routd", Sort: strings.Join([]string{
				"telegram:user/5", "mcp:send", "atlas", "chat_jid=telegram:user/5",
				field + "=telegram:user/5", "deny"}, store.KeySep)}
			before := objstore.Item{Key: key, Attrs: map[string]string{"granted_by": "telegram:user/5"}}
			items, err := telegramRekeyRow(renameRow{Owner: "routd", Entity: store.ACL, Before: before}, nil)
			if err != nil || len(items) != 1 {
				t.Fatalf("identity refused: %#v %v", items, err)
			}
			want := before
			want.Key.Sort = strings.Replace(key.Sort, "chat_jid=telegram:user/5",
				"chat_jid=telegram:*/user/5", 1)
			if !reflect.DeepEqual(items[0], want) {
				t.Fatalf("identity operand changed: %#v; want %#v", items[0], want)
			}
		})
	}
}

func TestTelegramRekeyUnknownMatcherRole(t *testing.T) {
	inventory, drivers := telegramFixture(t)
	key := objstore.Key{Partition: "routd", Sort: strings.Join([]string{
		"person", "mcp:send", "atlas", "unknown=telegram:user/5", "", "deny"}, store.KeySep)}
	renameSeed(t, &inventory, drivers, "routd", store.ACL, key, nil)
	counter := &renameFaultStore{Store: drivers["routd"]}
	drivers["routd"] = counter
	_, err := telegramRekeyPreflight(context.Background(), "fixture", "",
		telegramTestAccounts(), inventory, drivers)
	if err == nil || !strings.Contains(err.Error(), "unknown ACL matcher") ||
		!strings.Contains(err.Error(), fmt.Sprintf("%q", key.Sort)) {
		t.Fatalf("unclassified operand must refuse and name row: %v", err)
	}
	if counter.Calls != 0 {
		t.Fatal("refused preflight wrote")
	}
}
