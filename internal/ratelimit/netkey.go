package ratelimit

import "net/netip"

const (
	ipv6Prefix     = 64
	ipv6WidePrefix = 48
)

// NetKey is the limiter key for a client address: the address itself for IPv4 (and
// IPv4-mapped IPv6), its /64 for IPv6. It is total: anything that does not parse
// comes back unchanged, so callers that must fail closed check the address first.
func NetKey(ip string) string {
	return keyAt(ip, ipv6Prefix)
}

// WideKey is NetKey's coarser twin for budgets a /64 alone cannot hold: the /48 for
// IPv6, which is what a free tunnel hands out (65,536 /64s, each a fresh NetKey).
// IPv4 stays the address - behind CGNAT one address is already a crowd, and a wider
// IPv4 bucket would refuse a whole carrier for one abuser. Total, like NetKey.
func WideKey(ip string) string {
	return keyAt(ip, ipv6WidePrefix)
}

func keyAt(ip string, v6bits int) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.WithZone("").Unmap()
	if addr.Is4() {
		return addr.String()
	}
	return netip.PrefixFrom(addr, v6bits).Masked().String()
}
