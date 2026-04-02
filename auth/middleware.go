package auth

import (
	"net/http"
	"strings"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

func RegisterRoutes(mux *http.ServeMux, s *store.Store, cfg *core.Config) {
	secret := []byte(cfg.AuthSecret)
	secure := strings.HasPrefix(authBaseURL(cfg), "https://")

	mux.HandleFunc("GET /auth/login", handleLoginPage(cfg))
	mux.HandleFunc("POST /auth/login", handleLogin(s, secret, secure))
	mux.HandleFunc("POST /auth/refresh", handleRefresh(s, secret, secure))
	mux.HandleFunc("POST /auth/logout", handleLogout(s, secure))

	if cfg.GitHubClientID != "" {
		mux.HandleFunc("GET /auth/github",
			handleGitHubRedirect(cfg, secret, secure))
		mux.HandleFunc("GET /auth/github/callback",
			handleGitHubCallback(cfg, s, secret, secure))
	}
	if cfg.DiscordClientID != "" {
		mux.HandleFunc("GET /auth/discord",
			handleDiscordRedirect(cfg, secret, secure))
		mux.HandleFunc("GET /auth/discord/callback",
			handleDiscordCallback(cfg, s, secret, secure))
	}
	if cfg.GoogleClientID != "" {
		mux.HandleFunc("GET /auth/google",
			handleGoogleRedirect(cfg, secret, secure))
		mux.HandleFunc("GET /auth/google/callback",
			handleGoogleCallback(cfg, s, secret, secure))
	}
	if cfg.TelegramToken != "" {
		mux.HandleFunc("POST /auth/telegram",
			handleTelegram(cfg, s, secret, secure))
	}
}
