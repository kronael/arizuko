package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// tokenExpiry reads the `exp` claim of a JWT WITHOUT verifying the signature —
// anteval is not the verifier, it only wants to refuse a run it knows is dead.
// ok is false when the token is not a readable JWT; the caller then proceeds,
// because a shape anteval cannot read is not proof of a bad token.
func tokenExpiry(token string) (exp time.Time, ok bool) {
	parts := splitDots(token)
	if len(parts) != 3 {
		return time.Time{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0).UTC(), true
}

func splitDots(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// checkToken fails the run when the bearer is already expired. An expired token
// does not surface as one loud error: routd 401s every injection, so each case
// burns its whole timeout and reports a plain "timeout, 0 tokens" FAIL. A run
// like that reads as a product regression and costs a real investigation
// (2026-08-22: four smoke cases were diagnosed as an ipc regression before the
// token turned out to have expired mid-run).
func checkToken(token string, now time.Time) error {
	// An empty bearer fails exactly like an expired one — routd 401s every
	// injection and each case burns its whole timeout — but it is easier to
	// cause: a mint command that fails inside a shell pipeline leaves the
	// variable empty and the pipeline still exits 0 (2026-08-22).
	if token == "" {
		return fmt.Errorf("--token is empty (also read from AGENTEVAL_TOKEN) — mint one: " +
			"arizuko token <instance> issue bearer <folder> --scope messages:write,messages:read,cost:read")
	}
	exp, ok := tokenExpiry(token)
	if !ok {
		return nil
	}
	if !now.Before(exp) {
		return fmt.Errorf("bearer expired at %s (%s ago) — mint a new one: "+
			"arizuko token <instance> issue bearer <folder> --scope messages:write,messages:read,cost:read",
			exp.Format(time.RFC3339), now.Sub(exp).Truncate(time.Second))
	}
	return nil
}
