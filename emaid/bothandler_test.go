package main

import (
	"net"
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
