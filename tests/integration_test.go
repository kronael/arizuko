package tests

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/kronael/arizuko/api"
	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
)

// mockChannel simulates an external channel adapter's HTTP server.
type mockChannel struct {
	mu      sync.Mutex
	sent    []map[string]string
	healthy bool
	srv     *httptest.Server
}

func newMockChannel() *mockChannel {
	m := &mockChannel{healthy: true}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.sent = append(m.sent, body)
		m.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "message_id": fmt.Sprintf("mock-%d", len(m.sent)),
		})
	})
	mux.HandleFunc("POST /typing", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		h := m.healthy
		m.mu.Unlock()
		if !h {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "name": "mock"})
	})
	m.srv = httptest.NewServer(mux)
	return m
}

func (m *mockChannel) close() { m.srv.Close() }

func (m *mockChannel) sentMessages() []map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]map[string]string, len(m.sent))
	copy(cp, m.sent)
	return cp
}

func (m *mockChannel) setHealthy(v bool) {
	m.mu.Lock()
	m.healthy = v
	m.mu.Unlock()
}

// TestFullLifecycle tests the complete channel adapter lifecycle:
// register → deliver inbound → send outbound → health check → deregister
func TestFullLifecycle(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	mock := newMockChannel()
	defer mock.close()

	secret := "integration-secret"
	reg := chanreg.New(secret)
	apiSrv := api.New(reg, s)

	var registeredCh *chanreg.HTTPChannel
	apiSrv.OnRegister(func(name string, ch *chanreg.HTTPChannel) {
		registeredCh = ch
	})

	h := apiSrv.Handler()

	// 1. Register channel
	token := registerChannelWithName(t, h, secret, mock.srv.URL, "mock-tg", []string{"tg:"})

	if registeredCh == nil {
		t.Fatal("expected onRegister callback")
	}

	// 2. Deliver inbound message
	deliverMessage(t, h, token, "tg:123", "tg:456", "Alice", "hello from integration test")

	// Verify stored
	msgs, _, err := s.NewMessages([]string{"tg:123"}, time.Time{}, "bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Content != "hello from integration test" {
		t.Errorf("content = %q", msgs[0].Content)
	}

	// 3. Send outbound via HTTPChannel
	if _, err := registeredCh.Send("tg:123", "reply from router", "", "", ""); err != nil {
		t.Fatal(err)
	}

	sent := mock.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("mock received %d messages, want 1", len(sent))
	}
	if sent[0]["content"] != "reply from router" {
		t.Errorf("sent content = %q", sent[0]["content"])
	}

	// 4. Typing indicator
	if err := registeredCh.Typing("tg:123", true); err != nil {
		t.Fatal(err)
	}

	// 5. Health check succeeds
	if err := registeredCh.HealthCheck(); err != nil {
		t.Fatal(err)
	}

	// 6. Deregister
	w := postJSONWithToken(h, "/v1/channels/deregister", nil, token)
	if w.Code != 200 {
		t.Fatalf("deregister: status %d", w.Code)
	}

	if reg.Get("mock-tg") != nil {
		t.Error("channel should be deregistered")
	}
}

// TestHealthAutoDeregister verifies that 3 consecutive health failures
// cause auto-deregistration.
func TestHealthAutoDeregister(t *testing.T) {
	mock := newMockChannel()
	defer mock.close()

	reg := chanreg.New("secret")
	reg.Register("mock-tg", mock.srv.URL, []string{"tg:"}, nil)

	// healthy: reset works
	e := chanreg.NewHTTPChannel(reg.Get("mock-tg"), "secret")
	if err := e.HealthCheck(); err != nil {
		t.Fatal(err)
	}
	reg.ResetHealth("mock-tg")

	// unhealthy: 3 failures -> deregister
	mock.setHealthy(false)
	for i := 0; i < 3; i++ {
		fails := reg.RecordHealthFail("mock-tg")
		if i < 2 && reg.Get("mock-tg") == nil {
			t.Fatalf("deregistered too early at fail %d", i+1)
		}
		if fails == 3 {
			reg.Deregister("mock-tg")
		}
	}

	if reg.Get("mock-tg") != nil {
		t.Error("expected auto-deregister after 3 failures")
	}
}

