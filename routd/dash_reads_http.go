package routd

// dash_reads_http.go — the aggregate reads the operator cockpit needs, served
// by the daemon that OWNS the rows (spec 6/28 §"Read-path"). BUGS L142.
//
// These are plain handlers, not resreg resources, and that is the whole design
// choice: resreg gives REST and MCP from one declaration, so registering these
// as resources would hand every agent a tool for reading message aggregates and
// session counts across folders. The cockpit needs them; an agent does not.
// `handleUserScopes` is the same shape — a REST-only read on routd's own /v1.
//
// Each is an AGGREGATE, never a row dump: counts and last-seen timestamps, so
// the surface cannot become a way to read other folders' message content.

import (
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

// sessionCount is one folder's live session tally.
type sessionCount struct {
	Folder string `json:"folder"`
	Count  int    `json:"count"`
}

// handleSessionCounts serves GET /v1/sessions/counts — the Status page's
// per-folder session tally, in folder order.
func (s *Server) handleSessionCounts(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "sessions:read", "chats:read", "messages:read") {
		return
	}
	perFolder, err := s.db.TopicsPerFolder()
	if err != nil {
		writeErr(w, 500, "db_error", err.Error())
		return
	}
	folders := make([]string, 0, len(perFolder))
	for folder := range perFolder {
		folders = append(folders, folder)
	}
	sort.Strings(folders)
	out := make([]sessionCount, 0, len(folders))
	for _, folder := range folders {
		out = append(out, sessionCount{Folder: folder, Count: perFolder[folder]})
	}
	writeJSON(w, 200, out)
}

// dailyVolume is one day's human-message count.
type dailyVolume struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
}

// handleMessageVolume serves GET /v1/messages/volume — the usage page's 7-day
// human-message histogram. is_bot_message=0 is applied HERE, by the owner, so
// the caller cannot widen it to include the agent's own output.
func (s *Server) handleMessageVolume(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "messages:read", "chats:read") {
		return
	}
	rows, err := s.db.SQL().Query(
		`SELECT DATE(timestamp) AS day, COUNT(*) AS cnt
		   FROM messages
		  WHERE is_bot_message=0 AND timestamp >= datetime('now', '-7 days')
		  GROUP BY day ORDER BY day DESC`)
	if err != nil {
		writeErr(w, 500, "db_error", err.Error())
		return
	}
	defer rows.Close()
	out := []dailyVolume{}
	for rows.Next() {
		var d dailyVolume
		if err := rows.Scan(&d.Day, &d.Count); err != nil {
			writeErr(w, 500, "db_error", err.Error())
			return
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, 500, "db_error", err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// activityRow is one line of the cockpit's cross-folder activity feed. Content
// is truncated by the OWNER at 80 characters: the page shows a preview, and a
// preview is all this surface will ever serve.
type activityRow struct {
	Timestamp string `json:"timestamp"`
	Source    string `json:"source"`
	ChatJID   string `json:"chat_jid"`
	Sender    string `json:"sender"`
	Verb      string `json:"verb"`
	Preview   string `json:"preview"`
}

// handleActivity serves GET /v1/messages/activity?before=&limit= — the newest
// messages across every chat, which the cockpit's activity page used to read
// out of routd.db.
//
// The caller cannot widen the preview or the page: `limit` is clamped here, and
// the substr is the owner's. dashd applies its own per-row folder visibility on
// top, so this is the outer bound rather than the authorization.
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "messages:read", "chats:read") {
		return
	}
	limit := 1000
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v < limit {
		limit = v
	}
	q := `SELECT timestamp, source, chat_jid, sender, verb, substr(content,1,80) FROM messages`
	args := []any{}
	if before := r.URL.Query().Get("before"); before != "" {
		q += ` WHERE timestamp < ?`
		args = append(args, before)
	}
	q += ` ORDER BY timestamp DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.SQL().Query(q, args...)
	if err != nil {
		writeErr(w, 500, "db_error", err.Error())
		return
	}
	defer rows.Close()
	out := []activityRow{}
	for rows.Next() {
		var a activityRow
		if err := rows.Scan(&a.Timestamp, &a.Source, &a.ChatJID, &a.Sender, &a.Verb, &a.Preview); err != nil {
			writeErr(w, 500, "db_error", err.Error())
			return
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, 500, "db_error", err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// principal is one datalist entry for the grants form: who a grant can name.
type principal struct {
	Sub  string `json:"sub"`
	Name string `json:"name"`
}

// handlePrincipals serves GET /v1/principals — the subs a grant may name, from
// `user_profiles` plus the non-role children of `acl_membership`. It exists so
// the grants form and the profile page stop reading routd.db directly.
//
// It is TWO reads and a merge, because the two halves are two entities and an
// entity has no join. A sub named by both is listed once, and the profile's own
// name wins — an edge carries no name, so taking the edge's would blank a label
// the form has.
//
// Names and subs only. No email, no tokens, no profile body: the form needs a
// label and an identifier, so that is the whole projection.
func (s *Server) handlePrincipals(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "grants:read", "messages:read") {
		return
	}
	names, err := s.db.ProfileNames(r.Context())
	if err != nil {
		writeErr(w, 500, "db_error", err.Error())
		return
	}
	// The child leads an edge's coordinate, so every child is read off the key.
	// A `role:` child is a role, not a person, and naming one in a grant would
	// hand the form a principal no token ever carries.
	items, err := s.db.Obj().Query(r.Context(), objstore.Query{
		Entity: store.ACLMembership, Partition: store.OwnerRoutd,
	})
	if err != nil {
		writeErr(w, 500, "db_error", err.Error())
		return
	}
	for _, it := range items {
		child, _, ok := strings.Cut(it.Key.Sort, store.KeySep)
		if !ok || strings.HasPrefix(child, "role:") {
			continue
		}
		if _, seen := names[child]; !seen {
			names[child] = ""
		}
	}
	out := make([]principal, 0, len(names))
	for sub, name := range names {
		out = append(out, principal{Sub: sub, Name: name})
	}
	slices.SortFunc(out, func(a, b principal) int { return strings.Compare(a.Sub, b.Sub) })
	writeJSON(w, 200, out)
}
