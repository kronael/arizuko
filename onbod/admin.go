package main

import (
	"database/sql"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/store"
)

// admin serves onbod's bearer-gated /v1/* surface: the invite + gate writers
// that dashd, the host CLI, and routd's /invite + /gate commands reach instead
// of touching onbod's tables directly (spec 5/5 § Daemon ownership). All
// mutations go through store.New(db) against onbod's OWNED DB (onbod.db) using
// the same audited writers.
type admin struct {
	db *sql.DB
	ks *auth.KeySet // authd JWKS; nil (AUTHD_URL unset, e.g. local-dev) → open, like routd's nil verifier
}

// inviteJSON is the READ shape for an invite row. It has no token field at all:
// the bearer is shown once at creation and never again, so a list response
// cannot leak it however the handler is later edited. `ref` (store.TokenRef)
// identifies the row for DELETE /v1/invites/{ref} without being redeemable.
type inviteJSON struct {
	Ref         string `json:"ref"`
	TargetGlob  string `json:"target_glob"`
	IssuedBySub string `json:"issued_by_sub"`
	IssuedAt    string `json:"issued_at"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	MaxUses     int    `json:"max_uses"`
	UsedCount   int    `json:"used_count"`
}

// inviteCreatedJSON is the CREATE-only shape: the read shape plus the raw
// bearer, returned exactly once (mirrors route_tokens' issue verbs). The
// bearer never lives on store.Invite (I1) — the caller that just minted it
// passes it in separately.
type inviteCreatedJSON struct {
	inviteJSON
	Token string `json:"token"`
}

func toInviteJSON(inv store.Invite) inviteJSON {
	out := inviteJSON{
		Ref: inv.Ref, TargetGlob: inv.TargetGlob, IssuedBySub: inv.IssuedBySub,
		IssuedAt: inv.IssuedAt.Format(time.RFC3339), MaxUses: inv.MaxUses, UsedCount: inv.UsedCount,
	}
	if inv.ExpiresAt != nil {
		out.ExpiresAt = inv.ExpiresAt.Format(time.RFC3339)
	}
	return out
}

func toInviteCreatedJSON(inv store.Invite, rawToken string) inviteCreatedJSON {
	return inviteCreatedJSON{inviteJSON: toInviteJSON(inv), Token: rawToken}
}
