package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/onvos/arizuko/chanlib"
)

// threadIDFromMsgID mirrors the inline logic in handleMsg.
func threadIDFromMsgID(rootMsgID string) string {
	h := sha256.Sum256([]byte(rootMsgID))
	return fmt.Sprintf("%x", h[:6])
}

func TestThreadID(t *testing.T) {
	id1 := threadIDFromMsgID("msg-abc@example.com")
	id2 := threadIDFromMsgID("msg-abc@example.com")
	if id1 != id2 {
		t.Errorf("same input → different ID: %q vs %q", id1, id2)
	}
	if len(id1) != 12 {
		t.Errorf("thread ID len = %d, want 12", len(id1))
	}

	// different root → different ID
	other := threadIDFromMsgID("msg-xyz@example.com")
	if id1 == other {
		t.Errorf("different inputs → same ID: %q", id1)
	}
}

func TestExtractPlainText(t *testing.T) {
	// plain text MIME message
	raw := "Content-Type: text/plain\r\n\r\nhello world"
	got := extractPlainText(strings.NewReader(raw))
	if !strings.Contains(got, "hello world") {
		t.Errorf("plain text extraction: got %q", got)
	}
}

func TestExtractPlainText_MultipartPreferPlain(t *testing.T) {
	body := "--boundary\r\n" +
		"Content-Type: text/plain\r\n\r\nplain body\r\n" +
		"--boundary\r\n" +
		"Content-Type: text/html\r\n\r\n<b>html body</b>\r\n" +
		"--boundary--\r\n"
	mime := "Content-Type: multipart/alternative; boundary=boundary\r\n\r\n" + body
	got := extractPlainText(strings.NewReader(mime))
	if !strings.Contains(got, "plain body") {
		t.Errorf("multipart: got %q, want plain body", got)
	}
}

func TestExtractPlainText_Empty(t *testing.T) {
	// empty reader — no MIME headers, falls back to raw read
	got := extractPlainText(strings.NewReader(""))
	if got != "" {
		t.Errorf("empty reader: got %q", got)
	}
}

// TestExtractContent_WithAttachment verifies attachment metadata extraction from multipart MIME.
func TestExtractContent_WithAttachment(t *testing.T) {
	body := "--boundary\r\n" +
		"Content-Type: text/plain\r\n\r\nplain body\r\n" +
		"--boundary\r\n" +
		"Content-Type: application/pdf\r\n" +
		"Content-Disposition: attachment; filename=\"doc.pdf\"\r\n\r\n" +
		"pdfdata\r\n" +
		"--boundary--\r\n"
	mime := "Content-Type: multipart/mixed; boundary=boundary\r\n\r\n" + body

	reg := newAttRegistry()
	text, atts := extractContent([]byte(mime), 42, "http://emaid:9003", reg, 0)

	if !strings.Contains(text, "plain body") {
		t.Errorf("text = %q", text)
	}
	if len(atts) != 1 {
		t.Fatalf("got %d attachments, want 1", len(atts))
	}
	if atts[0].Mime != "application/pdf" {
		t.Errorf("mime = %q", atts[0].Mime)
	}
	if atts[0].Filename != "doc.pdf" {
		t.Errorf("filename = %q", atts[0].Filename)
	}
	if atts[0].URL != "http://emaid:9003/files/42/0" {
		t.Errorf("url = %q", atts[0].URL)
	}
	if atts[0].Size != int64(len("pdfdata")) {
		t.Errorf("size = %d", atts[0].Size)
	}

	// verify metadata in registry, NOT on disk
	meta, ok := reg.get("42-0")
	if !ok {
		t.Fatal("metadata not in registry")
	}
	if meta.Mime != "application/pdf" {
		t.Errorf("registry mime = %q", meta.Mime)
	}
	if meta.Filename != "doc.pdf" {
		t.Errorf("registry filename = %q", meta.Filename)
	}
}

// TestExtractContent_NoAttachments verifies text-only emails return no attachments.
func TestExtractContent_NoAttachments(t *testing.T) {
	raw := "Content-Type: text/plain\r\n\r\njust text"
	text, atts := extractContent([]byte(raw), 1, "http://emaid:9003", newAttRegistry(), 0)
	if !strings.Contains(text, "just text") {
		t.Errorf("text = %q", text)
	}
	if len(atts) != 0 {
		t.Errorf("got %d attachments, want 0", len(atts))
	}
}

