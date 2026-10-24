package routd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

func TestPairingExplicitSender(t *testing.T) {
	const chat = "discord:group/200"
	const sender = "discord:user/A"
	const parent = "github:alice"
	for _, tool := range []string{"issue_pairing_link", "unpair"} {
		t.Run(tool, func(t *testing.T) {
			db, sock := pairingFixture(t, "hq", "room=group/200")
			for i, row := range []core.Message{
				{ID: "A", ChatJID: chat, Sender: sender},
				{ID: "B", ChatJID: chat, Sender: "discord:user/B"},
				{ID: "C", ChatJID: "discord:group/other", Sender: "discord:user/C"},
				{ID: "bot", ChatJID: chat, Sender: "discord:user/bot", BotMsg: true},
				{ID: "out", ChatJID: chat, Sender: "discord:user/out", FromMe: true},
			} {
				row.Timestamp = time.Now().Add(time.Duration(i) * time.Second)
				if err := db.PutMessage(row); err != nil {
					t.Fatal(err)
				}
			}
			if tool == "unpair" {
				grantMCPTools(t, db, "hq", tool)
				sock = serveMembershipMCP(t, db, "hq", "folder:hq")
				for _, child := range []string{sender, "discord:user/B", "discord:user/C"} {
					if err := db.store().PutMembership(child, parent, store.PairingAddedBy); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, absent := range []string{"", "discord:user/C", "discord:user/bot", "discord:user/out"} {
				args := map[string]any{"jid": chat, "parent": parent}
				if absent != "" {
					args["sender"] = absent
				}
				_, e := callToolText(t, sock, tool, args)
				if e == "" || !strings.Contains(e, chat) || !strings.Contains(e, absent) {
					t.Errorf("%s sender %q: error = %q, want chat and sender refusal", tool, absent, e)
				}
			}
			if tool == "issue_pairing_link" && len(tokenItems(t, db)) != 0 {
				t.Error("refused mints wrote tokens")
			}
			text, e := callToolText(t, sock, tool, map[string]any{
				"jid": chat, "sender": sender, "parent": parent,
			})
			if e != "" {
				t.Fatal(e)
			}
			if tool == "issue_pairing_link" {
				var out struct{ URL string }
				if err := json.Unmarshal([]byte(text), &out); err != nil {
					t.Fatal(err)
				}
				raw := strings.TrimPrefix(out.URL, "https://x.test/pair/")
				got, err := db.store().PeekPairing(raw)
				if err != nil || got != sender {
					t.Fatalf("pairing bound %q, want %q (A spoke before B): %v", got, sender, err)
				}
			} else if hasEdge(db, sender, parent) || !hasEdge(db, "discord:user/B", parent) ||
				!hasEdge(db, "discord:user/C", parent) {
				t.Fatal("unpair must remove A alone and preserve B and C")
			}
		})
	}
}

func TestPairingSenderValidationBounded(t *testing.T) {
	const chat = "discord:group/200"
	db, _ := pairingFixture(t, "hq", "room=group/200")
	seedPairingInbound(t, db, "discord:group/other", "discord:user/C")
	reads := &folderMessageReads{Store: db.Obj()}
	s := &Server{db: OpenOver(reads), webHost: "https://x.test"}
	for _, tool := range []string{"issue_pairing_link", "unpair"} {
		for _, sender := range []string{"", "discord:user/C"} {
			reads.queries = nil
			x := resreg.Execution{Args: map[string]any{
				"jid": chat, "sender": sender, "parent": "github:alice",
			}}
			var err error
			if tool == "issue_pairing_link" {
				x.Action = routeTokensActionIssuePair
				_, err = s.routeTokensHandler(context.Background(), x)
			} else {
				x.Action = resreg.ActionDelete
				_, err = s.membershipHandler(context.Background(), x,
					func(resreg.Caller, resreg.Action, string) error { return nil })
			}
			var response *resreg.HandlerError
			if !errors.As(err, &response) || response.Code != http.StatusBadRequest ||
				!strings.Contains(err.Error(), chat) || !strings.Contains(err.Error(), sender) {
				t.Fatalf("%s sender %q: want 400 naming chat and sender, got %v", tool, sender, err)
			}
			if sender == "" {
				if len(reads.queries) != 0 {
					t.Fatal("missing sender read messages")
				}
				continue
			}
			if len(reads.queries) != 1 {
				t.Fatalf("message reads = %d, want one", len(reads.queries))
			}
			q := reads.queries[0]
			if q.Partition != chat || q.Limit != 1 {
				t.Fatalf("unbounded or foreign-chat read: %+v", q)
			}
			found := false
			for _, cmp := range q.Where {
				if cmp.Attr == "sender" && cmp.Op == objstore.Eq && cmp.Value == sender {
					found = true
				}
			}
			if !found {
				t.Fatalf("read does not filter explicit sender: %+v", q)
			}
		}
	}
}
