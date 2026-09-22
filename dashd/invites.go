package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/store"
)

// inviteRow is onbod's read shape for an invite (GET /v1/invites). It carries
// no token field, because the bearer is shown once at creation and never again.
type inviteRow struct {
	Ref         string `json:"ref"`
	TargetGlob  string `json:"target_glob"`
	IssuedBySub string `json:"issued_by_sub"`
	IssuedAt    string `json:"issued_at"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	MaxUses     int    `json:"max_uses"`
	UsedCount   int    `json:"used_count"`
}

// listInvites reads onbod's own /v1, not onbod's tables. The rows belong to
// onbod and onbod serves them on resreg, so the direct read was the one 6/28
// forbids without qualifying for its `secrets` exception — the owner WOULD
// serve this if asked, and does. The page's WRITES stay on the FS-mounted
// discipline CLAUDE.md governs; only the LIST moves.
func (d *dash) listInvites(ctx context.Context) ([]inviteRow, error) {
	buf, status, err := d.bearerCall(ctx, d.onbodURL, http.MethodGet, "/v1/invites", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("onbod /v1/invites: %d", status)
	}
	var out struct {
		Invites []inviteRow `json:"invites"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		return nil, err
	}
	return out.Invites, nil
}

// handleInvites renders the full invites page.
// GET lists all pending invites with an inline create form.
// POST /dash/invites/ creates a new invite.
// POST /dash/invites/{ref}/revoke deletes an invite.
func (d *dash) handleInvites(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	pageTopFor(w, r, "Invites")
	fmt.Fprint(w, `<p class="dim">Invite links. Each token is single-use by default; set max uses for multi-use links. `+
		`The link is shown once when you create it — this page lists each invite by its ref, which revokes but cannot redeem.</p>`)

	invites, err := d.listInvites(r.Context())
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
			if inv.ExpiresAt != "" {
				if t, err := time.Parse(time.RFC3339, inv.ExpiresAt); err == nil {
					expires = t.Format("2006-01-02")
				}
			}
			ref := inv.Ref
			// Fully-used invites can't be redeemed again; hide revoke. MaxUses<=0
			// means no use limit (always revocable).
			revokeBtn := `<span class="dim">used</span>`
			if inv.MaxUses <= 0 || inv.UsedCount < inv.MaxUses {
				revokeBtn = fmt.Sprintf(
					`<form method="post" action="/dash/invites/%s/revoke" class="form-inline"`+
						` onsubmit="return confirm('revoke invite %s?')">`+
						`<button type="submit">revoke</button></form>`,
					ref, esc(ref[:8]),
				)
			}
			tableRows[i] = []string{
				`<code>` + esc(ref) + `</code>`,
				`<code>` + esc(inv.TargetGlob) + `</code>`,
				esc(inv.IssuedBySub),
				abbrTS(inv.IssuedAt),
				esc(expires),
				fmt.Sprintf("%d / %d", inv.UsedCount, inv.MaxUses),
				revokeBtn,
			}
		}
		fmt.Fprint(w, htmlTable(
			[]string{"Ref", "Target", "Issued by", "Issued", "Expires", "Uses", ""},
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

	s := d.onbodStore()
	if s == nil {
		http.Error(w, "invites store unavailable", http.StatusServiceUnavailable)
		return
	}
	inv, rawToken, err := s.CreateInvite(targetGlob, sub, maxUses, expiresAt)
	if err != nil {
		slog.Warn("invites: create", "err", err)
		http.Error(w, "create failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	ref := inv.Ref
	slog.Info("invite created", "ref", ref, "target", targetGlob, "issuer", sub)

	// The bearer is shown HERE and nowhere else — the list page and every other
	// read surface carry only the ref — so this renders the link instead of
	// redirecting back to the list. connBaseURL is the configured public base
	// (AUTH_BASE_URL, then WEB_HOST); unset → the relative path, which the
	// operator prefixes with their own host.
	link := "/invite/" + rawToken
	if d.connBaseURL != "" {
		link = d.connBaseURL + link
	}
	pageTopFor(w, r, "Invite created")
	fmt.Fprint(w, htmlBanner("ok", "Invite created for "+targetGlob+". Copy the link now — it is not shown again."))
	fmt.Fprintf(w, `<p><code>%s</code></p>`, esc(link))
	fmt.Fprintf(w, `<p class="dim">Ref (for revoking): <code>%s</code></p>`, esc(ref))
	fmt.Fprint(w, `<p><a href="/dash/invites/">Back to invites</a></p>`)
	pageClose(w, r)
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
	s := d.onbodStore()
	if s == nil {
		http.Error(w, "invites store unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.RevokeInviteByRef(ref); err != nil {
		if errors.Is(err, store.ErrInviteRefUnknown) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.Warn("invites: revoke", "ref", ref, "err", err)
		http.Error(w, "revoke failed", http.StatusInternalServerError)
		return
	}
	slog.Info("invite revoked", "ref", ref)
	http.Redirect(w, r, "/dash/invites/", http.StatusSeeOther)
}
