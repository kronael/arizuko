package core

import (
	"errors"
	"testing"
	"time"
)

func TestNextRunBasic(t *testing.T) {
	next, err := NextRun("0 9 * * *", "UTC")
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

func TestNextRunEveryMinute(t *testing.T) {
	next, err := NextRun("* * * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	diff := time.Until(next)
	if diff > 2*time.Minute || diff < 0 {
		t.Fatal("every-minute cron should be within 2m, got", diff)
	}
}

func TestNextRunInterval(t *testing.T) {
	next, err := NextRun("1500", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	diff := time.Until(next)
	if diff < 500*time.Millisecond || diff > 3*time.Second {
		t.Fatal("interval next run not ~1.5s in future:", diff)
	}
}

func TestNextRunEmptyIsNoSchedule(t *testing.T) {
	if _, err := NextRun("", "UTC"); !errors.Is(err, ErrNoSchedule) {
		t.Fatalf("empty cron: got %v, want ErrNoSchedule", err)
	}
}

func TestNextRunInvalidExpression(t *testing.T) {
	_, err := NextRun("not a cron", "UTC")
	if err == nil {
		t.Fatal("expected error for invalid cron expression")
	}
	if errors.Is(err, ErrNoSchedule) {
		t.Fatal("an unparseable expression is a fault, not an absent schedule")
	}
}

func TestNextRunRespectsTimezone(t *testing.T) {
	utc, err := NextRun("0 12 * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	tokyo, err := NextRun("0 12 * * *", "Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	if utc.Equal(tokyo) {
		t.Fatal("UTC and Tokyo results should differ")
	}
}

func TestNextRunInvalidTimezoneDefaultsUTC(t *testing.T) {
	next, err := NextRun("0 12 * * *", "Invalid/Zone")
	if err != nil {
		t.Fatal(err)
	}
	if next.Location() != time.UTC {
		t.Fatal("expected UTC fallback, got", next.Location())
	}
}

func TestTimezoneFallsBackToUTC(t *testing.T) {
	t.Setenv("TZ", "Nowhere/Nozone")
	if got := Timezone(); got != "UTC" {
		t.Fatalf("unloadable TZ: got %q, want UTC", got)
	}
	t.Setenv("TZ", "")
	if got := Timezone(); got != "UTC" {
		t.Fatalf("unset TZ: got %q, want UTC", got)
	}
	t.Setenv("TZ", "Asia/Tokyo")
	if got := Timezone(); got != "Asia/Tokyo" {
		t.Fatalf("set TZ: got %q, want Asia/Tokyo", got)
	}
}
