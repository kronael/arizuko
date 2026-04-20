package main

import (
	"net"
	"net/smtp"
	"strings"
	"testing"

	"github.com/onvos/arizuko/chanlib"
)

// TestBotHandler_Send exercises Send through the thread-lookup path.
// Happy-path SMTP (STARTTLS) stubbing requires real TLS certs and a full
// SMTP state machine; see bugs.md "emaid SMTP integration test". Instead
// this test proves:
//   - thread lookup succeeds when the row exists
//   - SMTP dial is attempted against cfg.SMTPHost:cfg.SMTPPort
//   - the caller gets a wrapped "smtp dial" error on connection failure
func TestBotHandler_Send(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	_, err := db.Exec(
		`INSERT INTO email_threads (thread_id, from_address, root_msg_id)
		 VALUES (?, ?, ?)`,
		"thread-1", "user@example.com", "root-msg-id",
	)
	if err != nil {
		t.Fatalf("seed thread: %v", err)
	}

	// Bind an unused local port and close it immediately so smtp.Dial fails
	// deterministically.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().(*net.TCPAddr)
	l.Close()

	cfg := config{
		Name:     "email",
		Account:  "bot@example.com",
		Password: "pw",
		SMTPHost: "127.0.0.1",
		SMTPPort: itoa(addr.Port),
	}
	s := newServer(cfg, db, newAttRegistry())

	_, err = s.Send(chanlib.SendRequest{ChatJID: "email:thread-1", Content: "hello"})
	if err == nil {
		t.Fatal("expected SMTP dial error (listener was closed)")
	}
	if !strings.Contains(err.Error(), "smtp dial") {
		t.Errorf("unexpected error shape: %v", err)
	}
}

// TestBotHandler_Send_ThreadNotFound asserts the missing-thread branch returns
// a clear error before any network activity.
func TestBotHandler_Send_ThreadNotFound(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	s := newServer(config{Name: "email"}, db, newAttRegistry())
	_, err := s.Send(chanlib.SendRequest{ChatJID: "email:missing", Content: "x"})
	if err == nil || !strings.Contains(err.Error(), "thread not found") {
		t.Errorf("err = %v, want 'thread not found'", err)
	}
}

// TestBotHandler_Send_Success stubs smtpSender to prove the happy path:
// thread lookup → RFC822 assembly → handler returns success. Verifies the
// assembled message carries the expected headers (From/To/Subject/Date/
// In-Reply-To/References) and body, and that envelope addresses + auth
// credentials are forwarded intact.
func TestBotHandler_Send_Success(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	_, err := db.Exec(
		`INSERT INTO email_threads (thread_id, from_address, root_msg_id)
		 VALUES (?, ?, ?)`,
		"thread-ok", "user@example.com", "root-123@example.com",
	)
	if err != nil {
		t.Fatalf("seed thread: %v", err)
	}

	var gotAddr, gotFrom string
	var gotTo []string
	var gotMsg []byte
	var gotAuth smtp.Auth
	orig := smtpSender
	smtpSender = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		gotAddr = addr
		gotAuth = a
		gotFrom = from
		gotTo = to
		gotMsg = msg
		return nil
	}
	defer func() { smtpSender = orig }()

	cfg := config{
		Name:     "email",
		Account:  "bot@example.com",
		Password: "pw",
		SMTPHost: "smtp.example.com",
		SMTPPort: "587",
	}
	s := newServer(cfg, db, newAttRegistry())

	resp, err := s.Send(chanlib.SendRequest{ChatJID: "email:thread-ok", Content: "hello body"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp != "" {
		t.Errorf("resp = %q, want empty", resp)
	}

	if gotAddr != "smtp.example.com:587" {
		t.Errorf("addr = %q", gotAddr)
	}
	if gotFrom != "bot@example.com" {
		t.Errorf("from = %q", gotFrom)
	}
	if len(gotTo) != 1 || gotTo[0] != "user@example.com" {
		t.Errorf("to = %v", gotTo)
	}
	if gotAuth == nil {
		t.Errorf("auth not forwarded")
	}
	// PlainAuth exposes credentials via the Start handshake — drive it.
	proto, resp0, err := gotAuth.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: true, Auth: []string{"PLAIN"}})
	if err != nil {
		t.Fatalf("auth start: %v", err)
	}
	if proto != "PLAIN" {
		t.Errorf("auth proto = %q", proto)
	}
	if !strings.Contains(string(resp0), "bot@example.com") || !strings.Contains(string(resp0), "pw") {
		t.Errorf("auth payload missing creds: %q", resp0)
	}

	m := string(gotMsg)
	for _, want := range []string{
		"From: bot@example.com\r\n",
		"To: user@example.com\r\n",
		"Subject: Re: (arizuko)\r\n",
		"Date: ",
		"In-Reply-To: <root-123@example.com>\r\n",
		"References: <root-123@example.com>\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
		"\r\nhello body",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("message missing %q; got:\n%s", want, m)
		}
	}
}

func itoa(i int) string {
	// strconv.Itoa avoided to keep the import list minimal in a test helper.
	const d = "0123456789"
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{d[i%10]}, b...)
		i /= 10
	}
	return string(b)
}
