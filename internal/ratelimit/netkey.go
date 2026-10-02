package ratelimit

import "net/netip"

const ipv6Prefix = 64

// NetKey is the limiter key for a client address: the address itself for IPv4 (and
// IPv4-mapped IPv6), its /64 for IPv6. It is total: anything that does not parse
// comes back unchanged, so callers that must fail closed check the address first.
func NetKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.WithZone("").Unmap()
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.Prefix(ipv6Prefix)
	if err != nil {
		return addr.String()
	}
	return prefix.String()
}
