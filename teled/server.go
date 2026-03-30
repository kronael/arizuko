package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/onvos/arizuko/chanlib"
)

type server struct {
	cfg config
	bot *bot
}

func newServer(cfg config, b *bot) *server { return &server{cfg: cfg, bot: b} }

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /send", chanlib.Auth(s.cfg.ChannelSecret, s.handleSend))
	mux.HandleFunc("POST /send-file", chanlib.Auth(s.cfg.ChannelSecret, s.handleSendFile))
	mux.HandleFunc("POST /typing", chanlib.Auth(s.cfg.ChannelSecret, s.handleTyping))
	mux.HandleFunc("GET /files/", s.handleFile)
	mux.HandleFunc("GET /health", s.handleHealth)
	return mux
}

func (s *server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChatJID  string `json:"chat_jid"`
		Content  string `json:"content"`
		ReplyTo  string `json:"reply_to"`
		ThreadID string `json:"thread_id"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.ChatJID == "" || req.Content == "" {
		chanlib.WriteErr(w, 400, "chat_jid and content required")
		return
	}
	sentID, err := s.bot.send(req.ChatJID, req.Content, req.ReplyTo, req.ThreadID)
	if err != nil {
		chanlib.WriteErr(w, 502, err.Error())
		return
	}
	chanlib.WriteJSON(w, map[string]any{"ok": true, "id": sentID})
}

func (s *server) handleSendFile(w http.ResponseWriter, r *http.Request) {
	if r.ParseMultipartForm(50<<20) != nil {
		chanlib.WriteErr(w, 400, "invalid multipart")
		return
	}
	jid, name, caption := r.FormValue("chat_jid"), r.FormValue("filename"), r.FormValue("caption")
	if jid == "" {
		chanlib.WriteErr(w, 400, "chat_jid required")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		chanlib.WriteErr(w, 400, "file required")
		return
	}
	defer file.Close()
	if name == "" {
		name = hdr.Filename
	}
	tmp, err := os.CreateTemp("", "tg-*"+filepath.Ext(name))
	if err != nil {
		chanlib.WriteErr(w, 500, "temp file failed")
		return
	}
	defer os.Remove(tmp.Name())
	io.Copy(tmp, file)
	tmp.Close()
	if err := s.bot.sendFile(jid, tmp.Name(), name, caption); err != nil {
		chanlib.WriteErr(w, 502, err.Error())
		return
	}
	chanlib.WriteJSON(w, map[string]any{"ok": true})
}

func (s *server) handleTyping(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChatJID string `json:"chat_jid"`
		On      bool   `json:"on"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	s.bot.typing(req.ChatJID, req.On)
	chanlib.WriteJSON(w, map[string]any{"ok": true})
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	fileID := strings.TrimPrefix(r.URL.Path, "/files/")
	if fileID == "" {
		chanlib.WriteErr(w, 400, "file_id required")
		return
	}
	token := s.cfg.TelegramToken
	resp, err := http.Get(fmt.Sprintf(
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
	fileResp, err := http.Get(cdnURL)
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

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	chanlib.WriteJSON(w, map[string]any{
		"status": "ok", "name": s.cfg.Name,
		"jid_prefixes": []string{"telegram:"},
	})
}
