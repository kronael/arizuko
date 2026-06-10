package routd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

// --- outbound: send_voice synthesizes via TTS then delivers ---

// ttsStub stands in for ttsd's /v1/audio/speech: returns fixed Opus bytes and
// records the request body so the test can assert the synthesis params.
func ttsStub(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var reqs []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/speech" {
			http.Error(w, "not found", 404)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		reqs = append(reqs, body)
		w.Write([]byte("OggSfake-opus-bytes"))
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func TestSendVoice_SynthesizesAndDelivers(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	tts, reqs := ttsStub(t)
	cache := t.TempDir()
	dl := &recDeliverer{pid: "vp-1"}
	srv := NewServer(db, nil, dl, nil, 0, "")
	srv.SetTTS(TTSConfig(true, tts.URL, "af_bella", "kokoro", 5*time.Second, cache))

	if _, err := db.PutTurnContext("t1", "demo", "#deploy", "tg:42", "u1", ""); err != nil {
		t.Fatal(err)
	}
	fns := srv.buildGatedFns(turnMCP{folder: "demo", topic: "#deploy", chatJID: "tg:42", turnID: "t1"})

	pid, err := fns.SendVoice("tg:42", "hello there", "", "demo", "")
	if err != nil {
		t.Fatalf("SendVoice: %v", err)
	}
	if pid != "vp-1" {
		t.Fatalf("platform id=%q want vp-1", pid)
	}
	if len(dl.voices) != 1 {
		t.Fatalf("deliver.voices=%d want 1", len(dl.voices))
	}
	v := dl.voices[0]
	if v.jid != "tg:42" {
		t.Errorf("voice jid=%q want tg:42", v.jid)
	}
	// threadID falls back to the turn's topic when the caller passes none.
	if v.threadID != "#deploy" {
		t.Errorf("voice threadID=%q want #deploy (turn topic)", v.threadID)
	}
	// The synthesized Opus was cached to a .ogg under the cache dir and handed
	// to the Deliverer as a path on disk.
	if !strings.HasPrefix(v.audioPath, cache) || !strings.HasSuffix(v.audioPath, ".ogg") {
		t.Errorf("voice audioPath=%q want %s/*.ogg", v.audioPath, cache)
	}
	if data, _ := os.ReadFile(v.audioPath); string(data) != "OggSfake-opus-bytes" {
		t.Errorf("cached audio=%q want the stub's bytes", data)
	}
	// Synthesis request shape: opus format + the configured voice/model.
	if len(*reqs) != 1 {
		t.Fatalf("TTS hit %d times want 1", len(*reqs))
	}
	rb := (*reqs)[0]
	if rb["response_format"] != "opus" || rb["voice"] != "af_bella" || rb["model"] != "kokoro" || rb["input"] != "hello there" {
		t.Errorf("synthesize body=%+v want opus/af_bella/kokoro/'hello there'", rb)
	}
}

// A second send of the same (text, voice, model) reuses the cached file — the
// TTS service is hit exactly once (memoization parity with gated).
func TestSendVoice_CachesSynthesis(t *testing.T) {
	db, _ := OpenMem()
	t.Cleanup(func() { db.Close() })
	tts, reqs := ttsStub(t)
	dl := &recDeliverer{pid: "vp"}
	srv := NewServer(db, nil, dl, nil, 0, "")
	srv.SetTTS(TTSConfig(true, tts.URL, "v", "m", 5*time.Second, t.TempDir()))

	for i := 0; i < 2; i++ {
		if _, err := srv.sendVoice("tg:7", "same text", "", "", ""); err != nil {
			t.Fatalf("sendVoice #%d: %v", i, err)
		}
	}
	if len(*reqs) != 1 {
		t.Fatalf("TTS hit %d times, want 1 (second call should hit cache)", len(*reqs))
	}
}

// PERSONA.md `voice:` frontmatter resolves the voice when the caller passes
// none and overrides the instance default (gated resolveVoice parity).
func TestSendVoice_ResolvesPersonaVoice(t *testing.T) {
	db, _ := OpenMem()
	t.Cleanup(func() { db.Close() })
	tts, reqs := ttsStub(t)
	groups := t.TempDir()
	os.MkdirAll(filepath.Join(groups, "demo"), 0o755)
	os.WriteFile(filepath.Join(groups, "demo", "PERSONA.md"),
		[]byte("---\nvoice: bf_emma\n---\nbody\n"), 0o644)

	srv := NewServer(db, nil, &recDeliverer{pid: "vp"}, nil, 0, "")
	srv.SetDirs(groups, "")
	srv.SetTTS(TTSConfig(true, tts.URL, "instance_default", "m", 5*time.Second, t.TempDir()))

	if _, err := srv.sendVoice("tg:1", "hi", "", "demo", ""); err != nil {
		t.Fatalf("sendVoice: %v", err)
	}
	if (*reqs)[0]["voice"] != "bf_emma" {
		t.Errorf("voice=%v want bf_emma (PERSONA.md frontmatter)", (*reqs)[0]["voice"])
	}
}

// TTS off → the send_voice fn returns an Unsupported error and never touches
// the TTS service (faithful to gated with TTS_ENABLED=false).
func TestSendVoice_SkipsWhenDisabled(t *testing.T) {
	db, _ := OpenMem()
	t.Cleanup(func() { db.Close() })
	var hit bool
	tts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer tts.Close()

	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, 0, "")
	// Zero-value tts config: Enabled=false (SetTTS not called).
	if _, err := srv.sendVoice("tg:1", "hello", "", "", ""); err == nil {
		t.Fatal("send_voice with TTS off should error (unsupported)")
	}
	if hit {
		t.Error("TTS endpoint hit while disabled")
	}
	if len(dl.voices) != 0 {
		t.Error("delivered voice while TTS disabled")
	}
}

