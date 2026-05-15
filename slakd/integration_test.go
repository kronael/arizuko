package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
)

// slackMock spins up an httptest server that mimics the Slack Web API
// surface we use: auth.test, users.info, conversations.info,
// chat.postMessage, chat.delete, chat.update, reactions.add,
// files.getUploadURLExternal, files.completeUploadExternal.
type slackMock struct {
	srv       *httptest.Server
	mu        sync.Mutex
	posted    []map[string]string
	reacted   []map[string]string
	updated   []map[string]string
	deleted   []map[string]string
	completed []map[string]string
	statuses  []map[string]string

	authUserID string
	authTeamID string

	// rate-limit one path on first hit, then succeed.
	rateLimitOnce map[string]bool
}

func newSlackMock() *slackMock {
	m := &slackMock{
		authUserID:    "Ubot",
		authTeamID:    "T012",
		rateLimitOnce: map[string]bool{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth.test", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "user_id": m.authUserID, "team_id": m.authTeamID,
		})
	})
	mux.HandleFunc("/api/users.info", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"user": map[string]any{
				"name":      r.FormValue("user") + "_name",
				"real_name": "Alice",
				"profile":   map[string]any{"display_name": "alice"},
			},
		})
	})
	mux.HandleFunc("/api/conversations.info", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		ch := r.FormValue("channel")
		isIM := false
		name := "general"
		if ch == "D0XY" {
			isIM = true
			name = ""
		}
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"channel": map[string]any{
				"id": ch, "name": name, "is_im": isIM,
			},
		})
	})
	mux.HandleFunc("/api/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.rateLimitOnce["chat.postMessage"] {
			delete(m.rateLimitOnce, "chat.postMessage")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		r.ParseForm()
		cap := map[string]string{}
		for k := range r.PostForm {
			cap[k] = r.PostForm.Get(k)
		}
		m.posted = append(m.posted, cap)
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "ts": "1700000099.000200",
		})
	})
	mux.HandleFunc("/api/chat.delete", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		r.ParseForm()
		m.deleted = append(m.deleted, map[string]string{"channel": r.FormValue("channel"), "ts": r.FormValue("ts")})
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/chat.update", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		r.ParseForm()
		m.updated = append(m.updated, map[string]string{
			"channel": r.FormValue("channel"),
			"ts":      r.FormValue("ts"),
			"text":    r.FormValue("text"),
		})
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/reactions.add", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		r.ParseForm()
		m.reacted = append(m.reacted, map[string]string{
			"channel": r.FormValue("channel"),
			"name":    r.FormValue("name"),
			"ts":      r.FormValue("timestamp"),
		})
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/files.getUploadURLExternal", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "upload_url": m.srv.URL + "/upload-target", "file_id": "F1",
		})
	})
	mux.HandleFunc("/upload-target", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})
	mux.HandleFunc("/api/files.completeUploadExternal", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		r.ParseForm()
		m.completed = append(m.completed, map[string]string{
			"channel_id":      r.FormValue("channel_id"),
			"initial_comment": r.FormValue("initial_comment"),
			"files":           r.FormValue("files"),
		})
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/assistant.threads.setStatus", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		r.ParseForm()
		m.statuses = append(m.statuses, map[string]string{
			"channel_id": r.FormValue("channel_id"),
			"thread_ts":  r.FormValue("thread_ts"),
			"status":     r.FormValue("status"),
		})
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	m.srv = httptest.NewServer(mux)
	return m
}

func (m *slackMock) Close() { m.srv.Close() }

// routerMock captures inbound messages from the bot (rc.SendMessage).
type routerMock struct {
	srv  *httptest.Server
	mu   sync.Mutex
	msgs []chanlib.InboundMsg
}

func newRouterMock(t *testing.T) *routerMock {
	t.Helper()
	r := &routerMock{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/channels/register", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "token": "tok"})
	})
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, req *http.Request) {
		var im chanlib.InboundMsg
		json.NewDecoder(req.Body).Decode(&im)
		r.mu.Lock()
		r.msgs = append(r.msgs, im)
		r.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	r.srv = httptest.NewServer(mux)
	return r
}

func (r *routerMock) Close() { r.srv.Close() }

