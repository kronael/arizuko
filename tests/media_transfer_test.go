package tests

// File + voice transfer integration: inbound (user → routd enrich → agent
// prompt) and outbound (agent tool → routd → adapter). The LLM/container is
// faked (runed.FakeRuntime captures the rendered RunSpec.MessageBatch); the
// adapter, file server, Whisper, and TTS are stub httptest servers. Everything
// between — routd's real poll loop, enrichAttachments, downloadFile, the
// chanreg HTTPChannel multipart egress, the routd turn-callback handlers — is
// production code.
//
// Inbound regression guard: the krons poj.zip bug (2026-06-10) was MEDIA_ENABLED
// never reaching routd in the split (compose wired the media env to runed, which
// reads none of it). These tests assert the real download lands bytes under
// media/<date>/ AND the <attachment path=...> tag appears in the agent prompt —
// so a regression of the enrich path (or its env wiring being defaulted off)
// makes TestMediaInbound_DownloadsAndInlines fail.

import (
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
	routdv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/runed"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/types"
)

// ---- inbound harness: routd (real Loop + enrich) + runed (FakeRuntime) ----

// mediaFed boots routd + runed wired for inbound media enrichment. The
// FakeRuntime captures the rendered prompt (RunSpec.MessageBatch) per turn so a
// test can assert the <attachment> tag the enrich path inlined. groupsDir is a
// temp dir under which enrich writes media/<date>/<file>.
type mediaFed struct {
	authd     *fakeAuthd
	routdDB   *routd.DB
	routdTS   *httptest.Server
	groupsDir string

	mu      sync.Mutex
	batches map[string]string // turnID -> rendered MessageBatch the agent saw
}

func (f *mediaFed) batch(turnID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.batches[turnID]
}

// bootMediaFed stands up the federation with a media config. enabled toggles
// MEDIA_ENABLED; whisperURL "" disables transcription; maxBytes caps downloads.
func bootMediaFed(t *testing.T, enabled bool, whisperURL string, maxBytes int64) *mediaFed {
	t.Helper()
	f := &mediaFed{batches: map[string]string{}}
	f.authd = newFakeAuthd(t)
	f.authd.grant("user:agent", "messages:send:own_group", "chats:read:own_group")
	f.groupsDir = t.TempDir()

	rudb, err := runed.OpenMem()
	if err != nil {
		t.Fatalf("runed.OpenMem: %v", err)
	}
	t.Cleanup(func() { rudb.Close() })

	rt := runed.FakeRuntime{Fn: func(_ context.Context, spec runed.RunSpec) runed.RunResult {
		f.mu.Lock()
		f.batches[spec.TurnID] = spec.MessageBatch
		f.mu.Unlock()
		return runed.RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "sess-" + spec.TurnID}
	}}
	broker := runed.NewStaticBroker("fed.jws", "jti-fed")
	mgr := runed.NewManager(rudb, rt, broker, runed.ManagerConfig{
		Scopes:   []types.Scope{"messages:send:own_group", "chats:read:own_group"},
		Instance: "mediatest",
	})
	runedTS := httptest.NewServer(runed.NewServer(mgr, rudb, newFedVerifier(t, f.authd)).Handler())
	t.Cleanup(runedTS.Close)

	rodb, err := routd.OpenMem()
	if err != nil {
		t.Fatalf("routd.OpenMem: %v", err)
	}
	t.Cleanup(func() { rodb.Close() })
	f.routdDB = rodb

	svcTok := f.authd.mintService(t, "service:routd", "runs:run", "runs:kill")
	runedClient := runedv1.NewClient(runedTS.URL, svcTok, 10*time.Second)

	loop := routd.NewLoop(rodb, runedClient, routd.LoopConfig{
		InstanceName: "mediatest",
		PollEvery:    20 * time.Millisecond,
		RunScopes:    []types.Scope{"messages:send:own_group", "chats:read:own_group"},
		GroupsDir:    f.groupsDir,
		// nil-ish bearer → the enrich download goes out unauthenticated; the
		// stub file server accepts it.
		Media: routd.MediaConfig(enabled, maxBytes, whisperURL, "turbo",
			whisperURL != "", false, func(context.Context) (string, error) { return "", nil }),
	})
	routdSrv := routd.NewServer(rodb, loop, fedDeliverer{}, newFedVerifier(t, f.authd), 0, "https://media.test")
	loop.BindServer(routdSrv)
	f.routdTS = httptest.NewServer(routdSrv.Handler())
	t.Cleanup(f.routdTS.Close)

	if err := rodb.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	if _, err := rodb.AddRoute(core.Route{Match: "", Target: "demo"}); err != nil {
		t.Fatalf("add route: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go loop.Run(ctx)
	return f
}

// fileServer is a stub adapter /files endpoint: serves body for the registered
// path, or status for the error path. Records hit count.
type fileServer struct {
	ts     *httptest.Server
	mu     sync.Mutex
	hits   int
	body   []byte
	status int
}

func newFileServer(t *testing.T, body []byte, status int) *fileServer {
	t.Helper()
	fs := &fileServer{body: body, status: status}
	fs.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fs.mu.Lock()
		fs.hits++
		fs.mu.Unlock()
		if fs.status != 200 {
			w.WriteHeader(fs.status)
			return
		}
		w.Write(fs.body)
	}))
	t.Cleanup(fs.ts.Close)
	return fs
}

