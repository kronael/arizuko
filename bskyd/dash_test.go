package main

import (
	"strings"
	"testing"
)

// The shared page shows whether the adapter is connected; it cannot show WHICH
// account it is connected as, and on Bluesky that is the operator's real
// question — every post is attributed to the repo DID, and posting from the
// wrong account is silent (spec 6/32).
func TestDashSectionsNameTheAccount(t *testing.T) {
	bc := &bskyClient{cfg: config{Service: "https://bsky.social"}}
	bc.session = session{DID: "did:plc:abc", AccessJwt: "a", RefreshJwt: "r"}

	secs := bc.DashSections()
	if len(secs) != 1 {
		t.Fatalf("sections = %d, want 1", len(secs))
	}
	got := map[string]string{}
	for _, row := range secs[0].Rows {
		got[row[0]] = row[1]
	}
	if got["repo DID"] != "did:plc:abc" {
		t.Errorf("repo DID = %q", got["repo DID"])
	}
	if got["authenticated"] != "yes" {
		t.Errorf("authenticated = %q, want yes", got["authenticated"])
	}
}

// A session panel must never print the JWTs: they are bearer credentials, and
// rendering them turns an operator page into a credential leak.
func TestDashSectionsCarryNoCredentials(t *testing.T) {
	bc := &bskyClient{cfg: config{Service: "https://bsky.social"}}
	bc.session = session{DID: "did:plc:abc", AccessJwt: "SECRET-ACCESS", RefreshJwt: "SECRET-REFRESH"}

	var all strings.Builder
	for _, sec := range bc.DashSections() {
		for _, row := range sec.Rows {
			all.WriteString(row[0] + row[1])
		}
	}
	for _, secret := range []string{"SECRET-ACCESS", "SECRET-REFRESH"} {
		if strings.Contains(all.String(), secret) {
			t.Errorf("the session panel rendered %s", secret)
		}
	}
}

// No session is an answer, not a blank: an adapter that never authenticated
// looks identical to one whose panel failed to load.
func TestNoSessionSaysSo(t *testing.T) {
	bc := &bskyClient{}
	got := map[string]string{}
	for _, row := range bc.DashSections()[0].Rows {
		got[row[0]] = row[1]
	}
	if got["repo DID"] != "(no session)" {
		t.Errorf("repo DID = %q, want the no-session line", got["repo DID"])
	}
	if got["authenticated"] != "no" {
		t.Errorf("authenticated = %q, want no", got["authenticated"])
	}
}
