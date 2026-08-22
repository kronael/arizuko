package main

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"
)

func jwtWithExp(exp int64) string {
	body := base64.RawURLEncoding.EncodeToString([]byte(`{"exp":` + strconv.FormatInt(exp, 10) + `}`))
	return "hdr." + body + ".sig"
}

func TestCheckTokenRejectsExpired(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	err := checkToken(jwtWithExp(now.Unix()-60), now, time.Minute)
	if err == nil {
		t.Fatal("an expired bearer must fail the run, not let every case time out")
	}
	if !strings.Contains(err.Error(), "arizuko token") {
		t.Errorf("the error must tell the operator how to mint one: %v", err)
	}
}

func TestCheckTokenAcceptsLive(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	if err := checkToken(jwtWithExp(now.Unix()+3600), now, 10*time.Minute); err != nil {
		t.Fatalf("a live bearer must pass: %v", err)
	}
}

// A token anteval cannot read is not proof of a bad token — it must not block.
func TestCheckTokenIgnoresUnreadable(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, tok := range []string{"opaque-token", "a.b", "a.!!!.c"} {
		if err := checkToken(tok, now, time.Minute); err != nil {
			t.Errorf("checkToken(%q) = %v, want nil", tok, err)
		}
	}
}

func TestCheckTokenRejectsEmpty(t *testing.T) {
	if err := checkToken("", time.Unix(1_800_000_000, 0), time.Minute); err == nil {
		t.Fatal("an empty bearer must refuse the run; every case would 401 and burn its timeout")
	}
}

// The bearer must outlive the RUN, not just the moment it starts. A token that
// dies mid-suite fails exactly like a dead one and costs the cases already paid
// for (the mint TTL defaults to 1h; the smoke's worst case is longer).
func TestCheckTokenRejectsExpiryInsideTheRun(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	err := checkToken(jwtWithExp(now.Unix()+1800), now, time.Hour)
	if err == nil {
		t.Fatal("a bearer that expires mid-run must refuse before the run starts")
	}
	if !strings.Contains(err.Error(), "--ttl") {
		t.Errorf("the error must name the flag that fixes it: %v", err)
	}
}

func TestWorstCaseWallSumsCasesAndSettles(t *testing.T) {
	got := worstCaseWall([]int{60_000, 240_000}, 30*time.Second)
	want := 60*time.Second + 240*time.Second + 60*time.Second
	if got != want {
		t.Errorf("worstCaseWall = %s, want %s", got, want)
	}
}
