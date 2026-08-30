package main

import (
	"strings"
	"testing"
)

// The shared page says connected or disconnected; for email that is not enough
// to act on. BUGS L62 is the proof: marinade's emaid sat IMAP-auth-failing for
// three days and the only visible symptom was an unhealthy container. Which
// account against which host is what turns that into a fixable report
// (spec 6/32).
func TestDashSectionsNameTheMailbox(t *testing.T) {
	s := &server{cfg: config{
		Account:  "bot@example.com",
		IMAPHost: "imap.example.com:993",
		SMTPHost: "smtp.example.com:587",
	}}
	secs := s.DashSections()
	if len(secs) != 1 {
		t.Fatalf("sections = %d, want 1", len(secs))
	}
	got := map[string]string{}
	for _, row := range secs[0].Rows {
		got[row[0]] = row[1]
	}
	for k, want := range map[string]string{
		"account":   "bot@example.com",
		"IMAP host": "imap.example.com:993",
		"SMTP host": "smtp.example.com:587",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
}

// The password is the credential the failing login uses. An operator page that
// prints it trades a diagnosis for a leak.
func TestDashSectionsCarryNoPassword(t *testing.T) {
	s := &server{cfg: config{
		Account:  "bot@example.com",
		Password: "SECRET-PASSWORD",
		IMAPHost: "imap.example.com:993",
	}}
	var all strings.Builder
	for _, sec := range s.DashSections() {
		for _, row := range sec.Rows {
			all.WriteString(row[0] + row[1])
		}
	}
	if strings.Contains(all.String(), "SECRET-PASSWORD") {
		t.Error("the mailbox panel rendered the account password")
	}
}
