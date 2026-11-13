package routd

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

func setFetchTimeouts(t *testing.T, header, bodyBase time.Duration) {
	t.Helper()
	oldHeader, oldBody := fetchHeaderTimeout, fetchBodyBase
	fetchHeaderTimeout, fetchBodyBase = header, bodyBase
	t.Cleanup(func() { fetchHeaderTimeout, fetchBodyBase = oldHeader, oldBody })
}

// slowBody answers at once and then sends n bytes over the given time.
func slowBody(n int, over time.Duration, contentLength int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if contentLength > 0 {
			w.Header().Set("Content-Length", strconv.Itoa(contentLength))
		}
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		for range n {
			time.Sleep(over / time.Duration(n))
			_, _ = w.Write([]byte("x"))
			w.(http.Flusher).Flush()
		}
	}
}

// The TTS client's whole-request timeout once cut a 4.5 MB Telegram file: an
// attachment download must be bounded by its own header timeout, and a body
// that keeps arriving must not be cut by the TTS client's budget.
func TestDownloadFile_ALongBodyOutlivesTheTTSClientTimeout(t *testing.T) {
	setFetchTimeouts(t, time.Second, 5*time.Second)
	tts := httpClient
	httpClient = &http.Client{Timeout: 50 * time.Millisecond}
	t.Cleanup(func() { httpClient = tts })
	srv := httptest.NewServer(slowBody(10, 300*time.Millisecond, 0))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "long.bin")
	if err := downloadFile(context.Background(), srv.URL, dest, nil, 1<<20); err != nil {
		t.Fatalf("a body still arriving was cut: %v", err)
	}
	if data, _ := os.ReadFile(dest); len(data) != 10 {
		t.Errorf("wrote %d bytes, want 10", len(data))
	}
}

// A server that sends no headers fails the download after the header timeout,
// as a transient error, after exactly one request: the turn retries it, not
// the download.
func TestDownloadFile_NoHeadersIsOneTransientAttempt(t *testing.T) {
	setFetchTimeouts(t, 100*time.Millisecond, time.Second)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	start := time.Now()
	err := downloadFile(context.Background(), srv.URL, filepath.Join(t.TempDir(), "x"), nil, 1<<20)
	if !errors.Is(err, errFetchTimeout) {
		t.Fatalf("err = %v, want the header timeout", err)
	}
	if errors.Is(err, errFetchPermanent) {
		t.Errorf("a timeout was classed permanent: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("the header timeout took %v", elapsed)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}

// The body deadline grows with the announced size: a body slower than the base
// budget but within its size's allowance completes, and a stalled body of the
// same size is cut as a transient timeout.
func TestDownloadFile_BodyDeadlineScalesWithSize(t *testing.T) {
	setFetchTimeouts(t, time.Second, 100*time.Millisecond)
	size := 2 * fetchBodyRate // two more seconds of budget
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(size))
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(400 * time.Millisecond)
		_, _ = w.Write([]byte(strings.Repeat("x", size)))
	}))
	defer srv.Close()
	if err := downloadFile(context.Background(), srv.URL, filepath.Join(t.TempDir(), "big"), nil, 1<<20); err != nil {
		t.Fatalf("a body within its size's allowance was cut: %v", err)
	}

	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer stalled.Close()
	err := downloadFile(context.Background(), stalled.URL, filepath.Join(t.TempDir(), "small"), nil, 1<<20)
	if !errors.Is(err, errFetchTimeout) || errors.Is(err, errFetchPermanent) {
		t.Fatalf("a stalled body: err = %v, want a transient timeout", err)
	}
}

func TestDownloadFile_A4xxIsPermanentAnd5xxTransient(t *testing.T) {
	for code, permanent := range map[int]bool{404: true, 400: true, 429: false, 502: false, 503: false} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))
		err := downloadFile(context.Background(), srv.URL, filepath.Join(t.TempDir(), "x"), nil, 1<<20)
		srv.Close()
		if err == nil || errors.Is(err, errFetchPermanent) != permanent {
			t.Errorf("HTTP %d: err = %v, want permanent=%v", code, err, permanent)
		}
	}
}

