package main

import (
	"testing"
	"time"
)

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
