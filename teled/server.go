package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanlib/chanstore"
)

const telegramHeaderTimeout = 20 * time.Second

// httpClient serves /files. Each Telegram call waits telegramHeaderTimeout for
// its response headers; the proxied body is bounded only by the caller's
// request, so a large file is not cut at a fixed time.
var httpClient = &http.Client{Transport: headerTimeoutTransport()}

func headerTimeoutTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = telegramHeaderTimeout
	return transport
}

func tgGet(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", chanlib.UserAgent)
	return httpClient.Do(req)
}

type server struct {
	cfg           config
	prefixes      []string
	bot           chanlib.BotHandler
	chanStore     *chanstore.Store
	isConnected   func() bool
	lastInboundAt func() int64
}

func newServer(
	cfg config, prefixes []string, b chanlib.BotHandler,
	chanStore *chanstore.Store, isConnected func() bool, lastInboundAt func() int64,
) *server {
	return &server{
		cfg: cfg, prefixes: prefixes, bot: b, chanStore: chanStore,
		isConnected: isConnected, lastInboundAt: lastInboundAt,
	}
}

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, s.prefixes, s.bot, s.isConnected, s.lastInboundAt, s.chanStore)
	mux.HandleFunc("GET /files/", chanlib.Auth(s.handleFile))
	return mux
}

func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	r := strings.NewReplacer("\"", "", "\r", "", "\n", "", "\\", "")
	name = r.Replace(name)
	if name == "" || name == "." || name == "/" {
		return "file"
	}
	return name
}

func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	fileID := strings.TrimPrefix(r.URL.Path, "/files/")
	if fileID == "" {
		chanlib.WriteErr(w, 400, "file_id required")
		return
	}
	start := time.Now()
	token := s.cfg.TelegramToken
	resp, err := tgGet(r.Context(), fmt.Sprintf(
		"https://api.telegram.org/bot%s/getFile?file_id=%s",
		url.QueryEscape(token), url.QueryEscape(fileID)))
	if err != nil {
		fileFailed(w, 502, "getFile failed", start, withoutURL(err))
		return
	}
	if resp.StatusCode != 200 {
		_ = resp.Body.Close()
		code := 502
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			code = resp.StatusCode
		}
		fileFailed(w, code, "getFile failed", start, fmt.Errorf("telegram HTTP %d", resp.StatusCode))
		return
	}
	var apiResp struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	_ = resp.Body.Close()
	if err != nil || !apiResp.OK {
		fileFailed(w, 502, "getFile parse failed", start, err)
		return
	}
	fileResp, err := tgGet(r.Context(), fmt.Sprintf(
		"https://api.telegram.org/file/bot%s/%s",
		url.QueryEscape(token), escapePath(apiResp.Result.FilePath)))
	if err != nil {
		fileFailed(w, 502, "file download failed", start, withoutURL(err))
		return
	}
	defer func() { _ = fileResp.Body.Close() }()
	if fileResp.StatusCode != 200 {
		fileFailed(w, 502, "file download failed", start, fmt.Errorf("telegram HTTP %d", fileResp.StatusCode))
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(
		`attachment; filename="%s"`, sanitizeFilename(apiResp.Result.FilePath)))
	chanlib.ProxyFile(w, fileResp, s.cfg.MediaMaxBytes)
}

// fileFailed answers a /files request Telegram did not serve and logs the step
// that failed with the time spent.
func fileFailed(w http.ResponseWriter, code int, step string, start time.Time, err error) {
	slog.Warn("file proxy failed", "step", step, "status", code, "elapsed", time.Since(start), "err", err)
	chanlib.WriteErr(w, code, step)
}

// withoutURL drops the request URL from a transport error: it holds the bot
// token.
func withoutURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}
