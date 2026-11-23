package runed

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// Three guests up, then the oldest torn down and its tap given to the next turn:
// the teardown must leave nothing on the tap, and the next turn's rule set must
// read exactly as the first one did, accept above drop. With the rules deleted by
// spec the accept survived the teardown and the next guest's drop sat above it
// (BUGS L931). Real iptables and real taps, so it needs root in a network
// namespace of its own: `sudo unshare -n env RUNED_NETFILTER_TEST=1 ./runed.test`.
func TestTapRulesSurviveAThirdGuest(t *testing.T) {
	if os.Getenv("RUNED_NETFILTER_TEST") != "1" {
		t.Skip("needs root in a throwaway network namespace (RUNED_NETFILTER_TEST=1)")
	}
	q := &qemuRuntime{paths: qemuPaths{Firewall: "iptables", IP: "ip"}}
	var recs []*vmRecord
	for slot := range 3 {
		rec := &vmRecord{Tap: tapName("netfilter-test", slot), ProxyPort: 3128}
		var err error
		if rec.GuestIP, rec.HostIP, err = tapNet("10.97.0.0/24", slot); err != nil {
			t.Fatal(err)
		}
		if err := q.tapUp(rec); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { q.rulesDown(rec); q.tapDown(rec.Tap) })
		if err := q.rulesUp(rec); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, rec)
	}
	first := tapListing(t, recs[0].Tap)
	if len(first) == 0 {
		t.Fatal("rulesUp wrote nothing that names the tap")
	}

	q.rulesDown(recs[0])
	if left := tapListing(t, recs[0].Tap); len(left) > 0 {
		t.Fatalf("teardown with two other guests up left:\n%s", strings.Join(left, "\n"))
	}
	if err := q.rulesUp(recs[0]); err != nil {
		t.Fatal(err)
	}
	if again := tapListing(t, recs[0].Tap); !slices.Equal(again, first) {
		t.Errorf("the tap's next rule set differs from its first\ngot:\n%s\nwant:\n%s",
			strings.Join(again, "\n"), strings.Join(first, "\n"))
	}
}

// tapListing is every rule line that names the tap, in the order iptables reads
// each chain.
func tapListing(t *testing.T, tap string) []string {
	t.Helper()
	out, err := exec.Command("iptables", "-S").CombinedOutput()
	if err != nil {
		t.Fatalf("iptables -S: %v (%s)", err, out)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if slices.Contains(strings.Fields(line), tap) {
			lines = append(lines, line)
		}
	}
	return lines
}
