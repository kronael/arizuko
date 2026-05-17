package main

import (
	"testing"
)

// envMap builds a getenv closure for LoadAuthConfig tests.
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestClassify_TrustedAuthservDmarcPass_NoAllowlist(t *testing.T) {
	cfg := AuthConfig{TrustedAuthserv: "mx.google.com"}
	hdrs := []string{"mx.google.com; dmarc=pass header.from=example.com"}
	r := Classify(hdrs, "bob@example.com", cfg)
	if r.State != "trusted" || r.DMARC != "pass" {
		t.Fatalf("got %+v, want trusted/pass", r)
	}
	if VerbForState(r.State) != "message" {
		t.Errorf("verb = %s", VerbForState(r.State))
	}
}

func TestClassify_AllowlistMatchesFrom(t *testing.T) {
	cfg := AuthConfig{TrustedAuthserv: "mx.google.com", TrustedDomains: []string{"example.com"}}
	hdrs := []string{"mx.google.com; dmarc=pass header.from=example.com"}
	r := Classify(hdrs, "Bob <bob@example.com>", cfg)
	if r.State != "trusted" || !r.FromTrusted {
		t.Fatalf("got %+v", r)
	}
}

func TestClassify_AllowlistMiss(t *testing.T) {
	cfg := AuthConfig{TrustedAuthserv: "mx.google.com", TrustedDomains: []string{"mycompany.com"}}
	hdrs := []string{"mx.google.com; dmarc=pass"}
	r := Classify(hdrs, "bob@example.com", cfg)
	if r.State != "untrusted" {
		t.Fatalf("got %+v, want untrusted", r)
	}
	if VerbForState(r.State) != "untrusted" {
		t.Errorf("verb = %s", VerbForState(r.State))
	}
}

func TestClassify_DmarcFail(t *testing.T) {
	cfg := AuthConfig{TrustedAuthserv: "mx.google.com"}
	hdrs := []string{"mx.google.com; dmarc=fail"}
	r := Classify(hdrs, "bob@example.com", cfg)
	if r.State != "untrusted" || r.DMARC != "fail" {
		t.Fatalf("got %+v", r)
	}
}

func TestClassify_NoAuthResultsHeader(t *testing.T) {
	cfg := AuthConfig{TrustedAuthserv: "mx.google.com"}
	r := Classify(nil, "bob@example.com", cfg)
	if r.State != "untrusted" || r.DMARC != "missing" {
		t.Fatalf("got %+v", r)
	}
}

func TestClassify_ForgedAuthservHeader(t *testing.T) {
	cfg := AuthConfig{TrustedAuthserv: "mx.google.com"}
	hdrs := []string{"evil-authserv; dmarc=pass"}
	r := Classify(hdrs, "bob@example.com", cfg)
	if r.State != "untrusted" {
		t.Fatalf("forgery accepted: %+v", r)
	}
	if r.DMARC != "missing" {
		t.Errorf("DMARC should be 'missing' when attacker A-R is dropped, got %s", r.DMARC)
	}
}

func TestClassify_MultipleARHeaders_PinnedFailWins(t *testing.T) {
	cfg := AuthConfig{TrustedAuthserv: "mx.google.com"}
	// pinned one says fail, untrusted attacker one says pass — pinned must win.
	hdrs := []string{
		"evil-authserv; dmarc=pass",
		"mx.google.com; dmarc=fail",
	}
	r := Classify(hdrs, "bob@example.com", cfg)
	if r.State != "untrusted" || r.DMARC != "fail" {
		t.Fatalf("got %+v, want untrusted/fail", r)
	}
}

func TestClassify_LineFoldedARHeader(t *testing.T) {
	cfg := AuthConfig{TrustedAuthserv: "mx.google.com"}
	// authres.Parse accepts a single value string; callers must unfold per
	// RFC 5322 §2.2.3 before passing. Verify a value with embedded \r\n + WSP
	// (the unfolded form) still parses correctly.
	hdrs := []string{"mx.google.com;\r\n\tdmarc=pass header.from=example.com"}
	r := Classify(hdrs, "bob@example.com", cfg)
	if r.State != "trusted" || r.DMARC != "pass" {
		t.Fatalf("line-folded header not parsed: %+v", r)
	}
}

func TestClassify_TrustedAuthservUnset_FailClosed(t *testing.T) {
	cfg := AuthConfig{} // TrustedAuthserv empty
	hdrs := []string{"mx.google.com; dmarc=pass"}
	r := Classify(hdrs, "bob@example.com", cfg)
	if r.State != "untrusted" {
		t.Fatalf("fail-closed default violated: %+v", r)
	}
}

