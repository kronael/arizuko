package core

import (
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/robfig/cron/v3"
)

// cronParser reads the 5-field expression Task.Cron holds: minute, hour,
// day-of-month, month, day-of-week. No seconds field and no @descriptor, which
// is the dialect every operator surface accepts and every stored row carries.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// ErrNoSchedule reports a Cron value that names no instant: the empty value a
// one-shot carries once it has run.
var ErrNoSchedule = errors.New("cron value names no run")

// Timezone answers the zone a schedule is read in: the TZ environment variable,
// or UTC when TZ is unset or names no zone this build can load.
func Timezone() string {
	tz := os.Getenv("TZ")
	if _, err := time.LoadLocation(tz); tz == "" || err != nil {
		return "UTC"
	}
	return tz
}

// NextRun answers the first instant after now that cronVal names, over Task.Cron's
// whole value space: a positive integer is an interval in milliseconds, an empty
// value names nothing further (ErrNoSchedule), and anything else is a cron
// expression read in tz. A tz that will not load is read as UTC.
//
// It is the ONE place that turns a cron value into an instant. The seeder that
// writes a new task's first next_run, the fire loop that re-arms a task it has
// just run, and the two operator surfaces that accept a cron from a human all
// call it, so a task's first run obeys the same expression as its every later
// run. A seeder that instead wrote `now` made every new group fire all five of
// its compaction tasks on timed's next sweep (BUGS L312).
//
// The parse error is returned as the parser gave it: it already names the
// offending expression, and each caller adds the context it alone has.
func NextRun(cronVal, tz string) (time.Time, error) {
	if ms, err := strconv.ParseInt(cronVal, 10, 64); err == nil && ms > 0 {
		return time.Now().Add(time.Duration(ms) * time.Millisecond), nil
	}
	if cronVal == "" {
		return time.Time{}, ErrNoSchedule
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	sched, err := cronParser.Parse(cronVal)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(time.Now().In(loc)), nil
}
