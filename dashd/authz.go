package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/store"
)

// requireAdmin gates a write to scope (folder or "**" for global). Uses
// auth.Authorize with action="admin"; deny-wins, tier-default fallback
// off (admin is not an mcp:* action). Caller principal is X-User-Sub
// (proxyd-verified); X-User-Groups folds in as Extra principals so an
// operator with `**` admin on a parent folder authorizes a child write.
// 403 + log on denial. Returns false if the handler should abort.
func (d *dash) requireAdmin(w http.ResponseWriter, r *http.Request, scope string) (string, bool) {
	sub, ok := requireUser(w, r)
	if !ok {
		return "", false
	}
	if !requireSameOrigin(w, r) {
		return "", false
	}
	var extra []string
	if hdr := r.Header.Get("X-User-Groups"); hdr != "" {
		_ = json.Unmarshal([]byte(hdr), &extra)
		// Allow plain comma-separated fallback for non-JSON encoders.
		if len(extra) == 0 && !strings.HasPrefix(strings.TrimSpace(hdr), "[") {
			for _, p := range strings.Split(hdr, ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					extra = append(extra, p)
				}
			}
		}
	}
	s := store.New(d.adminDB())
	caller := auth.Caller{Principal: sub, Extra: extra}
	if !auth.Authorize(s, caller, "admin", scope, nil) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return "", false
	}
	return sub, true
}
