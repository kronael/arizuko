package routd

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/kronael/arizuko/ipc"
)

func callTurnMethod(t *testing.T, sock, method, turnID string) string {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(mcpReadDeadline)); err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": map[string]any{
		"turn_id": turnID, "status": "error", "error": "forged", "session_id": "forged-session", "text": "foreign status",
	}}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response struct{ Error *struct{ Message string } }
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		return response.Error.Message
	}
	return ""
}

// L459: all hidden methods must honor the socket's exact turn binding even
// when a supplied foreign ID names an existing context in another folder.
func TestTurnMethodsCannotMutateForeignExistingContext(t *testing.T) {
	for _, method := range []string{"check_turn", "submit_status", "submit_turn"} {
		t.Run(method, func(t *testing.T) {
			db, err := OpenMem()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			dl := &recDeliverer{}
			srv := NewServer(db, nil, dl, nil, 0, "")
			if _, err := db.PutTurnContext("bound", "demo", "", "tg:1", "u1", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := db.PutTurnContext("foreign", "other", "", "tg:2", "u2", ""); err != nil {
				t.Fatal(err)
			}
			if err := db.SetTurnAddressed("bound", true); err != nil {
				t.Fatal(err)
			}
			if err := db.SetTurnAddressed("foreign", true); err != nil {
				t.Fatal(err)
			}
			bound := turnMCP{folder: "demo", chatJID: "tg:1", turnID: "bound", trigger: "u1"}
			sock := t.TempDir() + "/gated.sock"
			stop, err := ipc.ServeMCP(sock, srv.buildGatedFns(bound), srv.buildStoreFns(bound), "demo", true, 0, "")
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			if msg := callTurnMethod(t, sock, method, "foreign"); msg == "" {
				t.Error("foreign existing turn accepted")
			}
			tc, _ := db.GetTurnContext("foreign")
			_, status := db.LastStatusRow("tg:2", "foreign")
			if tc.State != "running" || tc.Nudged != 0 || status || db.TurnResultRecorded("other", "foreign") || db.SessionID("other", "") != "" || len(dl.sends) != 0 {
				t.Errorf("foreign context mutated: context=%+v status=%v sends=%+v", tc, status, dl.sends)
			}
			if msg := callTurnMethod(t, sock, method, "bound"); msg != "" {
				t.Errorf("bound turn refused: %s", msg)
			}
			tc, _ = db.GetTurnContext("bound")
			if method == "check_turn" && tc.Nudged != 1 {
				t.Error("valid nudge lost")
			}
			if method == "submit_status" {
				if _, ok := db.LastStatusRow("tg:1", "bound"); !ok {
					t.Error("valid status lost")
				}
			}
			if method == "submit_turn" && !db.TurnResultRecorded("demo", "bound") {
				t.Error("valid result lost")
			}
		})
	}
}