// TestExtractContent_MultipleAttachments verifies multiple attachments are extracted.
func TestExtractContent_MultipleAttachments(t *testing.T) {
	body := "--boundary\r\n" +
		"Content-Type: text/plain\r\n\r\nhi\r\n" +
		"--boundary\r\n" +
		"Content-Type: image/png\r\n" +
		"Content-Disposition: attachment; filename=\"img.png\"\r\n\r\n" +
		"pngdata\r\n" +
		"--boundary\r\n" +
		"Content-Type: text/csv\r\n" +
		"Content-Disposition: attachment; filename=\"data.csv\"\r\n\r\n" +
		"a,b,c\r\n" +
		"--boundary--\r\n"
	mime := "Content-Type: multipart/mixed; boundary=boundary\r\n\r\n" + body

	reg := newAttRegistry()
	_, atts := extractContent([]byte(mime), 99, "http://emaid:9003", reg, 0)

	if len(atts) != 2 {
		t.Fatalf("got %d attachments, want 2", len(atts))
	}
	if atts[0].Filename != "img.png" {
		t.Errorf("att[0] filename = %q", atts[0].Filename)
	}
	if atts[1].Filename != "data.csv" {
		t.Errorf("att[1] filename = %q", atts[1].Filename)
	}
	if atts[0].URL != "http://emaid:9003/files/99/0" {
		t.Errorf("att[0] url = %q", atts[0].URL)
	}
	if atts[1].URL != "http://emaid:9003/files/99/1" {
		t.Errorf("att[1] url = %q", atts[1].URL)
	}

	// verify metadata in registry, not on disk
	if _, ok := reg.get("99-0"); !ok {
		t.Error("att[0] not in registry")
	}
	if _, ok := reg.get("99-1"); !ok {
		t.Error("att[1] not in registry")
	}
}

// TestRunIdle_ContextCancel_DeadTCP verifies that runIdle returns promptly
// when ctx is cancelled while the server has received DONE but never responds
// (network partition after DONE was written).
func TestRunIdle_ContextCancel_DeadTCP(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	stuck := make(chan struct{})
	go serveStuckIMAP(t, serverConn, stuck)

	p := &poller{
		cfg: config{
			IMAPHost: "fake",
			IMAPPort: "993",
			Account:  "user",
			Password: "pass",
		},
		db:  newTestDB(t),
		reg: newAttRegistry(),
		dialTLS: func(_ string, opts *imapclient.Options) (*imapclient.Client, error) {
			return imapclient.New(clientConn, opts), nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- p.runIdle(ctx, nil) }()

	// Wait until server is in IDLE state, then cancel.
	<-stuck
	time.Sleep(50 * time.Millisecond) // let client enter its select loop
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runIdle: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runIdle hung: idleCmd.Wait() blocked on dead TCP")
	}
}

// TestFetchUnseen_RouterFailure_NotMarkedSeen verifies that when SendMessage
// fails, the SEEN flag is not set on the IMAP server. The next poll will
// re-fetch the same UID via the NotFlag:FlagSeen filter, providing retry.
func TestFetchUnseen_RouterFailure_NotMarkedSeen(t *testing.T) {
	// Router always returns 500 — simulates transient delivery failure.
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer failSrv.Close()
	rc := chanlib.NewRouterClient(failSrv.URL, "")

	serverConn, clientConn := net.Pipe()
	storeCalled := make(chan struct{}, 1)
	go serveMsgIMAP(t, serverConn, storeCalled)

	p := &poller{
		cfg: config{IMAPHost: "fake", IMAPPort: "993", Account: "user", Password: "pass"},
		db:  newTestDB(t),
		reg: newAttRegistry(),
		dialTLS: func(_ string, opts *imapclient.Options) (*imapclient.Client, error) {
			return imapclient.New(clientConn, opts), nil
		},
	}

	if err := p.poll(context.Background(), rc); err != nil {
		t.Fatalf("poll: %v", err)
	}

	select {
	case <-storeCalled:
		t.Error("UID STORE \\Seen was issued despite router failure — message will not be retried")
	default:
		// Good: SEEN not set; next poll re-fetches via NotFlag:FlagSeen.
	}
}

