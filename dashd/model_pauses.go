package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/kronael/arizuko/resreg/resources"
)

func (d *dash) handleModelPauses(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	body, status, err := d.routdGet(r.Context(), "/v1/model_pauses")
	if err != nil || status != http.StatusOK {
		http.Error(w, upstreamErr("routd", status, body), http.StatusBadGateway)
		return
	}
	var rows []resources.ModelPauseRow
	if err := json.Unmarshal(body, &rows); err != nil {
		http.Error(w, "invalid model pause response", http.StatusBadGateway)
		return
	}
	pageTopFor(w, r, "model pauses")
	_, _ = fmt.Fprint(w, `<p>Your messages stay saved while model access is paused. A saved turn retries automatically.</p>`)
	var cells [][]string
	for _, row := range rows {
		next := "probe running"
		if row.NextAttemptAt != nil {
			next = *row.NextAttemptAt
		}
		control := fmt.Sprintf(`<form method="post" action="/dash/model-pauses/clear"><input type="hidden" name="opened_at" value="%s"><input type="hidden" name="probe_attempt_id" value="%s"><button>Clear pause</button></form>`, esc(row.OpenedAt), esc(row.ProbeAttemptID))
		cells = append(cells, []string{esc(row.Reason), esc(row.OpenedAt), esc(next),
			fmt.Sprint(row.QueuedCount), control})
	}
	_, _ = fmt.Fprint(w, htmlTable([]string{"reason", "opened", "next attempt", "saved messages", "control"}, cells, "model access is available"))
	pageClose(w, r)
}

func (d *dash) handleModelPauseClear(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) || !requireSameOrigin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	body, status, err := d.routdCall(r.Context(), http.MethodPost, "/v1/model_pauses/clear",
		map[string]string{"opened_at": r.FormValue("opened_at"), "probe_attempt_id": r.FormValue("probe_attempt_id")})
	if err != nil || status != http.StatusOK {
		http.Error(w, upstreamErr("routd", status, body), http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, "/dash/model-pauses/", http.StatusSeeOther)
}
