package routd

import (
	"context"
	"log/slog"
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
		next := task.NextRun.UTC().Format(time.RFC3339)
		id := "sched-" + task.ID + "@" + next
		sender := core.ScheduledSender
		if task.ContextMode == "isolated" {
			sender = core.IsolatedSenderPrefix + task.ID
		}
		_, err := s.ingestMessage(apiv1.Message{ID: id, ChatJID: task.Owner, ForwardedFrom: task.ChatJID, Sender: sender, Content: task.Prompt, Timestamp: start.Unix()}, "service:routd", "")
		status, errorText, retry := "success", "", ""
		if err != nil {
			status = "error"
			errorText = err.Error()
			state.Err = errorText
			slog.Error("enqueue task message", "task", task.ID, "jid", task.ChatJID, "err", errorText)
			if task.Cron == "" {
				retry = next
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