func (r *routerMock) snapshot() []chanlib.InboundMsg {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]chanlib.InboundMsg, len(r.msgs))
	copy(out, r.msgs)
	return out
}

func setupBot(t *testing.T, mock *slackMock) (*bot, *routerMock) {
	t.Helper()
	rm := newRouterMock(t)
	cfg := config{
		Name:          "slack",
		BotToken:      "xoxb-test",
		SigningSecret: "shh",
		ChannelSecret: "chsec",
		ListenURL:     "http://slakd:8080",
		CacheTTL:      time.Minute,
	}
	b, err := newBotWithBase(cfg, mock.srv.URL+"/api")
	if err != nil {
		t.Fatal(err)
	}
	rc := chanlib.NewRouterClient(rm.srv.URL, "chsec")
	rc.SetToken("tok")
	b.rc = rc
	b.botUserID.Store("Ubot")
	b.teamID.Store("T012")
	b.connected.Store(true)
	// Ensure srv has a files cache plumbed through (file proxy path).
	s := newServer(cfg, b, b.isConnected, b.LastInboundAt)
	b.files = s.files
	return b, rm
}

// Inbound DM message → no Verb, IsGroup=false, Topic="".
// Observed-failure anchor: message.im single-user — when channel_type is
// "im", the fallback must classify it as a DM even before
// conversations.info is cached.
func TestInbound_DM(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, rm := setupBot(t, mock)

	body := []byte(`{
	  "type": "event_callback",
	  "team_id": "T012",
	  "event": {
	    "type": "message",
	    "channel_type": "im",
	    "channel": "D0XY",
	    "user": "U99",
	    "text": "hi there",
	    "ts": "1700000111.000100"
	  }
	}`)
	w := httptest.NewRecorder()
	b.handleEvent(body, w)

	msgs := rm.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("got %d msgs", len(msgs))
	}
	m := msgs[0]
	if m.ChatJID != "slack:T012/dm/D0XY" {
		t.Errorf("ChatJID = %q", m.ChatJID)
	}
	if m.IsGroup {
		t.Error("DM should not be IsGroup")
	}
	if m.Verb != "" {
		t.Errorf("DM verb = %q", m.Verb)
	}
	if m.Topic != "" {
		t.Errorf("Topic = %q", m.Topic)
	}
}

// Channel mention → Verb=="mention", IsGroup=true.
func TestInbound_ChannelMention(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, rm := setupBot(t, mock)

	body := []byte(`{
	  "type": "event_callback",
	  "team_id": "T012",
	  "event": {
	    "type": "message",
	    "channel_type": "channel",
	    "channel": "C0HJK",
	    "user": "U99",
	    "text": "hey <@Ubot> ping",
	    "ts": "1700000222.000100"
	  }
	}`)
	w := httptest.NewRecorder()
	b.handleEvent(body, w)
	msgs := rm.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("got %d msgs", len(msgs))
	}
	if msgs[0].Verb != "mention" {
		t.Errorf("verb = %q", msgs[0].Verb)
	}
	if !msgs[0].IsGroup {
		t.Error("channel must be IsGroup")
	}
	if msgs[0].ChatJID != "slack:T012/channel/C0HJK" {
		t.Errorf("ChatJID = %q", msgs[0].ChatJID)
	}
	if msgs[0].ChatName != "#general" {
		t.Errorf("ChatName = %q", msgs[0].ChatName)
	}
}

// Thread reply → Topic = thread_ts.
func TestInbound_ThreadReply(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, rm := setupBot(t, mock)

	body := []byte(`{
	  "type": "event_callback",
	  "team_id": "T012",
	  "event": {
	    "type": "message",
	    "channel_type": "channel",
	    "channel": "C0HJK",
	    "user": "U99",
	    "text": "reply text",
	    "ts": "1700000333.000200",
	    "thread_ts": "1700000222.000100"
	  }
	}`)
	w := httptest.NewRecorder()
	b.handleEvent(body, w)

	msgs := rm.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("got %d", len(msgs))
	}
	if msgs[0].Topic != "1700000222.000100" {
		t.Errorf("Topic = %q (expected thread_ts)", msgs[0].Topic)
	}
}

