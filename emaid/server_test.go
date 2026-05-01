package main

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/onvos/arizuko/chanlib"
)

func testServer(t *testing.T, secret string) (*server, *sql.DB) {
	t.Helper()
	db := newTestDB(t)
	cfg := config{Name: "email", ChannelSecret: secret, DataDir: t.TempDir()}
	return newServer(cfg, db, newAttRegistry(), func() bool { return true }, func() int64 { return time.Now().Unix() }), db
}

func TestHandleSend_NoThread(t *testing.T) {
	s, _ := testServer(t, "")
	body, _ := json.Marshal(map[string]string{
		"chat_jid": "email:notexist", "content": "hello",
	})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 502 {
		t.Errorf("status = %d, want 502", w.Code)
	}
}

func TestHandleHealth(t *testing.T) {
	s, _ := testServer(t, "")
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" || resp["name"] != "email" {
		t.Errorf("health = %v", resp)
	}
}

func TestHandleHealthDisconnected(t *testing.T) {
	db := newTestDB(t)
	s := newServer(config{Name: "email"}, db, newAttRegistry(), func() bool { return false }, func() int64 { return time.Now().Unix() })
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "disconnected" {
		t.Errorf("status = %v", resp["status"])
	}
}

func TestAuthRequired(t *testing.T) {
	s, _ := testServer(t, "mysecret")
	body, _ := json.Marshal(map[string]string{
		"chat_jid": "email:tid", "content": "hello",
	})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// no Authorization header
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestHandleTyping(t *testing.T) {
	s, _ := testServer(t, "")
	body, _ := json.Marshal(map[string]any{"chat_jid": "email:tid", "on": true})
	req := httptest.NewRequest("POST", "/typing", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["ok"] != true {
		t.Errorf("ok = %v", resp["ok"])
	}
}

func TestAuthPassthrough(t *testing.T) {
	s, db := testServer(t, "tok")
	upsertThread(db, "root@x.com", "abc123def456", "alice@x.com", "root@x.com")

	// /typing always ok with valid auth
	body, _ := json.Marshal(map[string]any{"chat_jid": "email:tid", "on": false})
	req := httptest.NewRequest("POST", "/typing", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

// TestFileProxy verifies that the file proxy re-fetches from IMAP on demand.
func TestFileProxy(t *testing.T) {
	reg := newAttRegistry()
	reg.put("42-0", attMeta{Mime: "application/pdf", Filename: "doc.pdf", Size: 9, Part: []int{2}})

	serverConn, clientConn := net.Pipe()
	go serveAttachmentIMAP(t, serverConn, []int{2}, []byte("hello pdf"))

	db := newTestDB(t)
	s := &server{
		cfg:     config{Name: "email", IMAPHost: "fake", IMAPPort: "993", Account: "user", Password: "pass"},
		db:      db,
		reg:     reg,
		dialTLS: func(_ string, _ *imapclient.Options) (*imapclient.Client, error) {
			return imapclient.New(clientConn, nil), nil
		},
	}

	req := httptest.NewRequest("GET", "/files/42/0", nil)
	w := httptest.NewRecorder()
	s.handleFile(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("content-type = %q", ct)
	}
	if w.Body.String() != "hello pdf" {
		t.Errorf("body = %q", w.Body.String())
	}
	if w.Header().Get("Content-Disposition") != `attachment; filename="doc.pdf"` {
		t.Errorf("disposition = %q", w.Header().Get("Content-Disposition"))
	}
}

func TestFileProxyNotFound(t *testing.T) {
	s, _ := testServer(t, "")
	req := httptest.NewRequest("GET", "/files/999/0", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestFileProxyBadPath(t *testing.T) {
	s, _ := testServer(t, "")
	req := httptest.NewRequest("GET", "/files/notanumber/0", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestFileProxyAuthRequired(t *testing.T) {
	reg := newAttRegistry()
	reg.put("1-0", attMeta{Mime: "text/plain", Filename: "file.txt", Size: 4, Part: []int{1}})

	db := newTestDB(t)
	s := newServer(config{Name: "email", ChannelSecret: "secret123", DataDir: t.TempDir()}, db, reg, func() bool { return true }, func() int64 { return time.Now().Unix() })

	req := httptest.NewRequest("GET", "/files/1/0", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// TestFileProxyIMAPError verifies 502 when IMAP re-fetch fails.
func TestFileProxyIMAPError(t *testing.T) {
	reg := newAttRegistry()
	reg.put("10-0", attMeta{Mime: "image/png", Filename: "img.png", Size: 5, Part: []int{1}})

	db := newTestDB(t)
	s := &server{
		cfg: config{Name: "email", IMAPHost: "fake", IMAPPort: "993", Account: "user", Password: "pass"},
		db:  db,
		reg: reg,
		dialTLS: func(_ string, _ *imapclient.Options) (*imapclient.Client, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}

	req := httptest.NewRequest("GET", "/files/10/0", nil)
	w := httptest.NewRecorder()
	s.handleFile(w, req)

	if w.Code != 502 {
		t.Errorf("status = %d, want 502", w.Code)
	}
}

// TestFetchHistory_ThreadNotFound verifies that a request for an unknown
// thread returns an error (502 via handler) without contacting IMAP.
func TestFetchHistory_ThreadNotFound(t *testing.T) {
	s, _ := testServer(t, "")
	_, err := s.FetchHistory(chanlib.HistoryRequest{ChatJID: "email:nosuch", Limit: 10})
	if err == nil {
		t.Fatal("expected error for unknown thread, got nil")
	}
}

// TestFetchMsgToInbound verifies the IMAP→InboundMsg shape conversion used
// by FetchHistory matches the live poller's delivery format (no drift).
func TestFetchMsgToInbound(t *testing.T) {
	// Build a FetchMessageBuffer directly: envelope + a text/plain body.
	raw := []byte("Content-Type: text/plain\r\n\r\nhello history")
	msg := &imapclient.FetchMessageBuffer{
		UID: 7,
		Envelope: &imap.Envelope{
			MessageID: "<m1@example.com>",
			Subject:   "hi",
			Date:      time.Unix(1_700_000_000, 0).UTC(),
			From:      []imap.Address{{Name: "Alice", Mailbox: "alice", Host: "example.com"}},
			To:        []imap.Address{{Name: "", Mailbox: "bot", Host: "example.com"}},
		},
		BodySection: []imapclient.FetchBodySectionBuffer{
			{Section: &imap.FetchItemBodySection{}, Bytes: raw},
		},
	}

	im, ok := fetchMsgToInbound(msg, "tid123", "", newAttRegistry(), 0)
	if !ok {
		t.Fatal("fetchMsgToInbound returned !ok")
	}
	if im.ID != "m1@example.com" {
		t.Errorf("id = %q", im.ID)
	}
	if im.ChatJID != "email:thread/tid123" {
		t.Errorf("jid = %q", im.ChatJID)
	}
	if im.Sender != "email:address/alice@example.com" {
		t.Errorf("sender = %q", im.Sender)
	}
	if im.SenderName != "Alice" {
		t.Errorf("sender_name = %q", im.SenderName)
	}
	if !strings.Contains(im.Content, "Subject: hi") {
		t.Errorf("content missing Subject header: %q", im.Content)
	}
	if !strings.Contains(im.Content, "hello history") {
		t.Errorf("content missing body: %q", im.Content)
	}
	if im.Timestamp != 1_700_000_000 {
		t.Errorf("timestamp = %d", im.Timestamp)
	}
}

// TestFetchMsgToInbound_NoEnvelope verifies missing envelope → !ok (skip).
func TestFetchMsgToInbound_NoEnvelope(t *testing.T) {
	_, ok := fetchMsgToInbound(&imapclient.FetchMessageBuffer{UID: 1}, "tid", "", nil, 0)
	if ok {
		t.Error("expected !ok when envelope is nil")
	}
}

// serveAttachmentIMAP is a minimal IMAP server that serves a specific BODY section.
func serveAttachmentIMAP(t *testing.T, conn net.Conn, section []int, data []byte) {
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
			case "FETCH":
				// Build section string like "2" or "1.2"
				sectionParts := make([]string, len(section))
				for i, s := range section {
					sectionParts[i] = fmt.Sprintf("%d", s)
				}
				sectionStr := strings.Join(sectionParts, ".")
				resp := fmt.Sprintf("* 1 FETCH (UID 42 BODY[%s] {%d}\r\n%s)\r\n",
					sectionStr, len(data), data)
				flush(resp)
				flush(tag + " OK FETCH completed\r\n")
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

// TestFileProxy_MetadataRecordedDuringExtract verifies end-to-end: extractContent records
// metadata in the registry, and the server can look it up for re-fetch.
func TestFileProxy_MetadataRecordedDuringExtract(t *testing.T) {
	body := "--boundary\r\n" +
		"Content-Type: text/plain\r\n\r\ntext\r\n" +
		"--boundary\r\n" +
		"Content-Type: image/jpeg\r\n" +
		"Content-Disposition: attachment; filename=\"photo.jpg\"\r\n\r\n" +
		"jpegdata\r\n" +
		"--boundary--\r\n"
	mime := "Content-Type: multipart/mixed; boundary=boundary\r\n\r\n" + body

	reg := newAttRegistry()
	_, atts := extractContent([]byte(mime), 7, "http://emaid:9003", reg, 0)

	if len(atts) != 1 {
		t.Fatalf("got %d attachments, want 1", len(atts))
	}
	if atts[0].URL != "http://emaid:9003/files/7/0" {
		t.Errorf("url = %q", atts[0].URL)
	}

	meta, ok := reg.get("7-0")
	if !ok {
		t.Fatal("metadata not registered after extractContent")
	}
	if meta.Mime != "image/jpeg" {
		t.Errorf("meta.Mime = %q", meta.Mime)
	}
	if meta.Filename != "photo.jpg" {
		t.Errorf("meta.Filename = %q", meta.Filename)
	}
	// Section 2: text/plain is section 1, attachment is section 2.
	// Previous buggy behavior incremented only on attachment, producing [1].
	if len(meta.Part) != 1 || meta.Part[0] != 2 {
		t.Errorf("meta.Part = %v, want [2]", meta.Part)
	}
}