func attachmentRoutd(t *testing.T) (*DB, *Server, *stubRunner, string) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	groups := t.TempDir()
	if err := os.MkdirAll(filepath.Join(groups, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &stubRunner{t: t}
	loop := NewLoop(db, runner, LoopConfig{GroupsDir: groups,
		Media: mediaConfig{Enabled: true, MaxBytes: 1 << 20}})
	loop.StopQueue()
	srv := NewServer(db, loop, nil, nil, DefaultEngagementTTL, "https://example.test")
	runner.srv = srv
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	doJSON(t, srv.Handler(), "PUT", "/v1/routes", "", map[string]any{"routes": []apiv1.Route{{Seq: 0, Match: "platform=telegram", Target: "demo"}}})
	return db, srv, runner, groups
}

func pdf(url string) apiv1.Attachment {
	return apiv1.Attachment{URL: url, Mime: "application/pdf", Filename: "zmena.pdf"}
}

func sendAttachments(t *testing.T, srv *Server, atts []apiv1.Attachment, sent time.Time) {
	t.Helper()
	rec := doJSON(t, srv.Handler(), "POST", "/v1/messages", "", apiv1.Message{
		ID: "m1", ChatJID: "telegram:group/1", Sender: "telegram:user/1", Content: "the contract",
		Verb: "message", Timestamp: sent.Unix(),
		Attachments: atts,
	})
	if rec.Code != 200 {
		t.Fatalf("ingest status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// Measured on krons 2026-11-13: Telegram took over 30 s to hand teled a 4.5 MB
// PDF, routd wrote "the file could not be fetched" into the turn, and the agent
// asked the user to send it again. A fetch that times out must instead leave
// the message untouched and re-feed the turn; the retry fetches the file and
// the agent gets it, beside a sibling fetched on both attempts under its own
// name.
func TestTurn_ASlowAttachmentDefersTheTurnUntilItArrives(t *testing.T) {
	setFetchTimeouts(t, 100*time.Millisecond, time.Second)
	var requests atomic.Int32
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/files/cover" {
			_, _ = w.Write([]byte("PNG"))
			return
		}
		if requests.Add(1) == 1 {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			return
		}
		_, _ = w.Write([]byte("%PDF"))
	}))
	defer files.Close()
	db, srv, runner, groups := attachmentRoutd(t)
	cover := apiv1.Attachment{URL: files.URL + "/files/cover", Mime: "image/png", Filename: "cover.png"}
	sendAttachments(t, srv, []apiv1.Attachment{cover, pdf(files.URL + "/files/abc")}, time.Now())
	cursorBefore := db.GetAgentCursor("telegram:group/1")

	if _, err := srv.loop.processGroupMessages("telegram:group/1"); err != nil {
		t.Fatalf("a deferred fetch surfaced as an error: %v", err)
	}
	if runner.gotTurn != "" {
		t.Fatalf("the turn ran without its file: %q", runner.gotBatch)
	}
	if got := db.GetAgentCursor("telegram:group/1"); got != cursorBefore {
		t.Fatalf("cursor advanced past the deferred message: %q", got)
	}

	expireBusyWait(t, srv.loop, "telegram:group/1")
	if _, err := srv.loop.processGroupMessages("telegram:group/1"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	batch := html.UnescapeString(runner.gotBatch)
	if !strings.Contains(batch, "<attachment path=") || strings.Contains(batch, "unavailable=") {
		t.Fatalf("the retried turn does not carry the file: %q", runner.gotBatch)
	}
	if n := requests.Load(); n != 2 {
		t.Errorf("file requests = %d, want 2", n)
	}
	media, _ := filepath.Glob(filepath.Join(groups, "demo", "media", "*", "*"))
	if len(media) != 2 || filepath.Base(media[0]) != "cover.png" || filepath.Base(media[1]) != "zmena.pdf" {
		t.Errorf("media files = %v, want cover.png and zmena.pdf", media)
	}
}

// A permanent refusal is not retried: the turn runs at once and the agent is
// told the file could not be fetched.
func TestTurn_APermanentlyRefusedAttachmentRunsTheTurnAtOnce(t *testing.T) {
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer files.Close()
	_, srv, runner, _ := attachmentRoutd(t)
	sendAttachments(t, srv, []apiv1.Attachment{pdf(files.URL + "/files/gone")}, time.Now())

	if _, err := srv.loop.processGroupMessages("telegram:group/1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html.UnescapeString(runner.gotBatch), `unavailable="the file could not be fetched"`) {
		t.Fatalf("the agent was not told the file is unavailable: %q", runner.gotBatch)
	}
}

// A message whose file stayed unreachable for fetchGiveUp runs with a marker
// that says how long it was tried, so the chat is not held forever.
func TestTurn_AnAttachmentStillUnreachableAfterTheGiveUpRunsTheTurn(t *testing.T) {
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer files.Close()
	_, srv, runner, _ := attachmentRoutd(t)
	sendAttachments(t, srv, []apiv1.Attachment{pdf(files.URL + "/files/abc")}, time.Now().Add(-fetchGiveUp-time.Minute))

	if _, err := srv.loop.processGroupMessages("telegram:group/1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runner.gotBatch, "could not be fetched within 30 minutes") {
		t.Fatalf("the turn did not run with the give-up marker: %q", runner.gotBatch)
	}
}
