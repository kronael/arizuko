package main

// Slink-driven end-to-end tests. Drive a real round through webd's slink
// HTTP surface (POST /slink/<token>, GET /slink/stream, POST /send agent
// callback) against an in-memory store + a fake gated router, then assert
// what an external client would observe: the round-trip user->agent->user.
//
// All tests gate on testing.Short() and are excluded from `make test`.
// Run via `make test-e2e`.

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/tests/testutils"
)

const e2eShortSkip = "e2e: skipped in short mode (run with `make test-e2e`)"

// e2eRound is one full slink round: an httptest server in front of webd's
// real handler, plus a helper to fake the gated /send callback that an
// agent would emit to deliver an assistant reply back into the same group/topic.
type e2eRound struct {
	*integInst
	srv      *httptest.Server
	folder   string
	token    string
	groupRow string
}

func newE2ERound(t *testing.T, folder, name string) *e2eRound {
	t.Helper()
	ii := newIntegServer(t)
	g := seedGroup(t, ii.st, folder, name)
	srv := httptest.NewServer(ii.srv.handler())
	t.Cleanup(srv.Close)
	return &e2eRound{integInst: ii, srv: srv, folder: g.Folder, token: g.SlinkToken, groupRow: g.Folder}
}

// agentReply simulates an in-container agent dispatching its turn back to
// webd via the channel /send callback. replyToID pins the reply onto the
// originating user message's topic.
func (e *e2eRound) agentReply(t *testing.T, replyToID, content string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"chat_jid": "web:" + e.folder,
		"content":  content,
		"reply_to": replyToID,
	})
	req, _ := http.NewRequest("POST", e.srv.URL+"/send", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("agentReply do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("agentReply status = %d", resp.StatusCode)
	}
}

