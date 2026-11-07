package main

import (
	"testing"
	"time"
)

func TestWorstCaseWallSumsCasesAndSettles(t *testing.T) {
	got := worstCaseWall([]int{60_000, 240_000}, 30*time.Second)
	want := 60*time.Second + 240*time.Second + 60*time.Second
	if got != want {
		t.Errorf("worstCaseWall = %s, want %s", got, want)
	}
}