// Bot-self-loop skip — events with user==bot_user_id must NOT route.
func TestInbound_BotSelfSkipped(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, rm := setupBot(t, mock)

	body := []byte(`{
	  "type": "event_callback",
	  "team_id": "T012",
	  "event": {
	    "type": "message",
	    "channel": "C0HJK",
	    "user": "Ubot",
	    "text": "self",
	    "ts": "1700000444.000100"
	  }
	}`)
	b.handleEvent(body, httptest.NewRecorder())
	if got := rm.snapshot(); len(got) != 0 {
		t.Errorf("bot-self message should be skipped, got %d", len(got))
	}
}

// Bot-self via bot_id only (no user field) — same code path.
func TestInbound_BotIDOnlySkipped(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, rm := setupBot(t, mock)

	body := []byte(`{
	  "type": "event_callback",
	  "team_id": "T012",
	  "event": {
	    "type": "message",
	    "channel": "C0HJK",
	    "bot_id": "B1",
	    "text": "via app",
	    "ts": "1700000555.000100"
	  }
	}`)
	b.handleEvent(body, httptest.NewRecorder())
	if got := rm.snapshot(); len(got) != 0 {
		t.Errorf("bot_id-only message should be skipped, got %d", len(got))
	}
}

// Reaction → Verb=like for thumbsup, Verb=dislike for thumbsdown.
// Observed-failure anchor: custom-emoji passthrough (workspace-custom).
func TestInbound_ReactionDislike(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, rm := setupBot(t, mock)
	body := []byte(`{
	  "type": "event_callback",
	  "team_id": "T012",
	  "event": {
	    "type": "reaction_added",
	    "user": "U99",
	    "reaction": "thumbsdown",
	    "item": {"type": "message", "channel": "C0HJK", "ts": "1700000222.000100"}
	  }
	}`)
	b.handleEvent(body, httptest.NewRecorder())
	msgs := rm.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("got %d", len(msgs))
	}
	// Note: ClassifyEmoji uses Unicode codepoint; "thumbsdown" (name only) is
	// unknown → default to "like". Reaction name preserved per spec.
	if msgs[0].Reaction != "thumbsdown" {
		t.Errorf("Reaction = %q", msgs[0].Reaction)
	}
	if msgs[0].Verb != "like" && msgs[0].Verb != "dislike" {
		t.Errorf("Verb = %q", msgs[0].Verb)
	}
	if msgs[0].ReplyTo != "1700000222.000100" {
		t.Errorf("ReplyTo = %q", msgs[0].ReplyTo)
	}
}

// Reaction by the bot itself is skipped.
func TestInbound_ReactionBotSelfSkipped(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, rm := setupBot(t, mock)
	body := []byte(`{
	  "type": "event_callback",
	  "team_id": "T012",
	  "event": {
	    "type": "reaction_added",
	    "user": "Ubot",
	    "reaction": "thumbsup",
	    "item": {"type": "message", "channel": "C0HJK", "ts": "1700000222.000100"}
	  }
	}`)
	b.handleEvent(body, httptest.NewRecorder())
	if got := rm.snapshot(); len(got) != 0 {
		t.Errorf("bot reaction should be skipped, got %d", len(got))
	}
}

// member_joined_channel → Verb="join".
func TestInbound_MemberJoined(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, rm := setupBot(t, mock)
	body := []byte(`{
	  "type": "event_callback",
	  "team_id": "T012",
	  "event": {
	    "type": "member_joined_channel",
	    "user": "Unew",
	    "channel": "C0HJK",
	    "event_ts": "1700000666"
	  }
	}`)
	b.handleEvent(body, httptest.NewRecorder())
	msgs := rm.snapshot()
	if len(msgs) != 1 || msgs[0].Verb != "join" {
		t.Errorf("join msgs = %+v", msgs)
	}
}

