package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
)

// handleRuned renders GET /dash/runed/ — the container-runner cockpit: live
// runs (queued/running, with a per-folder kill) and recently finished runs.
// Data comes straight from runed.db (d.dbRuned). Operator-only.
func (d *dash) handleRuned(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "runed",
		struct{ Href, Label string }{"/dash/services/", "Services"},
		struct{ Href, Label string }{"", "runed"},
	)

	switch strings.TrimSpace(r.URL.Query().Get("msg")) {
	case "killed":
		fmt.Fprint(w, htmlBanner("ok", "kill requested — the run is being torn down"))
	case "noop":
		fmt.Fprint(w, htmlBanner("warn", "no active run for that folder"))
	}

	if d.dbRuned == nil {
		fmt.Fprint(w, htmlBanner("warn", "runed store unavailable — runs view needs runed.db"))
		pageClose(w, r)
		return
	}

	d.renderActiveRuns(w)
	d.renderRecentRuns(w)
	pageClose(w, r)
}

// renderActiveRuns writes the queued/running runs table with a per-folder kill.
func (d *dash) renderActiveRuns(w http.ResponseWriter) {
	fmt.Fprint(w, `<h2>Active runs</h2>`)
	rows, err := d.dbRuned.Query(
		`SELECT run_id, folder, state, created_at, COALESCE(started_at,''), COALESCE(session_id,'')
		 FROM spawns WHERE state IN ('queued','running')
		 ORDER BY created_at DESC LIMIT 20`)
	if err != nil {
		slog.Warn("runed page: active query", "err", err)
		fmt.Fprint(w, htmlBanner("err", "active runs error: "+err.Error()))
		return
	}
	defer rows.Close()

	var tableRows [][]string
	for rows.Next() {
		var runID, folder, state, createdAt, startedAt, sessionID string
		if err := rows.Scan(&runID, &folder, &state, &createdAt, &startedAt, &sessionID); err != nil {
			slog.Warn("runed page: active scan", "err", err)
			continue
		}
		age := relativeTS(createdAt)
		if state == "running" && startedAt != "" {
			age = relativeTS(startedAt)
		}
		kill := fmt.Sprintf(
			`<form method="post" action="/dash/runed/kill" class="form-inline"`+
				` onsubmit="return confirm('kill active runs for %s?')">`+
				`<input type="hidden" name="folder" value="%s">`+
				`<button class="btn-danger" type="submit">kill</button></form>`,
			esc(folder), esc(folder))
		tableRows = append(tableRows, []string{
			folderLink(folder),
			fmt.Sprintf(`<span class="status-%s">%s</span>`, runStateClass(state), esc(state)),
			esc(age),
			`<code>` + esc(shortID(runID)) + `</code>`,
			kill,
		})
	}
	if err := rows.Err(); err != nil {
		slog.Warn("runed page: active rows", "err", err)
	}
	fmt.Fprint(w, htmlTable([]string{"Folder", "State", "Age", "Run", ""}, tableRows,
		"No active runs."))
}

// renderRecentRuns writes the recently-finished runs table.
func (d *dash) renderRecentRuns(w http.ResponseWriter) {
	fmt.Fprint(w, `<h2>Recent runs</h2>`)
	rows, err := d.dbRuned.Query(
		`SELECT run_id, folder, state, COALESCE(outcome,''), exit_code,
		        COALESCE(started_at,''), COALESCE(ended_at,'')
		 FROM spawns WHERE state IN ('exited','error','killed')
		 ORDER BY ended_at DESC LIMIT 30`)
	if err != nil {
		slog.Warn("runed page: recent query", "err", err)
		fmt.Fprint(w, htmlBanner("err", "recent runs error: "+err.Error()))
		return
	}
	defer rows.Close()

	var tableRows [][]string
	for rows.Next() {
		var runID, folder, state, outcome, startedAt, endedAt string
		var exitCode interface{}
		if err := rows.Scan(&runID, &folder, &state, &outcome, &exitCode, &startedAt, &endedAt); err != nil {
			slog.Warn("runed page: recent scan", "err", err)
			continue
		}
		tableRows = append(tableRows, []string{
			folderLink(folder),
			fmt.Sprintf(`<span class="status-%s">%s</span>`, runStateClass(state), esc(state)),
			esc(outcome),
			esc(exitCodeStr(exitCode)),
			esc(durationBetween(startedAt, endedAt)),
			fmt.Sprintf(`<abbr title="%s">%s</abbr>`, esc(endedAt), esc(relativeTS(endedAt))),
		})
	}
	if err := rows.Err(); err != nil {
		slog.Warn("runed page: recent rows", "err", err)
	}
	fmt.Fprint(w, htmlTable([]string{"Folder", "State", "Outcome", "Exit", "Duration", "Ended"}, tableRows,
		"No recent runs."))
}

