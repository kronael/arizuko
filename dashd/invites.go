package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/store"
)

// inviteRef is an opaque, stable handle for an invite used in the revoke URL.
// The live token is a bearer, so putting it in a path leaks it to request /
// proxy logs; its sha256 identifies the row without being redeemable.
func inviteRef(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// handleInvites renders the full invites page.
// GET lists all pending invites with an inline create form.
// POST /dash/invites/ creates a new invite.
// POST /dash/invites/{token}/revoke deletes an invite.
func (d *dash) handleInvites(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Invites")
	fmt.Fprint(w, `<p class="dim">Invite links. Each token is single-use by default; set max uses for multi-use links.</p>`)

	idb := d.invitesDB()
	if idb == nil {
		fmt.Fprint(w, htmlBanner("err", "invites store unavailable"))
		pageClose(w, r)
		return
	}

	s := store.New(idb)
	invites, err := s.ListInvites("")
	if err != nil {
		slog.Warn("invites: list", "err", err)
		fmt.Fprint(w, htmlBanner("err", "invites query error: "+err.Error()))
		pageClose(w, r)
		return
	}

	if len(invites) == 0 {
		fmt.Fprint(w, `<p class="empty">No invites. Create one below.</p>`)
	} else {
		tableRows := make([][]string, len(invites))
		for i, inv := range invites {
			expires := "—"
			if inv.ExpiresAt != nil {
				expires = inv.ExpiresAt.Format("2006-01-02")
			}
			// Fully-used invites can't be redeemed again; hide revoke. MaxUses<=0
			// means no use limit (always revocable).
			revokeBtn := `<span class="dim">used</span>`
			if inv.MaxUses <= 0 || inv.UsedCount < inv.MaxUses {
				revokeBtn = fmt.Sprintf(
					`<form method="post" action="/dash/invites/%s/revoke" class="form-inline"`+
						` onsubmit="return confirm('revoke invite %s?')">`+
						`<button type="submit">revoke</button></form>`,
					inviteRef(inv.Token), esc(inv.Token[:8]),
				)
			}
			tableRows[i] = []string{
				`<code>` + esc(inv.Token) + `</code>`,
				`<code>` + esc(inv.TargetGlob) + `</code>`,
				esc(inv.IssuedBySub),
				`<abbr title="` + esc(inv.IssuedAt.UTC().Format(time.RFC3339)) + `">` + relativeTS(inv.IssuedAt.UTC().Format(time.RFC3339)) + `</abbr>`,
				esc(expires),
				fmt.Sprintf("%d / %d", inv.UsedCount, inv.MaxUses),
				revokeBtn,
			}
		}
		fmt.Fprint(w, htmlTable(
			[]string{"Token", "Target", "Issued by", "Issued", "Expires", "Uses", ""},
			tableRows,
		))
	}

	fmt.Fprint(w, `<h2>Create invite</h2><form method="post" action="/dash/invites/">`)
	fmt.Fprint(w, htmlFormRow("Target glob", `<input type="text" name="target_glob" placeholder="alice or alice/ for sub-world" required size="40">`))
	fmt.Fprint(w, htmlFormRow("Max uses", `<input type="number" name="max_uses" value="1" min="1" required>`))
	fmt.Fprint(w, htmlFormRow("Expires (optional)", `<input type="date" name="expires_at">`))
	fmt.Fprint(w, `<p><button type="submit">create</button></p></form>`)
	fmt.Fprint(w, `<p class="dim">Target <code>folder</code> grants direct access; <code>folder/</code> lets the user pick a username under that folder.</p>`)

	pageClose(w, r)
}

func (d *dash) handleInviteCreate(w http.ResponseWriter, r *http.Request) {
	sub, ok := d.requireAdmin(w, r, "**")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "parse form", http.StatusBadRequest)
		return
	}
	targetGlob := strings.TrimSpace(r.FormValue("target_glob"))
	if targetGlob == "" {
		http.Error(w, "target_glob required", http.StatusBadRequest)
		return
	}
	maxUses := 1
	if v := r.FormValue("max_uses"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			http.Error(w, "max_uses must be a positive integer", http.StatusBadRequest)
			return
		}
		maxUses = n
	}
	var expiresAt *time.Time
	if v := strings.TrimSpace(r.FormValue("expires_at")); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			http.Error(w, "expires_at: use YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		t = t.UTC().Add(24 * time.Hour) // end of day UTC
		if t.Before(time.Now().UTC()) {
			http.Error(w, "expires_at must be in the future", http.StatusBadRequest)
			return
		}
		expiresAt = &t
	}

	s := store.New(d.invitesDB())
	inv, err := s.CreateInvite(targetGlob, sub, maxUses, expiresAt)
	if err != nil {
		slog.Warn("invites: create", "err", err)
		http.Error(w, "create failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	slog.Info("invite created", "token", inv.Token[:8], "target", targetGlob, "issuer", sub)
	http.Redirect(w, r, "/dash/invites/", http.StatusSeeOther)
}

func (d *dash) handleInviteRevoke(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.requireAdmin(w, r, "**"); !ok {
		return
	}
	ref := r.PathValue("ref")
	if ref == "" {
		http.Error(w, "ref required", http.StatusBadRequest)
		return
	}
	s := store.New(d.invitesDB())
	// Resolve the opaque ref back to a token: the URL carries sha256(token), not
	// the bearer, so match it against the current invite set.
	invites, err := s.ListInvites("")
	if err != nil {
		slog.Warn("invites: revoke list", "err", err)
		http.Error(w, "revoke failed", http.StatusInternalServerError)
		return
	}
	token := ""
	for _, inv := range invites {
		if inviteRef(inv.Token) == ref {
			token = inv.Token
			break
		}
	}
	if token == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err := s.RevokeInvite(token); err != nil {
		slog.Warn("invites: revoke", "ref", ref, "err", err)
		http.Error(w, "revoke failed", http.StatusInternalServerError)
		return
	}
	slog.Info("invite revoked", "ref", ref)
	http.Redirect(w, r, "/dash/invites/", http.StatusSeeOther)
}
