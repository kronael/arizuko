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
// worstCaseWall is what the selected cases can cost end to end: each case's own
// wall cap plus the bounded wait for its turn to settle before the next one is
// injected. Cases run strictly sequentially into one folder, so this sums.
func worstCaseWall(caseWallMs []int, settle time.Duration) time.Duration {
	total := time.Duration(0)
	for _, ms := range caseWallMs {
		total += time.Duration(ms)*time.Millisecond + settle
	}
	return total
}

func checkToken(token string, now time.Time, budget time.Duration) error {
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
	// A token that dies mid-run fails exactly like one that was dead at the
	// start, and it costs more: the cases that already ran are paid for and the
	// rest 401 into their timeouts. The mint TTL defaults to 1h, which is less
	// than the smoke's own worst case.
	if end := now.Add(budget); exp.Before(end) {
		return fmt.Errorf("bearer expires at %s, %s before the selected cases can finish (worst case %s) — "+
			"mint a longer one: arizuko token <instance> issue bearer <folder> --ttl %s --scope messages:write,messages:read,cost:read",
			exp.Format(time.RFC3339), end.Sub(exp).Truncate(time.Second),
			budget.Truncate(time.Second), (budget + budget/2).Truncate(time.Minute))
	}
	return nil
}
