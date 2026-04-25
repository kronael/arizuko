package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/onvos/arizuko/chanlib"
)

// TestIsRateLimit exercises every detection branch of isRateLimit.
func TestIsRateLimit(t *testing.T) {
	if isRateLimit(nil) {
		t.Error("nil should not be rate limit")
	}
	if isRateLimit(errors.New("plain error")) {
		t.Error("plain error should not be rate limit")
	}
	if !isRateLimit(errors.New("HTTP 429 Too Many Requests")) {
		t.Error("429 substring should detect")
	}
	if !isRateLimit(errors.New("you are being RATE LIMITED")) {
		t.Error("rate limit substring should detect")
	}

	// Typed RESTError with 429
	rest := &discordgo.RESTError{
		Response: &http.Response{StatusCode: http.StatusTooManyRequests},
	}
	if !isRateLimit(rest) {
		t.Error("RESTError 429 should detect")
	}
	// RESTError with non-429 status and clean message — should not detect.
	rest500 := &discordgo.RESTError{
		Response:     &http.Response{StatusCode: 500},
		ResponseBody: []byte(`{"code":50007}`),
	}
	if isRateLimit(rest500) {
		t.Error("500 status should not detect")
	}

	// Typed RateLimitError
	rl := &discordgo.RateLimitError{RateLimit: &discordgo.RateLimit{URL: "x"}}
	if !isRateLimit(rl) {
		t.Error("RateLimitError should detect")
	}
}

// mockDiscordEndpoints overrides the package-level endpoint vars used by
// Session.ChannelMessageSend / ChannelTyping so requests land on httptest.
// Restores previous values via the returned cleanup.
func mockDiscordEndpoints(base string) func() {
	oldMsgs := discordgo.EndpointChannelMessages
	oldTyping := discordgo.EndpointChannelTyping
	discordgo.EndpointChannelMessages = func(cID string) string {
		return base + "/channels/" + cID + "/messages"
	}
	discordgo.EndpointChannelTyping = func(cID string) string {
		return base + "/channels/" + cID + "/typing"
	}
	return func() {
		discordgo.EndpointChannelMessages = oldMsgs
		discordgo.EndpointChannelTyping = oldTyping
	}
}

func newTestSession(t *testing.T) *discordgo.Session {
	t.Helper()
	s, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	// Disable rate-limit retries inside discordgo so our own retry loop is
	// what exercises the isRateLimit path.
	s.ShouldRetryOnRateLimit = false
	s.MaxRestRetries = 0
	s.State = discordgo.NewState()
	s.State.User = &discordgo.User{ID: "bot-self"}
	// Register a guild so non-DM ChannelAdd calls succeed in state.
	s.State.GuildAdd(&discordgo.Guild{ID: "g1"})
	return s
}

func addChannel(t *testing.T, s *discordgo.Session, id string, typ discordgo.ChannelType) {
	t.Helper()
	if err := s.State.ChannelAdd(&discordgo.Channel{ID: id, GuildID: "g1", Type: typ}); err != nil {
		t.Fatalf("ChannelAdd(%s): %v", id, err)
	}
}

