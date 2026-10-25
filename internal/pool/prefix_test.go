package pool

import "testing"

// The free counter belongs to the pool the hosting provider hands out, not to the
// single address, so one key must cover a whole /24 (IPv4) or /64 (IPv6).
func TestExitPrefixNamesThePool(t *testing.T) {
	cases := []struct{ ip, want string }{
		{"192.0.2.10", "192.0.2.0/24"},
		{"192.0.2.200", "192.0.2.0/24"},
		{"192.0.3.10", "192.0.3.0/24"},
		{"::ffff:192.0.2.10", "192.0.2.0/24"},
		{"2001:db8::1", "2001:db8::/64"},
		{"2001:db8:0:0:ffff::9", "2001:db8::/64"},
		{"2001:db8:0:1::1", "2001:db8:0:1::/64"},
		{"", ""},
		{"not-an-address", ""},
	}
	for _, c := range cases {
		if got := exitPrefix(c.ip); got != c.want {
			t.Errorf("exitPrefix(%q) = %q, want %q", c.ip, got, c.want)
		}
	}
}

func TestSameExitPoolAsksThePrefix(t *testing.T) {
	if !sameExitPool("192.0.2.10", "192.0.2.200") {
		t.Fatal("one /24 must be one pool")
	}
	if sameExitPool("192.0.2.10", "192.0.3.10") {
		t.Fatal("different /24s are different pools")
	}
	if sameExitPool("", "192.0.2.10") {
		t.Fatal("an unknown address must not match a pool")
	}
}
