package main

import (
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/obs"
	_ "github.com/kronael/arizuko/resreg/resources"
)

func main() {
	defer obs.Setup("timed", os.Getenv("ARIZUKO_INSTANCE"))()

	tz := core.Timezone()

	routerURL := os.Getenv("ROUTER_URL")
	if routerURL == "" {
		slog.Error("ROUTER_URL env required")
		os.Exit(1)
	}
	runSplit(routerURL, tz)
}

// computeNextRun renders the fire loop's next next_run: core.NextRun's instant as
// the RFC3339 text routd stores, or "" for a value that names no further run —
// a one-shot that has fired, or an expression that will not parse, which the
// caller turns into the task's 'completed' or 'error' status.
func computeNextRun(cronVal, tz, taskID string) string {
	next, err := core.NextRun(cronVal, tz)
	if errors.Is(err, core.ErrNoSchedule) {
		return ""
	}
	if err != nil {
		slog.Warn("parse cron expr", "task", taskID, "cron", cronVal, "err", err)
		return ""
	}
	return next.Format(time.RFC3339)
}
