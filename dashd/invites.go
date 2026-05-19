package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/store"
)

// handleInvites renders the full invites page.
// GET lists all pending invites with an inline create form.
// POST /dash/invites/ creates a new invite.
// POST /dash/invites/{token}/revoke deletes an invite.
func (d *dash) handleInvites(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUser(w, r); !ok {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Invites")
	fmt.Fprint(w, `<p class="dim">Invite links. Each token is single-use by default; set max uses for multi-use links.</p>`)

	if d.dbRW == nil {
		fmt.Fprint(w, `<div class="banner-err">invites store unavailable</div>`)
		fmt.Fprint(w, pageBot)
		return
	}

	s := store.New(d.dbRW)
	invites, err := s.ListInvites("")
	if err != nil {
		slog.Warn("invites: list", "err", err)
		fmt.Fprintf(w, `<div class="banner-err">invites query error: %s</div>`, esc(err.Error()))
		fmt.Fprint(w, pageBot)
		return
	}

	fmt.Fprint(w, `<table><thead><tr><th>Token</th><th>Target</th><th>Issued by</th><th>Issued</th><th>Expires</th><th>Uses</th><th></th></tr></thead><tbody>`)
	if len(invites) == 0 {
		fmt.Fprint(w, `<tr><td colspan=7 class="empty">No invites. Create one below.</td></tr>`)
	}
	for _, inv := range invites {
		expires := "—"
		if inv.ExpiresAt != nil {
			expires = inv.ExpiresAt.Format("2006-01-02")
		}
		fmt.Fprintf(w, `<tr>
<td><code>%s</code></td>
<td><code>%s</code></td>
<td>%s</td>
<td>%s</td>
<td>%s</td>
<td>%d / %d</td>
<td>
<form method="post" action="/dash/invites/%s/revoke" style="display:inline"
      onsubmit="return confirm('revoke invite %s?')">
<button type="submit">revoke</button>
</form>
</td>
</tr>`,
			esc(inv.Token),
			esc(inv.TargetGlob),
			esc(inv.IssuedBySub),
			esc(inv.IssuedAt.Format("2006-01-02")),
			esc(expires),
			inv.UsedCount, inv.MaxUses,
			esc(inv.Token),
			esc(inv.Token[:8]),
		)
	}
	fmt.Fprint(w, `</tbody></table>`)

	fmt.Fprintf(w, `<h2>Create invite</h2>
<form method="post" action="/dash/invites/">
<p><label>Target glob <input type="text" name="target_glob" placeholder="alice or alice/ for sub-world" required size="40"></label></p>
<p><label>Max uses <input type="number" name="max_uses" value="1" min="1" required></label></p>
<p><label>Expires (optional) <input type="date" name="expires_at"></label></p>
<p><button type="submit">create</button></p>
</form>
<p class="dim">Target <code>folder</code> grants direct access; <code>folder/</code> lets the user pick a username under that folder.</p>`)

	fmt.Fprint(w, pageBot)
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
		expiresAt = &t
	}

	s := store.New(d.dbRW)
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
	token := r.PathValue("token")
	if token == "" {
		http.Error(w, "token required", http.StatusBadRequest)
		return
	}
	s := store.New(d.dbRW)
	if err := s.RevokeInvite(token); err != nil {
		slog.Warn("invites: revoke", "token_prefix", token[:min(8, len(token))], "err", err)
		http.Error(w, "revoke failed", http.StatusInternalServerError)
		return
	}
	slog.Info("invite revoked", "token_prefix", token[:min(8, len(token))])
	http.Redirect(w, r, "/dash/invites/", http.StatusSeeOther)
}