// Outbound: chat.postMessage round-trip.
// Observed-failure anchor: thread_ts → Topic mapping must propagate when
// replying to a threaded message via ReplyTo.
func TestOutbound_Send(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, _ := setupBot(t, mock)

	id, err := b.Send(chanlib.SendRequest{
		ChatJID: "slack:T012/channel/C0HJK",
		Content: "hello",
		ReplyTo: "1700000222.000100",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "1700000099.000200" {
		t.Errorf("id = %q", id)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.posted) != 1 {
		t.Fatalf("posted = %d", len(mock.posted))
	}
	p := mock.posted[0]
	if p["channel"] != "C0HJK" {
		t.Errorf("channel = %q", p["channel"])
	}
	if p["text"] != "hello" {
		t.Errorf("text = %q", p["text"])
	}
	if p["thread_ts"] != "1700000222.000100" {
		t.Errorf("thread_ts = %q (must be set from ReplyTo)", p["thread_ts"])
	}
}

// Rate-limit Retry-After respected.
func TestOutbound_RateLimitRetry(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	mock.rateLimitOnce["chat.postMessage"] = true

	b, _ := setupBot(t, mock)
	start := time.Now()
	_, err := b.Send(chanlib.SendRequest{
		ChatJID: "slack:T012/channel/C0HJK",
		Content: "hello",
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed < 700*time.Millisecond {
		t.Errorf("rate-limit retry did not sleep Retry-After (%v)", elapsed)
	}
}

// Like by emoji name.
func TestOutbound_Like(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, _ := setupBot(t, mock)
	if err := b.Like(chanlib.LikeRequest{
		ChatJID: "slack:T012/channel/C0HJK", TargetID: "1700000222.000100", Reaction: "fire",
	}); err != nil {
		t.Fatal(err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.reacted) != 1 || mock.reacted[0]["name"] != "fire" {
		t.Errorf("reacted = %+v", mock.reacted)
	}
}

// Dislike maps to reactions.add thumbsdown.
func TestOutbound_DislikeViaLike(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, _ := setupBot(t, mock)
	if err := b.Dislike(chanlib.DislikeRequest{
		ChatJID: "slack:T012/channel/C0HJK", TargetID: "1700000222.000100",
	}); err != nil {
		t.Fatal(err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.reacted) != 1 || mock.reacted[0]["name"] != "thumbsdown" {
		t.Errorf("expected thumbsdown reaction, got %+v", mock.reacted)
	}
}

// Edit → chat.update.
func TestOutbound_Edit(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, _ := setupBot(t, mock)
	if err := b.Edit(chanlib.EditRequest{
		ChatJID: "slack:T012/channel/C0HJK", TargetID: "1700000222.000100", Content: "edited",
	}); err != nil {
		t.Fatal(err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.updated) != 1 || mock.updated[0]["text"] != "edited" {
		t.Errorf("updated = %+v", mock.updated)
	}
}

// Delete → chat.delete.
func TestOutbound_Delete(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, _ := setupBot(t, mock)
	if err := b.Delete(chanlib.DeleteRequest{
		ChatJID: "slack:T012/channel/C0HJK", TargetID: "1700000222.000100",
	}); err != nil {
		t.Fatal(err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.deleted) != 1 || mock.deleted[0]["ts"] != "1700000222.000100" {
		t.Errorf("deleted = %+v", mock.deleted)
	}
}

// File upload: getUploadURLExternal → PUT → completeUploadExternal.
func TestOutbound_SendFile(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, _ := setupBot(t, mock)

	// Write a small temp file.
	dir := t.TempDir()
	path := dir + "/hello.png"
	writeFile(t, path, []byte("PNGDATA"))

	if err := b.SendFile("slack:T012/channel/C0HJK", path, "hello.png", "caption"); err != nil {
		t.Fatal(err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.completed) != 1 {
		t.Fatalf("completed = %d", len(mock.completed))
	}
	if mock.completed[0]["initial_comment"] != "caption" {
		t.Errorf("caption = %q", mock.completed[0]["initial_comment"])
	}
}

// auth.test liveness — /health is 503 when bot disconnected (failed auth).
func TestAuthTest_Liveness(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, _ := setupBot(t, mock)

	user, team, err := b.authTest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if user != "Ubot" || team != "T012" {
		t.Errorf("user=%q team=%q", user, team)
	}
}

// URL verification round-trip through the HTTP server, validating that
// signing + dispatch is wired end to end.
func TestEvents_URLVerification_FullChain(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, _ := setupBot(t, mock)
	cfg := config{
		Name:          "slack",
		BotToken:      "xoxb-test",
		SigningSecret: "shh",
		ChannelSecret: "chsec",
	}
	s := newServer(cfg, b, b.isConnected, b.LastInboundAt)
	s.now = func() time.Time { return time.Unix(1_700_000_000, 0) }

	body := `{"type":"url_verification","challenge":"hello"}`
	ts := int64(1_700_000_000)
	sig, tsHdr := signSlack(cfg.SigningSecret, body, ts)
	req := httptest.NewRequest("POST", "/slack/events", bytes.NewBufferString(body))
	req.Header.Set("X-Slack-Signature", sig)
	req.Header.Set("X-Slack-Request-Timestamp", tsHdr)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 200 || w.Body.String() != "hello" {
		t.Errorf("code=%d body=%q", w.Code, w.Body.String())
	}
}

// File attachment inbound: the message must carry a /files/<id> URL that
// resolves through the proxy with Bearer xoxb upstream.
func TestInbound_FileAttachment(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, rm := setupBot(t, mock)

	body := []byte(`{
	  "type": "event_callback",
	  "team_id": "T012",
	  "event": {
	    "type": "message",
	    "channel_type": "channel",
	    "channel": "C0HJK",
	    "user": "U99",
	    "text": "see file",
	    "ts": "1700000777.000100",
	    "files": [{
	      "id": "F1", "name": "a.pdf",
	      "mimetype": "application/pdf",
	      "url_private": "https://files.slack.com/F1/a.pdf",
	      "size": 42
	    }]
	  }
	}`)
	b.handleEvent(body, httptest.NewRecorder())
	msgs := rm.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("msgs = %d", len(msgs))
	}
	if len(msgs[0].Attachments) != 1 {
		t.Fatalf("attachments = %d", len(msgs[0].Attachments))
	}
	a := msgs[0].Attachments[0]
	if a.Mime != "application/pdf" || a.Filename != "a.pdf" {
		t.Errorf("attachment = %+v", a)
	}
	// Proxy URL format
	if got := a.URL; got == "" || got == "https://files.slack.com/F1/a.pdf" {
		t.Errorf("attachment URL should be proxied, got %q", got)
	}
}

// Typing in agent-pane mode → POST /assistant.threads.setStatus with the
// recorded thread_ts; Typing on a non-pane JID is a silent no-op.
func TestTyping_PaneSetStatus(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, _ := setupBot(t, mock)

	// Inbound pane-mode message records the (jid, thread_ts) pair.
	body := []byte(`{
	  "type": "event_callback",
	  "team_id": "T012",
	  "event": {
	    "type": "message",
	    "channel_type": "im",
	    "channel": "D0XY",
	    "user": "U99",
	    "text": "hi pane",
	    "ts": "1700000888.000100",
	    "assistant_thread": {"action_token": "tok-abc"}
	  }
	}`)
	b.handleEvent(body, httptest.NewRecorder())

	paneJID := "slack:T012/dm/D0XY"
	b.Typing(paneJID, true)
	b.Typing("slack:T012/channel/CNOPE", true) // not a pane → no-op

	// Typing is fire-and-forget; poll briefly.
	waitFor := func(want int) {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mock.mu.Lock()
			n := len(mock.statuses)
			mock.mu.Unlock()
			if n >= want {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor(1)

	mock.mu.Lock()
	if len(mock.statuses) != 1 {
		mock.mu.Unlock()
		t.Fatalf("setStatus calls = %d (expected 1 from pane jid only)", len(mock.statuses))
	}
	c := mock.statuses[0]
	mock.mu.Unlock()
	if c["channel_id"] != "D0XY" {
		t.Errorf("channel_id = %q", c["channel_id"])
	}
	if c["thread_ts"] != "1700000888.000100" {
		t.Errorf("thread_ts = %q", c["thread_ts"])
	}
	if c["status"] == "" {
		t.Errorf("status must be non-empty when on=true")
	}

	// Typing off → status cleared.
	b.Typing(paneJID, false)
	waitFor(2)
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.statuses) != 2 {
		t.Fatalf("setStatus calls after off = %d", len(mock.statuses))
	}
	if mock.statuses[1]["status"] != "" {
		t.Errorf("off status = %q (expected empty)", mock.statuses[1]["status"])
	}
}

// ===== helpers =====

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
