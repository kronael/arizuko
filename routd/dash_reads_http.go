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
)

// sessionCount is one folder's live session tally.
type sessionCount struct {
	Folder string `json:"folder"`
	Count  int    `json:"count"`
}

// handleSessionCounts serves GET /v1/sessions/counts — the Status page's
// per-folder session tally, which used to be a direct `SELECT … FROM sessions`
// against routd.db from dashd.
func (s *Server) handleSessionCounts(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "sessions:read", "chats:read", "messages:read") {
		return
	}
	rows, err := s.db.SQL().Query(
		`SELECT group_folder, COUNT(*) FROM sessions GROUP BY group_folder ORDER BY group_folder`)
	if err != nil {
		writeErr(w, 500, "db_error", err.Error())
		return
	}
	defer rows.Close()
	out := []sessionCount{}
	for rows.Next() {
		var c sessionCount
		if err := rows.Scan(&c.Folder, &c.Count); err != nil {
			writeErr(w, 500, "db_error", err.Error())
			return
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, 500, "db_error", err.Error())
		return
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