func (fs *fileServer) count() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.hits
}

// ingestWithAttachment posts an inbound message carrying one attachment via the
// real /v1/messages service-token path and waits for the agent to run (its
// FakeRuntime captures the prompt). Returns the turn id.
func (f *mediaFed) ingestWithAttachment(t *testing.T, id, content string, att routdv1.Attachment) string {
	t.Helper()
	in := routdv1.Message{
		ID: id, ChatJID: "telegram:user/1", Sender: "telegram:user/1",
		Content: content, Verb: "message", Attachments: []routdv1.Attachment{att},
	}
	rec := postBearer(t, f.routdTS.URL, "POST", "/v1/messages", f.authd.mintAdapter(t, "teled"), "", in)
	if rec.StatusCode != 200 {
		t.Fatalf("ingest status=%d", rec.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.batch(id) == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if f.batch(id) == "" {
		t.Fatalf("agent never ran for turn %s (no MessageBatch captured)", id)
	}
	return id
}

func (f *mediaFed) mediaFiles(t *testing.T) []string {
	t.Helper()
	day := time.Now().Format("20060102")
	dir := filepath.Join(f.groupsDir, "demo", "media", day)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out
}

// ---- inbound tests ----

// TestMediaInbound_DownloadsAndInlines is the krons poj.zip guard: a reachable
// attachment URL → bytes land under media/<date>/ AND the agent prompt carries
// <attachment path=... mime=.../>. A .zip is downloaded AS-IS (not unpacked) so
// the agent can unzip it itself.
func TestMediaInbound_DownloadsAndInlines(t *testing.T) {
	zip := []byte("PK\x03\x04 fake zip bytes")
	fs := newFileServer(t, zip, 200)
	f := bootMediaFed(t, true, "", 20*1024*1024)

	turn := f.ingestWithAttachment(t, "doc.1", "[Document: poj.zip]", routdv1.Attachment{
		Mime: "application/zip", Filename: "poj.zip", URL: fs.ts.URL + "/files/abc",
	})

	if fs.count() == 0 {
		t.Fatal("enrich never hit the file server (download not attempted)")
	}
	files := f.mediaFiles(t)
	if len(files) != 1 {
		t.Fatalf("media dir has %d files, want 1 (the downloaded zip)", len(files))
	}
	if !strings.HasSuffix(files[0], "poj.zip") {
		t.Errorf("downloaded file %q, want it named poj.zip", files[0])
	}
	got, _ := os.ReadFile(files[0])
	if string(got) != string(zip) {
		t.Errorf("downloaded bytes = %q, want the zip AS-IS (documents are not unpacked)", got)
	}
	// The enriched content is rendered into the prompt with XML-escaping
	// (mdToXML escapes < > "), so assert the escaped attachment tag the agent
	// actually receives. The path points into the container media dir.
	prompt := f.batch(turn)
	if !strings.Contains(prompt, "attachment path=") ||
		!strings.Contains(prompt, "/home/node/media/") ||
		!strings.Contains(prompt, "poj.zip") ||
		!strings.Contains(prompt, "application/zip") {
		t.Errorf("agent prompt missing inlined <attachment> tag; got:\n%s", prompt)
	}
}

// TestMediaInbound_DownloadFailureGracefulSkip: a 500 from the file server →
// the attachment is skipped, the turn still runs, the raw [Document] label
// survives in the prompt (no <attachment> tag, no crash).
func TestMediaInbound_DownloadFailureGracefulSkip(t *testing.T) {
	fs := newFileServer(t, nil, 500)
	f := bootMediaFed(t, true, "", 20*1024*1024)

	turn := f.ingestWithAttachment(t, "doc.fail", "[Document: broken.pdf]", routdv1.Attachment{
		Mime: "application/pdf", Filename: "broken.pdf", URL: fs.ts.URL + "/files/x",
	})
	if got := f.mediaFiles(t); len(got) != 0 {
		t.Errorf("media dir should be empty on download failure, got %v", got)
	}
	prompt := f.batch(turn)
	if !strings.Contains(prompt, "[Document: broken.pdf]") {
		t.Errorf("raw label should survive a failed download; got:\n%s", prompt)
	}
	if strings.Contains(prompt, "attachment path=") {
		t.Errorf("no <attachment> tag should be emitted for a failed download; got:\n%s", prompt)
	}
}

// TestMediaInbound_EmptyURLSkipped: an attachment with neither URL nor inline
// data is skipped (the fix logs a WARN; here we assert no file + label survives,
// matching the teled-LISTEN_URL-unset case).
func TestMediaInbound_EmptyURLSkipped(t *testing.T) {
	f := bootMediaFed(t, true, "", 20*1024*1024)
	turn := f.ingestWithAttachment(t, "doc.empty", "[Document: noref.bin]", routdv1.Attachment{
		Mime: "application/octet-stream", Filename: "noref.bin", URL: "",
	})
	if got := f.mediaFiles(t); len(got) != 0 {
		t.Errorf("empty-URL attachment must not write a file, got %v", got)
	}
	if !strings.Contains(f.batch(turn), "[Document: noref.bin]") {
		t.Errorf("raw label should survive an empty-URL skip; got:\n%s", f.batch(turn))
	}
}

// TestMediaInbound_DisabledPassesRawLabel: MEDIA_ENABLED=false → no download,
// the raw label reaches the agent unchanged. This is exactly the krons
// regression state (env never reached routd); the prior test proves enabled
// works, this proves disabled is the only thing that suppresses the download.
func TestMediaInbound_DisabledPassesRawLabel(t *testing.T) {
	fs := newFileServer(t, []byte("data"), 200)
	f := bootMediaFed(t, false, "", 20*1024*1024)

	turn := f.ingestWithAttachment(t, "doc.off", "[Document: x.zip]", routdv1.Attachment{
		Mime: "application/zip", Filename: "x.zip", URL: fs.ts.URL + "/files/x",
	})
	if fs.count() != 0 {
		t.Errorf("MEDIA_ENABLED=false must not download, file server hit %d times", fs.count())
	}
	if got := f.mediaFiles(t); len(got) != 0 {
		t.Errorf("media dir must be empty when disabled, got %v", got)
	}
	if !strings.Contains(f.batch(turn), "[Document: x.zip]") {
		t.Errorf("raw label must pass through when media disabled; got:\n%s", f.batch(turn))
	}
}

// TestMediaInbound_OverSizeSkipped: a body over MEDIA_MAX_FILE_BYTES → the
// download is rejected, the partial file removed, the attachment skipped.
func TestMediaInbound_OverSizeSkipped(t *testing.T) {
	big := make([]byte, 64)
	fs := newFileServer(t, big, 200)
	f := bootMediaFed(t, true, "", 16) // cap below the body size

	turn := f.ingestWithAttachment(t, "doc.big", "[Document: big.bin]", routdv1.Attachment{
		Mime: "application/octet-stream", Filename: "big.bin", URL: fs.ts.URL + "/files/big",
	})
	if got := f.mediaFiles(t); len(got) != 0 {
		t.Errorf("oversize download must leave no file, got %v", got)
	}
	if strings.Contains(f.batch(turn), "attachment path=") {
		t.Errorf("oversize attachment must not be inlined; got:\n%s", f.batch(turn))
	}
}

// TestMediaInbound_VoiceTranscribed: a voice attachment + a stub Whisper →
// transcript inlined into the <attachment ... transcript="..."/> tag.
func TestMediaInbound_VoiceTranscribed(t *testing.T) {
	fs := newFileServer(t, []byte("OggS fake opus"), 200)
	var whisperHits int
	whisper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		whisperHits++
		json.NewEncoder(w).Encode(map[string]string{"text": "hello from the voice note"})
	}))
	t.Cleanup(whisper.Close)

	f := bootMediaFed(t, true, whisper.URL, 20*1024*1024)
	turn := f.ingestWithAttachment(t, "voice.1", "[Voice message]", routdv1.Attachment{
		Mime: "audio/ogg", Filename: "vm.ogg", URL: fs.ts.URL + "/files/v",
	})
	if whisperHits == 0 {
		t.Fatal("Whisper was never called for a voice attachment")
	}
	// The transcript rides inside the (XML-escaped) attachment tag; assert the
	// transcript text + the transcript= attribute are present.
	prompt := f.batch(turn)
	if !strings.Contains(prompt, "transcript=") ||
		!strings.Contains(prompt, "hello from the voice note") {
		t.Errorf("voice transcript not inlined into prompt; got:\n%s", prompt)
	}
}

