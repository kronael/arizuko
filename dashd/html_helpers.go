package main

import (
	"fmt"
	"net/http"
	"strings"
)

func isHtmx(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// htmlTable renders a <table> with thead+tbody. Empty rows emits a <p class="empty">.
// Pass an optional emptyMsg to override the default "no entries" text.
func htmlTable(headers []string, rows [][]string, emptyMsg ...string) string {
	if len(rows) == 0 {
		msg := "no entries"
		if len(emptyMsg) > 0 && emptyMsg[0] != "" {
			msg = emptyMsg[0]
		}
		return `<p class="empty">` + esc(msg) + `</p>`
	}
	var b strings.Builder
	b.WriteString(`<table><thead><tr>`)
	for _, h := range headers {
		fmt.Fprintf(&b, `<th>%s</th>`, esc(h))
	}
	b.WriteString(`</tr></thead><tbody>`)
	for _, row := range rows {
		b.WriteString(`<tr>`)
		for _, cell := range row {
			fmt.Fprintf(&b, `<td>%s</td>`, cell) // callers escape their own data
		}
		b.WriteString(`</tr>`)
	}
	b.WriteString(`</tbody></table>`)
	return b.String()
}

func htmlBanner(class, msg string) string {
	return fmt.Sprintf(`<div class="banner-%s">%s</div>`, class, esc(msg))
}

// htmlBannerRaw is htmlBanner for pre-escaped/trusted HTML content.
func htmlBannerRaw(class, html string) string {
	return fmt.Sprintf(`<div class="banner-%s">%s</div>`, class, html)
}

func htmlSection(title, body string) string {
	return fmt.Sprintf(`<h2>%s</h2>%s`, esc(title), body)
}

func htmlFormRow(label, inputHTML string) string {
	return fmt.Sprintf(`<p class="form-row"><label>%s %s</label></p>`, esc(label), inputHTML)
}

// htmlDetail renders a <tr> for use inside a detail <table>.
// key is escaped; val is passed raw (caller escapes as needed).
func htmlDetail(key, val string) string {
	return fmt.Sprintf(`<tr><th>%s</th><td>%s</td></tr>`, esc(key), val)
}