// TestOutboxReplay verifies that failed sends are queued and replayed.
func TestOutboxReplay(t *testing.T) {
	e := &chanreg.Entry{
		Name:         "tg",
		URL:          "http://127.0.0.1:1", // unreachable
		JIDPrefixes:  []string{"tg:"},
		Capabilities: map[string]bool{"send_text": true},
	}
	ch := chanreg.NewHTTPChannel(e, "secret")

	// Send fails, queued
	ch.Send("tg:123", "msg1", "", "", "") //nolint:errcheck
	ch.Send("tg:123", "msg2", "", "", "") //nolint:errcheck

	if ch.QueueLen() != 2 {
		t.Fatalf("queue = %d, want 2", ch.QueueLen())
	}

	// Now start a real server and update the entry
	mock := newMockChannel()
	defer mock.close()

	e.URL = mock.srv.URL
	ch.DrainOutbox()

	sent := mock.sentMessages()
	if len(sent) != 2 {
		t.Fatalf("drained %d, want 2", len(sent))
	}
	if ch.QueueLen() != 0 {
		t.Errorf("queue = %d after drain", ch.QueueLen())
	}
}

// TestMultipleChannels verifies that multiple channels can coexist.
func TestMultipleChannels(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "store"))
	defer s.Close()

	reg := chanreg.New("secret")
	apiSrv := api.New(reg, s)
	h := apiSrv.Handler()

	// Register two channels
	token1 := registerChannelWithName(t, h, "secret", "http://tg:9001", "telegram", []string{"tg:"})
	token2 := registerChannelWithName(t, h, "secret", "http://discord:9002", "discord", []string{"discord:"})

	// Deliver to each
	deliverMessage(t, h, token1, "tg:100", "tg:user1", "Alice", "hi from telegram")
	deliverMessage(t, h, token2, "discord:200", "discord:user2", "Bob", "hi from discord")

	// Check stored correctly
	tgMsgs, _, _ := s.NewMessages([]string{"tg:100"}, time.Time{}, "bot")
	dcMsgs, _, _ := s.NewMessages([]string{"discord:200"}, time.Time{}, "bot")

	if len(tgMsgs) != 1 || tgMsgs[0].Content != "hi from telegram" {
		t.Errorf("telegram messages: %v", tgMsgs)
	}
	if len(dcMsgs) != 1 || dcMsgs[0].Content != "hi from discord" {
		t.Errorf("discord messages: %v", dcMsgs)
	}

	// List channels
	w := getJSONWithToken(h, "/v1/channels", "secret")
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	channels := resp["channels"].([]any)
	if len(channels) != 2 {
		t.Errorf("channels = %d, want 2", len(channels))
	}
}

// TestReRegisterUpdatesURL verifies re-registration updates the channel.
func TestReRegisterUpdatesURL(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "store"))
	defer s.Close()

	reg := chanreg.New("secret")
	apiSrv := api.New(reg, s)
	h := apiSrv.Handler()

	registerChannelWithName(t, h, "secret", "http://old:9001", "tg", []string{"tg:"})
	token2 := registerChannelWithName(t, h, "secret", "http://new:9001", "tg", []string{"tg:"})

	e := reg.Get("tg")
	if e == nil || e.URL != "http://new:9001" {
		t.Errorf("url = %q, want http://new:9001", e.URL)
	}

	// Old token should be invalid
	w := postJSONWithToken(h, "/v1/messages", map[string]any{
		"chat_jid": "tg:1", "content": "test",
	}, "old-token-should-fail")
	if w.Code != 401 {
		t.Errorf("old token status = %d", w.Code)
	}

	// New token works
	w = postJSONWithToken(h, "/v1/messages", map[string]any{
		"chat_jid": "tg:1", "content": "test",
	}, token2)
	if w.Code != 200 {
		t.Errorf("new token status = %d", w.Code)
	}
}

// TestMessageAutoID generates ID when not provided.
func TestMessageAutoID(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "store"))
	defer s.Close()

	reg := chanreg.New("")
	apiSrv := api.New(reg, s)
	h := apiSrv.Handler()

	token, _ := reg.Register("tg", "http://tg:9001", []string{"tg:"}, nil)

	w := postJSONWithToken(h, "/v1/messages", map[string]any{
		"chat_jid": "tg:1", "content": "no id",
	}, token)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}

	msgs, _, _ := s.NewMessages([]string{"tg:1"}, time.Time{}, "bot")
	if len(msgs) != 1 {
		t.Fatal("expected 1 message")
	}
	if msgs[0].ID == "" {
		t.Error("expected auto-generated ID")
	}
}

// helpers