// ---- outbound: agent tool → routd handler → Deliverer → adapter ----

// outAdapter is a stub platform adapter receiving multipart /send-file and
// /send-voice. It records the last upload's fields + body for assertion.
type outAdapter struct {
	ts   *httptest.Server
	mu   sync.Mutex
	last struct {
		endpoint, jid, filename, caption, replyTo string
		body                                       []byte
	}
	fileHits, voiceHits int
	voiceUnsupported    bool // /send-voice → 501 Unsupported
}

func newOutAdapter(t *testing.T) *outAdapter {
	t.Helper()
	a := &outAdapter{}
	record := func(endpoint string, w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		file, hdr, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "no file", 400)
			return
		}
		defer file.Close()
		buf := new(strings.Builder)
		readMultipartInto(buf, file)
		a.mu.Lock()
		a.last.endpoint = endpoint
		a.last.jid = r.FormValue("chat_jid")
		a.last.filename = hdr.Filename
		a.last.caption = r.FormValue("caption")
		a.last.replyTo = r.FormValue("reply_to")
		a.last.body = []byte(buf.String())
		a.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"id": "platform-" + endpoint})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/send-file", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.fileHits++
		a.mu.Unlock()
		record("send-file", w, r)
	})
	mux.HandleFunc("/send-voice", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.voiceHits++
		unsupported := a.voiceUnsupported
		a.mu.Unlock()
		if unsupported {
			w.WriteHeader(http.StatusNotImplemented)
			json.NewEncoder(w).Encode(map[string]string{"error": "unsupported", "feature": "send_voice"})
			return
		}
		record("send-voice", w, r)
	})
	a.ts = httptest.NewServer(mux)
	t.Cleanup(a.ts.Close)
	return a
}

