package main

// GET /dash/audit/ — the operator audit log, FEDERATED across the daemons that
// own one (spec 5/I, BUGS F29).
//
// audit_log is per-daemon by design: each daemon owns and migrates its own DB
// and its own audit table, and correlation across them is the turn_id, not a
// shared table. This page used to read routd.db directly through d.adminDB(),
// so it showed routd's rows and only routd's — an operator who killed a run
// (runed's run.kill) or whose login was recorded (authd's login) got nothing.
//
// It now fans out to each owner's GET /v1/audit and merges. Reading runed.db
// and auth.db directly would have been fewer lines and is the thing this page
// must not do: dashd is FS-mounted on routd.db alone, and a second reader of a
// table whose owner exposes no contract is the recorded defect class that
// dashd/route_tokens.go already is. One path, three hosts.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
)

// auditFanoutTimeout bounds the whole fan-out. Three sequential daemon calls
// on a page an operator opens while something is already going wrong; a dead
// daemon must cost a banner, not a hung dashboard.
const auditFanoutTimeout = 10 * time.Second

// auditPageSize is the rendered window. Each source is asked for one more than
// this so a full page can tell "exactly 50" from "more behind it".
const auditPageSize = 50

// auditSource is one federated origin: the daemon's name (rendered, and the
// key in the composite cursor) and its base URL.
type auditSource struct {
	name string
	url  string
}

// auditSources lists the daemons that own an audit_log and publish it. Order is
// the tie-break when two rows share a created_at, so it is fixed rather than a
// map walk — a page that reshuffles equal-timestamp rows between reloads reads
// as data changing under the operator.
//
// onbod owns an audit_log too (migration 0002) and writes real rows, but it
// does not yet mount /v1/audit — recorded as BUGS F35 rather than added here,
// because a new API surface on a daemon the sign-off did not name is exactly
// the change that needs one.
func (d *dash) auditSources() []auditSource {
	return []auditSource{
		{"routd", d.routdURL},
		{"runed", d.runedURL},
		{"authd", d.authdURL},
	}
}

// auditRow is one merged row: the wire record plus which daemon served it.
type auditRow struct {
	audit.Row
	Source string
}

// handleAudit renders the federated log, newest first, operator-only.
func (d *dash) handleAudit(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	pageTopFor(w, r, "audit")

	q := r.URL.Query()
	f := auditFilters{
		cat:    strings.TrimSpace(q.Get("cat")),
		actor:  strings.TrimSpace(q.Get("actor")),
		folder: strings.TrimSpace(q.Get("folder")),
	}
	cursors := parseAuditCursor(q.Get("before"))

	fmt.Fprint(w, d.auditFilterForm(f))

	ctx, cancel := context.WithTimeout(r.Context(), auditFanoutTimeout)
	defer cancel()

	var merged []auditRow
	var failures []string
	for _, src := range d.auditSources() {
		if src.url == "" {
			failures = append(failures, src.name+": no URL configured")
			continue
		}
		rows, err := d.auditFetch(ctx, src, f, cursors[src.name])
		if err != nil {
			// Loud and visible: a source that cannot be read is NOT an empty
			// source. Rendering the survivors silently would tell an operator
			// "nothing happened in runed" when the truth is "runed did not
			// answer" — the worst possible lie for an audit page.
			slog.Warn("audit page: source failed", "source", src.name, "err", err)
			failures = append(failures, src.name+": "+err.Error())
			continue
		}
		for _, row := range rows {
			merged = append(merged, auditRow{Row: row, Source: src.name})
		}
	}
	for _, msg := range failures {
		fmt.Fprint(w, htmlBanner("err", "audit source unavailable — "+msg))
	}

	// Newest first across all sources. created_at is an ISO-8601 UTC string
	// with millisecond precision, so lexical order IS chronological order; the
	// source name then the id break ties so the sort is total and stable.
	sort.Slice(merged, func(i, j int) bool {
		a, b := merged[i], merged[j]
		if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt > b.CreatedAt
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.ID > b.ID
	})

	more := len(merged) > auditPageSize
	if more {
		merged = merged[:auditPageSize]
	}

	tableRows := make([][]string, 0, len(merged))
	for _, row := range merged {
		outcomeCell := fmt.Sprintf(`<span class="%s">%s</span>`, outcomeClass(row.Outcome), esc(row.Outcome))
		if row.ErrorMsg != "" {
			outcomeCell = fmt.Sprintf(`<abbr title="%s">%s</abbr>`, esc(row.ErrorMsg), outcomeCell)
		}
		tableRows = append(tableRows, []string{
			abbrTS(row.CreatedAt),
			esc(row.Source),
			esc(row.Category),
			esc(row.Action),
			esc(row.Actor),
			esc(row.Folder),
			outcomeCell,
			`<code>` + esc(row.Resource) + `</code>`,
			esc(row.Surface),
			esc(row.ParamsSummary),
		})
	}

	fmt.Fprint(w, htmlTable(
		[]string{"Time", "Source", "Category", "Action", "Actor", "Folder", "Outcome", "Resource", "Surface", "Params"},
		tableRows))

	if more {
		fmt.Fprintf(w, `<p><a class="btn" href="%s">older &rarr;</a></p>`,
			esc(auditOlderHref(nextAuditCursor(cursors, merged), f)))
	}

	pageClose(w, r)
}

