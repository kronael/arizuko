package main

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
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
