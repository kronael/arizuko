package routd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

// Tier-2 parity: enrich.go media/voice helpers shipped but under-tested vs
// gated — extFromMime, multi-language whisperTranscribe + readWhisperLanguages,
// the base64 attachment arm, and the skip-empty-URL guard.

// --- extFromMime canonical extension (mirror gated extFromMime) ---

// TestExtFromMime: the filename extension wins; otherwise canonical preferred
// extensions pin Claude-readable types; family fallbacks and .bin last.
func TestExtFromMime(t *testing.T) {
	cases := []struct {
		mime, filename, want string
	}{
		{"image/jpeg", "photo.PNG", ".png"}, // filename ext wins, lowercased
		{"image/jpeg", "", ".jpg"},          // canonical, not .jfif/.jpe
		{"image/png", "", ".png"},
		{"image/gif", "", ".gif"},
		{"image/webp", "", ".webp"},
		{"audio/ogg", "", ".ogg"},
		{"audio/mpeg", "", ".mp3"},
		{"audio/mp4", "", ".m4a"},
		{"video/mp4", "", ".mp4"},
		{"audio/x-weird", "", ".mp3"},            // audio/* family fallback
		{"application/octet-stream", "", ".bin"}, // unknown → .bin
	}
	for _, c := range cases {
		if got := extFromMime(c.mime, c.filename); got != c.want {
			t.Errorf("extFromMime(%q,%q) = %q, want %q", c.mime, c.filename, got, c.want)
		}
	}
}

// --- readWhisperLanguages (.whisper-language) ---

// TestReadWhisperLanguages: one language per non-blank line; missing file → nil.
func TestReadWhisperLanguages(t *testing.T) {
	dir := t.TempDir()
	if got := readWhisperLanguages(dir); got != nil {
		t.Errorf("missing .whisper-language → %v, want nil", got)
	}
	if err := os.WriteFile(filepath.Join(dir, ".whisper-language"), []byte("en\n\ncs\n  de  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readWhisperLanguages(dir)
	want := []string{"en", "cs", "de"}
	if len(got) != len(want) {
		t.Fatalf("readWhisperLanguages = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("lang[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// --- whisperTranscribe multi-language ---

// TestWhisperTranscribe_MultiLanguage: each configured language hits the service
// once; results join with newlines. Mirrors gated whisperTranscribe per-lang fan-out.
func TestWhisperTranscribe_MultiLanguage(t *testing.T) {
	var langs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		langs = append(langs, r.URL.Query().Get("language"))
		json.NewEncoder(w).Encode(map[string]string{"text": "[" + r.URL.Query().Get("language") + "]"})
	}))
	defer srv.Close()

	f := filepath.Join(t.TempDir(), "a.ogg")
	os.WriteFile(f, []byte("audio"), 0o644)
	got := whisperTranscribe(context.Background(), srv.URL, "m", f, "audio/ogg", []string{"en", "cs"})

	if got != "[en]\n[cs]" {
		t.Errorf("transcribe = %q, want %q (one line per language)", got, "[en]\n[cs]")
	}
	if len(langs) != 2 || langs[0] != "en" || langs[1] != "cs" {
		t.Errorf("service saw languages %v, want [en cs]", langs)
	}
}

// TestWhisperTranscribe_DefaultSingleLang: empty langs falls back to one
// transcription with no language hint (gated default arm).
func TestWhisperTranscribe_DefaultSingleLang(t *testing.T) {
	var hits int32
	var sawLang string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		sawLang = r.URL.Query().Get("language")
		json.NewEncoder(w).Encode(map[string]string{"text": "hi"})
	}))
	defer srv.Close()

	f := filepath.Join(t.TempDir(), "a.ogg")
	os.WriteFile(f, []byte("audio"), 0o644)
	if got := whisperTranscribe(context.Background(), srv.URL, "m", f, "audio/ogg", nil); got != "hi" {
		t.Errorf("transcribe = %q, want hi", got)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("service hit %d times, want 1 (single default lang)", hits)
	}
	if sawLang != "" {
		t.Errorf("default lang request carried language=%q, want unset", sawLang)
	}
}

// --- enrich base64 attachment arm + skip-empty-URL ---

func b64VoiceAtt(data string) string {
	raw, _ := json.Marshal([]chanlib.InboundAttachment{
		{Data: base64.StdEncoding.EncodeToString([]byte(data)), Mime: "audio/ogg", Filename: "note.ogg"},
	})
	return string(raw)
}

// TestEnrich_Base64Attachment: an attachment carrying inline base64 (no URL) is
// decoded to the media dir and transcribed (gated base64 arm of enrichAttachments).
func TestEnrich_Base64Attachment(t *testing.T) {
	whisper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"text": "decoded audio"})
	}))
	defer whisper.Close()

	db, l, groups := enrichLoop(t, mediaConfig{
		Enabled: true, MaxBytes: 1 << 20, WhisperURL: whisper.URL, VoiceEnabled: true,
	})
	msg := &core.Message{ID: "m1", ChatJID: "tg:1", Content: "voice", Attachments: b64VoiceAtt("raw-ogg-bytes")}
	if err := db.PutMessage(*msg); err != nil {
		t.Fatal(err)
	}
	l.enrichAttachments(context.Background(), msg, "demo")

	if !strings.Contains(msg.Content, `transcript="decoded audio"`) {
		t.Errorf("base64 attachment not transcribed: %q", msg.Content)
	}
	// the decoded bytes landed on disk in the dated media dir.
	day := time.Now().Format("20060102")
	mediaDir := groupfolder.GroupMediaDir(filepath.Join(groups, "demo"), day)
	files, _ := os.ReadDir(mediaDir)
	if len(files) != 1 {
		t.Fatalf("media dir has %d files, want 1", len(files))
	}
	if data, _ := os.ReadFile(filepath.Join(mediaDir, files[0].Name())); string(data) != "raw-ogg-bytes" {
		t.Errorf("decoded file=%q want raw-ogg-bytes", data)
	}
}

// TestEnrich_SkipsEmptyURLAndData: an attachment with neither URL nor Data is
// skipped (no panic, no <attachment> block), the rest of the turn proceeds
// (gated enrichAttachments empty-source guard).
func TestEnrich_SkipsEmptyURLAndData(t *testing.T) {
	_, l, _ := enrichLoop(t, mediaConfig{Enabled: true, MaxBytes: 1 << 20})
	raw, _ := json.Marshal([]chanlib.InboundAttachment{{Mime: "audio/ogg", Filename: "ghost.ogg"}})
	msg := &core.Message{ID: "m1", ChatJID: "tg:1", Content: "no media here", Attachments: string(raw)}
	l.enrichAttachments(context.Background(), msg, "demo")

	if strings.Contains(msg.Content, "<attachment") {
		t.Errorf("empty-source attachment produced a block: %q", msg.Content)
	}
	if msg.Content != "no media here" {
		t.Errorf("content mutated for skipped attachment: %q", msg.Content)
	}
}