func TestSend_Success(t *testing.T) {
	var calls int32
	var captured struct {
		sync.Mutex
		content   string
		channelID string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		captured.Lock()
		captured.content, _ = body["content"].(string)
		parts := strings.Split(r.URL.Path, "/")
		captured.channelID = parts[2]
		captured.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"msg-1","channel_id":"ch-1","content":"hi"}`))
	}))
	defer srv.Close()
	defer mockDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t)}
	id, err := b.Send(chanlib.SendRequest{ChatJID: "discord:ch-1", Content: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "msg-1" {
		t.Errorf("id = %q", id)
	}
	captured.Lock()
	defer captured.Unlock()
	if captured.content != "hello" {
		t.Errorf("content = %q", captured.content)
	}
	if captured.channelID != "ch-1" {
		t.Errorf("channel = %q", captured.channelID)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("calls = %d", calls)
	}
}

func TestSend_Chunks(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		w.Write([]byte(`{"id":"msg-` + string(rune('0'+n)) + `"}`))
	}))
	defer srv.Close()
	defer mockDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t)}
	// 2500 chars → 2 chunks at 2000 each
	content := strings.Repeat("a", 2500)
	id, err := b.Send(chanlib.SendRequest{ChatJID: "discord:ch-1", Content: content})
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("expected 2 calls for chunked send, got %d", calls)
	}
	if id != "msg-1" {
		t.Errorf("id = %q (should be first chunk's id)", id)
	}
}

func TestSend_UsesThreadID(t *testing.T) {
	var capturedPath string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedPath = r.URL.Path
		mu.Unlock()
		w.Write([]byte(`{"id":"m1"}`))
	}))
	defer srv.Close()
	defer mockDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t)}
	_, err := b.Send(chanlib.SendRequest{
		ChatJID: "discord:ch-1", Content: "x", ThreadID: "thread-99",
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(capturedPath, "thread-99") {
		t.Errorf("path did not use thread id: %q", capturedPath)
	}
}

func TestSend_Reply(t *testing.T) {
	var captured struct {
		sync.Mutex
		hasRef  bool
		refID   string
		callIdx int
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		captured.Lock()
		captured.callIdx++
		idx := captured.callIdx
		if ref, ok := body["message_reference"].(map[string]any); ok {
			captured.hasRef = true
			captured.refID, _ = ref["message_id"].(string)
		}
		captured.Unlock()
		w.Write([]byte(`{"id":"r` + string(rune('0'+idx)) + `"}`))
	}))
	defer srv.Close()
	defer mockDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t)}
	_, err := b.Send(chanlib.SendRequest{
		ChatJID: "discord:ch-1", Content: "reply text",
		ReplyTo: "parent-42",
	})
	if err != nil {
		t.Fatal(err)
	}
	captured.Lock()
	defer captured.Unlock()
	if !captured.hasRef {
		t.Error("first chunk should carry message_reference")
	}
	if captured.refID != "parent-42" {
		t.Errorf("refID = %q", captured.refID)
	}
}

func TestSend_RateLimitRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"message":"You are being rate limited.","retry_after":0.01,"global":false}`))
			return
		}
		w.Write([]byte(`{"id":"ok"}`))
	}))
	defer srv.Close()
	defer mockDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t)}
	start := time.Now()
	id, err := b.Send(chanlib.SendRequest{ChatJID: "discord:ch-1", Content: "hi"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if id != "ok" {
		t.Errorf("id = %q", id)
	}
	if atomic.LoadInt32(&calls) < 2 {
		t.Errorf("should have retried; calls = %d", calls)
	}
	// Sleep is 1s between attempts — but test with 2 calls should be ~1s.
	if elapsed > 5*time.Second {
		t.Errorf("elapsed %v; should be under 5s", elapsed)
	}
}

func TestSend_NonRetryableError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"forbidden","code":50001}`))
	}))
	defer srv.Close()
	defer mockDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t)}
	_, err := b.Send(chanlib.SendRequest{ChatJID: "discord:ch-1", Content: "hi"})
	if err == nil {
		t.Fatal("expected error")
	}
	// Should not retry non-rate-limit errors — expect exactly 1 call.
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("non-retryable should fire once; calls = %d", calls)
	}
}

func TestSendFile(t *testing.T) {
	var hitCount int32
	var gotFilename string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hitCount, 1)
		r.ParseMultipartForm(32 << 20)
		if r.MultipartForm != nil {
			for _, fh := range r.MultipartForm.File {
				if len(fh) > 0 {
					mu.Lock()
					gotFilename = fh[0].Filename
					mu.Unlock()
				}
			}
		}
		w.Write([]byte(`{"id":"f1"}`))
	}))
	defer srv.Close()
	defer mockDiscordEndpoints(srv.URL)()

	tmpDir := t.TempDir()
	path := tmpDir + "/hello.txt"
	os_WriteFile(t, path, []byte("hi"))

	b := &bot{session: newTestSession(t)}
	if err := b.SendFile("discord:ch-1", path, "hello.txt", "caption"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&hitCount) != 1 {
		t.Errorf("hits = %d", hitCount)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotFilename != "hello.txt" {
		t.Errorf("filename = %q", gotFilename)
	}
}

func TestSendFile_OpenFails(t *testing.T) {
	b := &bot{session: newTestSession(t)}
	err := b.SendFile("discord:ch-1", "/nonexistent/path", "x", "c")
	if err == nil {
		t.Fatal("expected open error")
	}
}

func TestTyping_DebouncesAndSends(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	defer mockDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t)}
	// Use a fast refresher so the test stays under budget.
	b.typing = chanlib.NewTypingRefresher(20*time.Millisecond, time.Second, b.sendTyping, nil)
	defer b.typing.Stop()

	b.Typing("discord:ch-1", true)
	// Allow at least one tick
	time.Sleep(80 * time.Millisecond)
	b.Typing("discord:ch-1", false)

	if atomic.LoadInt32(&calls) < 1 {
		t.Errorf("expected typing send; calls = %d", calls)
	}
}

func TestTyping_Off_NoSend(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer srv.Close()
	defer mockDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t)}
	b.typing = chanlib.NewTypingRefresher(20*time.Millisecond, time.Second, b.sendTyping, nil)
	defer b.typing.Stop()

	// Set off without ever setting on — should not trigger any API call.
	b.Typing("discord:ch-1", false)
	time.Sleep(60 * time.Millisecond)
	if atomic.LoadInt32(&calls) != 0 {
		t.Errorf("typing=off without on should not call API; calls = %d", calls)
	}
}

// TestOnMessage_Inbound exercises onMessage by wiring a router mock and
// preloading channel state so the handler doesn't hit the real HTTP API.
func TestOnMessage_Inbound(t *testing.T) {
	mr := newDiscdRouterMock()
	defer mr.close()
	rc := chanlib.NewRouterClient(mr.srv.URL, "")
	rc.SetToken("tok")

	sess := newTestSession(t)
	addChannel(t, sess, "ch-1", discordgo.ChannelTypeGuildText)

	srv := newServer(config{Name: "discord", ListenURL: "http://discd:9002"}, nil, func() bool { return true }, func() int64 { return time.Now().Unix() })
	b := &bot{session: sess, cfg: config{AssistantName: "Ari", ListenURL: "http://discd:9002"}, rc: rc, files: srv.files}

	msg := &discordgo.MessageCreate{Message: &discordgo.Message{
		ID:        "m1",
		ChannelID: "ch-1",
		Content:   "hi <@bot-self>",
		Author:    &discordgo.User{ID: "user-42", Username: "alice"},
		Timestamp: time.Unix(1_700_000_000, 0),
	}}
	b.onMessage(sess, msg)

	mr.mu.Lock()
	defer mr.mu.Unlock()
	if len(mr.msgs) != 1 {
		t.Fatalf("dispatched %d, want 1", len(mr.msgs))
	}
	got := mr.msgs[0]
	if got.ChatJID != "discord:ch-1" {
		t.Errorf("ChatJID = %q", got.ChatJID)
	}
	if got.Sender != "discord:user-42" {
		t.Errorf("Sender = %q", got.Sender)
	}
	if !strings.Contains(got.Content, "@Ari") {
		t.Errorf("mention not replaced: %q", got.Content)
	}
	if got.Topic != "" {
		t.Errorf("non-thread should have empty topic, got %q", got.Topic)
	}
}

func TestOnMessage_ThreadChannelTopic(t *testing.T) {
	mr := newDiscdRouterMock()
	defer mr.close()
	rc := chanlib.NewRouterClient(mr.srv.URL, "")
	rc.SetToken("tok")

	sess := newTestSession(t)
	addChannel(t, sess, "thread-7", discordgo.ChannelTypeGuildPublicThread)

	srv := newServer(config{Name: "discord"}, nil, func() bool { return true }, func() int64 { return time.Now().Unix() })
	b := &bot{session: sess, cfg: config{}, rc: rc, files: srv.files}
	b.onMessage(sess, &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "t1", ChannelID: "thread-7", Content: "hi",
		Author: &discordgo.User{ID: "u1"}, Timestamp: time.Now(),
	}})

	mr.mu.Lock()
	defer mr.mu.Unlock()
	if len(mr.msgs) != 1 || mr.msgs[0].Topic != "thread-7" {
		t.Errorf("thread topic not set: %+v", mr.msgs)
	}
}

func TestOnMessage_BotIgnored(t *testing.T) {
	mr := newDiscdRouterMock()
	defer mr.close()
	rc := chanlib.NewRouterClient(mr.srv.URL, "")
	rc.SetToken("tok")

	srv := newServer(config{Name: "discord"}, nil, func() bool { return true }, func() int64 { return time.Now().Unix() })
	sess := newTestSession(t)
	b := &bot{session: sess, rc: rc, files: srv.files}
	b.onMessage(sess, &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "x", ChannelID: "ch-1", Content: "hi",
		Author: &discordgo.User{ID: "bot-other", Bot: true},
	}})
	if len(mr.msgs) != 0 {
		t.Errorf("bot message should be ignored, got %d", len(mr.msgs))
	}
}

func TestOnMessage_NilAuthor(t *testing.T) {
	srv := newServer(config{Name: "discord"}, nil, func() bool { return true }, func() int64 { return time.Now().Unix() })
	b := &bot{session: newTestSession(t), files: srv.files}
	// nil message
	b.onMessage(nil, nil)
	// nil author
	b.onMessage(nil, &discordgo.MessageCreate{Message: &discordgo.Message{ChannelID: "x"}})
}

func TestOnMessage_Attachment(t *testing.T) {
	mr := newDiscdRouterMock()
	defer mr.close()
	rc := chanlib.NewRouterClient(mr.srv.URL, "")
	rc.SetToken("tok")

	sess := newTestSession(t)
	addChannel(t, sess, "ch-1", discordgo.ChannelTypeGuildText)

	srv := newServer(config{Name: "discord", ListenURL: "http://discd:9002"}, nil, func() bool { return true }, func() int64 { return time.Now().Unix() })
	b := &bot{session: sess, cfg: config{ListenURL: "http://discd:9002"}, rc: rc, files: srv.files}

	b.onMessage(sess, &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "m1", ChannelID: "ch-1", Content: "see attached",
		Author: &discordgo.User{ID: "u1"}, Timestamp: time.Now(),
		Attachments: []*discordgo.MessageAttachment{
			{URL: "https://cdn.discordapp.com/a.png", Filename: "a.png", ContentType: "image/png", Size: 1000},
		},
	}})
	mr.mu.Lock()
	defer mr.mu.Unlock()
	if len(mr.msgs) != 1 {
		t.Fatalf("messages = %d", len(mr.msgs))
	}
	m := mr.msgs[0]
	if len(m.Attachments) != 1 {
		t.Fatalf("attachments = %d", len(m.Attachments))
	}
	if !strings.Contains(m.Content, "[Attachment: a.png]") {
		t.Errorf("content = %q", m.Content)
	}
	if !strings.HasPrefix(m.Attachments[0].URL, "http://discd:9002/files/") {
		t.Errorf("attachment URL = %q", m.Attachments[0].URL)
	}
}

func TestOnMessage_EmptyContentAndNoAttachments_Skipped(t *testing.T) {
	mr := newDiscdRouterMock()
	defer mr.close()
	rc := chanlib.NewRouterClient(mr.srv.URL, "")
	rc.SetToken("tok")

	sess := newTestSession(t)
	addChannel(t, sess, "ch-1", discordgo.ChannelTypeGuildText)

	srv := newServer(config{Name: "discord"}, nil, func() bool { return true }, func() int64 { return time.Now().Unix() })
	b := &bot{session: sess, rc: rc, files: srv.files}
	b.onMessage(sess, &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "m1", ChannelID: "ch-1", Content: "",
		Author: &discordgo.User{ID: "u1"}, Timestamp: time.Now(),
	}})
	mr.mu.Lock()
	defer mr.mu.Unlock()
	if len(mr.msgs) != 0 {
		t.Errorf("empty content should be skipped, got %d", len(mr.msgs))
	}
}

func TestOnReactionAdd_LikeAndDislike(t *testing.T) {
	mr := newDiscdRouterMock()
	defer mr.close()
	rc := chanlib.NewRouterClient(mr.srv.URL, "")
	rc.SetToken("tok")

	sess := newTestSession(t)
	b := &bot{session: sess, cfg: config{}, rc: rc}

	b.onReactionAdd(sess, &discordgo.MessageReactionAdd{MessageReaction: &discordgo.MessageReaction{
		UserID: "u1", MessageID: "m1", ChannelID: "ch-1",
		Emoji: discordgo.Emoji{Name: "👍"},
	}})
	b.onReactionAdd(sess, &discordgo.MessageReactionAdd{MessageReaction: &discordgo.MessageReaction{
		UserID: "u2", MessageID: "m2", ChannelID: "ch-1",
		Emoji: discordgo.Emoji{Name: "👎"},
	}})

	mr.mu.Lock()
	defer mr.mu.Unlock()
	if len(mr.msgs) != 2 {
		t.Fatalf("got %d msgs, want 2", len(mr.msgs))
	}
	if mr.msgs[0].Verb != "like" || mr.msgs[0].Reaction != "👍" {
		t.Errorf("msg[0] verb=%q reaction=%q", mr.msgs[0].Verb, mr.msgs[0].Reaction)
	}
	if mr.msgs[1].Verb != "dislike" || mr.msgs[1].Reaction != "👎" {
		t.Errorf("msg[1] verb=%q reaction=%q", mr.msgs[1].Verb, mr.msgs[1].Reaction)
	}
	if mr.msgs[0].ReplyTo != "m1" {
		t.Errorf("ReplyTo = %q, want m1", mr.msgs[0].ReplyTo)
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	t.Setenv("DISCORD_BOT_TOKEN", "tok")
	t.Setenv("ROUTER_URL", "http://router:7000")
	t.Setenv("CHANNEL_NAME", "")
	t.Setenv("LISTEN_ADDR", "")
	cfg := loadConfig()
	if cfg.Name != "discord" {
		t.Errorf("name = %q", cfg.Name)
	}
	if cfg.ListenAddr != ":9002" {
		t.Errorf("addr = %q", cfg.ListenAddr)
	}
}

// router mock
type discdRouter struct {
	mu   sync.Mutex
	msgs []chanlib.InboundMsg
	srv  *httptest.Server
}

func newDiscdRouterMock() *discdRouter {
	m := &discdRouter{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		var im chanlib.InboundMsg
		json.NewDecoder(r.Body).Decode(&im)
		m.mu.Lock()
		m.msgs = append(m.msgs, im)
		m.mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	})
	m.srv = httptest.NewServer(mux)
	return m
}

func (m *discdRouter) close() { m.srv.Close() }

func TestFetchHistory(t *testing.T) {
	var capturedQuery string
	var capturedPath string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedPath = r.URL.Path
		capturedQuery = r.URL.RawQuery
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"id":"m2","channel_id":"ch-1","content":"second","timestamp":"2026-04-18T12:00:00Z",
			 "author":{"id":"u1","username":"alice"},
			 "message_reference":{"message_id":"parent-9"}},
			{"id":"m1","channel_id":"ch-1","content":"first","timestamp":"2026-04-18T11:00:00Z",
			 "author":{"id":"u1","username":"alice"},
			 "attachments":[{"id":"a1","filename":"pic.png","url":"https://cdn/x","content_type":"image/png","size":42}]}
		]`))
	}))
	defer srv.Close()
	defer mockDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t), cfg: config{AssistantName: "Ari"}}
	// leave b.files nil — FetchHistory should fall back to the raw CDN URL.

	before, _ := time.Parse(time.RFC3339, "2026-04-19T00:00:00Z")
	resp, err := b.FetchHistory(chanlib.HistoryRequest{
		ChatJID: "discord:ch-1", Before: before, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Source != "platform" {
		t.Errorf("source = %q", resp.Source)
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("got %d messages", len(resp.Messages))
	}
	if resp.Messages[0].ID != "m2" || resp.Messages[0].ReplyTo != "parent-9" {
		t.Errorf("msg[0] = %+v", resp.Messages[0])
	}
	if resp.Messages[1].ID != "m1" || len(resp.Messages[1].Attachments) != 1 {
		t.Errorf("msg[1] = %+v", resp.Messages[1])
	}
	if resp.Messages[1].Attachments[0].URL != "https://cdn/x" {
		t.Errorf("attachment url = %q (expected raw CDN when files cache nil)", resp.Messages[1].Attachments[0].URL)
	}
	if resp.Messages[1].Sender != "discord:u1" || resp.Messages[1].SenderName != "alice" {
		t.Errorf("sender = %s / %s", resp.Messages[1].Sender, resp.Messages[1].SenderName)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.HasSuffix(capturedPath, "/channels/ch-1/messages") {
		t.Errorf("path = %q", capturedPath)
	}
	if !strings.Contains(capturedQuery, "limit=50") {
		t.Errorf("query missing limit: %q", capturedQuery)
	}
	if !strings.Contains(capturedQuery, "before=") {
		t.Errorf("query missing before snowflake: %q", capturedQuery)
	}
}

func TestFetchHistory_ClampsLimit(t *testing.T) {
	var captured string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		captured = r.URL.RawQuery
		mu.Unlock()
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	defer mockDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t)}
	_, err := b.FetchHistory(chanlib.HistoryRequest{ChatJID: "discord:ch-1", Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(captured, "limit=100") {
		t.Errorf("limit not clamped to 100: %q", captured)
	}
}

func TestFetchHistory_HTTPRegistered(t *testing.T) {
	// The adapter mux should register /v1/history when the bot implements
	// HistoryProvider. Assert that the real *bot satisfies the interface.
	var _ chanlib.HistoryProvider = (*bot)(nil)
}

func os_WriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
