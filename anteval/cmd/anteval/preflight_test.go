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
	err := checkToken(jwtWithExp(now.Unix()-60), now)
	if err == nil {
		t.Fatal("an expired bearer must fail the run, not let every case time out")
	}
	if !strings.Contains(err.Error(), "arizuko token") {
		t.Errorf("the error must tell the operator how to mint one: %v", err)
	}
}

func TestCheckTokenAcceptsLive(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	if err := checkToken(jwtWithExp(now.Unix()+3600), now); err != nil {
		t.Fatalf("a live bearer must pass: %v", err)
	}
}

// A token anteval cannot read is not proof of a bad token — it must not block.
func TestCheckTokenIgnoresUnreadable(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, tok := range []string{"", "opaque-token", "a.b", "a.!!!.c"} {
		if err := checkToken(tok, now); err != nil {
			t.Errorf("checkToken(%q) = %v, want nil", tok, err)
		}
	}
}