// Text is validated BEFORE TTS: empty / oversize never reaches the service
// (mirror of gateway TestSendVoice_RejectsEmptyAndOversize).
func TestSendVoice_RejectsEmptyAndOversize(t *testing.T) {
	db, _ := OpenMem()
	t.Cleanup(func() { db.Close() })
	var hit bool
	tts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer tts.Close()
	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, 0, "")
	srv.SetTTS(TTSConfig(true, tts.URL, "v", "m", 5*time.Second, t.TempDir()))

	if _, err := srv.sendVoice("tg:1", "", "", "", ""); err == nil {
		t.Error("empty text should be rejected before TTS")
	}
	if _, err := srv.sendVoice("tg:1", strings.Repeat("a", 6000), "", "", ""); err == nil {
		t.Error("6000-char text should be rejected before TTS")
	}
	if hit {
		t.Error("TTS endpoint hit on invalid text")
	}
	if len(dl.voices) != 0 {
		t.Error("delivered voice on invalid text")
	}
}

// --- inbound: enrichAttachments downloads media + transcribes voice ---

func enrichLoop(t *testing.T, media mediaConfig) (*DB, *Loop, string) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	groups := t.TempDir()
	os.MkdirAll(filepath.Join(groups, "demo"), 0o755)
	l := NewLoop(db, &recRunner{}, LoopConfig{GroupsDir: groups, Media: media})
	l.StopQueue()
	return db, l, groups
}

func voiceAtt(url string) string {
	raw, _ := json.Marshal([]chanlib.InboundAttachment{{URL: url, Mime: "audio/ogg", Filename: "note.ogg"}})
	return string(raw)
}

func TestEnrich_TranscribesVoiceAttachment(t *testing.T) {
	// Whisper stub serves /v1/audio/transcriptions; media server serves the file.
	whisper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			http.Error(w, "nope", 404)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"text": "  hello from whisper  "})
	}))
	defer whisper.Close()
	mediaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("fake-ogg-audio"))
	}))
	defer mediaSrv.Close()

	db, l, groups := enrichLoop(t, mediaConfig{
		Enabled: true, MaxBytes: 1 << 20, WhisperURL: whisper.URL,
		WhisperModel: "turbo", VoiceEnabled: true,
	})

	msg := &core.Message{ID: "m1", ChatJID: "tg:1", Content: "voice msg", Attachments: voiceAtt(mediaSrv.URL + "/n.ogg")}
	if err := db.PutMessage(*msg); err != nil {
		t.Fatal(err)
	}
	l.enrichAttachments(context.Background(), msg, "demo")

	// Transcript inlined into the in-memory message...
	if !strings.Contains(msg.Content, "transcript=\"hello from whisper\"") {
		t.Errorf("content missing transcript: %q", msg.Content)
	}
	if msg.Attachments != "" {
		t.Errorf("attachments not cleared: %q", msg.Attachments)
	}
	// ...the <attachment> block points at the in-container media path...
	if !strings.Contains(msg.Content, core.ContainerHome+"/media/") {
		t.Errorf("content missing container media path: %q", msg.Content)
	}
	// ...the file was downloaded into the group's dated media dir...
	day := time.Now().Format("20060102")
	mediaDir := groupfolder.GroupMediaDir(filepath.Join(groups, "demo"), day)
	files, _ := os.ReadDir(mediaDir)
	if len(files) != 1 {
		t.Fatalf("media dir has %d files, want 1", len(files))
	}
	if data, _ := os.ReadFile(filepath.Join(mediaDir, files[0].Name())); string(data) != "fake-ogg-audio" {
		t.Errorf("downloaded file=%q want fake-ogg-audio", data)
	}
	// ...and the rewrite was persisted (later turns' observed context sees it).
	got, _ := db.MessagesSince("tg:1", "")
	if len(got) != 1 || !strings.Contains(got[0].Content, "transcript=") {
		t.Errorf("persisted row not enriched: %+v", got)
	}
}

