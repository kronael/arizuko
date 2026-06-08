package routd

import (
	"encoding/json"
	"testing"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// socialDeliverer records the social/feed verb args so the REST turn-face tests
// can assert the handler routed to the right Deliverer method with returnTarget
// applied.
type socialDeliverer struct {
	fakeDeliverer

	postJID, postContent       string
	postMedia                  []string
	fwdSrc, fwdTarget, fwdComm string
	quoteJID, quoteSrc, quoteC string
	repostJID, repostSrc       string
	voiceJID, voiceText        string
}

func (d *socialDeliverer) Post(jid, content string, media []string) (string, error) {
	d.postJID, d.postContent, d.postMedia = jid, content, media
	return d.platformID, nil
}
func (d *socialDeliverer) Forward(src, target, comment string) (string, error) {
	d.fwdSrc, d.fwdTarget, d.fwdComm = src, target, comment
	return d.platformID, nil
}
func (d *socialDeliverer) Quote(jid, src, comment string) (string, error) {
	d.quoteJID, d.quoteSrc, d.quoteC = jid, src, comment
	return d.platformID, nil
}
func (d *socialDeliverer) Repost(jid, src string) (string, error) {
	d.repostJID, d.repostSrc = jid, src
	return d.platformID, nil
}
func (d *socialDeliverer) SendVoice(jid, _, _, _ string) (string, error) {
	d.voiceJID = jid
	return d.platformID, nil
}

func socialServer(t *testing.T) (*DB, *socialDeliverer, *Server) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	dl := &socialDeliverer{fakeDeliverer: fakeDeliverer{platformID: "pid-42"}}
	srv := NewServer(db, nil, dl, nil, 0, "")
	return db, dl, srv
}

func sentResult(t *testing.T, rec interface{ Bytes() []byte }) apiv1.SendResult {
	t.Helper()
	var out apiv1.SendResult
	json.Unmarshal(rec.Bytes(), &out)
	return out
}

// TestTurnPostRelay: /post hits Deliverer.Post with the jid + content, returns
// the platform id, and a closed turn 409s.
func TestTurnPostRelay(t *testing.T) {
	db, dl, srv := socialServer(t)
	db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "")
	h := srv.Handler()

	rec := doJSONKey(t, h, "POST", "/v1/turns/t1/post", "k1",
		apiv1.PostRequest{JID: "slack:T/C/U", Content: "hello world", MediaPaths: []string{"a.png"}})
	if rec.Code != 200 {
		t.Fatalf("post status=%d body=%s", rec.Code, rec.Body.String())
	}
	if out := sentResult(t, rec.Body); out.PlatformID != "pid-42" || out.Status != core.MessageStatusSent {
		t.Fatalf("post result=%+v", out)
	}
	if dl.postJID != "slack:T/C/U" || dl.postContent != "hello world" || len(dl.postMedia) != 1 {
		t.Fatalf("post args jid=%q content=%q media=%v", dl.postJID, dl.postContent, dl.postMedia)
	}

	// closed turn → 409.
	db.SetRunReturned("t1")
	rec2 := doJSONKey(t, h, "POST", "/v1/turns/t1/post", "k2",
		apiv1.PostRequest{JID: "slack:T/C/U", Content: "late"})
	if rec2.Code != 409 {
		t.Fatalf("closed-turn status=%d want 409", rec2.Code)
	}
}

// TestTurnPostReturnTarget: a delegated turn (return_to set) redirects post's
// jid to the origin chat, exactly like reply/document.
func TestTurnPostReturnTarget(t *testing.T) {
	db, dl, srv := socialServer(t)
	db.PutTurnContext("t1", "child", "", "web:child", "u1", "slack:origin/C/U")
	h := srv.Handler()

	rec := doJSONKey(t, h, "POST", "/v1/turns/t1/post", "k1",
		apiv1.PostRequest{JID: "web:child", Content: "hi"})
	if rec.Code != 200 {
		t.Fatalf("post status=%d body=%s", rec.Code, rec.Body.String())
	}
	if dl.postJID != "slack:origin/C/U" {
		t.Fatalf("returnTarget not applied: post jid=%q want slack:origin/C/U", dl.postJID)
	}
}

