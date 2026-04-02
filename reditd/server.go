package main

import (
	"net/http"

	"github.com/onvos/arizuko/chanlib"
)

type sender interface {
	comment(thingID, text string) error
	submit(text string) error
}

type botAdapter struct {
	chanlib.NoFileSender
	rc sender
}

func (a *botAdapter) Send(req chanlib.SendRequest) (string, error) {
	if req.ReplyTo != "" {
		return "", a.rc.comment(req.ReplyTo, req.Content)
	}
	return "", a.rc.submit(req.Content)
}

func (a *botAdapter) Typing(string, bool) {}

type server struct {
	cfg config
	rc  sender
}

func newServer(cfg config, rc sender) *server { return &server{cfg: cfg, rc: rc} }

func (s *server) handler() http.Handler {
	return chanlib.NewAdapterMux(
		s.cfg.Name, s.cfg.ChannelSecret, []string{"reddit:"}, &botAdapter{rc: s.rc})
}
