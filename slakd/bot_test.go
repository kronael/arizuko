package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
)

func TestParseJID(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantErr   bool
		workspace string
		kind      string
		id        string
	}{
		{"channel", "slack:T012/channel/C0HJK", false, "T012", "channel", "C0HJK"},
		{"dm", "slack:T012/dm/D0XY", false, "T012", "dm", "D0XY"},
		{"group_mpim", "slack:T012/group/G123", false, "T012", "group", "G123"},
		{"missing_prefix", "discord:T/channel/C", true, "", "", ""},
		{"missing_kind_seg", "slack:T012", true, "", "", ""},
		{"missing_id", "slack:T012/channel", true, "", "", ""},
		{"empty_id", "slack:T012/channel/", true, "", "", ""},
		{"bad_kind", "slack:T012/private/C0", true, "", "", ""},
		{"empty_workspace", "slack:/channel/C0", true, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseJID(c.in)
			if c.wantErr {
				if err == nil {
					t.Errorf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got.Workspace != c.workspace || got.Kind != c.kind || got.ID != c.id {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestFormatJID(t *testing.T) {
	got := chanlib.FormatSlackJID("T012", "channel", "C0HJK")
	if got != "slack:T012/channel/C0HJK" {
		t.Errorf("got %q", got)
	}
}

func TestChanKind(t *testing.T) {
	if k := chanKind(true, false); k != "dm" {
		t.Errorf("im → %q", k)
	}
	if k := chanKind(false, true); k != "group" {
		t.Errorf("mpim → %q", k)
	}
	if k := chanKind(false, false); k != "channel" {
		t.Errorf("regular → %q", k)
	}
}

func TestParseSlackTS(t *testing.T) {
	if got := parseSlackTS("1700000000.000200"); got != 1700000000 {
		t.Errorf("got %d", got)
	}
	if got := parseSlackTS("1700000000"); got != 1700000000 {
		t.Errorf("got %d", got)
	}
	if parseSlackTS("") == 0 {
		t.Error("empty TS should fall back to now, not 0")
	}
}

// Signature must accept a body signed within the window.
func TestVerifySignature_Good(t *testing.T) {
	secret := "shh"
	body := []byte(`{"type":"url_verification","challenge":"x"}`)
	ts := int64(1_700_000_000)
	tsHdr := strconv.FormatInt(ts, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + tsHdr + ":" + string(body)))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if err := verifySignature(secret, sig, tsHdr, body, time.Unix(ts+10, 0)); err != nil {
		t.Errorf("want nil, got %v", err)
	}
}

// Empty secret → strict refusal (no fallback to "any signature").
func TestVerifySignature_NoSecret(t *testing.T) {
	if err := verifySignature("", "v0=x", "1", []byte(`{}`), time.Now()); err == nil {
		t.Error("missing secret must error")
	}
}

// Bad timestamp string.
func TestVerifySignature_BadTS(t *testing.T) {
	if err := verifySignature("shh", "v0=x", "not-a-number", []byte(`{}`), time.Now()); err == nil {
		t.Error("non-numeric ts must error")
	}
}

// Signing window: a request exactly at the 300s boundary is accepted;
// one second past is rejected.
func TestVerifySignature_WindowBoundary(t *testing.T) {
	secret := "shh"
	body := []byte(`{}`)
	ts := int64(1_700_000_000)
	tsHdr := strconv.FormatInt(ts, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + tsHdr + ":" + string(body)))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	// exactly 300s skew → accept.
	if err := verifySignature(secret, sig, tsHdr, body, time.Unix(ts+300, 0)); err != nil {
		t.Errorf("300s boundary should be accepted, got %v", err)
	}
	// 301s skew → reject.
	if err := verifySignature(secret, sig, tsHdr, body, time.Unix(ts+301, 0)); err == nil {
		t.Error("301s past boundary must be rejected")
	}
}

// thread_broadcast is a display copy of a thread reply Slack also surfaces
// in the parent channel — the original reply already arrived as a regular
// message, so the broadcast must be dropped to avoid double delivery.
func TestDispatch_ThreadBroadcastDropped(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, rm := setupBot(t, mock)

	body := []byte(`{
	  "type": "event_callback",
	  "team_id": "T012",
	  "event": {
	    "type": "message",
	    "subtype": "thread_broadcast",
	    "channel_type": "channel",
	    "channel": "C0HJK",
	    "user": "U99",
	    "text": "broadcast",
	    "ts": "1700000999.000100",
	    "thread_ts": "1700000222.000100"
	  }
	}`)
	b.handleEvent(body, httptest.NewRecorder())

	if got := rm.snapshot(); len(got) != 0 {
		t.Errorf("thread_broadcast must be dropped, got %d msgs: %+v", len(got), got)
	}
}

// app_mention is sent by Slack when the bot is @mentioned in a channel.
// It has the same payload shape as a message event and must be delivered
// with verb=mention so routing rules like "verb=mention|atlas" fire.
// Slack sends file uploads as message subtype file_share; they must reach
// handleMessage so attachmentsFor can extract the download URL.
func TestDispatch_FileShareDelivered(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, rm := setupBot(t, mock)

	body := []byte(`{
	  "type": "event_callback",
	  "team_id": "T012",
	  "event": {
	    "type": "message",
	    "subtype": "file_share",
	    "channel_type": "channel",
	    "channel": "C0HJK",
	    "user": "U99",
	    "text": "here is the file",
	    "ts": "1700001300.000100",
	    "files": [{"name":"doc.pdf","mimetype":"application/pdf","url_private":"https://files.slack.com/doc.pdf","size":1024}]
	  }
	}`)
	b.handleEvent(body, httptest.NewRecorder())

	msgs := rm.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("file_share must deliver 1 msg, got %d", len(msgs))
	}
}

func TestDispatch_AppMentionDeliveredAsMention(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, rm := setupBot(t, mock)

	body := []byte(`{
	  "type": "event_callback",
	  "team_id": "T012",
	  "event": {
	    "type": "app_mention",
	    "channel_type": "channel",
	    "channel": "C0HJK",
	    "user": "U99",
	    "text": "<@Ubot> what is limiting these validators?",
	    "ts": "1700001234.000100"
	  }
	}`)
	b.handleEvent(body, httptest.NewRecorder())

	msgs := rm.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("app_mention must deliver 1 msg, got %d", len(msgs))
	}
	if msgs[0].Verb != "mention" {
		t.Errorf("verb = %q, want mention", msgs[0].Verb)
	}
}

// DM messages are direct — there's no need for an @mention to address the
// bot, so Verb must stay empty even when the text contains <@BOTID>.
func TestInbound_DMNeverMentionVerb(t *testing.T) {
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
	    "text": "hey <@Ubot> in dm",
	    "ts": "1700001111.000100"
	  }
	}`)
	b.handleEvent(body, httptest.NewRecorder())

	msgs := rm.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("got %d msgs", len(msgs))
	}
	if msgs[0].Verb == "mention" {
		t.Errorf("DM must not carry verb=mention even with <@BOTID> in text")
	}
}

// A message inside a thread must carry ReplyTo = thread root, so the spec
// 5/L reply-to-bot promotion can fire when the root is the bot's own message
// (atlas auto-attends a thread it started without a re-@mention).
func TestInbound_ThreadReplySetsReplyTo(t *testing.T) {
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
	    "text": "follow-up in the thread",
	    "ts": "1700002222.000200",
	    "thread_ts": "1700002000.000100"
	  }
	}`)
	b.handleEvent(body, httptest.NewRecorder())

	msgs := rm.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("got %d msgs", len(msgs))
	}
	if msgs[0].ReplyTo != "1700002000.000100" {
		t.Errorf("in-thread message must set ReplyTo=thread_ts, got %q", msgs[0].ReplyTo)
	}
	if msgs[0].Topic != "1700002000.000100" {
		t.Errorf("in-thread message must set Topic=thread_ts, got %q", msgs[0].Topic)
	}
}

