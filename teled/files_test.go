package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// toServer sends every Telegram request to a local server instead.
type toServer struct {
	target *url.URL
	next   http.RoundTripper
}

func (rt toServer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = rt.target.Scheme, rt.target.Host
	return rt.next.RoundTrip(req)
}

func redirectTelegram(t *testing.T, serverURL string) {
	t.Helper()
	target, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	prod := httpClient
	httpClient = &http.Client{Transport: toServer{target: target, next: prod.Transport}}
	t.Cleanup(func() { httpClient = prod })
}

func getFile(t *testing.T, token string) *httptest.ResponseRecorder {
	t.Helper()
	s := newServer(config{Name: "telegram", TelegramToken: token}, []string{"telegram:1/"}, &stubBot{}, nil,
		func() bool { return true }, func() int64 { return time.Now().Unix() })
	req := httptest.NewRequest("GET", "/files/abc", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	return w
}

// A large file proxied from Telegram must not be cut at a fixed time: only the
// response headers of each Telegram call are timed.
func TestTelegramFileClientTimesHeadersNotTheBody(t *testing.T) {
	if httpClient.Timeout != 0 {
		t.Fatalf("httpClient.Timeout = %v cuts every proxied body at that age", httpClient.Timeout)
	}
	transport, ok := httpClient.Transport.(*http.Transport)
	if !ok || transport.ResponseHeaderTimeout != telegramHeaderTimeout {
		t.Fatalf("Telegram calls are not bounded by a header timeout: %#v", httpClient.Transport)
	}
}

// Telegram refuses a file over its 20 MB getFile limit with 400. routd must see
// a 4xx, which it does not retry; an outage stays a 502, which it does.
func TestHandleFile_ATelegramRefusalStaysA4xx(t *testing.T) {
	for upstream, want := range map[int]int{400: 400, 429: 502, 502: 502, 504: 502} {
		t.Run(strconv.Itoa(upstream), func(t *testing.T) {
			tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, `{"ok":false,"description":"Bad Request: file is too big"}`, upstream)
			}))
			defer tg.Close()
			redirectTelegram(t, tg.URL)
			if got := getFile(t, "tok").Code; got != want {
				t.Errorf("Telegram %d: /files answered %d, want %d", upstream, got, want)
			}
		})
	}
}

// The krons timeout of 2026-11-13 left no trace in teled. A failed step is
// logged with its time, and a transport error, whose URL holds the bot token,
// is logged without that URL.
func TestHandleFile_LogsTheFailedStepWithoutTheToken(t *testing.T) {
	tg := httptest.NewServer(http.NotFoundHandler())
	tg.Close()
	redirectTelegram(t, tg.URL)
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if got := getFile(t, "123456:SECRET-BOT-TOKEN").Code; got != 502 {
		t.Fatalf("status = %d, want 502", got)
	}
	out := logs.String()
	if !strings.Contains(out, "file proxy failed") || !strings.Contains(out, `step="getFile failed"`) ||
		!strings.Contains(out, "elapsed=") {
		t.Errorf("the failed step is not logged: %s", out)
	}
	if strings.Contains(out, "SECRET-BOT-TOKEN") {
		t.Errorf("the log carries the bot token: %s", out)
	}
}
