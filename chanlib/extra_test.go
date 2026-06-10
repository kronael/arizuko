package chanlib

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// EnvInt / EnvDur / EnvBytes / ShortHash
// ---------------------------------------------------------------------------

func TestEnvInt(t *testing.T) {
	k := "CHANLIB_TEST_INT"
	os.Unsetenv(k)
	if got := EnvInt(k, 42); got != 42 {
		t.Errorf("unset: got %d, want 42", got)
	}
	os.Setenv(k, "7")
	defer os.Unsetenv(k)
	if got := EnvInt(k, 42); got != 7 {
		t.Errorf("set: got %d, want 7", got)
	}
	os.Setenv(k, "abc")
	if got := EnvInt(k, 42); got != 42 {
		t.Errorf("invalid: got %d, want fallback 42", got)
	}
}

func TestEnvDur(t *testing.T) {
	k := "CHANLIB_TEST_DUR"
	os.Unsetenv(k)
	if got := EnvDur(k, 5*time.Second); got != 5*time.Second {
		t.Errorf("unset: got %v, want 5s", got)
	}
	// 2000 ms → 2s
	os.Setenv(k, "2000")
	defer os.Unsetenv(k)
	if got := EnvDur(k, time.Second); got != 2*time.Second {
		t.Errorf("set: got %v, want 2s", got)
	}
	os.Setenv(k, "bad")
	if got := EnvDur(k, 3*time.Second); got != 3*time.Second {
		t.Errorf("invalid: got %v, want fallback 3s", got)
	}
}

func TestEnvBytes(t *testing.T) {
	k := "CHANLIB_TEST_BYTES"
	os.Unsetenv(k)
	if got := EnvBytes(k, 1024); got != 1024 {
		t.Errorf("unset: got %d, want 1024", got)
	}
	os.Setenv(k, "4096")
	defer os.Unsetenv(k)
	if got := EnvBytes(k, 1024); got != 4096 {
		t.Errorf("set: got %d, want 4096", got)
	}
	os.Setenv(k, "0")
	if got := EnvBytes(k, 999); got != 999 {
		t.Errorf("zero: got %d, want fallback 999", got)
	}
	os.Setenv(k, "notanumber")
	if got := EnvBytes(k, 999); got != 999 {
		t.Errorf("invalid: got %d, want fallback 999", got)
	}
}

func TestShortHash(t *testing.T) {
	if got := ShortHash(""); got != "" {
		t.Errorf("empty: got %q, want \"\"", got)
	}
	a := ShortHash("hello")
	b := ShortHash("hello")
	if a != b {
		t.Errorf("same input: %q != %q", a, b)
	}
	if len(a) != 8 { // 4 bytes → 8 hex chars
		t.Errorf("len = %d, want 8", len(a))
	}
	c := ShortHash("world")
	if a == c {
		t.Errorf("different inputs produced same hash")
	}
}

// ---------------------------------------------------------------------------
// NoSocial / NoPinSupport — every verb returns ErrUnsupported
// ---------------------------------------------------------------------------

func TestNoSocialAllVerbs(t *testing.T) {
	ns := NoSocial{}
	var errs []error
	_, e := ns.Post(PostRequest{})
	errs = append(errs, e)
	errs = append(errs, ns.Like(LikeRequest{}))
	errs = append(errs, ns.Delete(DeleteRequest{}))
	_, e = ns.Forward(ForwardRequest{})
	errs = append(errs, e)
	_, e = ns.Quote(QuoteRequest{})
	errs = append(errs, e)
	_, e = ns.Repost(RepostRequest{})
	errs = append(errs, e)
	errs = append(errs, ns.Dislike(DislikeRequest{}))
	errs = append(errs, ns.Edit(EditRequest{}))
	errs = append(errs, ns.Pin(PinRequest{}))
	errs = append(errs, ns.Unpin(UnpinRequest{}))
	for i, err := range errs {
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("verb[%d]: want ErrUnsupported, got %v", i, err)
		}
	}
}

func TestNoPinSupport(t *testing.T) {
	np := NoPinSupport{}
	if !errors.Is(np.Pin(PinRequest{}), ErrUnsupported) {
		t.Error("NoPinSupport.Pin should return ErrUnsupported")
	}
	if !errors.Is(np.Unpin(UnpinRequest{}), ErrUnsupported) {
		t.Error("NoPinSupport.Unpin should return ErrUnsupported")
	}
}

