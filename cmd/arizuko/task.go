package main

import (
	"context"
	"fmt"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

func cmdTask(args []string) {
	args = need(args, 3, 3, "arizuko task run [-i instance] <task-id>")
	if args[1] != "run" {
		usage(nil, "arizuko task run [-i instance] <task-id>")
	}
	mustInstanceDir(args[0])
	s := mustOpen(store.OwnerRoutd)
	defer func() { _ = s.Close() }()
	if err := scheduleTaskNow(s, args[2]); err != nil {
		die("Failed: %v", err)
	}
	fmt.Printf("task %q scheduled for the next tick\n", args[2])
}

func scheduleTaskNow(s *store.Store, id string) error {
	task, ok, err := s.TaskByID(id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("task %q not found", id)
	}
	if task.Status != core.TaskActive {
		return fmt.Errorf("task %q is %s; only active tasks can run", id, task.Status)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var ws store.WriteSet
	if err := s.MoveTaskInto(&ws, task, task.Status, &now,
		fmt.Errorf("task %q changed; retry", id)); err != nil {
		return err
	}
	return s.Apply(context.Background(), &ws)
}
