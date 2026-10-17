package main

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/store"
)

// maxWebhookBody is GitHub's own payload cap.
const maxWebhookBody = 25 << 20

// secretSource resolves a registered source's raw secret — store.Store in
// production, a map in tests.
type secretSource interface {
	GitHubSourceSecret(key string) (string, bool, error)
}

type server struct {
	cfg       config
	bot       *bot
	st        secretSource
	chanStore *chanstore.Store
	rc        *chanlib.RouterClient
	now       func() time.Time
}

func newServer(cfg config, b *bot, st secretSource, chanStore *chanstore.Store, rc *chanlib.RouterClient) *server {
	return &server{cfg: cfg, bot: b, st: st, chanStore: chanStore, rc: rc, now: time.Now}
}

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, []string{"github:"}, s.bot, s.bot.isConnected, s.bot.LastInboundAt, s.chanStore)
	// The path names the registered source, which selects the secret; the
	// payload never chooses its own verifier.
	mux.HandleFunc("POST /github/{source...}", s.handleWebhook)
	return mux
}

func (s *server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("source")
	kind, name, err := store.ParseGitHubSourceKey(key)
	if err != nil {
		chanlib.WriteErr(w, 404, "unknown source")
		return
	}
	secret, ok, err := s.st.GitHubSourceSecret(key)
	if err != nil {
		slog.Error("github source lookup failed", "source", key, "err", err)
		chanlib.WriteErr(w, 500, "source lookup failed")
		return
	}
	if !ok {
		chanlib.WriteErr(w, 404, "unknown source")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		chanlib.WriteErr(w, 400, "body read failed")
		return
	}
	if err := verifySignature(secret, r.Header.Get("X-Hub-Signature-256"), body); err != nil {
		slog.Warn("github webhook: signature rejected", "source", key, "err", err)
		chanlib.WriteErr(w, 401, "invalid signature")
		return
	}
	event, delivery := r.Header.Get("X-GitHub-Event"), r.Header.Get("X-GitHub-Delivery")
	if event == "" {
		chanlib.WriteErr(w, 400, "missing X-GitHub-Event")
		return
	}
	msg, drop, err := mapEvent(kind, name, event, delivery, body, s.now(), s.bot.Login())
	if err != nil {
		slog.Warn("github webhook: rejected", "source", key, "event", event, "err", err)
		chanlib.WriteErr(w, 400, err.Error())
		return
	}
	if login := s.bot.Login(); drop == "" && login != "" && msg.Sender == "github:"+login {
		drop = "self"
	}
	s.bot.markDelivery(key, s.now())
	if drop != "" {
		chanlib.WriteJSON(w, map[string]any{"ok": true, "dropped": drop})
		return
	}
	// Before routd hears of it: routd may start the turn, and ask for the
	// indicator, before this handler returns.
	s.bot.noteTrigger(msg)
	err = s.rc.SendMessage(msg)
	switch {
	case err == nil:
		chanlib.WriteJSON(w, map[string]any{"ok": true, "id": msg.ID, "jid": msg.ChatJID, "verb": msg.Verb})
	case chanlib.PermanentIngress(err):
		chanlib.WriteErr(w, 400, "routd refused: "+err.Error())
	case errors.Is(err, chanlib.ErrInboundLost):
		// GitHub never redelivers on its own and this response IS its
		// acknowledgement, so a 202 here retires the only copy of the message.
		// A 5xx leaves the redeliver button in GitHub's UI meaningful.
		slog.Error("github webhook neither delivered nor retained",
			"source", key, "id", msg.ID, "chat_jid", msg.ChatJID, "err", err)
		chanlib.WriteErr(w, http.StatusServiceUnavailable, "neither routd nor retention took the message: "+err.Error())
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		chanlib.WriteJSON(w, map[string]any{"ok": true, "retained": true, "id": msg.ID, "jid": msg.ChatJID})
	}
}
