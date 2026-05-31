package routd

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

// TestServeTurnMCP_ReplyOverSocket is the end-to-end proof of the cutover flip:
// routd stands up the per-turn agent MCP socket in-process, and a real MCP
// tools/call("reply") over that unix socket flows through buildMCPServer (grants
// filter + authz) → the in-process GatedFns.SendReply → appendAndDeliver → the
// bot row is persisted AND the Deliverer is invoked. No docker, no federation —
// the same path a spawned agent drives, minus the container.
func TestServeTurnMCP_ReplyOverSocket(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// The reply tool's authorizeJID requires the chat to resolve to the folder,
	// so seed a route (mirrors the other routd tests) and use a slack jid.
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	const jid = "slack:team/channel/c1"
	if _, err := db.PutTurnContext("t1", "demo", "", jid, "u1", ""); err != nil {
		t.Fatal(err)
	}

	deliver := &recDeliverer{pid: "pid-x"}
	srv := NewServer(db, nil, deliver, nil, 0, "")

	// Non-existent nested dir → also exercises ServeTurnMCP's mkdir.
	ipcDir := filepath.Join(t.TempDir(), "ipc", "demo")
	stop, err := srv.ServeTurnMCP(
		turnMCP{folder: "demo", topic: "", chatJID: jid, turnID: "t1", trigger: "u1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	defer stop()

	sock := groupfolder.IpcSocket(ipcDir)
	_, errText := callToolOverSock(t, sock, "reply",
		map[string]any{"chatJid": jid, "text": "hello from the socket"})
	if errText != "" {
		t.Fatalf("reply tool error: %s", errText)
	}

	if len(deliver.sends) != 1 || deliver.sends[0].text != "hello from the socket" {
		t.Fatalf("deliver.sends=%+v want one 'hello from the socket'", deliver.sends)
	}
	// Exactly one bot row persists — via the ipc layer's recordOutbound, NOT a
	// second copy from the in-process closure (the double-persist regression).
	if n := countBots(t, db, jid); n != 1 {
		t.Fatalf("bot rows=%d want 1 (single recordOutbound persist, no double)", n)
	}
}

// callToolOverSock drives one MCP tools/call over the unix socket and returns
// the parsed JSON payload (or the tool's error text). Mirrors ipc's test client.
func callToolOverSock(t *testing.T, sock, name string, args map[string]any) (map[string]any, string) {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial %s: %v", sock, err)
	}
	defer c.Close()
	req := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	}
	b, _ := json.Marshal(req)
	c.Write(append(b, '\n'))
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var parsed struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("unmarshal %q: %v", resp, err)
	}
	if parsed.Error != nil {
		t.Fatalf("rpc error: %s", parsed.Error.Message)
	}
	if len(parsed.Result.Content) == 0 {
		t.Fatalf("no content: %s", resp)
	}
	text := parsed.Result.Content[0].Text
	if parsed.Result.IsError {
		return nil, text
	}
	var payload map[string]any
	_ = json.Unmarshal([]byte(text), &payload)
	return payload, ""
}
