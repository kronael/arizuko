package container

import (
	"net"
	"testing"
)

// Two independent draws over ~250 usable hosts collide about once in 250, and
// the collision is silent: docker gives both containers the same address and
// the folder's egress is broken until the next turn re-rolls (BUGS L240). The
// pair is derived now, so the count that used to find a collision finds none.
func TestPickPair_NeverSharesAnAddress(t *testing.T) {
	const subnet = "10.99.42.0/24"
	_, n, err := net.ParseCIDR(subnet)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2000 {
		spawnIP, proxyIP, err := pickPair(subnet)
		if err != nil {
			t.Fatalf("draw %d: %v", i, err)
		}
		if spawnIP == proxyIP {
			t.Fatalf("draw %d: proxy and spawn share %s", i, spawnIP)
		}
		for _, ip := range []string{spawnIP, proxyIP} {
			parsed := net.ParseIP(ip)
			if !n.Contains(parsed) {
				t.Fatalf("draw %d: %s is outside %s", i, ip, subnet)
			}
			last := parsed.To4()[3]
			if last < 4 || last == 255 {
				t.Fatalf("draw %d: %s lands on a reserved address", i, ip)
			}
		}
	}
}
