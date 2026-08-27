package auth

import (
	"encoding/json"
	"strings"
	"testing"
)

// safeReturn is the open-redirect guard on every ?return= the auth flow
// carries across a provider round trip. A miss here sends a user who just
// authenticated to an attacker's page with their session live. It had no test.
func TestSafeReturnRejectsEveryOffSiteForm(t *testing.T) {
	bad := []struct{ name, in string }{
		{"empty", ""},
		{"a bare path with no leading slash", "dash/"},
		{"an absolute http url", "http://evil.example/"},
		{"an absolute https url", "https://evil.example/"},
		{"protocol-relative — the classic bypass", "//evil.example/"},
		{"protocol-relative with a path", "//evil.example/dash/"},
		{"backslash after the slash", "/\\evil.example/"},
		{"a backslash anywhere", "/dash\\evil"},
		{"a carriage return (header splitting)", "/dash\rSet-Cookie: a=b"},
		{"a newline (header splitting)", "/dash\nSet-Cookie: a=b"},
		{"a NUL byte", "/dash\x00"},
		{"a scheme-only url", "javascript:alert(1)"},
		{"a mailto", "mailto:a@b.c"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			got, ok := safeReturn(c.in)
			if ok {
				t.Errorf("safeReturn(%q) accepted it as %q — a logged-in user lands off-site", c.in, got)
			}
			if got != "" {
				t.Errorf("a rejected value must return the empty string, got %q", got)
			}
		})
	}
}

func TestSafeReturnAcceptsAnOwnSitePath(t *testing.T) {
	good := []string{
		"/",
		"/dash/",
		"/dash/approvals/",
		"/dash/approvals/?id=42",
		"/dash/approvals/#pending",
		"/invite/abc123",
	}
	for _, in := range good {
		got, ok := safeReturn(in)
		if !ok {
			t.Errorf("safeReturn(%q) rejected an own-site path; the user is bounced to the root after login", in)
			continue
		}
		if got != in {
			t.Errorf("safeReturn(%q) = %q, want it returned unchanged", in, got)
		}
	}
}

// jsSafe escapes the three sequences that break out of a <script> block
// holding JSON. Without it any string that reaches the page — a display name,
// a folder, a provider error — is a stored XSS.
func TestJSSafeClosesTheScriptEscapeHatches(t *testing.T) {
	cases := []struct{ name, in, wantAbsent string }{
		{"a closing script tag", `{"n":"</script><script>alert(1)</script>"}`, "</script"},
		{"any closing tag", `{"n":"</b>"}`, "</"},
		{"U+2028 line separator", "{\"n\":\"a b\"}", " "},
		{"U+2029 paragraph separator", "{\"n\":\"a b\"}", " "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := jsSafe([]byte(c.in))
			if strings.Contains(got, c.wantAbsent) {
				t.Errorf("jsSafe left %q in the output: %s", c.wantAbsent, got)
			}
		})
	}
}

// The escaping has to survive the round trip: the browser's JSON parser must
// read back exactly what was serialized, or escaping would silently corrupt
// every name carrying a "</".
func TestJSSafeStaysValidJSON(t *testing.T) {
	original := map[string]string{"name": "a</script>b c d"}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]string
	if err := json.Unmarshal([]byte(jsSafe(raw)), &back); err != nil {
		t.Fatalf("jsSafe output is no longer parseable JSON: %v", err)
	}
	if back["name"] != original["name"] {
		t.Errorf("round trip changed the value:\n got %q\nwant %q", back["name"], original["name"])
	}
}

// The exported wrappers are what other packages call; they must not drift from
// the unexported implementations.
func TestExportedWrappersMatchTheirImplementations(t *testing.T) {
	for _, in := range []string{"/dash/", "//evil.example/", "http://evil.example/", ""} {
		gotV, gotOK := SafeReturn(in)
		wantV, wantOK := safeReturn(in)
		if gotV != wantV || gotOK != wantOK {
			t.Errorf("SafeReturn(%q) = (%q,%v), safeReturn = (%q,%v)", in, gotV, gotOK, wantV, wantOK)
		}
	}
	in := []byte(`{"n":"</script>"}`)
	if JSSafe(in) != jsSafe(in) {
		t.Error("JSSafe and jsSafe disagree")
	}
}