// handleRunedKill handles POST /dash/runed/kill — proxy the operator-kill to
// runed's POST /v1/runs/stop for every active run in the folder. Operator-only.
func (d *dash) handleRunedKill(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	folder := strings.TrimSpace(r.FormValue("folder"))
	if folder == "" {
		http.Error(w, "folder required", http.StatusBadRequest)
		return
	}
	if d.runedURL == "" {
		http.Error(w, "RUNED_URL not configured", http.StatusServiceUnavailable)
		return
	}

	killed, err := d.killFolder(r.Context(), folder)
	if err != nil {
		slog.Warn("runed kill", "folder", folder, "err", err)
		http.Error(w, "kill failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategoryMutation,
		Action:   "runed.kill",
		Actor:    "rest:dashd",
		Surface:  audit.SurfaceREST,
		Resource: "spawns/" + folder,
		Folder:   folder,
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"killed": killed,
		},
	})
	slog.Info("runed kill", "folder", folder, "killed", killed)
	msg := "killed"
	if !killed {
		msg = "noop"
	}
	http.Redirect(w, r, "/dash/runed/?msg="+msg, http.StatusSeeOther)
}

// killFolder POSTs {folder} to runed's /v1/runs/stop with the service:dashd
// bearer and reports whether a live spawn was found + killed.
func (d *dash) killFolder(ctx context.Context, folder string) (bool, error) {
	body, _ := json.Marshal(map[string]string{"folder": folder})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.runedURL+"/v1/runs/stop", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if d.svc != nil {
		tok, terr := d.svc(ctx)
		if terr != nil {
			return false, terr
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return false, fmt.Errorf("runed %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out struct {
		Killed bool `json:"killed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.Killed, nil
}

// folderLink renders a folder as a link to its chat page.
func folderLink(folder string) string {
	return fmt.Sprintf(`<a href="/dash/chat/%s/">%s</a>`, folderPath(folder), esc(folder))
}

// shortID truncates a run_id to its first 8 chars for compact display.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// runStateClass maps a spawn state to a status CSS class: exited/running→ok,
// error→err, killed/queued→unknown (SCREENS.md /dash/runed/ "State CSS").
func runStateClass(state string) string {
	switch state {
	case "exited", "running":
		return statusOK
	case "error":
		return statusErr
	default:
		return statusUnknown
	}
}

// exitCodeStr renders an exit_code (nullable INTEGER) as a string; "" when NULL.
func exitCodeStr(v interface{}) string {
	switch n := v.(type) {
	case nil:
		return ""
	case int64:
		return fmt.Sprintf("%d", n)
	case int:
		return fmt.Sprintf("%d", n)
	default:
		return fmt.Sprintf("%v", n)
	}
}

// durationBetween renders the wall-clock span between two ISO timestamps as a
// compact label ("12s", "3m", "1h"); "" when either bound is missing/unparseable.
func durationBetween(start, end string) string {
	s, ok1 := parseTS(start)
	e, ok2 := parseTS(end)
	if !ok1 || !ok2 {
		return ""
	}
	d := e.Sub(s)
	if d < 0 {
		return ""
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// parseTS parses an ISO timestamp in the formats dashd timestamps appear in.
func parseTS(ts string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
