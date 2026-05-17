package main

import (
	"net/mail"
	"strings"

	"github.com/emersion/go-msgauth/authres"
	"golang.org/x/net/idna"
)

// AuthConfig drives the inbound sender classifier. All fields read from
// env at startup (see loadConfig). Behavior is fail-closed: a zero value
// classifies every message as untrusted.
type AuthConfig struct {
	// TrustedAuthserv pins the upstream MTA whose Authentication-Results
	// headers we trust (e.g. "mx.google.com"). Per RFC 8601 §5, A-R headers
	// from other authserv-ids are dropped. Empty = no trust possible.
	TrustedAuthserv string
	// TrustedDomains is the From-domain allowlist. Empty = allowlist
	// inactive (DMARC pass alone is enough). Compared lowercase, IDN-
	// normalized to ASCII Punycode.
	TrustedDomains []string
	// StrictAuth=true tells the caller to DROP untrusted mail entirely.
	// This struct just classifies; the drop happens in the caller.
	StrictAuth bool
	// SubjectPrefix=true asks the caller to prefix "[UNVERIFIED] " on
	// untrusted subjects. Default false — signal lives in Verb only.
	SubjectPrefix bool
	// VerifyDKIM is Tier 3 — independent DKIM verification. Pre-wired
	// for future implementation; classifier ignores it today.
	// TODO(spec 8/17 Tier 3): wire to dkim.Verify on the raw message.
	VerifyDKIM bool
}

// ClassifyResult is the structured outcome of authenticating one inbound.
type ClassifyResult struct {
	// State is "trusted" or "untrusted". Drives Verb selection.
	State string
	// DMARC is "pass", "fail", "none", or "missing" (no pinned A-R found).
	DMARC string
	// FromTrusted reports whether the From: addr-spec domain is in the
	// allowlist. Always false when the allowlist is empty AND a single
	// canonical From is required (multi-mailbox From → false).
	FromTrusted bool
	// Reason is a one-line operator-readable explanation, log-friendly.
	Reason string
}

// envBool accepts truthy strings: "true", "1", "yes", "on" (case-insensitive).
// Spec security knob `EMAIL_STRICT_AUTH` fail-closed must not silently fail
// open on `=1` typos; same for other bool envs.
func envBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

// LoadAuthConfig reads env vars relevant to the auth classifier.
func LoadAuthConfig(getenv func(string) string) AuthConfig {
	cfg := AuthConfig{
		TrustedAuthserv: strings.ToLower(strings.TrimSpace(getenv("EMAIL_TRUSTED_AUTHSERV"))),
		StrictAuth:      envBool(getenv("EMAIL_STRICT_AUTH")),
		SubjectPrefix:   envBool(getenv("EMAIL_UNVERIFIED_SUBJECT_PREFIX")),
		VerifyDKIM:      envBool(getenv("EMAIL_VERIFY_DKIM")),
	}
	if raw := getenv("EMAIL_TRUSTED_DOMAINS"); raw != "" {
		for _, d := range strings.Split(raw, ",") {
			d = strings.ToLower(strings.TrimSpace(d))
			if d == "" {
				continue
			}
			if ascii, err := idna.Lookup.ToASCII(d); err == nil {
				d = ascii
			}
			cfg.TrustedDomains = append(cfg.TrustedDomains, d)
		}
	}
	return cfg
}

// Classify is the single decision function for inbound sender trust.
// Pure: given the raw header slice (each entry is one A-R header VALUE,
// already unfolded) + the raw From: header value + a config, returns the
// trust classification. No I/O, no state — easy to unit-test.
func Classify(authResultsHeaders []string, fromHeader string, cfg AuthConfig) ClassifyResult {
	r := ClassifyResult{DMARC: "missing", State: "untrusted"}

	// Tier 1: A-R header parse, pinned to trusted authserv-id.
	if cfg.TrustedAuthserv != "" {
		for _, h := range authResultsHeaders {
			identifier, results, err := authres.Parse(h)
			if err != nil {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(identifier), cfg.TrustedAuthserv) {
				continue
			}
			for _, res := range results {
				if dm, ok := res.(*authres.DMARCResult); ok {
					r.DMARC = string(dm.Value)
					break
				}
			}
			// Once we find a matching authserv, stop — multiple matching
			// A-R headers from the same authserv are spec-unusual; first
			// one wins is safe enough and avoids ambiguous merges.
			if r.DMARC != "missing" {
				break
			}
		}
	}

	// Tier 2: From-domain allowlist.
	r.FromTrusted = fromInAllowlist(fromHeader, cfg.TrustedDomains)

	allowlistActive := len(cfg.TrustedDomains) > 0
	switch {
	case cfg.TrustedAuthserv == "":
		r.Reason = "EMAIL_TRUSTED_AUTHSERV unset (fail-closed default)"
	case r.DMARC == "missing":
		r.Reason = "no Authentication-Results from trusted authserv"
	case r.DMARC != "pass":
		r.Reason = "dmarc=" + r.DMARC
	case allowlistActive && !r.FromTrusted:
		r.Reason = "From not in EMAIL_TRUSTED_DOMAINS"
	default:
		r.State = "trusted"
		r.Reason = "dmarc=pass" + allowlistTag(allowlistActive)
	}
	return r
}

func allowlistTag(active bool) string {
	if active {
		return " + From allowlisted"
	}
	return ""
}

// fromInAllowlist returns true only when From contains exactly one mailbox
// whose addr-spec domain (lowercased, IDN-normalized) is in the allowlist.
// Multi-address From always returns false (spoofing surface). Empty
// allowlist returns false — caller must check len(list) separately to
// decide whether the allowlist is even active.
func fromInAllowlist(fromHeader string, allowlist []string) bool {
	if len(allowlist) == 0 || fromHeader == "" {
		return false
	}
	addrs, err := mail.ParseAddressList(fromHeader)
	if err != nil || len(addrs) != 1 {
		return false
	}
	at := strings.LastIndex(addrs[0].Address, "@")
	if at < 0 {
		return false
	}
	dom := strings.ToLower(addrs[0].Address[at+1:])
	if ascii, err := idna.Lookup.ToASCII(dom); err == nil {
		dom = ascii
	}
	for _, t := range allowlist {
		if dom == t {
			return true
		}
	}
	return false
}

// VerbForState maps a classify result to the InboundMsg.Verb string.
func VerbForState(state string) string {
	if state == "trusted" {
		return "message"
	}
	return "untrusted"
}