// auditFilters are the operator's filter-form values, carried through the
// fan-out and back into the pagination link.
type auditFilters struct {
	cat    string
	actor  string
	folder string
}

// auditFetch reads one source's GET /v1/audit with the service:dashd bearer.
//
// The bearer is the WHOLE authorization: each daemon gates /v1/audit on the
// audit:read scope, which only service:dashd holds. dashd does not forward
// X-User-* here — unlike proxydCall, where proxyd authorizes the forwarded
// operator identity — because the operator check already happened at the top
// of this handler and no audit daemon consults a forwarded header. The two
// gates in series are requireOperator (human) and audit:read (transit).
func (d *dash) auditFetch(ctx context.Context, src auditSource, f auditFilters, before int64) ([]audit.Row, error) {
	v := url.Values{}
	// One more than the page: the merge needs to know a source had more behind
	// it even when another source supplied every rendered row.
	v.Set("limit", strconv.Itoa(auditPageSize+1))
	if f.cat != "" {
		v.Set("category", f.cat)
	}
	if f.actor != "" {
		v.Set("actor", f.actor)
	}
	if f.folder != "" {
		v.Set("folder", f.folder)
	}
	if before > 0 {
		v.Set("before_id", strconv.FormatInt(before, 10))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.url+"/v1/audit?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if d.svc != nil {
		tok, terr := d.svc(ctx)
		if terr != nil {
			return nil, terr
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("%s %d: %s", src.name, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var rows []audit.Row
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// parseAuditCursor decodes `routd:123,runed:45` into per-source row ids.
//
// The cursor is COMPOSITE because `id` is a per-DB AUTOINCREMENT: one integer
// cannot page three independent sequences. The alternative — paginating on
// created_at — silently drops rows sharing a millisecond across sources, which
// on an audit page is a missing record rather than a cosmetic glitch.
// Unparseable pairs are skipped, which reads as "start that source from the
// newest row" rather than failing the page on a hand-edited URL.
func parseAuditCursor(raw string) map[string]int64 {
	out := map[string]int64{}
	for part := range strings.SplitSeq(raw, ",") {
		name, idStr, ok := strings.Cut(strings.TrimSpace(part), ":")
		// An empty name (":9") parses as a valid pair and would ride into the
		// NEXT cursor as a phantom source that matches no daemon, growing the
		// link on every page turn.
		if !ok || name == "" {
			continue
		}
		if id, err := strconv.ParseInt(idStr, 10, 64); err == nil && id > 0 {
			out[name] = id
		}
	}
	return out
}

// nextAuditCursor advances each source to the OLDEST row it actually
// contributed to the rendered page. A source that contributed none keeps its
// previous cursor: it was outbid on this page, not exhausted, and resetting it
// would replay its newest rows forever.
func nextAuditCursor(prev map[string]int64, rendered []auditRow) string {
	next := map[string]int64{}
	for k, v := range prev {
		next[k] = v
	}
	for _, row := range rendered {
		if cur, ok := next[row.Source]; !ok || row.ID < cur {
			next[row.Source] = row.ID
		}
	}
	names := make([]string, 0, len(next))
	for name := range next {
		names = append(names, name)
	}
	slices.Sort(names) // stable link text across reloads
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+":"+strconv.FormatInt(next[name], 10))
	}
	return strings.Join(parts, ",")
}

// auditFilterForm renders the inline GET filter form. The category dropdown is
// audit.AllCategories — the closed enum — rather than a SELECT DISTINCT: the
// vocabulary is fixed, and asking three daemons for their distinct values would
// be three more round-trips to answer a question the constant already answers.
func (d *dash) auditFilterForm(f auditFilters) string {
	var opts strings.Builder
	opts.WriteString(`<option value="">all categories</option>`)
	for _, c := range audit.AllCategories {
		sel := ""
		if c == f.cat {
			sel = ` selected`
		}
		fmt.Fprintf(&opts, `<option value="%s"%s>%s</option>`, esc(c), sel, esc(c))
	}
	return fmt.Sprintf(
		`<form method="get" action="/dash/audit/" class="filter-row">`+
			`<select name="cat">%s</select>`+
			`<input name="actor" placeholder="actor" value="%s">`+
			`<input name="folder" placeholder="folder" value="%s">`+
			`<button class="btn" type="submit">filter</button>`+
			`</form>`,
		opts.String(), esc(f.actor), esc(f.folder))
}

// auditOlderHref builds the "older" pagination link, carrying the filters and
// the composite cursor.
func auditOlderHref(cursor string, f auditFilters) string {
	v := url.Values{}
	v.Set("before", cursor)
	if f.cat != "" {
		v.Set("cat", f.cat)
	}
	if f.actor != "" {
		v.Set("actor", f.actor)
	}
	if f.folder != "" {
		v.Set("folder", f.folder)
	}
	return "/dash/audit/?" + v.Encode()
}

// outcomeClass maps an audit outcome to its status CSS class. "ok" is green;
// anything else (error/denied) is red.
func outcomeClass(outcome string) string {
	if outcome == "ok" {
		return "status-ok"
	}
	return "status-err"
}
