package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/theme"
)

const collideTTL = 10 * time.Minute

type collideToken struct {
	NewSub       string `json:"n"`
	NewName      string `json:"nm"`
	NewCanonical string `json:"nc,omitempty"`
	CurrentSub   string `json:"c"`
	Iat          int64  `json:"t"`
}

func signCollide(secret []byte, t collideToken) string {
	t.Iat = time.Now().Unix()
	raw, _ := json.Marshal(t)
	body := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(body))
	return body + "." + hex.EncodeToString(mac.Sum(nil))
}

func verifyCollide(secret []byte, s string) (collideToken, bool) {
	var empty collideToken
	parts := strings.SplitN(s, ".", 2)
	if len(parts) != 2 {
		return empty, false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(parts[0]))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[1]), []byte(expected)) {
		return empty, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return empty, false
	}
	var t collideToken
	if err := json.Unmarshal(raw, &t); err != nil {
		return empty, false
	}
	if time.Since(time.Unix(t.Iat, 0)) > collideTTL {
		return empty, false
	}
	return t, true
}

var collideTmpl = template.Must(template.New("collide").Parse(`<!DOCTYPE html><html>{{.Head}}<body>
<div class="page-center">
<div class="card card-sm" style="padding:2rem;max-width:32rem">
<h1 style="margin-bottom:.2em">Account collision</h1>
<p class="sub">You are signed in as <b>{{.CurrentSub}}</b>.</p>
<p>You just authenticated with <b>{{.NewSub}}</b>{{if .NewCanonical}}, which already belongs to a different account ({{.NewCanonical}}){{end}}.</p>
<p>How do you want to continue?</p>
<form method="POST" action="/auth/collide" style="margin-top:1.5rem">
<input type="hidden" name="token" value="{{.Token}}">
<button name="choice" value="link" type="submit" {{if .NewCanonical}}disabled title="cannot merge two existing accounts"{{end}} style="width:100%;margin-bottom:.6rem">Link {{.NewSub}} to current account</button>
<button name="choice" value="logout" type="submit" style="width:100%">Log out and continue as {{if .NewCanonical}}{{.NewCanonical}}{{else}}{{.NewSub}}{{end}}</button>
</form>
</div></div></body></html>`))

func renderCollision(w http.ResponseWriter, secret []byte, newSub, newName, newCanonical, currentSub string, _ bool) {
	tok := signCollide(secret, collideToken{
		NewSub: newSub, NewName: newName,
		NewCanonical: newCanonical, CurrentSub: currentSub,
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	collideTmpl.Execute(w, struct {
		Head                                   template.HTML
		NewSub, NewCanonical, CurrentSub, Token string
	}{
		template.HTML(theme.Head("Account collision")),
		newSub, newCanonical, currentSub, tok,
	})
}

func handleCollideChoice(s *store.Store, secret []byte, secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		t, ok := verifyCollide(secret, r.FormValue("token"))
		if !ok {
			http.Error(w, "invalid token", http.StatusForbidden)
			return
		}
		switch r.FormValue("choice") {
		case "link":
			if t.NewCanonical != "" && t.NewCanonical != t.CurrentSub {
				http.Error(w, "cannot merge two existing accounts", http.StatusConflict)
				return
			}
			if err := s.LinkSubToCanonical(t.NewSub, t.NewName, t.CurrentSub); err != nil {
				slog.Error("collide link", "sub", t.NewSub, "canonical", t.CurrentSub, "err", err)
				http.Error(w, "link failed", http.StatusBadGateway)
				return
			}
			issueSession(w, r, s, secret, t.CurrentSub, t.NewName, secure)
		case "logout":
			if c, err := r.Cookie(cookieName); err == nil {
				s.DeleteAuthSession(HashToken(c.Value))
				http.SetCookie(w, &http.Cookie{
					Name: cookieName, Value: "", Path: "/",
					MaxAge: -1, HttpOnly: true,
					Secure: secure, SameSite: http.SameSiteLaxMode,
				})
			}
			if _, exists := s.AuthUserBySub(t.NewSub); !exists {
				if err := s.CreateAuthUser(t.NewSub, t.NewSub, "", t.NewName); err != nil {
					slog.Error("collide create", "sub", t.NewSub, "err", err)
					http.Error(w, "internal error", http.StatusInternalServerError)
					return
				}
			}
			issueSession(w, r, s, secret, t.NewSub, t.NewName, secure)
		default:
			http.Error(w, "bad choice", http.StatusBadRequest)
		}
	}
}
