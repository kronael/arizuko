package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// The L33 case end to end: a one-shot fires, routd RECORDS the row but the
// response is lost, so timed sees an error and re-arms. The retry must carry
// the SAME message id — that is the only thing routd's id-PK dedup can key on,
// and without it the task ran twice.
func TestLostResponseRetryReusesTheFireID(t *testing.T) {
	next := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	task := dueTask{ID: "once", ChatJID: "web:main", Prompt: "do it", Cron: "", NextRun: next}

	var enqueued []apiv1.Message
	var reschedules []rescheduleCall
	fail := true
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/tasks/due", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tasks": []dueTask{task}})
	})
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		var m apiv1.Message
		_ = json.NewDecoder(r.Body).Decode(&m)
		enqueued = append(enqueued, m)
		if fail {
			// routd stored the row; the response never arrived.
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /v1/tasks/runlog", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /v1/tasks/{id}/reschedule", func(w http.ResponseWriter, r *http.Request) {
		var c rescheduleCall
		_ = json.NewDecoder(r.Body).Decode(&c)
		c.ID = r.PathValue("id")
		reschedules = append(reschedules, c)
		// routd is the writer: the re-armed next_run is what the next claim
		// returns, so mirror it onto the task the stub serves.
		task.NextRun = c.NextRun
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r := &router{base: srv.URL, http: srv.Client(), tz: "UTC"}
	r.fireSplit(context.Background()) // response lost
	fail = false
	r.fireSplit(context.Background()) // the retry

	if len(enqueued) != 2 {
		t.Fatalf("enqueued %d messages, want 2 (the lost one and the retry)", len(enqueued))
	}
	if enqueued[0].ID != enqueued[1].ID {
		t.Fatalf("retry minted a fresh id %q for the same fire as %q — routd cannot dedup it",
			enqueued[1].ID, enqueued[0].ID)
	}
	// The one-shot went back to the next_run it was claimed at, not now+1m:
	// that value IS the fire identity.
	if len(reschedules) != 2 {
		t.Fatalf("reschedules = %d, want 2", len(reschedules))
	}
	if reschedules[0].NextRun != next {
		t.Errorf("error-path reschedule next_run = %q, want the claimed %q",
			reschedules[0].NextRun, next)
	}
	if reschedules[1].Status != "completed" {
		t.Errorf("after the successful retry status = %q, want completed", reschedules[1].Status)
	}
}

// A one-shot an operator RE-ARMS is a different fire and must not be deduped
// against the original: a new next_run gives a new id.
func TestReArmedOneShotGetsANewFireID(t *testing.T) {
	base := dueTask{ID: "once", ChatJID: "web:main", Prompt: "do it"}
	first := base
	first.NextRun = "2026-08-15T09:00:00Z"
	second := base
	second.NextRun = "2026-08-16T09:00:00Z"

	if fireID(first) == fireID(second) {
		t.Fatalf("a re-armed one-shot reuses id %q — the second fire would be silently dropped",
			fireID(first))
	}
	if !strings.HasPrefix(fireID(first), "sched-once@") {
		t.Errorf("fireID = %q, want a sched-<task>@<next_run> shape", fireID(first))
	}
}

// Two attempts at the SAME fire are the same id, with no wall-clock component.
func TestFireIDIsStableAcrossAttempts(t *testing.T) {
	tk := dueTask{ID: "t", NextRun: "2026-08-15T09:00:00Z"}
	a := fireID(tk)
	time.Sleep(2 * time.Millisecond)
	if b := fireID(tk); a != b {
		t.Fatalf("fireID is not stable: %q then %q", a, b)
	}
}

// No scheduled time to key on: fall back to the wall clock rather than mint a
// stable-looking id that would dedup two legitimate fires into one.
func TestFireIDWithoutNextRunStaysUnique(t *testing.T) {
	tk := dueTask{ID: "t"}
	a := fireID(tk)
	b := fireID(tk)
	if a == b {
		t.Fatalf("a next_run-less task reused id %q; a second fire would be dropped", a)
	}
	if !strings.HasPrefix(a, "sched-t-") {
		t.Errorf("fallback id = %q, want the sched-<task>-<nano> shape", a)
	}
}

// A cron task keeps taking its next natural slot on the error path: the missed
// fire is not the same fire as the next one.
func TestCronErrorPathTakesTheNextSlot(t *testing.T) {
	claimed := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	var reschedules []rescheduleCall
	mux := http.NewServeMux()
	served := false
	mux.HandleFunc("GET /v1/tasks/due", func(w http.ResponseWriter, _ *http.Request) {
		var out []dueTask
		if !served {
			out = []dueTask{{ID: "c1", ChatJID: "web:main", Prompt: "p",
				Cron: "0 9 * * *", NextRun: claimed}}
		}
		served = true
		_ = json.NewEncoder(w).Encode(map[string]any{"tasks": out})
	})
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	mux.HandleFunc("POST /v1/tasks/runlog", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /v1/tasks/{id}/reschedule", func(w http.ResponseWriter, r *http.Request) {
		var c rescheduleCall
		_ = json.NewDecoder(r.Body).Decode(&c)
		reschedules = append(reschedules, c)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r := &router{base: srv.URL, http: srv.Client(), tz: "UTC"}
	r.fireSplit(context.Background())

	if len(reschedules) != 1 {
		t.Fatalf("reschedules = %d, want 1", len(reschedules))
	}
	at, err := time.Parse(time.RFC3339, reschedules[0].NextRun)
	if err != nil || !at.After(time.Now()) {
		t.Fatalf("cron error path next_run = %q, want the next future slot",
			reschedules[0].NextRun)
	}
}
