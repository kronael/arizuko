package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// helper: sign a body the way Slack does so handleEvents accepts it.
func signSlack(secret, body string, ts int64) (sig, tsHdr string) {
	tsHdr = strconv.FormatInt(ts, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + tsHdr + ":" + body))
	sig = "v0=" + hex.EncodeToString(mac.Sum(nil))
	return
}

func newTestServer(t *testing.T, secret string) (*server, *bot) {
	t.Helper()
	cfg := config{
		Name:          "slack",
		BotToken:      "xoxb-test",
		SigningSecret: secret,
		ChannelSecret: "chsec",
		ListenURL:     "http://slakd:9009",
		CacheTTL:      time.Minute,
	}
	b, err := newBotWithBase(cfg, "http://slack.invalid/api")
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(cfg, b, b.isConnected, b.LastInboundAt)
	s.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	b.files = s.files
	return s, b
}

// URL verification handshake — observed-failure anchor: a misrouted
// challenge during initial setup blocks operator setup entirely.
func TestEvents_URLVerification(t *testing.T) {
	s, _ := newTestServer(t, "shh")
	body := `{"type":"url_verification","challenge":"abc123"}`
	ts := int64(1_700_000_000)
	sig, tsHdr := signSlack("shh", body, ts)

	req := httptest.NewRequest("POST", "/slack/events", bytes.NewBufferString(body))
	req.Header.Set("X-Slack-Signature", sig)
	req.Header.Set("X-Slack-Request-Timestamp", tsHdr)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("code = %d", w.Code)
	}
	if w.Body.String() != "abc123" {
		t.Errorf("body = %q", w.Body.String())
	}
}

// Signing-secret skew rejection — observed-failure anchor: a replayed
// webhook older than 5 minutes must be rejected.
func TestEvents_SignatureSkew(t *testing.T) {
	s, _ := newTestServer(t, "shh")
	body := `{"type":"url_verification","challenge":"x"}`
	// Sign with ts 6 minutes ago.
	ts := int64(1_700_000_000) - 6*60
	sig, tsHdr := signSlack("shh", body, ts)
	req := httptest.NewRequest("POST", "/slack/events", bytes.NewBufferString(body))
	req.Header.Set("X-Slack-Signature", sig)
	req.Header.Set("X-Slack-Request-Timestamp", tsHdr)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("expected 401 on skew, got %d", w.Code)
	}
}

// Signing-secret mismatch rejection.
func TestEvents_SignatureMismatch(t *testing.T) {
	s, _ := newTestServer(t, "shh")
	body := `{"type":"url_verification","challenge":"x"}`
	_, tsHdr := signSlack("different", body, 1_700_000_000)
	req := httptest.NewRequest("POST", "/slack/events", bytes.NewBufferString(body))
	req.Header.Set("X-Slack-Signature", "v0=deadbeef")
	req.Header.Set("X-Slack-Request-Timestamp", tsHdr)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("expected 401 on forged sig, got %d", w.Code)
	}
}

// Missing headers also return 401 — strict, not magical.
func TestEvents_NoHeaders(t *testing.T) {
	s, _ := newTestServer(t, "shh")
	req := httptest.NewRequest("POST", "/slack/events", bytes.NewBufferString(`{}`))
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// File proxy returns 401 without ChannelSecret bearer — observed-failure
// anchor: file_shared upstream-auth (file_private requires Bearer xoxb).
func TestFileProxy_AuthRequired(t *testing.T) {
	s, _ := newTestServer(t, "shh")
	s.files.Put("https://files.slack.com/x")
	req := httptest.NewRequest("GET", "/files/anything", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("status = %d", w.Code)
	}
}

func TestFileProxy_NotFound(t *testing.T) {
	s, _ := newTestServer(t, "shh")
	req := httptest.NewRequest("GET", "/files/missing", nil)
	req.Header.Set("Authorization", "Bearer chsec")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 404 {
		t.Errorf("status = %d", w.Code)
	}
}

// File proxy must add Authorization: Bearer xoxb upstream — Slack rejects
// url_private fetches without it.
func TestFileProxy_AddsUpstreamAuth(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/pdf")
		w.Write([]byte("pdfdata"))
	}))
	defer upstream.Close()
	s, _ := newTestServer(t, "shh")
	id := s.files.Put(upstream.URL + "/private")

	req := httptest.NewRequest("GET", "/files/"+id, nil)
	req.Header.Set("Authorization", "Bearer chsec")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if gotAuth != "Bearer xoxb-test" {
		t.Errorf("upstream auth = %q (must use SLACK_BOT_TOKEN)", gotAuth)
	}
	if w.Body.String() != "pdfdata" {
		t.Errorf("body = %q", w.Body.String())
	}
}

func TestHealth_Disconnected(t *testing.T) {
	s, b := newTestServer(t, "shh")
	b.connected.Store(false)
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatalf("status = %d", w.Code)
	}
	var body map[string]any
	json.NewDecoder(w.Body).Decode(&body)
	if body["status"] != "disconnected" {
		t.Errorf("status = %v", body["status"])
	}
}

func TestHealth_OK(t *testing.T) {
	s, b := newTestServer(t, "shh")
	b.connected.Store(true)
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}
