package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// handleAudit renders GET /dash/audit/ — the read-only operator audit log.
// Newest first, 50 rows per page, with category/actor/folder filters and a
// cursor (?before=<id>) for older pages. Operator-only.
func (d *dash) handleAudit(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Audit")

	if d.db == nil {
		fmt.Fprint(w, htmlBanner("err", "audit store unavailable"))
		pageClose(w, r)
		return
	}

	q := r.URL.Query()
	cat := strings.TrimSpace(q.Get("cat"))
	actor := strings.TrimSpace(q.Get("actor"))
	folder := strings.TrimSpace(q.Get("folder"))
	before := strings.TrimSpace(q.Get("before"))

	// Filter form — category dropdown from distinct DB values, free-text rest.
	fmt.Fprint(w, d.auditFilterForm(cat, actor, folder))

	where := []string{"1=1"}
	args := []any{}
	if cat != "" {
		where = append(where, "category = ?")
		args = append(args, cat)
	}
	if actor != "" {
		where = append(where, "actor LIKE '%'||?||'%'")
		args = append(args, actor)
	}
	if folder != "" {
		where = append(where, "folder = ?")
		args = append(args, folder)
	}
	if before != "" {
		if id, err := strconv.ParseInt(before, 10, 64); err == nil {
			where = append(where, "id < ?")
			args = append(args, id)
		}
	}

	rows, err := d.db.Query(
		`SELECT id, created_at, category, action, actor, COALESCE(folder,''),
		        outcome, COALESCE(resource,''), COALESCE(surface,''),
		        COALESCE(params_summary,''), COALESCE(error_msg,'')
		 FROM audit_log
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY id DESC LIMIT 51`, args...)
	if err != nil {
		slog.Warn("audit page: query", "err", err)
		fmt.Fprint(w, htmlBanner("err", "audit query error: "+err.Error()))
		pageClose(w, r)
		return
	}
	defer rows.Close()

	var tableRows [][]string
	var lastID int64
	for rows.Next() {
		var id int64
		var createdAt, category, action, actr, fld, outcome, resource, surface, params, errMsg string
		if err := rows.Scan(&id, &createdAt, &category, &action, &actr, &fld,
			&outcome, &resource, &surface, &params, &errMsg); err != nil {
			slog.Warn("audit page: scan", "err", err)
			continue
		}
		lastID = id
		outcomeCell := fmt.Sprintf(`<span class="%s">%s</span>`, outcomeClass(outcome), esc(outcome))
		if errMsg != "" {
			outcomeCell = fmt.Sprintf(`<abbr title="%s">%s</abbr>`, esc(errMsg), outcomeCell)
		}
		tableRows = append(tableRows, []string{
			fmt.Sprintf(`<abbr title="%s">%s</abbr>`, esc(createdAt), esc(relativeTS(createdAt))),
			esc(category),
			esc(action),
			esc(actr),
			esc(fld),
			outcomeCell,
			`<code>` + esc(resource) + `</code>`,
			esc(surface),
			esc(params),
		})
	}
	if err := rows.Err(); err != nil {
		slog.Warn("audit page: rows", "err", err)
	}

	// 51st row signals more — drop it and offer an "older" link carrying filters.
	more := len(tableRows) > 50
	if more {
		tableRows = tableRows[:50]
	}

	fmt.Fprint(w, htmlTable(
		[]string{"Time", "Category", "Action", "Actor", "Folder", "Outcome", "Resource", "Surface", "Params"},
		tableRows))

	if more {
		fmt.Fprintf(w, `<p><a class="btn" href="%s">older &rarr;</a></p>`,
			esc(auditOlderHref(lastID, cat, actor, folder)))
	}

	pageClose(w, r)
}

// auditFilterForm renders the inline GET filter form. The category <select> is
// populated from the distinct categories present in audit_log.
func (d *dash) auditFilterForm(cat, actor, folder string) string {
	var opts strings.Builder
	opts.WriteString(`<option value="">all categories</option>`)
	rows, err := d.db.Query(`SELECT DISTINCT category FROM audit_log ORDER BY category`)
	if err != nil {
		slog.Warn("audit page: distinct categories", "err", err)
	} else {
		defer rows.Close()
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				continue
			}
			sel := ""
			if c == cat {
				sel = ` selected`
			}
			fmt.Fprintf(&opts, `<option value="%s"%s>%s</option>`, esc(c), sel, esc(c))
		}
	}

	return fmt.Sprintf(
		`<form method="get" action="/dash/audit/" class="filter-row">`+
			`<select name="cat">%s</select>`+
			`<input name="actor" placeholder="actor" value="%s">`+
			`<input name="folder" placeholder="folder" value="%s">`+
			`<button class="btn" type="submit">filter</button>`+
			`</form>`,
		opts.String(), esc(actor), esc(folder))
}

// auditOlderHref builds the "older" pagination link, carrying current filters
// and a before=<lastID> cursor.
func auditOlderHref(lastID int64, cat, actor, folder string) string {
	v := url.Values{}
	v.Set("before", strconv.FormatInt(lastID, 10))
	if cat != "" {
		v.Set("cat", cat)
	}
	if actor != "" {
		v.Set("actor", actor)
	}
	if folder != "" {
		v.Set("folder", folder)
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