// TestTurnForwardRelay: /forward hits Deliverer.Forward; returnTarget applies to
// the target jid; an unknown turn 409s.
func TestTurnForwardRelay(t *testing.T) {
	db, dl, srv := socialServer(t)
	db.PutTurnContext("t1", "child", "", "web:child", "u1", "slack:origin/C/U")
	h := srv.Handler()

	rec := doJSONKey(t, h, "POST", "/v1/turns/t1/forward", "k1",
		apiv1.ForwardRequest{SourceMsgID: "m99", TargetJID: "web:child", Comment: "fyi"})
	if rec.Code != 200 {
		t.Fatalf("forward status=%d body=%s", rec.Code, rec.Body.String())
	}
	if dl.fwdSrc != "m99" || dl.fwdTarget != "slack:origin/C/U" || dl.fwdComm != "fyi" {
		t.Fatalf("forward args src=%q target=%q comment=%q", dl.fwdSrc, dl.fwdTarget, dl.fwdComm)
	}
	if out := sentResult(t, rec.Body); out.PlatformID != "pid-42" {
		t.Fatalf("forward result=%+v", out)
	}

	// unknown turn → 409.
	rec2 := doJSONKey(t, h, "POST", "/v1/turns/nope/forward", "k2",
		apiv1.ForwardRequest{SourceMsgID: "m1", TargetJID: "web:child"})
	if rec2.Code != 409 {
		t.Fatalf("unknown-turn status=%d want 409", rec2.Code)
	}
}

// TestTurnQuoteRelay: /quote hits Deliverer.Quote with returnTarget on the jid.
func TestTurnQuoteRelay(t *testing.T) {
	db, dl, srv := socialServer(t)
	db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "")
	h := srv.Handler()

	rec := doJSONKey(t, h, "POST", "/v1/turns/t1/quote", "k1",
		apiv1.QuoteRequest{JID: "slack:T/C/U", SourceMsgID: "m7", Comment: "see this"})
	if rec.Code != 200 {
		t.Fatalf("quote status=%d body=%s", rec.Code, rec.Body.String())
	}
	if dl.quoteJID != "slack:T/C/U" || dl.quoteSrc != "m7" || dl.quoteC != "see this" {
		t.Fatalf("quote args jid=%q src=%q comment=%q", dl.quoteJID, dl.quoteSrc, dl.quoteC)
	}
}

// TestTurnRepostRelay: /repost hits Deliverer.Repost with returnTarget on the jid.
func TestTurnRepostRelay(t *testing.T) {
	db, dl, srv := socialServer(t)
	db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "")
	h := srv.Handler()

	rec := doJSONKey(t, h, "POST", "/v1/turns/t1/repost", "k1",
		apiv1.RepostRequest{JID: "slack:T/C/U", SourceMsgID: "m5"})
	if rec.Code != 200 {
		t.Fatalf("repost status=%d body=%s", rec.Code, rec.Body.String())
	}
	if dl.repostJID != "slack:T/C/U" || dl.repostSrc != "m5" {
		t.Fatalf("repost args jid=%q src=%q", dl.repostJID, dl.repostSrc)
	}
}

// TestTurnSendVoiceRelay: /send_voice reuses mcpSendVoice → sendVoice. With TTS
// disabled the synthesis returns Unsupported → 422 (the relay error path); the
// jid is still resolved through returnTarget before that.
func TestTurnSendVoiceRelay(t *testing.T) {
	db, _, srv := socialServer(t)
	db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "")
	h := srv.Handler()

	rec := doJSONKey(t, h, "POST", "/v1/turns/t1/send_voice", "k1",
		apiv1.VoiceRequest{JID: "slack:T/C/U", Text: "say this"})
	// TTS off (zero ttsConfig) → sendVoice returns Unsupported → 422.
	if rec.Code != 422 {
		t.Fatalf("send_voice status=%d want 422 (tts off) body=%s", rec.Code, rec.Body.String())
	}

	// closed turn → 409 (guarded before synthesis).
	db.SetRunReturned("t1")
	rec2 := doJSONKey(t, h, "POST", "/v1/turns/t1/send_voice", "k2",
		apiv1.VoiceRequest{JID: "slack:T/C/U", Text: "late"})
	if rec2.Code != 409 {
		t.Fatalf("closed-turn status=%d want 409", rec2.Code)
	}
}

// TestTurnSocialBadRequest: missing required fields → 400 for each verb.
func TestTurnSocialBadRequest(t *testing.T) {
	db, _, srv := socialServer(t)
	db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "")
	h := srv.Handler()
	cases := []struct {
		path string
		body any
	}{
		{"/v1/turns/t1/post", apiv1.PostRequest{Content: "x"}},
		{"/v1/turns/t1/forward", apiv1.ForwardRequest{SourceMsgID: "m1"}},
		{"/v1/turns/t1/quote", apiv1.QuoteRequest{JID: "j"}},
		{"/v1/turns/t1/repost", apiv1.RepostRequest{JID: "j"}},
		{"/v1/turns/t1/send_voice", apiv1.VoiceRequest{JID: "j"}},
	}
	for _, c := range cases {
		rec := doJSONKey(t, h, "POST", c.path, "", c.body)
		if rec.Code != 400 {
			t.Errorf("%s missing-field status=%d want 400", c.path, rec.Code)
		}
	}
}
