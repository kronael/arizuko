package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/onvos/arizuko/chanlib"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

type server struct {
	cfg config
	bot chanlib.BotHandler
}

func newServer(cfg config, b chanlib.BotHandler) *server { return &server{cfg: cfg, bot: b} }

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"telegram:"}, s.bot)
	mux.HandleFunc("GET /files/", chanlib.Auth(s.cfg.ChannelSecret, s.handleFile))
	return mux
}

// sanitizeFilename strips characters that could inject headers (quotes, CR/LF)
// and path separators from a filename for use in Content-Disposition.
func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	r := strings.NewReplacer("\"", "", "\r", "", "\n", "", "\\", "")
	name = r.Replace(name)
	if name == "" || name == "." || name == "/" {
		return "file"
	}
	return name
}

// escapePath URL-escapes each path segment while preserving slashes.
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
	token := s.cfg.TelegramToken
	resp, err := httpClient.Get(fmt.Sprintf(
		"https://api.telegram.org/bot%s/getFile?file_id=%s",
		url.QueryEscape(token), url.QueryEscape(fileID)))
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		chanlib.WriteErr(w, 502, "getFile failed")
		return
	}
	var apiResp struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	err = json.NewDecoder(resp.Body).Decode(&apiResp)
	resp.Body.Close()
	if err != nil || !apiResp.OK {
		chanlib.WriteErr(w, 502, "getFile parse failed")
		return
	}
	// Telegram's file_path is a relative path like "photos/file_0.jpg".
	// URL-escape each segment but preserve slashes.
	fileResp, err := httpClient.Get(fmt.Sprintf(
		"https://api.telegram.org/file/bot%s/%s",
		url.QueryEscape(token), escapePath(apiResp.Result.FilePath)))
	if err != nil || fileResp.StatusCode != 200 {
		if fileResp != nil {
			fileResp.Body.Close()
		}
		chanlib.WriteErr(w, 502, "file download failed")
		return
	}
	defer fileResp.Body.Close()
	if ct := fileResp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(
		`attachment; filename="%s"`, sanitizeFilename(apiResp.Result.FilePath)))
	max := s.cfg.MediaMaxBytes
	if max <= 0 {
		max = 20 * 1024 * 1024
	}
	io.Copy(w, io.LimitReader(fileResp.Body, max))
}