// ---------------------------------------------------------------------------
// CapImplReport — cap↔impl drift detection
// ---------------------------------------------------------------------------

// fakeBot has all-Unsupported verbs (via NoSocial/NoVoiceSender/NoFileSender)
// except the ones we override below to be "real" (return a non-Unsupported
// error). It is NOT a HistoryProvider, so fetch_history probes as a stub.
type fakeBot struct {
	NoSocial
	NoVoiceSender
	NoFileSender
}

func (fakeBot) Send(SendRequest) (string, error) { return "", nil }
func (fakeBot) Typing(string, bool)              {}

// realDislikeBot overrides Dislike with a real impl (non-Unsupported error).
type realDislikeBot struct{ fakeBot }

func (realDislikeBot) Dislike(DislikeRequest) error { return errors.New("network") }

func TestCapImplReport_AdvertisedStubFlagged(t *testing.T) {
	// Advertise dislike on an all-stub bot → must be flagged.
	drift := CapImplReport(fakeBot{}, map[string]bool{"dislike": true})
	if len(drift) == 0 {
		t.Fatal("advertising a stub verb should report drift")
	}
	if !strings.Contains(strings.Join(drift, "\n"), "dislike: advertised but verb returns Unsupported") {
		t.Errorf("drift = %v", drift)
	}
}

func TestCapImplReport_RealUnadvertisedFlagged(t *testing.T) {
	// Real dislike impl but cap not advertised → must be flagged.
	drift := CapImplReport(realDislikeBot{}, map[string]bool{})
	joined := strings.Join(drift, "\n")
	if !strings.Contains(joined, "dislike: verb implemented but not advertised") {
		t.Errorf("expected dislike unadvertised drift, got %v", drift)
	}
}

func TestCapImplReport_ConsistentIsEmpty(t *testing.T) {
	// All-stub bot advertising nothing gated → no drift.
	if drift := CapImplReport(fakeBot{}, map[string]bool{}); len(drift) != 0 {
		t.Errorf("all-stub + no caps should be clean, got %v", drift)
	}
}

// ---------------------------------------------------------------------------
// writeBotResult — structured UnsupportedError encoding (non-Post verbs)
// ---------------------------------------------------------------------------

func newMuxWith(bot BotHandler) *http.ServeMux {
	return NewAdapterMux("t", []string{"t:"}, bot,
		func() bool { return true }, func() int64 { return time.Now().Unix() })
}

type fullBot struct {
	NoFileSender
	NoVoiceSender
	likeErr    error
	deleteErr  error
	forwardErr error
	quoteErr   error
	repostErr  error
	dislikeErr error
	editErr    error
	pinErr     error
	unpinErr   error
}

func (b *fullBot) Send(SendRequest) (string, error) { return "", nil }
func (b *fullBot) Typing(string, bool)              {}
func (b *fullBot) Post(PostRequest) (string, error) { return "", nil }
func (b *fullBot) Like(LikeRequest) error           { return b.likeErr }
func (b *fullBot) Delete(DeleteRequest) error       { return b.deleteErr }
func (b *fullBot) Forward(ForwardRequest) (string, error) {
	return "", b.forwardErr
}
func (b *fullBot) Quote(QuoteRequest) (string, error)   { return "", b.quoteErr }
func (b *fullBot) Repost(RepostRequest) (string, error) { return "", b.repostErr }
func (b *fullBot) Dislike(DislikeRequest) error         { return b.dislikeErr }
func (b *fullBot) Edit(EditRequest) error               { return b.editErr }
func (b *fullBot) Pin(PinRequest) error                 { return b.pinErr }
func (b *fullBot) Unpin(UnpinRequest) error             { return b.unpinErr }

