package config

import (
	"net/netip"
	"testing"
)

func TestPrefixesAcceptsAddressesAndCIDRs(t *testing.T) {
	got, err := prefixes("192.0.2.10, 2001:db8::/64")
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32"), netip.MustParsePrefix("2001:db8::/64")}
	if len(got) != len(want) {
		t.Fatalf("got %d prefixes, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("prefix %d = %v, want %v", i, got[i], want[i])
		}
	}
	if _, err := prefixes("not-an-address"); err == nil {
		t.Fatal("invalid trusted proxy accepted")
	}
}
