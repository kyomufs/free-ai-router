package pool

import "net/netip"

// exitPrefix is the network an exit address belongs to, as a string: /24 for IPv4
// and /64 for IPv6. Unparsable addresses have no prefix.
//
// The provider's free counter is not per address, it is per pool the hosting
// provider hands out: five nodes measured inside one /24 serve five times the same
// allowance, and the first one spent takes the other four with it. So the pool
// deduplicates and quarantines by this key rather than by the full address — two
// nodes in one network are one budget, and keeping both only makes the list look
// longer than it is.
func exitPrefix(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	addr = addr.Unmap()
	bits := 64
	if addr.Is4() {
		bits = 24
	}
	prefix, err := addr.Prefix(bits)
	if err != nil {
		return ""
	}
	return prefix.String()
}

// sameExitPool reports whether two exit addresses draw on one allowance.
func sameExitPool(a, b string) bool {
	pa := exitPrefix(a)
	return pa != "" && pa == exitPrefix(b)
}