func postJSON(h http.Handler, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer sec")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestHandlerLike_UnsupportedStructured(t *testing.T) {
	bot := &fullBot{likeErr: Unsupported("like", "testplt", "no reactions")}
	h := newMuxWith(bot)
	w := postJSON(h, "/like", map[string]string{"chat_jid": "t:1", "target_id": "msg1", "reaction": "👍"})
	if w.Code != 501 {
		t.Fatalf("status = %d, want 501", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["tool"] != "like" || resp["platform"] != "testplt" {
		t.Errorf("body = %v", resp)
	}
}

func TestHandlerDelete_Unsupported(t *testing.T) {
	bot := &fullBot{deleteErr: ErrUnsupported}
	h := newMuxWith(bot)
	w := postJSON(h, "/delete", map[string]string{"chat_jid": "t:1", "target_id": "m1"})
	if w.Code != 501 {
		t.Fatalf("status = %d, want 501", w.Code)
	}
}

func TestHandlerForward_Success(t *testing.T) {
	bot := &fullBot{}
	h := newMuxWith(bot)
	w := postJSON(h, "/forward", map[string]string{"source_msg_id": "s1", "target_jid": "t:2"})
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandlerForward_MissingFields(t *testing.T) {
	bot := &fullBot{}
	h := newMuxWith(bot)
	w := postJSON(h, "/forward", map[string]string{"source_msg_id": "s1"})
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandlerQuote_Unsupported(t *testing.T) {
	bot := &fullBot{quoteErr: Unsupported("quote", "plt", "")}
	h := newMuxWith(bot)
	w := postJSON(h, "/quote", map[string]string{"chat_jid": "t:1", "source_msg_id": "m1", "comment": "hi"})
	if w.Code != 501 {
		t.Fatalf("status = %d, want 501", w.Code)
	}
}

func TestHandlerRepost_Success(t *testing.T) {
	bot := &fullBot{}
	h := newMuxWith(bot)
	w := postJSON(h, "/repost", map[string]string{"chat_jid": "t:1", "source_msg_id": "m1"})
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandlerDislike_UnsupportedStructured(t *testing.T) {
	bot := &fullBot{dislikeErr: Unsupported("dislike", "plt", "use like(👎)")}
	h := newMuxWith(bot)
	w := postJSON(h, "/dislike", map[string]string{"chat_jid": "t:1", "target_id": "m1"})
	if w.Code != 501 {
		t.Fatalf("status = %d, want 501", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["hint"] != "use like(👎)" {
		t.Errorf("hint = %v", resp["hint"])
	}
}

func TestHandlerEdit_MissingContent(t *testing.T) {
	bot := &fullBot{}
	h := newMuxWith(bot)
	w := postJSON(h, "/edit", map[string]string{"chat_jid": "t:1", "target_id": "m1"})
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandlerPin_Success(t *testing.T) {
	bot := &fullBot{}
	h := newMuxWith(bot)
	w := postJSON(h, "/pin", map[string]string{"chat_jid": "t:1", "target_id": "m1"})
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandlerUnpin_AllMode(t *testing.T) {
	bot := &fullBot{}
	h := newMuxWith(bot)
	w := postJSON(h, "/unpin", map[string]any{"chat_jid": "t:1", "all": true})
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandlerUnpin_MissingTargetWithoutAll(t *testing.T) {
	bot := &fullBot{}
	h := newMuxWith(bot)
	w := postJSON(h, "/unpin", map[string]string{"chat_jid": "t:1"})
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// ---------------------------------------------------------------------------
// handleHistory — HistoryProvider wired by NewAdapterMux
// ---------------------------------------------------------------------------

type histBot struct {
	fullBot
	resp HistoryResponse
	err  error
}

func (b *histBot) FetchHistory(HistoryRequest) (HistoryResponse, error) {
	return b.resp, b.err
}

func TestHandlerHistory_Success(t *testing.T) {
	bot := &histBot{resp: HistoryResponse{
		Source:   "platform",
		Messages: []InboundMsg{{ID: "m1", Content: "hi"}},
	}}
	h := newMuxWith(bot)
	req := httptest.NewRequest("GET", "/v1/history?jid=test:1", nil)
	req.Header.Set("Authorization", "Bearer sec")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp HistoryResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Source != "platform" || len(resp.Messages) != 1 {
		t.Errorf("resp = %+v", resp)
	}
}

func TestHandlerHistory_MissingJID(t *testing.T) {
	bot := &histBot{}
	h := newMuxWith(bot)
	req := httptest.NewRequest("GET", "/v1/history", nil)
	req.Header.Set("Authorization", "Bearer sec")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandlerHistory_InvalidBefore(t *testing.T) {
	bot := &histBot{}
	h := newMuxWith(bot)
	req := httptest.NewRequest("GET", "/v1/history?jid=t:1&before=notadate", nil)
	req.Header.Set("Authorization", "Bearer sec")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandlerHistory_NotWiredForNonHistoryBot(t *testing.T) {
	// A bot without FetchHistory should not expose /v1/history
	bot := &fullBot{}
	h := newMuxWith(bot)
	req := httptest.NewRequest("GET", "/v1/history?jid=t:1", nil)
	req.Header.Set("Authorization", "Bearer sec")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	// ServeMux returns 405 or 404 when no handler matches.
	if w.Code == 200 {
		t.Fatal("expected non-200 when history not wired")
	}
}

// ---------------------------------------------------------------------------
// FileProxyHandler
// ---------------------------------------------------------------------------

func TestFileProxyHandler_Success(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("fakepng"))
	}))
	defer upstream.Close()

	cache := NewURLCache(10)
	id := cache.Put(upstream.URL)

	h := FileProxyHandler(FileProxyOpts{Resolve: cache.Get})
	req := httptest.NewRequest("GET", "/files/"+id, nil)
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(string(w.Body.Bytes()), "fakepng") {
		t.Errorf("body = %q", w.Body.Bytes())
	}
	if w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("content-type = %q", w.Header().Get("Content-Type"))
	}
}

func TestFileProxyHandler_NotFound(t *testing.T) {
	h := FileProxyHandler(FileProxyOpts{Resolve: func(string) (string, bool) { return "", false }})
	req := httptest.NewRequest("GET", "/files/nope", nil)
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != 404 {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestFileProxyHandler_EmptyID(t *testing.T) {
	h := FileProxyHandler(FileProxyOpts{Resolve: func(string) (string, bool) { return "", true }})
	req := httptest.NewRequest("GET", "/files/", nil)
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestFileProxyHandler_PanicOnNilResolve(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil Resolve")
		}
	}()
	FileProxyHandler(FileProxyOpts{})
}

// ---------------------------------------------------------------------------
// ProxyFile — upstream non-200 → 502
// ---------------------------------------------------------------------------

func TestProxyFile_Non200Upstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	w := httptest.NewRecorder()
	ProxyFile(w, resp, 0)
	if w.Code != 502 {
		t.Fatalf("status = %d, want 502", w.Code)
	}
}

// ---------------------------------------------------------------------------
// HealthCheck custom thresholds (email name → 10-min stale, reddit → 60-min)
// ---------------------------------------------------------------------------

func TestHandlerHealthEmailThreshold(t *testing.T) {
	// email: stale after 10min. 6min old → NOT stale.
	last := time.Now().Add(-6 * time.Minute).Unix()
	h := NewAdapterMux("email", []string{"email:"}, &mockBot{},
		func() bool { return true }, func() int64 { return last })
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] == "stale" {
		t.Error("email 6-min-old should not be stale (threshold=10min)")
	}
}

func TestHandlerHealthDefaultThreshold(t *testing.T) {
	// default: stale after 5min. 6min old → stale.
	last := time.Now().Add(-6 * time.Minute).Unix()
	h := NewAdapterMux("telegram", []string{"telegram:"}, &mockBot{},
		func() bool { return true }, func() int64 { return last })
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "stale" {
		t.Error("telegram 6-min-old should be stale (threshold=5min)")
	}
	// Non-strict adapters keep stale as an informational 200.
	if w.Code != 200 {
		t.Errorf("telegram stale code = %d, want 200 (non-strict)", w.Code)
	}
}

// strictStale adapters (slack) return 503 when stale so Docker's healthcheck
// can mark the container unhealthy — the 2026-06-05 outage regression guard.
func TestHandlerHealthStrictStaleReturns503(t *testing.T) {
	last := time.Now().Add(-6 * time.Minute).Unix()
	h := NewAdapterMux("slack", []string{"slack:"}, &mockBot{},
		func() bool { return true }, func() int64 { return last })
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatalf("slack stale code = %d, want 503", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "stale" {
		t.Errorf("status = %v, want stale (body must still diagnose staleness)", resp["status"])
	}
	if _, ok := resp["stale_seconds"]; !ok {
		t.Error("stale_seconds missing from strict-stale body")
	}
}

// A fresh strict-stale adapter still returns 200 — the happy path must not regress.
func TestHandlerHealthStrictFreshReturns200(t *testing.T) {
	last := time.Now().Unix()
	h := NewAdapterMux("slack", []string{"slack:"}, &mockBot{},
		func() bool { return true }, func() int64 { return last })
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("slack fresh code = %d, want 200", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("status = %v, want ok", resp["status"])
	}
}
