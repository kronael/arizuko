package auth

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/kronael/arizuko/store"
)

const linkCodeTTL = 10 * time.Minute

func handleLinkCode(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sub := r.Header.Get("X-User-Sub")
		if sub == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		idn, _, ok := s.GetIdentityForSub(sub)
		if !ok {
			name := r.Header.Get("X-User-Name")
			if name == "" {
				name = sub
			}
			created, err := s.CreateIdentity(name)
			if err != nil {
				slog.Error("create identity", "sub", sub, "err", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if err := s.LinkSub(created.ID, sub); err != nil {
				slog.Error("link self sub", "sub", sub, "err", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			idn = created
		}
		code, err := s.MintLinkCode(idn.ID, linkCodeTTL)
		if err != nil {
			slog.Error("mint link code", "identity", idn.ID, "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		slog.Info("link code minted", "identity", idn.ID, "sub", sub)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"code":       code,
			"ttl":        int(linkCodeTTL.Seconds()),
			"expires_at": time.Now().Add(linkCodeTTL).Format(time.RFC3339),
			"identity":   idn.ID,
		})
	}
}
