package ipc

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"
)

// TestServeMCP_FindMessages_HappyPath confirms the tool is wired:
// arg parse → store call → JSON envelope → result rendering. Tier-0
// caller, ACL trivially true.
func TestServeMCP_FindMessages_HappyPath(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"
	now := time.Now()
	called := 0
	db := StoreFns{
		FindMessages: func(q, scope, sender, since string, limit int) ([]FoundMessage, error) {
			called++
			if q != "budget" {
				t.Errorf("query=%q, want budget", q)
			}
			if limit != 20 {
				t.Errorf("limit=%d, want 20", limit)
			}
			return []FoundMessage{
				{ChatJID: "tg:1", Sender: "alice", Timestamp: now,
					Content: "Q3 «budget» meeting", Rank: -1.5},
			}, nil
		},
		JIDRoutedToFolder: func(jid, folder string) bool { return true },
	}
	stop, err := ServeMCP(sock, GatedFns{}, db, "world", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	payload, errText := callTool(t, sock, "find_messages", map[string]any{
		"query": "budget",
	})
	if errText != "" {
		t.Fatalf("find_messages error: %s", errText)
	}
	if called != 1 {
		t.Fatalf("FindMessages calls = %d, want 1", called)
	}
	if payload["count"].(float64) != 1 {
		t.Fatalf("count = %v, want 1", payload["count"])
	}
	if payload["source"] != "local-db" {
		t.Fatalf("source = %v, want local-db", payload["source"])
	}
}

// TestServeMCP_FindMessages_QueryRequired asserts arg validation.
func TestServeMCP_FindMessages_QueryRequired(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"
	called := 0
	db := StoreFns{
		FindMessages: func(q, scope, sender, since string, limit int) ([]FoundMessage, error) {
			called++
			return nil, nil
		},
	}
	stop, err := ServeMCP(sock, GatedFns{}, db, "world", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	_, errText := callTool(t, sock, "find_messages", map[string]any{})
	if errText == "" {
		t.Fatal("expected query-required error")
	}
	if !strings.Contains(errText, "query") {
		t.Errorf("error mentions query? got %q", errText)
	}
	if called != 0 {
		t.Errorf("FindMessages called %d times despite missing query", called)
	}
}

// TestServeMCP_FindMessages_ACLDropsForeignRows asserts that for tier ≥ 1,
// the post-fetch ACL filter drops rows whose chat_jid isn't routed to the
// caller's folder. The store returns three hits, only one passes the gate.
func TestServeMCP_FindMessages_ACLDropsForeignRows(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"
	now := time.Now()
	db := StoreFns{
		FindMessages: func(q, scope, sender, since string, limit int) ([]FoundMessage, error) {
			return []FoundMessage{
				{ChatJID: "tg:own", Sender: "u", Timestamp: now, Content: "x", Rank: -1.0},
				{ChatJID: "tg:other", Sender: "u", Timestamp: now, Content: "y", Rank: -0.5},
				{ChatJID: "tg:foreign", Sender: "u", Timestamp: now, Content: "z", Rank: -0.4},
			}, nil
		},
		// Caller folder is world/a/b (tier 2). Only tg:own is routed to it.
		JIDRoutedToFolder: func(jid, folder string) bool {
			return jid == "tg:own" && folder == "world/a/b"
		},
	}
	stop, err := ServeMCP(sock, GatedFns{}, db, "world/a/b", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	payload, errText := callTool(t, sock, "find_messages", map[string]any{
		"query": "anything",
	})
	if errText != "" {
		t.Fatalf("find_messages: %s", errText)
	}
	if payload["count"].(float64) != 1 {
		t.Fatalf("count after ACL = %v, want 1", payload["count"])
	}
}

// TestServeMCP_FindMessages_OperatorBypassesACL asserts tier 0 (folder
// = "root" or equivalent) sees all rows even when JIDRoutedToFolder
// would otherwise drop them.
func TestServeMCP_FindMessages_OperatorBypassesACL(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"
	now := time.Now()
	db := StoreFns{
		FindMessages: func(q, scope, sender, since string, limit int) ([]FoundMessage, error) {
			return []FoundMessage{
				{ChatJID: "tg:a", Sender: "u", Timestamp: now, Content: "x", Rank: -1.0},
				{ChatJID: "tg:b", Sender: "u", Timestamp: now, Content: "y", Rank: -0.5},
			}, nil
		},
		// Even if this returned false for everything, tier-0 must bypass.
		JIDRoutedToFolder: func(jid, folder string) bool { return false },
	}
	// "world" is tier-1 in auth.Resolve, not tier-0. Use the literal tier-0
	// folder "root" — same convention as TestSocialActionsRegistered etc.
	stop, err := ServeMCP(sock, GatedFns{}, db, "root", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	payload, errText := callTool(t, sock, "find_messages", map[string]any{
		"query": "x",
	})
	if errText != "" {
		t.Fatalf("find_messages: %s", errText)
	}
	if payload["count"].(float64) != 2 {
		t.Fatalf("count for operator = %v, want 2", payload["count"])
	}
}

// TestServeMCP_FindMessages_ToolRegistered_NoStoreFn asserts the tool
// stays unregistered when StoreFns.FindMessages is nil — same pattern
// as MessagesBefore et al. Verified by calling tools/list and checking
// the absence of "find_messages".
func TestServeMCP_FindMessages_ToolRegistered_NoStoreFn(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"
	db := StoreFns{} // no FindMessages
	stop, err := ServeMCP(sock, GatedFns{}, db, "world", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	if listContainsTool(t, sock, "find_messages") {
		t.Errorf("find_messages registered despite nil StoreFns.FindMessages")
	}
}

// TestServeMCP_FindMessages_ToolRegistered_WithStoreFn confirms the tool
// IS visible in tools/list when the store wires the helper.
func TestServeMCP_FindMessages_ToolRegistered_WithStoreFn(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"
	db := StoreFns{
		FindMessages: func(q, scope, sender, since string, limit int) ([]FoundMessage, error) {
			return nil, nil
		},
	}
	stop, err := ServeMCP(sock, GatedFns{}, db, "world", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()
	if !listContainsTool(t, sock, "find_messages") {
		t.Errorf("find_messages missing from tools/list")
	}
}

// listContainsTool reads tools/list and checks whether `name` is present.
func listContainsTool(t *testing.T, sock, name string) bool {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	c.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n"))
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return strings.Contains(string(resp), `"name":"`+name+`"`)
}
