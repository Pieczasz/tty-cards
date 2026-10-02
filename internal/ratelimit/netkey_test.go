package ratelimit_test

import (
	"testing"

	"github.com/Pieczasz/tty-cards/internal/ratelimit"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNetKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ip   string
		want string
	}{
		{name: "ipv4 is its own network", ip: "198.51.100.7", want: "198.51.100.7"},
		{name: "ipv4-mapped ipv6 unmaps to the plain address", ip: "::ffff:198.51.100.7", want: "198.51.100.7"},
		{name: "ipv6 collapses to its /64", ip: "2001:db8:1:2:3:4:5:6", want: "2001:db8:1:2::/64"},
		{name: "zone is not part of the identity", ip: "fe80::1%eth0", want: "fe80::/64"},
		{name: "loopback", ip: "::1", want: "::/64"},
		{name: "unparseable input is limited as-is", ip: "not-an-ip", want: "not-an-ip"},
		{name: "empty input", ip: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ratelimit.NetKey(tt.ip))
		})
	}
}

func TestNetKey_AddressesInOneAllocationShareAKey(t *testing.T) {
	t.Parallel()

	const want = "2001:db8:abcd:1234::/64"
	rotated := []string{
		"2001:db8:abcd:1234::1",
		"2001:db8:abcd:1234::dead:beef",
		"2001:db8:abcd:1234:ffff:ffff:ffff:ffff",
	}

	for _, ip := range rotated {
		assert.Equal(t, want, ratelimit.NetKey(ip), "%s must share the allocation's budget", ip)
	}
}

func TestNetKey_AdjacentAllocationsDoNotShareAKey(t *testing.T) {
	t.Parallel()

	assert.NotEqual(t,
		ratelimit.NetKey("2001:db8:abcd:1234::1"),
		ratelimit.NetKey("2001:db8:abcd:1235::1"),
	)
}

func FuzzNetKey(f *testing.F) {
	for _, seed := range []string{"", "198.51.100.7", "::ffff:0.0.0.0", "2001:db8::1", "fe80::1%eth0", "0", "::/0", "999.999.999.999"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, ip string) {
		key := ratelimit.NetKey(ip)

		if ip != "" {
			require.NotEmpty(t, key, "a non-empty address must map to a non-empty key, or every client shares one budget")
		}
		assert.Equal(t, key, ratelimit.NetKey(key))

		wide := ratelimit.WideKey(ip)
		if ip != "" {
			require.NotEmpty(t, wide, "a non-empty address must map to a non-empty wide key")
		}
		assert.Equal(t, wide, ratelimit.WideKey(wide))
	})
}

// A /48 is what a free tunnel broker hands out: 65,536 /64s, each a fresh NetKey
// budget. WideKey folds them back into one. IPv4 stays per address - behind CGNAT one
// address is already a crowd, and a wider v4 bucket would lock out a whole carrier.
func TestWideKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ip   string
		want string
	}{
		{name: "ipv4 is its own network", ip: "198.51.100.7", want: "198.51.100.7"},
		{name: "ipv4-mapped ipv6 unmaps to the plain address", ip: "::ffff:198.51.100.7", want: "198.51.100.7"},
		{name: "ipv6 collapses to its /48", ip: "2001:db8:1:2:3:4:5:6", want: "2001:db8:1::/48"},
		{name: "zone is not part of the identity", ip: "fe80::1%eth0", want: "fe80::/48"},
		{name: "unparseable input is limited as-is", ip: "not-an-ip", want: "not-an-ip"},
		{name: "empty input", ip: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ratelimit.WideKey(tt.ip))
		})
	}
}

func TestWideKey_EverySlash64OfATunnelSharesOneKey(t *testing.T) {
	t.Parallel()

	first, last := "2001:db8:abcd:0::1", "2001:db8:abcd:ffff::1"
	assert.NotEqual(t, ratelimit.NetKey(first), ratelimit.NetKey(last))
	assert.Equal(t, ratelimit.WideKey(first), ratelimit.WideKey(last))
	assert.NotEqual(t, ratelimit.WideKey(first), ratelimit.WideKey("2001:db8:abce::1"),
		"the neighbouring /48 is somebody else")
}
