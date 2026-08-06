package audit

import (
	"encoding/json"
	"strings"
	"testing"
)

// The pinned answers to specs/5/I open question 1: which keys redact, and how
// an oversized field is encoded.

// TestRedactRE_PinnedKeySet walks the whole vocabulary in both directions. The
// must-NOT half is the one that earns its lines: an over-eager pattern is not a
// harmless excess of caution, it blinds the forensic queries the table exists
// for, and `serving_keys` is a live example — authd's daemon.start writes it
// and PLAN.md's proposed unanchored `key` would have redacted the count.
func TestRedactRE_PinnedKeySet(t *testing.T) {
	redacted := []string{
		"password", "passphrase", "pass",
		"token", "access_token", "refresh_token",
		"secret", "client_secret",
		"credential", "credentials",
		"authorization", "cookie",
		"dsn", "database_dsn",
		"api_key", "apikey", "key",
		"private_key", "signing_key", "enc_key", "service_key",
	}
	for _, k := range redacted {
		out := redactParams(map[string]any{k: "sensitive-value-here"})
		s, _ := out[k].(string)
		if !strings.HasPrefix(s, "<redacted:") {
			t.Errorf("key %q NOT redacted (got %v) — it names a credential", k, out[k])
		}
	}

	kept := []string{
		"serving_keys", "service_subs", "key_count", "keyboard", "monkey",
		"folder", "session_id", "turn_id", "actor", "resource", "scope",
		"instance", "run_id", "duration_ms",
	}
	for _, k := range kept {
		out := redactParams(map[string]any{k: "plain-value"})
		if out[k] != "plain-value" {
			t.Errorf("key %q was redacted to %v — over-redaction blinds the "+
				"queries audit_log exists to answer", k, out[k])
		}
	}
}

// TestRedactValueLengthIsStated: the marker carries the original length, so a
// reader can tell a redacted empty string from a redacted 64-char key.
func TestRedactValueLengthIsStated(t *testing.T) {
	out := redactParams(map[string]any{"token": "0123456789"})
	if out["token"] != "<redacted:10chars>" {
		t.Errorf("token = %v, want <redacted:10chars>", out["token"])
	}
}

// TestMarshalParams_OversizeValueKeepsSiblings is the pinned answer to the
// cap-encoding half of open question 1.
//
// Before per-value truncation, one fat argument pushed the map over the cap and
// the whole thing collapsed to {"_truncated":true} — the caller's folder, the
// resource it touched, every sibling field, gone with it. An audit row that
// cannot say what it was about is not a smaller audit row, it is none.
func TestMarshalParams_OversizeValueKeepsSiblings(t *testing.T) {
	out := marshalParams(map[string]any{
		"blob":   strings.Repeat("a", 5000),
		"folder": "atlas/support",
		"target": "routes/17",
	})
	if len(out) > maxParamsBytes {
		t.Fatalf("len=%d exceeds the %d cap: %s", len(out), maxParamsBytes, out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not valid JSON (%v): %s", err, out)
	}
	if got["folder"] != "atlas/support" || got["target"] != "routes/17" {
		t.Errorf("siblings lost to one oversized field: %s", out)
	}
	blob, _ := got["blob"].(string)
	if !strings.HasSuffix(blob, "<truncated:5000chars>") {
		t.Errorf("truncated value should state the original length: %q", blob)
	}
	if len([]rune(strings.TrimSuffix(blob, "…<truncated:5000chars>"))) != maxValueChars {
		t.Errorf("kept %d runes, want the %d budget", len([]rune(blob)), maxValueChars)
	}
}

// TestMarshalParams_TruncationIsRuneWise: cutting UTF-8 mid-codepoint yields a
// replacement char, which silently corrupts the one field a reader most wants
// verbatim. The budget is runes, not bytes.
func TestMarshalParams_TruncationIsRuneWise(t *testing.T) {
	out := marshalParams(map[string]any{"note": strings.Repeat("é", 400)})
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not valid JSON (%v): %s", err, out)
	}
	note, _ := got["note"].(string)
	if strings.Contains(note, "�") {
		t.Errorf("byte-wise cut produced U+FFFD: %q", note)
	}
	if !strings.HasPrefix(note, strings.Repeat("é", maxValueChars)) {
		t.Errorf("kept prefix is not the first %d runes intact: %q", maxValueChars, note)
	}
}

// TestMarshalParams_RedactionBeatsTruncation: a secret longer than the value
// budget must be redacted, never truncated. Truncation would publish its first
// 200 characters, which for an API key is the whole exploitable prefix.
func TestMarshalParams_RedactionBeatsTruncation(t *testing.T) {
	secret := "sk-" + strings.Repeat("9", 500)
	out := marshalParams(map[string]any{"api_key": secret})
	if strings.Contains(out, "sk-999") {
		t.Fatalf("a long secret leaked its prefix through truncation: %s", out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not valid JSON (%v): %s", err, out)
	}
	if got["api_key"] != "<redacted:503chars>" {
		t.Errorf("want the redaction marker, got: %v", got["api_key"])
	}
}

// TestMarshalParams_ManyFieldsStillCapped: the whole-map cap remains the
// backstop for a map with hundreds of small keys, which per-value truncation
// cannot help with.
func TestMarshalParams_ManyFieldsStillCapped(t *testing.T) {
	in := map[string]any{}
	for i := range 200 {
		in["field_number_"+strings.Repeat("x", i%9)+string(rune('a'+i%26))] = "value"
	}
	out := marshalParams(in)
	if len(out) > maxParamsBytes {
		t.Errorf("len=%d exceeds the %d cap: %s", len(out), maxParamsBytes, out)
	}
	if !strings.Contains(out, "_truncated") {
		t.Errorf("an over-cap map must say so: %s", out)
	}
}