// strict-mode drop is the caller's responsibility (main.go), but assert
// the cfg flag flows correctly through LoadAuthConfig + that the
// classifier state is "untrusted" so the caller will drop.
func TestClassify_StrictAuthFlag_Untrusted(t *testing.T) {
	env := envMap(map[string]string{
		"EMAIL_TRUSTED_AUTHSERV": "mx.google.com",
		"EMAIL_STRICT_AUTH":      "true",
	})
	cfg := LoadAuthConfig(env)
	if !cfg.StrictAuth {
		t.Fatalf("StrictAuth not loaded")
	}
	r := Classify([]string{"mx.google.com; dmarc=fail"}, "bob@example.com", cfg)
	if r.State != "untrusted" {
		t.Fatalf("expected untrusted, got %+v", r)
	}
}

func TestClassify_DisplayNameSpoofing(t *testing.T) {
	cfg := AuthConfig{
		TrustedAuthserv: "mx.google.com",
		TrustedDomains:  []string{"trusted.com"},
	}
	// addr-spec is bob@untrusted.com, attacker stuffs trusted addr into name.
	from := `"attacker@trusted.com" <bob@untrusted.com>`
	r := Classify([]string{"mx.google.com; dmarc=pass"}, from, cfg)
	if r.FromTrusted {
		t.Fatalf("display name spoof passed: %+v", r)
	}
	if r.State != "untrusted" {
		t.Errorf("state = %s", r.State)
	}
}

func TestClassify_IDNDomainNormalize(t *testing.T) {
	env := envMap(map[string]string{
		"EMAIL_TRUSTED_AUTHSERV": "mx.google.com",
		// UTF-8 form in env; classifier should normalize From: Punycode to
		// match this allowlist entry after both are converted to ASCII.
		"EMAIL_TRUSTED_DOMAINS": "münchen.de",
	})
	cfg := LoadAuthConfig(env)
	// From: arrives with the Punycode form (typical for SMTP).
	from := "ops@xn--mnchen-3ya.de"
	r := Classify([]string{"mx.google.com; dmarc=pass"}, from, cfg)
	if !r.FromTrusted || r.State != "trusted" {
		t.Fatalf("IDN normalize failed: %+v (allowlist=%v)", r, cfg.TrustedDomains)
	}
}

func TestClassify_MultiMailboxFrom(t *testing.T) {
	cfg := AuthConfig{
		TrustedAuthserv: "mx.google.com",
		TrustedDomains:  []string{"trusted.com"},
	}
	hdrs := []string{"mx.google.com; dmarc=pass"}
	r := Classify(hdrs, "alice@trusted.com, bob@trusted.com", cfg)
	if r.FromTrusted {
		t.Fatalf("multi-mailbox From treated as trusted: %+v", r)
	}
}

// Reply-to-bot collision lives in api/api_test.go (see spec §collision).

func TestLoadAuthConfig_SubjectPrefixDefaultOff(t *testing.T) {
	cfg := LoadAuthConfig(envMap(map[string]string{}))
	if cfg.SubjectPrefix {
		t.Errorf("SubjectPrefix default should be false")
	}
	cfg = LoadAuthConfig(envMap(map[string]string{"EMAIL_UNVERIFIED_SUBJECT_PREFIX": "true"}))
	if !cfg.SubjectPrefix {
		t.Errorf("SubjectPrefix=true not parsed")
	}
}

// Extra case beyond the spec's 15: empty/whitespace TRUSTED_DOMAINS entries
// must not match a From with an empty domain (regression guard against
// "" ∈ allowlist after a stray trailing comma).
// Spec security knob: EMAIL_STRICT_AUTH must accept truthy strings, not
// just the literal "true". Operator typing "1" / "yes" / "TRUE" should
// land fail-closed, not silently fail-open.
func TestLoadAuthConfig_BoolEnvAcceptsTruthy(t *testing.T) {
	for _, v := range []string{"true", "True", "TRUE", "1", "yes", "on"} {
		cfg := LoadAuthConfig(envMap(map[string]string{
			"EMAIL_STRICT_AUTH": v,
		}))
		if !cfg.StrictAuth {
			t.Errorf("StrictAuth = false for env value %q; want true", v)
		}
	}
	for _, v := range []string{"", "false", "no", "off", "0", "anything-else"} {
		cfg := LoadAuthConfig(envMap(map[string]string{
			"EMAIL_STRICT_AUTH": v,
		}))
		if cfg.StrictAuth {
			t.Errorf("StrictAuth = true for env value %q; want false", v)
		}
	}
}

func TestLoadAuthConfig_DropsBlankAllowlistEntries(t *testing.T) {
	cfg := LoadAuthConfig(envMap(map[string]string{
		"EMAIL_TRUSTED_AUTHSERV": "mx.google.com",
		"EMAIL_TRUSTED_DOMAINS":  "good.com, ,, ",
	}))
	if len(cfg.TrustedDomains) != 1 || cfg.TrustedDomains[0] != "good.com" {
		t.Fatalf("blank entries leaked: %v", cfg.TrustedDomains)
	}
	// A pathological From with no domain must not match an empty list slot.
	r := Classify(
		[]string{"mx.google.com; dmarc=pass"},
		"bogus@",
		cfg,
	)
	if r.FromTrusted {
		t.Fatalf("empty allowlist entry matched empty domain: %+v", r)
	}
}