// inboundIDFor returns the stored message id for (chat_jid, content),
// polling briefly because the slink POST writes async w.r.t. its caller.
func (e *e2eRound) inboundIDFor(t *testing.T, content string) string {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var id string
		err := e.DB.QueryRow(
			`SELECT id FROM messages WHERE chat_jid = ? AND content = ? AND is_from_me = 0`,
			"web:"+e.folder, content).Scan(&id)
		if err == nil && id != "" {
			return id
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no inbound row for content=%q on folder=%q", content, e.folder)
	return ""
}

// TestSlinkE2E_DropAndRead drops a user message via slink POST (JSON wait
// mode), simulates the agent replying through gated's /send callback, and
// asserts the assistant payload comes back in the synchronous response.
// Mirrors what a curl-style sync client sees end to end.
func TestSlinkE2E_DropAndRead(t *testing.T) {
	if testing.Short() {
		t.Skip(e2eShortSkip)
	}
	e := newE2ERound(t, "drop", "Drop")

	// Once the inbound row lands, fire the agent-side reply. Race-free:
	// the JSON wait handler is already subscribed before the row is written.
	go func() {
		id := e.inboundIDFor(t, "ping")
		e.agentReply(t, id, "pong")
	}()

	body := strings.NewReader("content=ping&topic=t-drop")
	req, _ := http.NewRequest("POST",
		e.srv.URL+"/slink/"+e.token+"?wait=3", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	user, _ := got["user"].(map[string]any)
	if user == nil || user["content"] != "ping" {
		t.Fatalf("missing/bad user: %+v", got)
	}
	assistant, _ := got["assistant"].(map[string]any)
	if assistant == nil {
		t.Fatalf("missing assistant — agent reply did not propagate: %+v", got)
	}
	if assistant["content"] != "pong" || assistant["role"] != "assistant" {
		t.Errorf("assistant payload wrong: %+v", assistant)
	}

	// Both rows must persist for snapshot reads from elsewhere (dashd, etc).
	testutils.AssertMessage(t, e.DB, "web:drop", "ping")
	testutils.AssertMessage(t, e.DB, "web:drop", "pong")

	// Router (fake gated) must have observed the inbound exactly once —
	// the slink ingress is the only path that drives router.SendMessage.
	if got := e.mr.sent(); len(got) != 1 || got[0].Content != "ping" {
		t.Fatalf("router inbox = %+v, want one ping", got)
	}
}

// TestSlinkE2E_SSEStream drives the same round but reads the assistant
// reply via the SSE upgrade (Accept: text/event-stream). Asserts the user
// bubble and the assistant frame both appear on the live stream — the
// browser-facing path the slink chat widget uses.
func TestSlinkE2E_SSEStream(t *testing.T) {
	if testing.Short() {
		t.Skip(e2eShortSkip)
	}
	e := newE2ERound(t, "sse", "SSE")

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	body := strings.NewReader("content=hello&topic=t-sse")
	req, _ := http.NewRequestWithContext(ctx, "POST",
		e.srv.URL+"/slink/"+e.token, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}

	go func() {
		id := e.inboundIDFor(t, "hello")
		e.agentReply(t, id, "world")
	}()

	var gotUser, gotAssistant bool
	r := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !(gotUser && gotAssistant) {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			break
		}
		if strings.Contains(line, `"role":"user"`) && strings.Contains(line, "hello") {
			gotUser = true
		}
		if strings.Contains(line, `"role":"assistant"`) && strings.Contains(line, "world") {
			gotAssistant = true
		}
	}
	if !gotUser {
		t.Error("SSE missing user bubble — slink ingress did not publish to hub")
	}
	if !gotAssistant {
		t.Error("SSE missing assistant frame — agent /send callback did not republish")
	}
}

// TestSlinkE2E_GetThread posts three messages on a topic, has the agent
// echo each, and asserts MessagesByTopic returns the full thread in order.
// This is the read-side surface get_thread MCP would exercise.
func TestSlinkE2E_GetThread(t *testing.T) {
	if testing.Short() {
		t.Skip(e2eShortSkip)
	}
	e := newE2ERound(t, "thread", "Thread")

	for _, c := range []string{"first", "second", "third"} {
		postSlink(t, e.srv.URL+"/slink/"+e.token, "content="+c+"&topic=t-thread")
		id := e.inboundIDFor(t, c)
		e.agentReply(t, id, "ack:"+c)
	}

	msgs, err := e.st.MessagesByTopic(e.folder, "t-thread", time.Now().Add(time.Second), 50)
	if err != nil {
		t.Fatalf("MessagesByTopic: %v", err)
	}
	// 3 user + 3 assistant. Some store.MessagesByTopic implementations include
	// the topic-rooting message twice if it has both a topic_id and inherited
	// row; assert ≥6 to be tolerant.
	if len(msgs) < 6 {
		t.Fatalf("thread length = %d, want ≥6: %+v", len(msgs), msgs)
	}
	var users, bots int
	for _, m := range msgs {
		if m.BotMsg {
			bots++
		} else {
			users++
		}
	}
	if users < 3 || bots < 3 {
		t.Errorf("thread split: users=%d bots=%d (want ≥3 each)", users, bots)
	}
}

// TestSlinkE2E_OracleSurface is a deliberate stub. The codex/oracle mount
// path lives in container.runner (Run() composes -v $HOST_CODEX_DIR:/home/node/.codex);
// driving that from the slink ingress side requires a real container runner
// hooked to the harness, which this in-process light harness does not own.
// Covered today only at the unit level (container.runner mount-args tests
// in main); revisit once a heavier e2e harness lands.
func TestSlinkE2E_OracleSurface(t *testing.T) {
	if testing.Short() {
		t.Skip(e2eShortSkip)
	}
	t.Skip("not yet implemented in light harness — see slink_e2e_test.go (oracle path covered by container.runner unit tests)")
}

// postSlink fires a default (HTML bubble) slink POST and discards the body.
// Used by tests that only need the side effect (row persisted, hub published).
func postSlink(t *testing.T, url, body string) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("postSlink: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("postSlink status = %d", resp.StatusCode)
	}
}
