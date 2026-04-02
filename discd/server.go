package main

import (
	"net/http"

	"github.com/onvos/arizuko/chanlib"
)

type botIface interface {
	send(jid, content, replyTo, threadID string) (string, error)
	sendFile(jid, path, name, caption string) error
	typing(jid string, on bool) error
}

type botAdapter struct{ b botIface }

func (a *botAdapter) Send(req chanlib.SendRequest) (string, error) {
	return a.b.send(req.ChatJID, req.Content, req.ReplyTo, req.ThreadID)
}

func (a *botAdapter) SendFile(jid, path, name, caption string) error {
	return a.b.sendFile(jid, path, name, caption)
}

func (a *botAdapter) Typing(jid string, on bool) { a.b.typing(jid, on) }

type server struct {
	cfg config
	bot botIface
}

func newServer(cfg config, b botIface) *server { return &server{cfg: cfg, bot: b} }

func (s *server) handler() http.Handler {
	return chanlib.NewAdapterMux(
		s.cfg.Name, s.cfg.ChannelSecret, []string{"discord:"}, &botAdapter{s.bot})
}
