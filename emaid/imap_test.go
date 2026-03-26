package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
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
		db: newTestDB(t),
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