// handleAssistantThreadStarted must not panic when b.store is nil — pane
// persistence + context propagation just no-op in that mode.
func TestAssistantThreadStarted_NilStoreNoPanic(t *testing.T) {
	mock := newSlackMock()
	defer mock.Close()
	b, _ := setupBot(t, mock)
	b.store = nil

	raw, _ := json.Marshal(map[string]any{
		"assistant_thread": map[string]any{
			"user_id":    "U99",
			"channel_id": "D0XY",
			"thread_ts":  "1700001234.000100",
			"context":    map[string]any{"channel_id": "C42", "team_id": "T012"},
		},
	})
	// Must return cleanly. If it panics, the test fails with a stack.
	b.handleAssistantThreadStarted("T012", raw)
}

func TestTTLCache(t *testing.T) {
	c := newTTLCache(50 * time.Millisecond)
	c.put("k", "v")
	got, ok := c.get("k")
	if !ok || got != "v" {
		t.Fatalf("get miss")
	}
	time.Sleep(70 * time.Millisecond)
	if _, ok := c.get("k"); ok {
		t.Error("expected TTL eviction")
	}
}

func TestToMrkdwn(t *testing.T) {
	cases := []struct{ in, want string }{
		{"**bold** text", "*bold* text"},
		{"__also bold__", "*also bold*"},
		{"see [the docs](https://x.com/y)", "see <https://x.com/y|the docs>"},
		{"plain *italic* stays", "plain *italic* stays"},
		{"code `**not bold**` kept", "code `**not bold**` kept"},
		{"```\n**fenced** kept\n```", "```\n**fenced** kept\n```"},
		{"no markup here", "no markup here"},
	}
	for _, c := range cases {
		if got := toMrkdwn(c.in); got != c.want {
			t.Errorf("toMrkdwn(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
