package main

import (
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/theme"
)

// dash.go is timed's operator overview at GET /dash/timed/ (spec 6/8): fire-loop
// health (timed-local, in-memory) + the scheduled-task table. timed owns no DB
// in the split, so task rows arrive from routd's GET /v1/tasks over the same
// service:timed bearer the fire loop uses; only loop health is timed-local.

// twoTicks is the lag/stuck threshold: the fire loop ticks every 60s, so a row
// whose next_run passed >2 ticks ago (active) or that has been 'firing' >2 ticks
// is flagged. ≤1 tick of lag is normal (waiting on the next tick).
const twoTicks = 2 * time.Minute

// dashServer renders /dash/timed/. r is the routd client (task reads + the
// in-memory loop health); ks is the operator gate switch (nil → open).
type dashServer struct {
	r  *router
	ks *auth.KeySet
}

// taskRow mirrors core.Task's JSON (no struct tags → Go field names). Only the
// fields the overview renders are decoded.
type taskRow struct {
	ID          string     `json:"ID"`
	Owner       string     `json:"Owner"`
	ChatJID     string     `json:"ChatJID"`
	Prompt      string     `json:"Prompt"`
	Cron        string     `json:"Cron"`
	NextRun     *time.Time `json:"NextRun"`
	Status      string     `json:"Status"`
	ContextMode string     `json:"ContextMode"`
}

// requireOperator gates the dashboard to an operator (`**` in the proxyd-stamped
// X-User-Groups). nil ks (local dev / tests, AUTHD_URL unset) → open, mirroring
// onbod's dashboard gate and routd's nil-verifier path.
func (d *dashServer) requireOperator(w http.ResponseWriter, r *http.Request) bool {
	if d.ks == nil {
		return true
	}
	hdr := r.Header.Get("X-User-Groups")
	if hdr != "" {
		var groups []string
		_ = json.Unmarshal([]byte(hdr), &groups)
		for _, g := range groups {
			if g == "**" {
				return true
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprint(w, theme.Page("Forbidden",
		template.HTML(`<div class="banner-err">Operator access required.</div>`)))
	return false
}

// handleDash renders the overview: fire-loop health + per-status counts + the
// task table sorted by next_run. Tasks come from routd /v1/tasks; loop health
// is timed-local in-memory state.
func (d *dashServer) handleDash(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}

	var out struct {
		Tasks []taskRow `json:"tasks"`
	}
	if err := d.r.call(r.Context(), "GET", "/v1/tasks", nil, &out); err != nil {
		slog.Error("dash: list tasks", "err", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, theme.Page("timed",
			template.HTML(`<div class="banner-err">Could not reach routd for task data.</div>`)))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, theme.Page("timed", renderDash(out.Tasks, d.r.loopHealth(), time.Now())))
}

// renderDash builds the overview body: a loop-health line, a per-status count
// strip, then the task table. now is passed in so the test can assert lag/stuck
// rendering deterministically.
func renderDash(tasks []taskRow, loop loopState, now time.Time) template.HTML {
	var s []byte
	s = append(s, loopSection(loop, now)...)
	s = append(s, statusCounts(tasks)...)
	s = append(s, taskTable(tasks, now)...)
	return template.HTML(s)
}

// loopSection renders the fire-loop health line: last tick time, tasks fired,
// and any claim error.
func loopSection(loop loopState, now time.Time) []byte {
	if loop.LastTick.IsZero() {
		return []byte(`<div class="dim">No tick yet.</div>`)
	}
	dot := "dot-ok"
	detail := fmt.Sprintf("fired %d", loop.Fired)
	if loop.Err != "" {
		dot = "dot-err"
		detail = "error: " + esc(loop.Err)
	}
	return []byte(fmt.Sprintf(
		`<p>Last tick <span class="mono">%s</span> (%s ago) — %s<span class="dot %s"></span></p>`,
		esc(loop.LastTick.UTC().Format(time.RFC3339)),
		esc(roundDur(now.Sub(loop.LastTick))), detail, dot))
}

// statusCounts renders the per-status count strip. Statuses are whatever routd
// returns (active|paused|firing|completed); unknown values still count.
func statusCounts(tasks []taskRow) []byte {
	counts := map[string]int{}
	for _, t := range tasks {
		counts[t.Status]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []byte(fmt.Sprintf(`<p class="dim">%d tasks`, len(tasks)))
	for _, k := range keys {
		out = append(out, []byte(fmt.Sprintf(" · %s %d", esc(k), counts[k]))...)
	}
	return append(out, []byte(`</p>`)...)
}

// taskTable renders the task rows sorted by next_run ascending (soonest first;
// nil next_run last). Each row flags lag (active, next_run >2 ticks past) and
// stuck fires (firing >2 ticks) per spec 6/8.
func taskTable(tasks []taskRow, now time.Time) []byte {
	sort.SliceStable(tasks, func(i, j int) bool {
		ni, nj := tasks[i].NextRun, tasks[j].NextRun
		switch {
		case ni == nil && nj == nil:
			return false
		case ni == nil:
			return false
		case nj == nil:
			return true
		default:
			return ni.Before(*nj)
		}
	})

	out := []byte(`<table><tr><th>ID</th><th>Owner</th><th>Chat</th><th>Prompt</th>` +
		`<th>Cron</th><th>Next run</th><th>Status</th></tr>`)
	for _, t := range tasks {
		out = append(out, []byte(fmt.Sprintf(
			`<tr><td class="mono">%s</td><td>%s</td><td class="mono">%s</td>`+
				`<td>%s</td><td class="mono">%s</td><td class="mono">%s</td><td>%s</td></tr>`,
			esc(t.ID), esc(emptyDash(t.Owner)), esc(emptyDash(t.ChatJID)),
			esc(truncate(t.Prompt, 60)), esc(emptyDash(t.Cron)),
			esc(nextRunCell(t.NextRun)), statusCell(t, now)))...)
	}
	if len(tasks) == 0 {
		out = append(out, []byte(`<tr><td colspan="7" class="empty">No scheduled tasks.</td></tr>`)...)
	}
	return append(out, []byte(`</table>`)...)
}

// statusCell renders the status text plus the lag/stuck flag for one row.
func statusCell(t taskRow, now time.Time) string {
	base := esc(t.Status)
	if t.Status == "firing" && t.NextRun != nil && now.Sub(*t.NextRun) > twoTicks {
		return base + ` <span class="dot dot-err"></span>stuck`
	}
	if t.Status == "active" && t.NextRun != nil && now.Sub(*t.NextRun) > twoTicks {
		return base + ` <span class="dot dot-warn"></span>lagging`
	}
	return base
}

func nextRunCell(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.UTC().Format(time.RFC3339)
}

func roundDur(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Minute).String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func esc(s string) string { return html.EscapeString(s) }

func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
