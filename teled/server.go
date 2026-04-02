package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/onvos/arizuko/chanlib"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

type botAdapter struct{ b *bot }

func (a *botAdapter) Send(req chanlib.SendRequest) (string, error) {
	return a.b.send(req.ChatJID, req.Content, req.ReplyTo, req.ThreadID)
}

func (a *botAdapter) SendFile(jid, path, name, caption string) error {
	return a.b.sendFile(jid, path, name, caption)
}

func (a *botAdapter) Typing(jid string, on bool) { a.b.typing(jid, on) }

type server struct {
	cfg config
	bot *bot
}

func newServer(cfg config, b *bot) *server { return &server{cfg: cfg, bot: b} }

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(
		s.cfg.Name, s.cfg.ChannelSecret, []string{"telegram:"}, &botAdapter{s.bot})
	mux.HandleFunc("GET /files/", chanlib.Auth(s.cfg.ChannelSecret, s.handleFile))
	return mux
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	fileID := strings.TrimPrefix(r.URL.Path, "/files/")
	if fileID == "" {
		chanlib.WriteErr(w, 400, "file_id required")
		return
	}
	token := s.cfg.TelegramToken
	resp, err := httpClient.Get(fmt.Sprintf(
		"https://api.telegram.org/bot%s/getFile?file_id=%s", token, fileID))
	if err != nil {
		chanlib.WriteErr(w, 502, "getFile failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		chanlib.WriteErr(w, 502, "getFile failed")
		return
	}
	var apiResp struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if json.NewDecoder(resp.Body).Decode(&apiResp) != nil || !apiResp.OK {
		chanlib.WriteErr(w, 502, "getFile parse failed")
		return
	}
	cdnURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", token, apiResp.Result.FilePath)
	fileResp, err := httpClient.Get(cdnURL)
	if err != nil {
		chanlib.WriteErr(w, 502, "file download failed")
		return
	}
	defer fileResp.Body.Close()
	if fileResp.StatusCode != 200 {
		chanlib.WriteErr(w, 502, "file download failed")
		return
	}
	if ct := fileResp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(apiResp.Result.FilePath)))
	io.Copy(w, fileResp.Body)
}
