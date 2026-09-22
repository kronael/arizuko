package routd

// dash_reads_http.go — the aggregate reads the operator cockpit needs, served
// by the daemon that OWNS the rows (spec 6/28 §"Read-path", BUGS L142). Plain
// handlers, not resreg resources: a resource would hand every agent a
// cross-folder tool for these aggregates. Each is an AGGREGATE, never a row
// dump, so the surface cannot become a way to read other folders' content.

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

// handleSessionCounts serves GET /v1/sessions/counts, in folder order.
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

// handleMessageVolume serves GET /v1/messages/volume, the usage page's 7-day
// human-message histogram. is_bot_message=0 is applied HERE, by the owner, so
// the caller cannot widen it to include the agent's own output.
func (s *Server) handleMessageVolume(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "messages:read", "chats:read") {
		return
	}
	counts, err := s.db.DailyVolume(r.Context(), volumeDays)
	if err != nil {
		writeErr(w, 500, "db_error", err.Error())
		return
	}
	out := make([]dailyVolume, 0, len(counts))
	for day, n := range counts {
		out = append(out, dailyVolume{Day: day, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day > out[j].Day })
	writeJSON(w, 200, out)
}

// volumeDays is the histogram's window.
const volumeDays = 7

// activityRow is one line of the cross-folder activity feed. Content is
// truncated by the OWNER at 80 characters: a preview is all this surface will
// ever serve.
type activityRow struct {
	Timestamp string `json:"timestamp"`
	Source    string `json:"source"`
	ChatJID   string `json:"chat_jid"`
	Sender    string `json:"sender"`
	Verb      string `json:"verb"`
	Preview   string `json:"preview"`
}

// handleActivity serves GET /v1/messages/activity?before=&limit=. It reads
// the activity feed — one partition, newest first, its own retention;
// answering from the chats would read all 136 partitions and merge (10,525
// items to show 1,000 on krons). `limit` is clamped here; dashd applies its
// own per-row folder visibility on top, so this is the outer bound.
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "messages:read", "chats:read") {
		return
	}
	limit := 1000
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v < limit {
		limit = v
	}
	rows, err := s.db.RecentActivity(r.Context(), r.URL.Query().Get("before"), limit)
	if err != nil {
		writeErr(w, 500, "db_error", err.Error())
		return
	}
	out := make([]activityRow, 0, len(rows))
	for _, a := range rows {
		out = append(out, activityRow{
			Timestamp: a.Timestamp, Source: a.Source, ChatJID: a.ChatJID,
			Sender: a.Sender, Verb: a.Verb, Preview: a.Preview,
		})
	}
	writeJSON(w, 200, out)
}

// principal is one datalist entry for the grants form: who a grant can name.
type principal struct {
	Sub  string `json:"sub"`
	Name string `json:"name"`
}

// handlePrincipals serves GET /v1/principals — the subs a grant may name, from
// `user_profiles` plus the non-role children of `acl_membership`. TWO reads
// and a merge, because an entity has no join: a sub in both is listed once and
// the profile's name wins. Names and subs only — no email, no tokens, no
// profile body.
func (s *Server) handlePrincipals(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "grants:read", "messages:read") {
		return
	}
	names, err := s.db.ProfileNames(r.Context())
	if err != nil {
		writeErr(w, 500, "db_error", err.Error())
		return
	}
	// The child leads an edge's coordinate. A `role:` child is a role, not a
	// person, and no token ever carries one.
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