func readMultipartInto(b *strings.Builder, f multipart.File) {
	buf := make([]byte, 4096)
	for {
		n, err := f.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			return
		}
	}
}

// outboundDeliverer wires a chanreg.Registry with the stub adapter registered
// under "telegram" (owning telegram:) and routd's real chanDeliverer over it.
func outboundDeliverer(t *testing.T, a *outAdapter, caps map[string]bool) routd.Deliverer {
	t.Helper()
	reg := chanreg.New()
	if _, err := reg.Register("telegram", a.ts.URL, []string{"telegram:"}, caps); err != nil {
		t.Fatalf("register stub adapter: %v", err)
	}
	d, _, _ := routd.NewChannelDeliverer(reg, nil, func(string) string { return "telegram" })
	return d
}

// bootOutbound boots a routd Server whose Deliverer routes telegram: jids to the
// stub adapter, and seeds an open turn context so the turn-callback handlers run.
func bootOutbound(t *testing.T, a *outAdapter, caps map[string]bool, tts ...routd.Deliverer) (*httptest.Server, *fakeAuthd) {
	t.Helper()
	authd := newFakeAuthd(t)
	authd.grant("user:agent", "messages:send:own_group")
	rodb, err := routd.OpenMem()
	if err != nil {
		t.Fatalf("routd.OpenMem: %v", err)
	}
	t.Cleanup(func() { rodb.Close() })
	loop := routd.NewLoop(rodb, nil, routd.LoopConfig{InstanceName: "out"})
	srv := routd.NewServer(rodb, loop, outboundDeliverer(t, a, caps), newFedVerifier(t, authd), 0, "https://out.test")
	loop.BindServer(srv)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	if _, err := rodb.PutTurnContext("turn-out", "demo", "", "telegram:user/9", "u1", ""); err != nil {
		t.Fatalf("put turn context: %v", err)
	}
	return ts, authd
}