// serveMsgIMAP is a minimal IMAP server with one unseen message (UID 1).
// It signals storeCalled if the client issues a UID STORE command.
func serveMsgIMAP(t *testing.T, conn net.Conn, storeCalled chan<- struct{}) {
	t.Helper()
	defer conn.Close()

	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	flush := func(s string) {
		bw.WriteString(s) //nolint:errcheck
		bw.Flush()        //nolint:errcheck
	}

	flush("* OK [CAPABILITY IMAP4rev1] ready\r\n")

	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 2 {
			continue
		}
		tag, cmd := parts[0], strings.ToUpper(parts[1])

		switch cmd {
		case "LOGIN":
			flush(tag + " OK logged in\r\n")
		case "SELECT":
			flush("* 1 EXISTS\r\n* 0 RECENT\r\n")
			flush(tag + " OK [READ-WRITE] SELECT completed\r\n")
		case "UID":
			if len(parts) < 3 {
				flush(tag + " BAD missing args\r\n")
				continue
			}
			sub := strings.ToUpper(strings.Fields(parts[2])[0])
			switch sub {
			case "SEARCH":
				flush("* SEARCH 1\r\n")
				flush(tag + " OK SEARCH completed\r\n")
			case "FETCH":
				body := "hello"
				env := `(NIL "Test" (("Alice" NIL "alice" "example.com")) NIL NIL ` +
					`(("Bob" NIL "bob" "example.com")) NIL NIL NIL "<test@example.com>")`
				resp := fmt.Sprintf("* 1 FETCH (UID 1 ENVELOPE %s BODY[] {%d}\r\n%s)\r\n",
					env, len(body), body)
				flush(resp)
				flush(tag + " OK FETCH completed\r\n")
			case "STORE":
				select {
				case storeCalled <- struct{}{}:
				default:
				}
				flush(tag + " OK STORE completed\r\n")
			default:
				flush(tag + " BAD unknown UID subcommand\r\n")
			}
		case "LOGOUT":
			flush("* BYE\r\n" + tag + " OK LOGOUT completed\r\n")
			return
		default:
			flush(tag + " BAD unknown command\r\n")
		}
	}
}

// serveStuckIMAP is a minimal IMAP server that processes LOGIN, SELECT, SEARCH,
// and IDLE, then reads DONE but never sends a response — simulating a network
// partition where the server received DONE but the reply is lost.
func serveStuckIMAP(t *testing.T, conn net.Conn, stuck chan struct{}) {
	t.Helper()
	defer conn.Close()

	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	flush := func(s string) {
		bw.WriteString(s) //nolint:errcheck
		bw.Flush()        //nolint:errcheck
	}

	// Include CAPABILITY in greeting so client skips the CAPABILITY round-trip.
	flush("* OK [CAPABILITY IMAP4rev1 IDLE] ready\r\n")

	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 2 {
			continue
		}
		tag, cmd := parts[0], strings.ToUpper(parts[1])

		switch cmd {
		case "LOGIN":
			flush(tag + " OK logged in\r\n")
		case "SELECT":
			flush("* 0 EXISTS\r\n* 0 RECENT\r\n")
			flush(tag + " OK [READ-WRITE] SELECT completed\r\n")
		case "UID":
			// UID SEARCH — return empty result.
			flush("* SEARCH\r\n")
			flush(tag + " OK SEARCH completed\r\n")
		case "IDLE":
			flush("+ idling\r\n")
			close(stuck) // signal: IDLE is established
			// Read DONE (sent by client on ctx cancel) but do not respond.
			br.ReadString('\n') //nolint:errcheck
			// Block until the client closes the connection (fix calls c.Close).
			conn.Read(make([]byte, 1)) //nolint:errcheck
			return
		case "LOGOUT":
			flush("* BYE\r\n" + tag + " OK LOGOUT completed\r\n")
			return
		default:
			flush(tag + " BAD unknown command\r\n")
		}
	}
}
