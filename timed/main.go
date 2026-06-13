package main

import (
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/kronael/arizuko/obs"
	_ "github.com/kronael/arizuko/resreg/resources"
	"github.com/robfig/cron/v3"
	_ "modernc.org/sqlite"
)

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func main() {
	defer obs.Setup("timed", os.Getenv("ARIZUKO_INSTANCE"))()

	tz := os.Getenv("TZ")
	if _, err := time.LoadLocation(tz); tz == "" || err != nil {
		tz = "UTC"
	}

	routerURL := os.Getenv("ROUTER_URL")
	if routerURL == "" {
		slog.Error("ROUTER_URL env required")
		os.Exit(1)
	}
	runSplit(routerURL, tz)
}

func computeNextRun(cronVal, tz, taskID string) string {
	if ms, err := strconv.ParseInt(cronVal, 10, 64); err == nil && ms > 0 {
		return time.Now().Add(time.Duration(ms) * time.Millisecond).Format(time.RFC3339)
	}
	if cronVal == "" {
		return ""
	}
	next, err := nextCron(cronVal, tz)
	if err != nil {
		slog.Warn("parse cron expr", "task", taskID, "cron", cronVal, "err", err)
		return ""
	}
	return next.Format(time.RFC3339)
}

func nextCron(expr, tz string) (time.Time, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	s, err := cronParser.Parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	return s.Next(time.Now().In(loc)), nil
}
