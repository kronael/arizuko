package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
	"time"
)

func TestParseJID(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantErr   bool
		workspace string
		kind      string
		id        string
	}{
		{"channel", "slack:T012/channel/C0HJK", false, "T012", "channel", "C0HJK"},
		{"dm", "slack:T012/dm/D0XY", false, "T012", "dm", "D0XY"},
		{"group_mpim", "slack:T012/group/G123", false, "T012", "group", "G123"},
		{"missing_prefix", "discord:T/channel/C", true, "", "", ""},
		{"missing_kind_seg", "slack:T012", true, "", "", ""},
		{"missing_id", "slack:T012/channel", true, "", "", ""},
		{"empty_id", "slack:T012/channel/", true, "", "", ""},
		{"bad_kind", "slack:T012/private/C0", true, "", "", ""},
		{"empty_workspace", "slack:/channel/C0", true, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseJID(c.in)
			if c.wantErr {
				if err == nil {
					t.Errorf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got.workspace != c.workspace || got.kind != c.kind || got.id != c.id {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestFormatJID(t *testing.T) {
	got := formatJID("T012", "channel", "C0HJK")
	if got != "slack:T012/channel/C0HJK" {
		t.Errorf("got %q", got)
	}
}

func TestChanKind(t *testing.T) {
	if k := chanKind(true, false); k != "dm" {
		t.Errorf("im → %q", k)
	}
	if k := chanKind(false, true); k != "group" {
		t.Errorf("mpim → %q", k)
	}
	if k := chanKind(false, false); k != "channel" {
		t.Errorf("regular → %q", k)
	}
}

func TestParseSlackTS(t *testing.T) {
	if got := parseSlackTS("1700000000.000200"); got != 1700000000 {
		t.Errorf("got %d", got)
	}
	if got := parseSlackTS("1700000000"); got != 1700000000 {
		t.Errorf("got %d", got)
	}
	if parseSlackTS("") == 0 {
		t.Error("empty TS should fall back to now, not 0")
	}
}

// Signature must accept a body signed within the window.
func TestVerifySignature_Good(t *testing.T) {
	secret := "shh"
	body := []byte(`{"type":"url_verification","challenge":"x"}`)
	ts := int64(1_700_000_000)
	tsHdr := strconv.FormatInt(ts, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + tsHdr + ":" + string(body)))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if err := verifySignature(secret, sig, tsHdr, body, time.Unix(ts+10, 0)); err != nil {
		t.Errorf("want nil, got %v", err)
	}
}

// Empty secret → strict refusal (no fallback to "any signature").
func TestVerifySignature_NoSecret(t *testing.T) {
	if err := verifySignature("", "v0=x", "1", []byte(`{}`), time.Now()); err == nil {
		t.Error("missing secret must error")
	}
}

// Bad timestamp string.
func TestVerifySignature_BadTS(t *testing.T) {
	if err := verifySignature("shh", "v0=x", "not-a-number", []byte(`{}`), time.Now()); err == nil {
		t.Error("non-numeric ts must error")
	}
}

func TestTTLCache(t *testing.T) {
	c := newTTLCache(50 * time.Millisecond)
	c.put("k", "v")
	got, ok := c.get("k")
	if !ok || got != "v" {
		t.Fatalf("get miss")
	}
	time.Sleep(70 * time.Millisecond)
	if _, ok := c.get("k"); ok {
		t.Error("expected TTL eviction")
	}
}