func registerChannelWithName(t *testing.T, h http.Handler, secret, url, name string, prefixes []string) string {
	t.Helper()
	w := postJSONWithToken(h, "/v1/channels/register", map[string]any{
		"name":         name,
		"url":          url,
		"jid_prefixes": prefixes,
		"capabilities": map[string]bool{"send_text": true, "send_file": true, "typing": true},
	}, secret)
	if w.Code != 200 {
		t.Fatalf("register %s: status %d, body = %s", name, w.Code, w.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	return resp["token"].(string)
}

func deliverMessage(t *testing.T, h http.Handler, token, chatJID, sender, senderName, content string) {
	t.Helper()
	w := postJSONWithToken(h, "/v1/messages", map[string]any{
		"chat_jid":    chatJID,
		"sender":      sender,
		"sender_name": senderName,
		"content":     content,
		"timestamp":   time.Now().Unix(),
	}, token)
	if w.Code != 200 {
		t.Fatalf("deliver: status %d, body = %s", w.Code, w.Body.String())
	}
}

func postJSONWithToken(h http.Handler, path string, body any, token string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func getJSONWithToken(h http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// connIO adapts a net.Conn to transport.NewIO's (reader, writeCloser).
// Closing the writer closes the underlying conn (once).
type connIO struct{ net.Conn }

func (c connIO) Close() error { return c.Conn.Close() }

// openRawSQLite opens a second handle on the shared messages.db file,
// used by tests to bypass store method filters.
func openRawSQLite(path string) (*sql.DB, error) {
	return sql.Open("sqlite", path+"?_busy_timeout=5000")
}

// TestMCPSocketRoundtrip binds ipc.ServeMCP on a tmpdir unix socket,
// dials it, drives the MCP handshake over the socket, calls send,
// and asserts the gated SendMessage callback fired and a messages row
// with the outbound text landed in the store via recordOutbound.
func TestMCPSocketRoundtrip(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var (
		sentMu sync.Mutex
		sent   []struct{ jid, text string }
	)
	gated := ipc.GatedFns{
		SendMessage: func(jid, text string) (string, error) {
			sentMu.Lock()
			sent = append(sent, struct{ jid, text string }{jid, text})
			sentMu.Unlock()
			return "platform-mid-1", nil
		},
	}
	// Outbound JID auth requires a route covering tg:42 → world's subtree.
	// Add it before calling send (per specs/4/11-auth.md outbound auth).
	if _, err := s.AddRoute(core.Route{Match: "room=42", Target: "world"}); err != nil {
		t.Fatalf("AddRoute: %v", err)
	}
	db := ipc.StoreFns{
		PutMessage:          s.PutMessage,
		DefaultFolderForJID: s.DefaultFolderForJID,
	}

	sock := filepath.Join(dir, "mcp.sock")
	// expectedUID <= 0 disables SO_PEERCRED check for the test.
	stop, err := ipc.ServeMCP(sock, gated, db, "world", []string{"*"}, -1)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	tr := transport.NewIO(conn, connIO{conn}, io.NopCloser(bytes.NewReader(nil)))
	c := mcpclient.NewClient(tr)
	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("client start: %v", err)
	}
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "tests", Version: "1"},
		},
	}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "send",
			Arguments: map[string]any{
				"chatJid": "tg:42",
				"text":    "hello from mcp",
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}

	sentMu.Lock()
	got := append([]struct{ jid, text string }(nil), sent...)
	sentMu.Unlock()
	if len(got) != 1 {
		t.Fatalf("SendMessage calls = %d, want 1", len(got))
	}
	if got[0].jid != "tg:42" || got[0].text != "hello from mcp" {
		t.Errorf("got %+v", got[0])
	}

	// recordOutbound wrote a PutMessage row with FromMe=true, BotMsg=true.
	// Re-open the DB file directly so we can inspect without the
	// MessagesBefore bot-filter.
	dsn := filepath.Join(dir, "store", "messages.db")
	raw, err := openRawSQLite(dsn)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer raw.Close()
	var content string
	var fromMe int
	if err := raw.QueryRow(
		`SELECT content, is_from_me FROM messages WHERE chat_jid = ? AND is_from_me = 1`,
		"tg:42").Scan(&content, &fromMe); err != nil {
		t.Fatalf("outbound row not found: %v", err)
	}
	if content != "hello from mcp" || fromMe != 1 {
		t.Errorf("outbound row: content=%q fromMe=%d", content, fromMe)
	}
}
