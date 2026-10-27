package routd

import (
	"crypto/ecdsa"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

func TestChatsRESTSignedServiceAuthorizationAndAudit(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	const jid = "discord:g/a"
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "demo"}})
	if err := db.SetChatIsGroup(jid, true); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatIsGroup("discord:g/forged", true); err != nil {
		t.Fatal(err)
	}
	key, err := auth.NewSigningKey("chat-test")
	if err != nil {
		t.Fatal(err)
	}
	mint := func(sub string) string {
		t.Helper()
		tok, err := key.Sign(auth.TokenClaims{Sub: sub, Typ: "service", Scope: []string{"messages:write"}}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	srv := NewServer(db, nil, nil, es256Verifier{auth.NewKeySet(map[string]*ecdsa.PublicKey{"chat-test": &key.Priv.PublicKey})}, 0, "")
	h := srv.Handler()
	token := mint("service:dashd")
	read := func(tok string) int {
		return doJSONWithBearer(t, h, "GET", "/v1/chats/"+url.PathEscape(jid), tok, "", nil).Code
	}
	put := func(tok string, v any) int {
		return doJSONWithBearer(t, h, "PUT", "/v1/chats/"+url.PathEscape(jid), tok, "", v).Code
	}
	initial := doJSONWithBearer(t, h, "GET", "/v1/chats/"+url.PathEscape(jid), token, "", nil)
	var response resources.ChatSettingsResponse
	if initial.Code != 200 || json.Unmarshal(initial.Body.Bytes(), &response) != nil || response.JID != jid || !response.ThreadReplies {
		t.Fatalf("GET = %d %s", initial.Code, initial.Body.String())
	}
	if code := put(mint("service:other"), map[string]any{"jid": "discord:g/forged", "thread_replies": false}); code != 403 {
		t.Fatalf("other service = %d", code)
	}
	if code := put(token, map[string]any{"jid": "discord:g/forged", "thread_replies": false}); code != 200 {
		t.Fatalf("PUT = %d", code)
	}
	if n := auditRows(t, db, "chats:update"); n != 1 {
		t.Fatalf("successful mutation audit rows = %d", n)
	}
	if code := put(token, map[string]any{"jid": jid, "thread_replies": "false"}); code != 400 {
		t.Fatalf("wrong type = %d", code)
	}
	if got, err := db.store().ChatThreadReplies(jid); err != nil || got {
		t.Fatalf("saved preference = %v, %v", got, err)
	}
	if got, err := db.store().ChatThreadReplies("discord:g/forged"); err != nil || !got {
		t.Fatalf("body jid changed a different chat = %v, %v", got, err)
	}
	for _, value := range []map[string]any{{"thread_replies": nil}, {}} {
		if code := put(token, value); code != 200 {
			t.Fatalf("reset = %d", code)
		}
		if got, err := db.store().ChatThreadReplies(jid); err != nil || !got {
			t.Fatalf("reset = %v, %v", got, err)
		}
	}
	row := core.ACLRow{Principal: "service:dashd", Action: "chats:update", Scope: "**", Effect: store.EffectAllow}
	if err := db.store().RemoveACLRow(row); err != nil {
		t.Fatal(err)
	}
	if code := put(token, map[string]any{"jid": jid, "thread_replies": false}); code != 403 {
		t.Fatalf("revoked update = %d", code)
	}
	if code := read(token); code != 200 {
		t.Fatalf("GET after revoke = %d", code)
	}
	row.Action, row.Effect = "chats:get", store.EffectDeny
	if err := db.store().AddACLRow(row); err != nil {
		t.Fatal(err)
	}
	if code := read(token); code != 403 {
		t.Fatalf("deny did not win = %d", code)
	}
}

func TestChatSettingsAgentTargetScope(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	const own, other = "discord:g/a", "discord:g/b"
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + own, Target: "world/a"}, {Match: "chat_jid=" + other, Target: "world/b"}})
	grantMCPTools(t, db, "world/a", "get_chat", "set_chat")
	srv := NewServer(db, nil, nil, nil, 0, "")
	folder := "world/a"
	bound := turnMCP{folder: folder}
	sock := groupfolder.IpcSocket(t.TempDir())
	pb := srv.chatsPostBuild(folder, "folder:"+folder, srv.turnAuthorize(bound), srv.turnVisible(bound))
	stop, err := ipc.ServeMCP(sock, srv.buildRoutdFns(bound), srv.buildStoreFns(bound), folder, false, 0, "folder:"+folder, pb)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	result, msg := callToolOverSock(t, sock, "set_chat", map[string]any{"jid": own, "thread_replies": true})
	if msg != "" || result["jid"] != own || result["thread_replies"] != true {
		t.Fatalf("own setting = %+v, %q", result, msg)
	}
	result, msg = callToolOverSock(t, sock, "get_chat", map[string]any{"jid": own})
	if msg != "" || result["thread_replies"] != true {
		t.Fatalf("own read = %+v, %q", result, msg)
	}
	if _, msg := callToolOverSock(t, sock, "set_chat", map[string]any{"jid": other, "thread_replies": true}); msg == "" {
		t.Fatal("cross-folder mutation allowed")
	}
	if got, err := db.store().ChatThreadReplies(other); err != nil || got {
		t.Fatalf("other chat changed = %v, %v", got, err)
	}
	if _, msg := callToolOverSock(t, sock, "get_chat", map[string]any{"jid": other}); msg == "" {
		t.Fatal("cross-folder read allowed")
	}
	if _, msg := callToolOverSock(t, sock, "get_chat", map[string]any{"jid": "unrouted:chat"}); msg == "" {
		t.Fatal("unrouted chat accepted")
	}
}

func TestChatSettingsOpenAPIAndCatalog(t *testing.T) {
	r := resreg.Lookup(resources.ChatsName)
	if r == nil || r.DB != "" || len(r.EntitySortKey) != 0 || r.Table != "chats" {
		t.Fatalf("catalog = %+v", r)
	}
	var doc map[string]any
	if err := json.Unmarshal(routdDoc(t), &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)
	path := paths["/v1/chats/{jid}"].(map[string]any)
	if path["get"] == nil || path["put"] == nil {
		t.Fatalf("paths = %+v", path)
	}
	params, ok := path["parameters"].([]any)
	if !ok || len(params) != 1 {
		t.Fatalf("path parameters = %+v", path["parameters"])
	}
	param := params[0].(map[string]any)
	if param["name"] != "jid" || param["in"] != "path" || param["required"] != true {
		t.Fatalf("jid parameter = %+v", param)
	}
}
