package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/store"
)

// Per folder, `usage` counts each turn once however many models it called,
// sums tokens and credits, and adds a metered turn's self-reported cost to
// nothing: the gateway holds that number.
func TestRunUsageSumsPerFolderAndLeavesMeteredCostToTheGateway(t *testing.T) {
	s := budgetDB(t)
	record := func(folder, turn string, metered bool, models string) {
		t.Helper()
		usage := store.TurnUsage{Harness: "codex", Metered: metered, Models: models}
		if _, err := s.RecordTurnResult(folder, turn, "", "success", "", usage); err != nil {
			t.Fatal(err)
		}
	}
	record("a", "t1", false, `{"m1":{"input":10,"output":1,"cache_read":100,"cache_write":0,"cost_cents":3,"credits_spent":1.25},`+
		`"m2":{"input":5,"output":1,"cache_read":0,"cache_write":2,"cost_cents":1}}`)
	record("a", "t2", true, `{"m1":{"input":1,"output":1,"cache_read":0,"cache_write":0,"cost_cents":40}}`)
	record("b", "t3", false, `{"m1":{"input":7,"output":2,"cache_read":0,"cache_write":0,"cost_cents":2,"credits_spent":0.5}}`)

	var buf bytes.Buffer
	if err := runUsage(s, time.Time{}, "folder", &buf); err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(strings.Split(buf.String(), "\n")[1])
	want := []string{"a", "2", "1", "16", "100", "2", "3", "4", "1.25"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("folder a = %v, want %v\n%s", got, want, buf.String())
	}

	buf.Reset()
	if err := runUsage(s, time.Now().Add(24*time.Hour), "day", &buf); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(buf.String()), "\n"); len(lines) != 1 {
		t.Errorf("usage since tomorrow = %q, want the header alone", lines)
	}
}
