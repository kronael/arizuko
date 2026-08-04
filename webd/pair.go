package main

// Identity pairing, browser half (spec 5/31). A pairing link binds a channel
// identity (telegram:user/123) to the account of whoever redeems it, by writing
// the acl_membership edge that makes the channel identity resolve to that human.
//
// Consent is the security boundary. The edge is directional: whoever controls
// the channel account can then act as the human, so the human bears the whole
// risk — and the browser step is the only step the human performs. The attack is
// consent phishing (Mallory mints for HER OWN chat and sends the link to Alice),
// so the confirm page names the channel identity and states the consequence in
// one sentence, and the write is a POST carrying a double-submit CSRF token.
//
// GET is SIDE-EFFECT-FREE: chat platforms unfurl links and an unfurl bot must
// not spend a pairing.
//
// Both verbs sit behind proxyd's `user` gate, so an unauthenticated visitor is
// bounced to OAuth with the pairing URL carried in the signed StateIntent.Return
// (auth_return → authd's consumeReturn) and lands back here signed in.

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/store"
)

// pairCSRFCookie is the double-submit cookie for the confirm form.
const pairCSRFCookie = "arz_pair_csrf"

// GET /pair/{token} — resolve the token and render the confirm page. No write.
func (s *server) handlePairGet(w http.ResponseWriter, r *http.Request) {
	jid, ok := s.peekPairing(w, r)
	if !ok {
		return
	}
	csrf := auth.EnsureCSRF(w, r, pairCSRFCookie, auth.SecureRequest(r))
	who := userName(r)
	if who == "" {
		who = userSub(r)
	}
	pairPage(w, http.StatusOK, "Confirm pairing", fmt.Sprintf(`
<h1>Link this chat account to you?</h1>
<p>You are signed in as <strong>%s</strong>.</p>
<p>Confirming links the channel identity <code>%s</code> to your account:
<strong>anyone who controls that account will be able to act as you.</strong></p>
<form method="POST" action="/pair/%s">
  <input type="hidden" name="%s" value="%s">
  <button type="submit">Link it to me</button>
</form>
<p class="dim">If you did not ask for this link, close this page.</p>`,
		htmlEscape(who), htmlEscape(jid), htmlEscape(r.PathValue("token")),
		auth.CSRFField, htmlEscape(csrf)))
}

// POST /pair/{token} — write the edge and consume the token in one transaction.
func (s *server) handlePairPost(w http.ResponseWriter, r *http.Request) {
	if !auth.CheckCSRF(r, pairCSRFCookie) {
		slog.Warn("pairing: csrf check failed", "sub", userSub(r))
		pairPage(w, http.StatusForbidden, "Pairing failed", `
<h1>That request could not be confirmed</h1>
<p>Open the pairing link again and confirm from that page.</p>`)
		return
	}
	sub := userSub(r)
	jid, err := s.stRoutd.RedeemPairing(r.PathValue("token"), auth.BareSub(sub))
	switch {
	case errors.Is(err, store.ErrPairingConflict):
		// The ONE distinct error: it is the only one the user can act on.
		slog.Warn("pairing: identity already linked elsewhere", "sub", sub)
		pairPage(w, http.StatusConflict, "Already linked", `
<h1>That chat account is already linked</h1>
<p>It belongs to a different account. Unlink it there first, then ask for a new
pairing link.</p>`)
	case errors.Is(err, store.ErrPairingUnavailable):
		pairUnavailable(w)
	case err != nil:
		slog.Error("pairing: redeem failed", "sub", sub, "err", err)
		pairPage(w, http.StatusInternalServerError, "Pairing failed", `
<h1>Something went wrong</h1>
<p>The pairing was not saved. Ask for a new link and try again.</p>`)
	default:
		slog.Info("pairing: linked", "jid", jid, "sub", sub)
		pairPage(w, http.StatusOK, "Linked", fmt.Sprintf(`
<h1>Linked</h1>
<p><code>%s</code> now acts as you. Your next message from that chat carries your
account's access.</p>`, htmlEscape(jid)))
	}
}

// peekPairing resolves the URL's token without consuming it, writing the
// response itself when it cannot.
func (s *server) peekPairing(w http.ResponseWriter, r *http.Request) (string, bool) {
	jid, err := s.stRoutd.PeekPairing(r.PathValue("token"))
	switch {
	case err == nil:
		return jid, true
	case errors.Is(err, store.ErrPairingUnavailable):
		pairUnavailable(w)
	default:
		slog.Error("pairing: token lookup failed", "err", err)
		pairPage(w, http.StatusInternalServerError, "Pairing failed", `
<h1>Something went wrong</h1>
<p>Ask for a new pairing link and try again.</p>`)
	}
	return "", false
}

// pairUnavailable is the ONE response missing, expired, consumed and malformed
// tokens share — a redeemer must not be able to tell them apart.
func pairUnavailable(w http.ResponseWriter) {
	pairPage(w, http.StatusNotFound, "Link unavailable", `
<h1>This link is unavailable</h1>
<p>Pairing links work once and expire after ten minutes. Ask in the chat for a
new one.</p>`)
}

func pairPage(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	// client disconnect; response already committed
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en"><head><meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title><link rel="stylesheet" href="/static/style.css"></head>
<body><main>%s</main></body></html>`, htmlEscape(title), body)
}