// TestOutboundDocument_AdapterReceivesMultipart: POST /v1/turns/{id}/document
// (what send_file forwards to) → the stub adapter receives the file with the
// right filename, caption, jid, and bytes.
func TestOutboundDocument_AdapterReceivesMultipart(t *testing.T) {
	a := newOutAdapter(t)
	ts, authd := bootOutbound(t, a, map[string]bool{"send_text": true, "send_file": true})

	tmp := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(tmp, []byte("# report body"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	req := routdv1.DocumentRequest{JID: "telegram:user/9", Path: tmp, Name: "report.md", Caption: "here you go"}
	rec := postBearer(t, ts.URL, "POST", "/v1/turns/turn-out/document",
		authd.mintUser(t, "user:agent", "demo"), "doc-idem-1", req)
	if rec.StatusCode != 200 {
		t.Fatalf("document status=%d, want 200", rec.StatusCode)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fileHits != 1 {
		t.Fatalf("adapter /send-file hits = %d, want 1", a.fileHits)
	}
	if a.last.jid != "telegram:user/9" || a.last.filename != "report.md" || a.last.caption != "here you go" {
		t.Errorf("adapter got jid=%q file=%q caption=%q", a.last.jid, a.last.filename, a.last.caption)
	}
	if string(a.last.body) != "# report body" {
		t.Errorf("adapter got body %q, want the file bytes", a.last.body)
	}
}

// TestOutboundSendVoice_TTSDisabled: POST /send_voice with TTS off →
// Unsupported (the handler relays the tts.go refusal); the adapter is never
// touched.
func TestOutboundSendVoice_TTSDisabled(t *testing.T) {
	a := newOutAdapter(t)
	ts, authd := bootOutbound(t, a, map[string]bool{"send_text": true, "send_voice": true})

	req := routdv1.VoiceRequest{JID: "telegram:user/9", Text: "say this aloud"}
	rec := postBearer(t, ts.URL, "POST", "/v1/turns/turn-out/send_voice",
		authd.mintUser(t, "user:agent", "demo"), "", req)
	// The send_voice handler relays a non-2xx for an unsupported feature.
	if rec.StatusCode == 200 {
		t.Fatalf("send_voice with TTS disabled returned 200, want a refusal")
	}
	if a.voiceHits != 0 {
		t.Errorf("adapter /send-voice must not be hit when TTS is off, hits=%d", a.voiceHits)
	}
}

// TestOutboundSendVoice_TTSStubSynthesizes: POST /send_voice with a stub TTS →
// the synthesized audio reaches the adapter's /send-voice as multipart.
func TestOutboundSendVoice_TTSStubSynthesizes(t *testing.T) {
	a := newOutAdapter(t)
	var ttsHits int
	tts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ttsHits++
		w.Write([]byte("OggS synthesized opus"))
	}))
	t.Cleanup(tts.Close)

	authd := newFakeAuthd(t)
	authd.grant("user:agent", "messages:send:own_group")
	rodb, err := routd.OpenMem()
	if err != nil {
		t.Fatalf("routd.OpenMem: %v", err)
	}
	t.Cleanup(func() { rodb.Close() })
	loop := routd.NewLoop(rodb, nil, routd.LoopConfig{InstanceName: "out"})
	srv := routd.NewServer(rodb, loop,
		outboundDeliverer(t, a, map[string]bool{"send_text": true, "send_voice": true}),
		newFedVerifier(t, authd), 0, "https://out.test")
	srv.SetTTS(routd.TTSConfig(true, tts.URL, "af_bella", "kokoro", 10*time.Second, t.TempDir()))
	loop.BindServer(srv)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	if _, err := rodb.PutTurnContext("turn-out", "demo", "", "telegram:user/9", "u1", ""); err != nil {
		t.Fatalf("put turn context: %v", err)
	}

	req := routdv1.VoiceRequest{JID: "telegram:user/9", Text: "say this aloud"}
	rec := postBearer(t, ts.URL, "POST", "/v1/turns/turn-out/send_voice",
		authd.mintUser(t, "user:agent", "demo"), "", req)
	if rec.StatusCode != 200 {
		t.Fatalf("send_voice with TTS enabled status=%d, want 200", rec.StatusCode)
	}
	if ttsHits == 0 {
		t.Fatal("TTS service was never called")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.voiceHits != 1 {
		t.Fatalf("adapter /send-voice hits = %d, want 1", a.voiceHits)
	}
	if string(a.last.body) != "OggS synthesized opus" {
		t.Errorf("adapter got voice body %q, want the synthesized audio", a.last.body)
	}
}

// ---- outbound chanreg-level: cap gating + nonexistent path ----

// TestOutboundChan_SendFileNonexistentPath: SendFile with a path that doesn't
// exist → error, and (since the open fails before any POST) the adapter is
// never hit.
func TestOutboundChan_SendFileNonexistentPath(t *testing.T) {
	a := newOutAdapter(t)
	reg := chanreg.New()
	if _, err := reg.Register("telegram", a.ts.URL, []string{"telegram:"},
		map[string]bool{"send_file": true}); err != nil {
		t.Fatalf("register: %v", err)
	}
	ch := chanreg.NewHTTPChannel(reg.Resolve("telegram", "telegram:user/9"), func(context.Context) (string, error) { return "", nil })
	_, err := ch.SendFile("telegram:user/9", "/no/such/file.bin", "x.bin", "", "", "")
	if err == nil {
		t.Fatal("SendFile with a nonexistent path must error")
	}
	if a.fileHits != 0 {
		t.Errorf("adapter must not be hit for a nonexistent file, hits=%d", a.fileHits)
	}
}

// TestOutboundChan_NoCapUnsupported: an adapter that doesn't advertise send_file
// / send_voice → Unsupported, no network call.
func TestOutboundChan_NoCapUnsupported(t *testing.T) {
	a := newOutAdapter(t)
	reg := chanreg.New()
	if _, err := reg.Register("telegram", a.ts.URL, []string{"telegram:"},
		map[string]bool{"send_text": true}); err != nil { // NO send_file / send_voice
		t.Fatalf("register: %v", err)
	}
	ch := chanreg.NewHTTPChannel(reg.Resolve("telegram", "telegram:user/9"), func(context.Context) (string, error) { return "", nil })

	tmp := filepath.Join(t.TempDir(), "f.bin")
	os.WriteFile(tmp, []byte("x"), 0o644)
	if _, err := ch.SendFile("telegram:user/9", tmp, "f.bin", "", "", ""); err == nil {
		t.Error("SendFile must be unsupported when cap absent")
	}
	if _, err := ch.SendVoice("telegram:user/9", tmp, "", ""); err == nil {
		t.Error("SendVoice must be unsupported when cap absent")
	}
	if a.fileHits != 0 || a.voiceHits != 0 {
		t.Errorf("no network call when cap absent, file=%d voice=%d", a.fileHits, a.voiceHits)
	}
}
