package routd

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

type scheduleState struct {
	LastTick time.Time
	Fired    int
	Err      string
}
type ingressResult struct {
	header http.Header
	status int
	body   strings.Builder
}

func (r *ingressResult) Header() http.Header         { return r.header }
func (r *ingressResult) WriteHeader(status int)      { r.status = status }
func (r *ingressResult) Write(p []byte) (int, error) { return r.body.Write(p) }

func (s *Server) ScheduleLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	s.fireTasks()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.fireTasks()
		}
	}
}

func (s *Server) fireTasks() {
	tasks, err := s.db.DueTasks(time.Now())
	state := scheduleState{LastTick: time.Now(), Fired: len(tasks)}
	if err != nil {
		state.Err = err.Error()
		slog.Error("claim due tasks", "err", err)
	}
	for _, task := range tasks {
		start := time.Now()
		next := ""
		if task.NextRun != nil {
			next = task.NextRun.UTC().Format(time.RFC3339)
		}
		id := core.MsgID("sched-" + task.ID)
		if next != "" {
			id = "sched-" + task.ID + "@" + next
		}
		sender := core.ScheduledSender
		if task.ContextMode == "isolated" {
			sender = "timed-isolated:" + task.ID
		}
		result := &ingressResult{header: make(http.Header)}
		s.ingestMessage(result, apiv1.Message{ID: id, ChatJID: task.Owner, ForwardedFrom: task.ChatJID, Sender: sender, Content: task.Prompt, Timestamp: start.Unix()}, "service:timed", "")
		status, errorText, retry := "success", "", ""
		if result.status >= 400 {
			status = "error"
			errorText = fmt.Sprintf("enqueue status %d: %s", result.status, result.body.String())
			state.Err = errorText
			slog.Error("enqueue task message", "task", task.ID, "jid", task.ChatJID, "err", errorText)
			if task.Cron == "" {
				retry = next
				if retry == "" {
					retry = time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
				}
			}
		}
		if status == "error" {
			s.recordScheduleRun(task.ID, status, errorText, start, &state)
		}
		if err := s.db.RescheduleTask(task.ID, retry); err != nil {
			state.Err = err.Error()
			if status == "error" {
				slog.Error("task not restored to active; it stays claimed and no tick re-fires it", "task", task.ID, "jid", task.ChatJID, "err", err)
			} else {
				slog.Error("reschedule task", "task", task.ID, "err", err)
			}
		}
		if status == "success" {
			s.recordScheduleRun(task.ID, status, errorText, start, &state)
		}
	}
	s.scheduleMu.Lock()
	s.schedule = state
	s.scheduleMu.Unlock()
}

func (s *Server) recordScheduleRun(id, status, errorText string, start time.Time, state *scheduleState) {
	if err := s.db.RecordTaskRun(store.TaskRunLog{TaskID: id, Status: status, Error: errorText, DurationMS: time.Since(start).Milliseconds()}); err != nil {
		state.Err = err.Error()
		slog.Error("task run log", "task", id, "err", err)
	}
}
