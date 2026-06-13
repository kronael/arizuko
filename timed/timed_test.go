package main

import (
	"testing"
	"time"
)

func TestNextCronBasic(t *testing.T) {
	next, err := nextCron("0 9 * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if !next.After(time.Now()) {
		t.Fatal("next should be in the future")
	}
	if next.Hour() != 9 || next.Minute() != 0 {
		t.Fatalf("expected 09:00, got %02d:%02d", next.Hour(), next.Minute())
	}
}

func TestNextCronEveryMinute(t *testing.T) {
	next, err := nextCron("* * * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	diff := time.Until(next)
	if diff > 2*time.Minute || diff < 0 {
		t.Fatal("every-minute cron should be within 2m, got", diff)
	}
}

func TestNextCronInvalidExpression(t *testing.T) {
	_, err := nextCron("not a cron", "UTC")
	if err == nil {
		t.Fatal("expected error for invalid cron expression")
	}
}

func TestNextCronRespectsTimezone(t *testing.T) {
	utc, err := nextCron("0 12 * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	tokyo, err := nextCron("0 12 * * *", "Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	if utc.Equal(tokyo) {
		t.Fatal("UTC and Tokyo results should differ")
	}
}

func TestNextCronInvalidTimezoneDefaultsUTC(t *testing.T) {
	next, err := nextCron("0 12 * * *", "Invalid/Zone")
	if err != nil {
		t.Fatal(err)
	}
	if next.Location() != time.UTC {
		t.Fatal("expected UTC fallback, got", next.Location())
	}
}

func TestComputeNextRunInterval(t *testing.T) {
	got := computeNextRun("1500", "UTC", "x")
	nr, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatal("bad rfc3339:", got)
	}
	diff := time.Until(nr)
	if diff < 500*time.Millisecond || diff > 3*time.Second {
		t.Fatal("interval next_run not ~1.5s in future:", diff)
	}
}

func TestComputeNextRunEmpty(t *testing.T) {
	if got := computeNextRun("", "UTC", "x"); got != "" {
		t.Fatal("empty cron should return empty, got", got)
	}
}

func TestComputeNextRunCron(t *testing.T) {
	got := computeNextRun("0 9 * * *", "UTC", "x")
	if got == "" {
		t.Fatal("expected next_run, got empty")
	}
	nr, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatal("bad rfc3339:", got)
	}
	if nr.Hour() != 9 || nr.Minute() != 0 {
		t.Fatalf("expected 09:00, got %02d:%02d", nr.Hour(), nr.Minute())
	}
}

func TestComputeNextRunInvalidCron(t *testing.T) {
	if got := computeNextRun("not a cron", "UTC", "x"); got != "" {
		t.Fatal("invalid cron should return empty, got", got)
	}
}