// Media off → enrich is a no-op: no download, no transcription, attachments
// preserved (faithful to gated with MEDIA_ENABLED=false).
func TestEnrich_SkipsWhenDisabled(t *testing.T) {
	var hit bool
	mediaSrv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer mediaSrv.Close()

	_, l, _ := enrichLoop(t, mediaConfig{Enabled: false})
	atts := voiceAtt(mediaSrv.URL + "/n.ogg")
	msg := &core.Message{ID: "m1", ChatJID: "tg:1", Content: "voice", Attachments: atts}
	l.enrichAttachments(context.Background(), msg, "demo")

	if hit {
		t.Error("media server hit while media disabled")
	}
	if msg.Attachments != atts {
		t.Error("attachments mutated while media disabled")
	}
	if strings.Contains(msg.Content, "<attachment") {
		t.Errorf("content rewritten while disabled: %q", msg.Content)
	}
}

// Voice transcription off (media on, VoiceEnabled false) → the file still
// downloads and gets an <attachment> block, but no transcript.
func TestEnrich_DownloadsWithoutTranscriptWhenVoiceOff(t *testing.T) {
	var whisperHit bool
	whisper := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { whisperHit = true }))
	defer whisper.Close()
	mediaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("audio"))
	}))
	defer mediaSrv.Close()

	_, l, _ := enrichLoop(t, mediaConfig{
		Enabled: true, MaxBytes: 1 << 20, WhisperURL: whisper.URL, VoiceEnabled: false,
	})
	msg := &core.Message{ID: "m1", ChatJID: "tg:1", Content: "v", Attachments: voiceAtt(mediaSrv.URL + "/n.ogg")}
	l.enrichAttachments(context.Background(), msg, "demo")

	if whisperHit {
		t.Error("whisper hit while VoiceEnabled=false")
	}
	if !strings.Contains(msg.Content, "<attachment") || strings.Contains(msg.Content, "transcript=") {
		t.Errorf("want attachment block without transcript, got %q", msg.Content)
	}
}

// --- helper parity (mirror of gateway HTTP-helper tests) ---

func TestDownloadFile_OversizeErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "out.bin")
	if err := downloadFile(context.Background(), srv.URL, dest, nil, 10); err == nil {
		t.Fatal("downloadFile accepted oversize body; would silently truncate")
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Error("oversize download left a partial file behind")
	}
}

func TestDownloadFile_AtLimitSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(strings.Repeat("x", 10)))
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "ok.bin")
	if err := downloadFile(context.Background(), srv.URL, dest, nil, 10); err != nil {
		t.Fatalf("body exactly at limit should succeed: %v", err)
	}
	if data, _ := os.ReadFile(dest); len(data) != 10 {
		t.Errorf("wrote %d bytes, want 10", len(data))
	}
}

func TestTranscribeOnce_Non200ClosesBody(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "boom", 500)
	}))
	defer srv.Close()
	f := filepath.Join(t.TempDir(), "a.ogg")
	os.WriteFile(f, []byte("audio"), 0o644)
	if got := transcribeOnce(context.Background(), srv.URL, "m", f, "", "audio/ogg"); got != "" {
		t.Errorf("transcribeOnce on 500 = %q, want empty", got)
	}
	if got := transcribeOnce(context.Background(), srv.URL, "m", f, "", "audio/ogg"); got != "" {
		t.Errorf("second transcribeOnce = %q, want empty", got)
	}
	if calls != 2 {
		t.Errorf("server saw %d calls, want 2", calls)
	}
}
