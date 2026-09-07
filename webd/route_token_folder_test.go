package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

// A suffixed JID resolves to the folder the mint RECORDED, not the folder its
// grammar reads. `web:a/b` is byte-identical to a no-suffix mint at folder
// `a/b`, which exists here, so the grammar's answer would hand this request to
// another tenant (BUGS L4).
//
// proxyd used to make this call and stamp the answer in X-Folder; specs/6/22
// step 1 left the single resolution here, so the test came with it.
func TestChatToken_SuffixedJIDServesTheMintedFolder(t *testing.T) {
	st := testutils.NewRoutdStore(t)
	for _, f := range []string{"a", "a/b"} {
		if err := st.PutGroup(core.Group{Folder: f, AddedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	raw := store.GenRouteToken()
	if err := st.InsertRouteToken(raw, store.RouteToken{
		JID: "web:a/b", OwnerFolder: "a", TargetFolder: "a",
	}); err != nil {
		t.Fatal(err)
	}

	rc := chanlib.NewRouterClient("http://127.0.0.1:1")
	rc.SetToken("test-token")
	s := newServer(config{assistantName: "assistant"}, st, newHub(), rc, nil, nil)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/chat/" + raw + "/config")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["folder"] != "a" {
		t.Errorf("folder = %v, want a (the JID grammar's answer is a/b)", out["folder"])
	}
}
