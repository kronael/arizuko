package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
)

var errBlockedAddress = errors.New("blocked address")

// address.go is the address half of the enforcement point. match.Host answers
// "is this NAME allowed"; this answers "is the ADDRESS behind that name a place
// the proxy may go", and hands back the exact address the caller must dial.
//
// The two questions are separate because the answers can disagree. Checking a
// name and then dialling that name uses two independent resolutions. The second
// resolution can return an internal address. In arizuko, each turn's proxy
// listens on the host side of that turn's tap pair. The guest reaches only
// the proxy port. The proxy reaches allowed destinations through runed's network.
// Resolving once and dialling the checked address prevents DNS rebinding.
//
// Nothing here decrypts or inspects anything. This is address hygiene, not
// interception; MITM stays declined (egress/README.md § The address check).

// blockedIP reports whether ip is a class the proxy must never reach on a
// sandbox's behalf, whatever the allowlist says.
//
// Loopback is the proxy's own listener and anything else bound to 127.0.0.1.
// Link-local carries every cloud instance-metadata service — 169.254.169.254 on
// AWS/GCP/Azure, and `metadata.google.internal` is just a name for it, so the
// address check covers the names without a second list to keep current. The
// IPv6 IMDS endpoint has its own address outside link-local and is named below.
// Unspecified and multicast are not destinations a forward proxy has any use
// for.
func (p *Proxy) blockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() && !p.allowLoopback {
		return true
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	// AWS IMDSv2 over IPv6. Not link-local, so the checks above miss it.
	return ip.Equal(net.ParseIP("fd00:ec2::254"))
}

// privateIP reports whether ip is private or unspecified. Private destinations
// can be allowed internal services, so --deny-private controls their refusal.
func privateIP(ip net.IP) bool {
	return ip != nil && (ip.IsPrivate() || ip.IsUnspecified())
}

// resolveDest resolves host and returns the one address the caller must dial.
//
// Strict on purpose: a host is refused when ANY of its answers is blocked, not
// merely when the one we would have picked is. A name that resolves to both a
// public address and 169.254.169.254 is a name under someone else's control,
// and picking the good answer would make the outcome depend on resolver
// ordering.
func (p *Proxy) resolveDest(ctx context.Context, host string) (net.IP, error) {
	// A literal address never reaches a resolver. match.Host already refuses
	// these (it skips IP entries in the allowlist, so no literal can match),
	// but this function must be correct on its own — it is the last check
	// before a socket.
	if ip := net.ParseIP(host); ip != nil {
		if p.blockedIP(ip) || (p.denyPrivate && privateIP(ip)) {
			return nil, fmt.Errorf("destination %s: %w", host, errBlockedAddress)
		}
		return ip, nil
	}
	ips, err := p.lookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("resolve %s: no addresses", host)
	}
	for _, ip := range ips {
		if p.blockedIP(ip) || (p.denyPrivate && privateIP(ip)) {
			return nil, fmt.Errorf("%s resolves to %s: %w", host, ip, errBlockedAddress)
		}
	}
	return ips[0], nil
}

// dialAllowed is the single dial path for both the CONNECT tunnel and the
// plain-HTTP forward. addr arrives as "host:port"; the name is resolved and
// checked once here, and the socket goes to that exact address — so the
// address that passed IS the address connected to.
func (p *Proxy) dialAllowed(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("split %s: %w", addr, err)
	}
	ip, err := p.resolveDest(ctx, host)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: dialTimeout}
	return d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
}
