package ipc

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	routv1 "github.com/kronael/arizuko/routd/api/v1"
)

// findRecorder is a FindMessages that records each request it is handed.
type findRecorder struct {
	requests []routv1.FindRequest
	answer   routv1.FindResponse
}

func (f *findRecorder) find(_ context.Context, req routv1.FindRequest) (routv1.FindResponse, error) {
	f.requests = append(f.requests, req)
	return f.answer, nil
}

// The tool hands its arguments to the one handler and returns the handler's
// answer as it is: the contract GET /v1/messages/find answers.
func TestServeMCP_FindMessages_HappyPath(t *testing.T) {
	sock := t.TempDir() + "/gated.sock"
	rec := &findRecorder{answer: routv1.FindResponse{
		Messages: []routv1.FoundMessage{{ChatJID: "tg:1", Sender: "alice",
			Timestamp: "2026-11-25T08:00:00Z", Content: "Q3 «budget» meeting"}},
		Count: 1, SearchedTo: "2026-11-25T08:00:00Z", Complete: false,
	}}
	stop, err := ServeMCP(sock, RoutdFns{}, StoreFns{FindMessages: rec.find}, "world", true, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	payload, errText := callTool(t, sock, "find_messages", map[string]any{
		"query": " budget ", "sender": "alice", "since": "2026-11-01T00:00:00Z",
		"before": "2026-11-26T00:00:00Z",
	})
	if errText != "" {
		t.Fatalf("find_messages error: %s", errText)
	}
	want := routv1.FindRequest{Query: "budget", Sender: "alice", Limit: 20,
		Since: "2026-11-01T00:00:00Z", Before: "2026-11-26T00:00:00Z"}
	if len(rec.requests) != 1 || rec.requests[0] != want {
		t.Fatalf("handler got %+v, want %+v", rec.requests, want)
	}
	if payload["count"] != float64(1) || payload["complete"] != false ||
		payload["searched_to"] != "2026-11-25T08:00:00Z" {
		t.Errorf("payload = %v, want the handler's count, complete and searched_to", payload)
	}
	hit := payload["messages"].([]any)[0].(map[string]any)
	if hit["chat_jid"] != "tg:1" || hit["content"] != "Q3 «budget» meeting" {
		t.Errorf("hit = %v", hit)
	}
	if len(payload) != 4 {
		t.Errorf("payload carries %d fields, want messages, count, searched_to, complete: %v",
			len(payload), payload)
	}
}

func TestServeMCP_FindMessages_QueryRequired(t *testing.T) {
	sock := t.TempDir() + "/gated.sock"
	rec := &findRecorder{}
	stop, err := ServeMCP(sock, RoutdFns{}, StoreFns{FindMessages: rec.find}, "world", true, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	_, errText := callTool(t, sock, "find_messages", map[string]any{})
	if !strings.Contains(errText, "query") {
		t.Errorf("error = %q, want one that names query", errText)
	}
	if len(rec.requests) != 0 {
		t.Errorf("handler called %d times despite missing query", len(rec.requests))
	}
}

// The scope is confined BEFORE the search, so `limit` counts hits the caller
// can read. A filter applied to the hits afterwards answered an empty list
// while the caller's own matches lay below limit foreign ones.
func TestServeMCP_FindMessages_ConfinesTheScopeBeforeTheSearch(t *testing.T) {
	sock := t.TempDir() + "/gated.sock"
	rec := &findRecorder{}
	db := StoreFns{
		FindMessages: rec.find,
		JIDRoutedToFolder: func(jid, folder string) bool {
			return jid == "tg:own" && folder == "world/a/b"
		},
	}
	stop, err := ServeMCP(sock, RoutdFns{}, db, "world/a/b", false, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	for _, c := range []struct{ scope, searched string }{
		{"", "world/a/b"},
		{"world/a/b", "world/a/b"},
		{"world/a/b/c", "world/a/b/c"},
		{"tg:own", "tg:own"},
	} {
		rec.requests = nil
		_, errText := callTool(t, sock, "find_messages",
			map[string]any{"query": "x", "scope": c.scope})
		if errText != "" {
			t.Errorf("scope %q refused: %s", c.scope, errText)
			continue
		}
		if len(rec.requests) != 1 || rec.requests[0].Scope != c.searched {
			t.Errorf("scope %q searched %+v, want scope %q", c.scope, rec.requests, c.searched)
		}
	}
	for _, scope := range []string{"world/a", "world/a/bc", "world", "other", "tg:foreign"} {
		rec.requests = nil
		_, errText := callTool(t, sock, "find_messages",
			map[string]any{"query": "x", "scope": scope})
		if !strings.Contains(errText, "access_denied") {
			t.Errorf("scope %q answered %q, want access_denied", scope, errText)
		}
		if len(rec.requests) != 0 {
			t.Errorf("scope %q reached the handler: %+v", scope, rec.requests)
		}
	}
}

// A root turn is not confined: an empty scope stays every folder, and any
// chat or folder is searchable.
func TestServeMCP_FindMessages_OperatorIsNotConfined(t *testing.T) {
	sock := t.TempDir() + "/gated.sock"
	rec := &findRecorder{}
	db := StoreFns{
		FindMessages:      rec.find,
		JIDRoutedToFolder: func(jid, folder string) bool { return false },
	}
	stop, err := ServeMCP(sock, RoutdFns{}, db, "", true, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	for _, scope := range []string{"", "tg:any", "other/folder"} {
		rec.requests = nil
		_, errText := callTool(t, sock, "find_messages",
			map[string]any{"query": "x", "scope": scope})
		if errText != "" || len(rec.requests) != 1 || rec.requests[0].Scope != scope {
			t.Errorf("scope %q: err=%q requests=%+v", scope, errText, rec.requests)
		}
	}
}

func TestServeMCP_FindMessages_ToolRegistered_NoStoreFn(t *testing.T) {
	sock := t.TempDir() + "/gated.sock"
	stop, err := ServeMCP(sock, RoutdFns{}, StoreFns{}, "world", true, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	if listContainsTool(t, sock, "find_messages") {
		t.Errorf("find_messages registered despite nil StoreFns.FindMessages")
	}
}

func TestServeMCP_FindMessages_ToolRegistered_WithStoreFn(t *testing.T) {
	sock := t.TempDir() + "/gated.sock"
	rec := &findRecorder{}
	stop, err := ServeMCP(sock, RoutdFns{}, StoreFns{FindMessages: rec.find}, "world", true, 0, "")
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
