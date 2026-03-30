package main

import (
	"strings"
	"testing"

	tgbotapi "github.com/matterbridge/telegram-bot-api/v6"

	"github.com/onvos/arizuko/chanlib"
)

func TestParseChatID(t *testing.T) {
	tests := []struct {
		jid  string
		want int64
	}{
		{"telegram:123456", 123456},
		{"telegram:-1001234567890", -1001234567890},
	}
	for _, tt := range tests {
		got, err := parseChatID(tt.jid)
		if err != nil {
			t.Errorf("parseChatID(%q) error: %v", tt.jid, err)
		}
		if got != tt.want {
			t.Errorf("parseChatID(%q) = %d, want %d", tt.jid, got, tt.want)
		}
	}
}

func TestMdToHTML(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"**bold**", "<b>bold</b>"},
		{"`code`", "<code>code</code>"},
		{"```\nblock\n```", "<pre>block\n</pre>"},
		{"# Header", "<b>Header</b>"},
	}
	for _, tt := range tests {
		if got := mdToHTML(tt.in); got != tt.want {
			t.Errorf("mdToHTML(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestExtractMedia_Photo(t *testing.T) {
	msg := &tgbotapi.Message{
		Photo:   []tgbotapi.PhotoSize{{FileID: "abc", FileSize: 1024}},
		Caption: "nice shot",
	}
	r := extractMedia(msg, "http://teled:9001")
	if r.content != "[Photo] nice shot" {
		t.Errorf("content = %q", r.content)
	}
	if len(r.attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(r.attachments))
	}
	att := r.attachments[0]
	if att.Mime != "image/jpeg" {
		t.Errorf("mime = %q, want image/jpeg", att.Mime)
	}
	if !strings.HasSuffix(att.URL, "/files/abc") {
		t.Errorf("URL = %q, want suffix /files/abc", att.URL)
	}
}

func TestExtractMedia_Voice(t *testing.T) {
	msg := &tgbotapi.Message{
		Voice: &tgbotapi.Voice{FileID: "xyz", FileSize: 512},
	}
	r := extractMedia(msg, "http://teled:9001")
	if r.content != "[Voice message]" {
		t.Errorf("content = %q", r.content)
	}
	if len(r.attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(r.attachments))
	}
	if r.attachments[0].Mime != "audio/ogg" {
		t.Errorf("mime = %q, want audio/ogg", r.attachments[0].Mime)
	}
}

func TestExtractMedia_Document(t *testing.T) {
	msg := &tgbotapi.Message{
		Document: &tgbotapi.Document{FileID: "docid", FileName: "report.pdf", MimeType: "application/pdf"},
	}
	r := extractMedia(msg, "http://teled:9001")
	if !strings.Contains(r.content, "report.pdf") {
		t.Errorf("content = %q, want to contain filename", r.content)
	}
	if len(r.attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(r.attachments))
	}
	if r.attachments[0].Filename != "report.pdf" {
		t.Errorf("filename = %q, want report.pdf", r.attachments[0].Filename)
	}
}

func TestExtractMedia_NoListenURL(t *testing.T) {
	msg := &tgbotapi.Message{
		Photo: []tgbotapi.PhotoSize{{FileID: "abc", FileSize: 1024}},
	}
	r := extractMedia(msg, "")
	if len(r.attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(r.attachments))
	}
	if r.attachments[0].URL != "" {
		t.Errorf("URL should be empty when listenURL is empty, got %q", r.attachments[0].URL)
	}
}

func TestExtractMedia_Sticker(t *testing.T) {
	msg := &tgbotapi.Message{
		Sticker: &tgbotapi.Sticker{Emoji: "🔥"},
	}
	r := extractMedia(msg, "http://teled:9001")
	if !strings.Contains(r.content, "Sticker") {
		t.Errorf("content = %q", r.content)
	}
	if len(r.attachments) != 0 {
		t.Errorf("sticker should have no attachments, got %d", len(r.attachments))
	}
}

func TestExtractMedia_NoMedia(t *testing.T) {
	msg := &tgbotapi.Message{Text: "plain text"}
	r := extractMedia(msg, "http://teled:9001")
	if r.content != "" {
		t.Errorf("content = %q, want empty for plain text message", r.content)
	}
	if len(r.attachments) != 0 {
		t.Errorf("attachments = %d, want 0", len(r.attachments))
	}
}

func TestChunk(t *testing.T) {
	c := chanlib.Chunk("abcdefgh", 3)
	if len(c) != 3 || c[0] != "abc" || c[1] != "def" || c[2] != "gh" {
		t.Errorf("chunk = %v", c)
	}
	if s := chanlib.Chunk("ab", 10); len(s) != 1 || s[0] != "ab" {
		t.Errorf("single = %v", s)
	}
	if s := chanlib.Chunk("", 10); len(s) != 1 || s[0] != "" {
		t.Errorf("empty = %v", s)
	}
}
