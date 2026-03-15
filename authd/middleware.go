package authd

import (
	"net/http"
	"strings"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

var publicPrefixes = []string{
	"/auth/",
	"/pub/",
	"/_REDACTED/",
}

var publicExact = []string{
	"/favicon.ico",
	"/robots.txt",
}

func Middleware(secret []byte, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		for _, pre := range publicPrefixes {
			if strings.HasPrefix(p, pre) {
				next.ServeHTTP(w, r)
				return
			}
		}
		for _, exact := range publicExact {
			if p == exact {
				next.ServeHTTP(w, r)
				return
			}
		}
		ext := ""
		if i := strings.LastIndex(p, "."); i >= 0 {
			ext = p[i:]
		}
		switch ext {
		case ".css", ".js", ".png", ".ico", ".svg", ".woff", ".woff2":
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		if token == auth || token == "" {
			http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
			return
		}
		if _, err := VerifyJWT(secret, token); err != nil {
			http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func RegisterRoutes(mux *http.ServeMux, s *store.Store, cfg *core.Config) {
	secret := []byte(cfg.AuthSecret)
	mux.HandleFunc("GET /auth/login", handleLoginPage)
	mux.HandleFunc("POST /auth/login", handleLogin(s, secret))
	mux.HandleFunc("POST /auth/refresh", handleRefresh(s, secret))
	mux.HandleFunc("POST /auth/logout", handleLogout(s))

	if cfg.GitHubClientID != "" {
		mux.HandleFunc("GET /auth/github",
			handleGitHubRedirect(cfg, secret))
		mux.HandleFunc("GET /auth/github/callback",
			handleGitHubCallback(cfg, s, secret))
	}
	if cfg.DiscordClientID != "" {
		mux.HandleFunc("GET /auth/discord",
			handleDiscordRedirect(cfg, secret))
		mux.HandleFunc("GET /auth/discord/callback",
			handleDiscordCallback(cfg, s, secret))
	}
	if cfg.TelegramToken != "" {
		mux.HandleFunc("POST /auth/telegram",
			handleTelegram(cfg, s, secret))
	}
}
