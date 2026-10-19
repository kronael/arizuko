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
	"github.com/kronael/arizuko/ipc"
)

// replyForTest drives the actual reply handler when a lifecycle fixture needs
// an explicit delivery. It replaces the retired prose-output callback.
func (s *Server) replyForTest(t *testing.T, turnID, jid, text string) {
	t.Helper()
	tc, ok := s.db.GetTurnContext(turnID)
	if !ok {
		t.Fatalf("missing turn %s", turnID)
	}
	bound := turnMCP{folder: tc.Folder, topic: tc.Topic, chatJID: tc.ChatJID, turnID: turnID, trigger: tc.Trigger}
	sock := t.TempDir() + "/gated.sock"
	stop, err := ipc.ServeMCP(sock, s.buildGatedFns(bound), s.buildStoreFns(bound), tc.Folder, true, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if _, errText := callToolOverSock(t, sock, "reply", map[string]any{"chatJid": jid, "text": text}); errText != "" {
		t.Fatal(errText)
	}
}

// TestServeTurnMCP_ReplyOverSocket is the end-to-end proof of the cutover flip:
// routd stands up the per-turn agent MCP socket in-process, and a real MCP
// tools/call("reply") over that unix socket flows through buildMCPServer (grants
// filter + authz) → the in-process GatedFns.SendReply → recordOutbound → the
// bot row is persisted AND the Deliverer is invoked. No docker, no federation —
// the same path a spawned agent drives, minus the container.
func TestServeTurnMCP_ReplyOverSocket(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// PutGroup binds demo to role:member (5/33-paths-roles messaging floor) so reply is visible.
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
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
// mcpReadDeadline is how long a test waits for one MCP tool response. One value
// for every socket caller, so a slow store cannot make one of them flake while
// its neighbours hold.
const mcpReadDeadline = 15 * time.Second

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
	// The turn's work crosses a network store before the response comes back, so
	// the deadline is the turn's budget and not a local socket's. Two seconds
	// timed out on a loaded host while the send had already succeeded.
	c.SetReadDeadline(time.Now().Add(mcpReadDeadline))
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

// TestMCPReplyRetryReusesIdempotencyKey: the agent's `reply` and the retry
// sweep must present the adapter the SAME idempotency key, because that key is
// the only thing chanlib's deliverOnce dedups on. routd minted a throwaway key
// for the MCP send while the row it persisted carried another, so a first
// attempt whose response was lost left a claim the sweep never matched: it
// opened a fresh one and the user got the message twice (BUGS L106).
//
// The sweep sends under the row id, so this drives a failing reply over the
// real socket, then sweeps the row it left behind and compares the two keys.
func TestMCPReplyRetryReusesIdempotencyKey(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	const jid = "slack:team/channel/c1"
	if _, err := db.PutTurnContext("t1", "demo", "", jid, "u1", ""); err != nil {
		t.Fatal(err)
	}

	// failSend is the live failure: the adapter posted and the response was
	// lost, or it never posted at all. Either way routd owes a retry.
	deliver := &recDeliverer{pid: "pid-x", failSend: true}
	srv := NewServer(db, nil, deliver, nil, 0, "")
	ipcDir := filepath.Join(t.TempDir(), "ipc", "demo")
	stop, err := srv.ServeTurnMCP(
		turnMCP{folder: "demo", topic: "", chatJID: jid, turnID: "t1", trigger: "u1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	defer stop()

	if _, errText := callToolOverSock(t, groupfolder.IpcSocket(ipcDir), "reply",
		map[string]any{"chatJid": jid, "text": "hello"}); errText == "" {
		t.Fatal("reply reported success though the Deliverer failed")
	}
	sends := deliver.sentMsgs()
	if len(sends) != 1 {
		t.Fatalf("deliver.sends=%d want 1", len(sends))
	}
	firstKey := sends[0].idem
	if firstKey == "" {
		t.Fatal("the first send presented no idempotency key, so the adapter cannot dedup it at all")
	}

	// The row the sweep re-sends must BE that key: it sends under m.ID.
	pending, err := db.PendingOutbound(time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending rows=%d want 1 (the failed reply)", len(pending))
	}
	if pending[0].ID != firstKey {
		t.Fatalf("row id=%q but the send claimed %q — the sweep would open a second claim",
			pending[0].ID, firstKey)
	}

	loop := NewLoop(db, runnerFn(nil), LoopConfig{Deliver: deliver, MaxOutboundRetry: 10})
	sweepNow(loop, time.Now().Add(2*time.Minute))

	sends = deliver.sentMsgs()
	if len(sends) != 2 {
		t.Fatalf("deliver.sends=%d want 2 (the reply and its retry)", len(sends))
	}
	if sends[1].idem != firstKey {
		t.Fatalf("retry key=%q, first send key=%q — a mismatch posts the message twice",
			sends[1].idem, firstKey)
	}
}
