package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// runSplit is timed's federated main loop: it exchanges AUTHD_SERVICE_KEY for a
// service:timed token (mirroring runed's boot-exchange), then ticks the
// federated fire loop. It opens NO messages.db. AUTHD_URL/AUTHD_SERVICE_KEY are
// required; without them routd's bearer gate denies every call.
func runSplit(routerURL, tz string) {
	authdURL := os.Getenv("AUTHD_URL")
	serviceKey := os.Getenv("AUTHD_SERVICE_KEY")
	var token *auth.TokenSource
	if authdURL != "" && serviceKey != "" {
		ts, err := auth.ServiceToken(authdURL, "timed", serviceKey)
		if err != nil {
			slog.Error("timed service-token bootstrap", "err", err)
			os.Exit(1)
		}
		token = ts
		slog.Info("timed service-token bootstrap via authd", "authd", authdURL)
	} else {
		slog.Warn("split mode without AUTHD_URL/AUTHD_SERVICE_KEY; routd will deny",
			"authd_url", authdURL != "", "service_key", serviceKey != "")
	}

	r := &router{
		base:  routerURL,
		token: token,
		http:  &http.Client{Timeout: 15 * time.Second},
		tz:    tz,
	}
	slog.Info("scheduler started (split)", "router", routerURL, "tz", tz)

	// ks gates /dash/timed/ to operators (proxyd-stamped X-User-Groups `**`).
	// AUTHD_URL set → fetch authd's JWKS as the enforce switch; unset (local dev)
	// → nil ks → open, mirroring onbod's dashboard gate.
	var ks *auth.KeySet
	if authdURL != "" {
		if k, kerr := auth.FetchKeys(context.Background(), authdURL); kerr != nil {
			slog.Warn("fetch authd keys for dash gate", "err", kerr)
		} else {
			ks = k
		}
	}
	dash := &dashServer{r: r, ks: ks}

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		})
		mux.HandleFunc("GET /openapi.json", resreg.OpenAPIHandler("timed", []string{"scheduled_tasks"}))
		mux.HandleFunc("GET /dash/timed/", dash.handleDash)
		if err := http.ListenAndServe(":8080", mux); err != nil {
			slog.Error("health server", "err", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	tick := time.NewTicker(60 * time.Second)
	defer tick.Stop()

	ctx := context.Background()
	r.fireSplit(ctx)
	for {
		select {
		case <-tick.C:
			r.fireSplit(ctx)
		case <-stop:
			slog.Info("scheduler stopped")
			return
		}
	}
}

// split.go is timed's federated fire loop: in the split topology timed opens NO
// messages.db. It claims due tasks, enqueues their prompts, logs the run, and
// reschedules — all over routd's HTTP surface (GET /v1/tasks/due,
// POST /v1/messages, POST /v1/tasks/runlog, POST /v1/tasks/{id}/reschedule),
// presenting an auto-refreshing service:timed token. computeNextRun stays
// client-side (cron/interval parsing); routd is a boring writer.

// router is the split-mode client: the routd base URL + a live service token.
// loop holds the in-memory fire-loop health the /dash/timed/ overview renders;
// timed owns the fire runtime (routd owns the at-rest rows), so last-tick
// time/outcome is timed-local state, mutex-guarded against the dash reader.
type router struct {
	base  string
	token *auth.TokenSource
	http  *http.Client
	tz    string

	mu   sync.Mutex
	loop loopState
}

// loopState is the last fire-loop tick's outcome, read by the dashboard.
type loopState struct {
	LastTick time.Time
	Fired    int
	Err      string
}

func (r *router) recordTick(fired int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loop.LastTick = time.Now()
	r.loop.Fired = fired
	if err != nil {
		r.loop.Err = err.Error()
	} else {
		r.loop.Err = ""
	}
}

func (r *router) loopHealth() loopState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loop
}

// dueTask mirrors routd's GET /v1/tasks/due row.
type dueTask struct {
	ID          string `json:"id"`
	ChatJID     string `json:"chat_jid"`
	Prompt      string `json:"prompt"`
	Cron        string `json:"cron"`
	ContextMode string `json:"context_mode"`
}

// fireSplit runs one federated tick: claim due tasks at routd, then for each
// enqueue the prompt + log the run + reschedule. No local DB.
func (r *router) fireSplit(ctx context.Context) {
	tasks, err := r.due(ctx)
	if err != nil {
		slog.Error("claim due tasks (split)", "err", err)
		r.recordTick(0, err)
		return
	}
	defer r.recordTick(len(tasks), nil)
	for _, t := range tasks {
		start := time.Now()
		sender := "timed"
		if t.ContextMode == "isolated" {
			sender = "timed-isolated:" + t.ID
		}
		id := core.MsgID("sched-" + t.ID)
		if err := r.enqueue(ctx, apiv1.Message{
			ID: id, ChatJID: t.ChatJID, Sender: sender, Content: t.Prompt,
			Timestamp: start.Unix(),
		}); err != nil {
			slog.Error("enqueue message (split)", "task", t.ID, "jid", t.ChatJID, "err", err)
			r.runlog(ctx, t.ID, "error", err.Error(), time.Since(start).Milliseconds())
			// Restore to active so the next tick re-fires (mirrors the monolith).
			r.reschedule(ctx, t.ID, "", "active")
			continue
		}

		nextRun := computeNextRun(t.Cron, r.tz, t.ID)
		status := "active"
		if nextRun == "" && t.Cron == "" {
			status = "completed"
		}
		if err := r.reschedule(ctx, t.ID, nextRun, status); err != nil {
			slog.Error("reschedule task (split)", "task", t.ID, "err", err)
		}
		r.runlog(ctx, t.ID, "success", "", time.Since(start).Milliseconds())
		slog.Info("fired task (split)",
			"id", t.ID, "jid", t.ChatJID, "cron", t.Cron,
			"context_mode", t.ContextMode, "next_run", nextRun)
	}
}

func (r *router) due(ctx context.Context) ([]dueTask, error) {
	var out struct {
		Tasks []dueTask `json:"tasks"`
	}
	if err := r.call(ctx, "GET", "/v1/tasks/due", nil, &out); err != nil {
		return nil, err
	}
	return out.Tasks, nil
}

func (r *router) enqueue(ctx context.Context, m apiv1.Message) error {
	return r.call(ctx, "POST", "/v1/messages", m, nil)
}

func (r *router) runlog(ctx context.Context, taskID, status, errText string, durationMs int64) {
	body := map[string]any{
		"task_id": taskID, "status": status, "error": errText, "duration_ms": durationMs,
	}
	if err := r.call(ctx, "POST", "/v1/tasks/runlog", body, nil); err != nil {
		slog.Error("runlog (split)", "task", taskID, "err", err)
	}
}

func (r *router) reschedule(ctx context.Context, taskID, nextRun, status string) error {
	body := map[string]string{"next_run": nextRun, "status": status}
	// PathEscape the id: sub-folder task ids carry a slash (main/trading-mem-0),
	// and a raw slash breaks the {id} path-segment wildcard → 404 → the reschedule
	// silently fails → the task is stranded in 'firing' and stops firing
	// (stalled every sub-folder group's compaction crons). %2F matches the segment.
	return r.call(ctx, "POST", "/v1/tasks/"+url.PathEscape(taskID)+"/reschedule", body, nil)
}

// call performs one authenticated round-trip: the live service token in the
// Authorization header, JSON in and (optionally) out.
func (r *router) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(r.base, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.token != nil {
		tok, terr := r.token.Token(ctx)
		if terr != nil {
			return fmt.Errorf("service token: %w", terr)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
