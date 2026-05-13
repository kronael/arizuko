package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tgbotapi "github.com/matterbridge/telegram-bot-api/v6"

	"github.com/kronael/arizuko/chanlib"
)

func TestParseChatID(t *testing.T) {
	tests := []struct {
		jid  string
		want int64
	}{
		// Legacy.
		{"telegram:123456", 123456},
		{"telegram:-1001234567890", -1001234567890},
		// Typed (post-migration). group/<id> re-signs to negative.
		{"telegram:user/123456", 123456},
		{"telegram:group/1001234567890", -1001234567890},
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

func TestChatJIDFromID(t *testing.T) {
	cases := []struct {
		id   int64
		want string
	}{
		{123456, "telegram:user/123456"},
		{-1001234, "telegram:group/1001234"},
	}
	for _, tt := range cases {
		if got := chatJIDFromID(tt.id); got != tt.want {
			t.Errorf("chatJIDFromID(%d) = %q, want %q", tt.id, got, tt.want)
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

func TestExtractMedia_Audio(t *testing.T) {
	// audio with explicit filename
	msg := &tgbotapi.Message{
		Audio: &tgbotapi.Audio{FileID: "aud1", FileName: "track.mp3", FileSize: 2048},
	}
	r := extractMedia(msg, "http://teled:9001")
	if r.content != "[Audio]" {
		t.Errorf("content = %q, want [Audio]", r.content)
	}
	if len(r.attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(r.attachments))
	}
	if r.attachments[0].Filename != "track.mp3" {
		t.Errorf("filename = %q, want track.mp3", r.attachments[0].Filename)
	}

	// audio with no filename falls back to fileID.mp3
	msg2 := &tgbotapi.Message{
		Audio: &tgbotapi.Audio{FileID: "aud2", FileSize: 512},
	}
	r2 := extractMedia(msg2, "http://teled:9001")
	if r2.attachments[0].Filename != "aud2.mp3" {
		t.Errorf("fallback filename = %q, want aud2.mp3", r2.attachments[0].Filename)
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

func TestUserName(t *testing.T) {
	cases := []struct {
		u    *tgbotapi.User
		want string
	}{
		{nil, "unknown"},
		{&tgbotapi.User{FirstName: "Alice", LastName: "Liddell"}, "Alice Liddell"},
		{&tgbotapi.User{FirstName: "Bob"}, "Bob"},
		{&tgbotapi.User{ID: 42}, "42"},
	}
	for _, c := range cases {
		if got := userName(c.u); got != c.want {
			t.Errorf("userName(%+v) = %q, want %q", c.u, got, c.want)
		}
	}
}

func TestUserID(t *testing.T) {
	if userID(nil) != "" {
		t.Error("nil user should return empty ID")
	}
	if userID(&tgbotapi.User{ID: 123}) != "123" {
		t.Error("user ID should be stringified")
	}
}

func TestEntityExtract(t *testing.T) {
	// "@bot hello" — mention at offset 0 length 4
	text := "@bot hello"
	e := tgbotapi.MessageEntity{Type: "mention", Offset: 0, Length: 4}
	if got := entity(text, e); got != "@bot" {
		t.Errorf("entity = %q, want @bot", got)
	}
	// Clamp over-length
	e2 := tgbotapi.MessageEntity{Type: "mention", Offset: 0, Length: 999}
	if got := entity(text, e2); got != text {
		t.Errorf("clamped = %q, want %q", got, text)
	}
}

func TestLoadSaveOffset(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "offset")
	b := &bot{cfg: config{StateFile: stateFile}}

	// No file → 0
	if got := b.loadOffset(); got != 0 {
		t.Errorf("loadOffset empty = %d, want 0", got)
	}

	b.saveOffset(42)
	if got := b.loadOffset(); got != 42 {
		t.Errorf("loadOffset after save = %d, want 42", got)
	}

	// Corrupt file → 0
	os.WriteFile(stateFile, []byte("not-a-number"), 0o644)
	if got := b.loadOffset(); got != 0 {
		t.Errorf("loadOffset corrupt = %d, want 0", got)
	}

	// Whitespace tolerated
	os.WriteFile(stateFile, []byte("  99\n"), 0o644)
	if got := b.loadOffset(); got != 99 {
		t.Errorf("loadOffset whitespace = %d, want 99", got)
	}
}

func TestLoadSaveOffsetEmptyStateFile(t *testing.T) {
	// Empty StateFile → no-op (no panic, no file created)
	b := &bot{cfg: config{StateFile: ""}}
	b.saveOffset(123)
	if got := b.loadOffset(); got != 0 {
		t.Errorf("empty state file path, got %d", got)
	}
}

func TestExtractMedia_Video(t *testing.T) {
	msg := &tgbotapi.Message{
		Video:   &tgbotapi.Video{FileID: "vid1", FileSize: 5000},
		Caption: "cool",
	}
	r := extractMedia(msg, "http://teled:9001")
	if r.content != "[Video] cool" {
		t.Errorf("content = %q", r.content)
	}
	if len(r.attachments) != 1 || r.attachments[0].Mime != "video/mp4" {
		t.Errorf("attachments = %+v", r.attachments)
	}
}

func TestExtractMedia_Location(t *testing.T) {
	msg := &tgbotapi.Message{Location: &tgbotapi.Location{}}
	r := extractMedia(msg, "")
	if r.content != "[Location]" {
		t.Errorf("content = %q", r.content)
	}
}

func TestExtractMedia_Contact(t *testing.T) {
	msg := &tgbotapi.Message{Contact: &tgbotapi.Contact{PhoneNumber: "+1"}}
	r := extractMedia(msg, "")
	if r.content != "[Contact]" {
		t.Errorf("content = %q", r.content)
	}
}

func TestMdToHTMLEscape(t *testing.T) {
	// HTML specials must be escaped before markdown conversion
	if got := mdToHTML("<script>"); got != "&lt;script&gt;" {
		t.Errorf("mdToHTML(<script>) = %q", got)
	}
	if got := mdToHTML("a & b"); got != "a &amp; b" {
		t.Errorf("mdToHTML(a & b) = %q", got)
	}
}

func TestFetchHistoryUnsupported(t *testing.T) {
	b := &bot{}
	resp, err := b.FetchHistory(chanlib.HistoryRequest{ChatJID: "telegram:1", Limit: 50})
	if err != nil {
		t.Fatalf("FetchHistory error: %v", err)
	}
	if resp.Source != "unsupported" {
		t.Errorf("source = %q, want unsupported", resp.Source)
	}
	if len(resp.Messages) != 0 {
		t.Errorf("messages = %d, want 0", len(resp.Messages))
	}
	if resp.Cap == "" {
		t.Error("Cap note should explain why history is unsupported")
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
