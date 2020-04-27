package ice

import (
	"net"
	"regexp"
	"testing"
)

func TestRandSeq(t *testing.T) {
	if len(randSeq(10)) != 10 {
		t.Errorf("randSeq return invalid length")
	}

	var isLetter = regexp.MustCompile(`^[a-zA-Z]+$`).MatchString
	if !isLetter(randSeq(10)) {
		t.Errorf("randSeq should be AlphaNumeric only")
	}
}

func TestIsSupportedIPv6(t *testing.T) {
	if isSupportedIPv6(net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1}) {
		t.Errorf("isSupportedIPv6 return true with IPv4-compatible IPv6 address")
	}

	if isSupportedIPv6(net.ParseIP("fec0::2333")) {
		t.Errorf("isSupportedIPv6 return true with IPv6 site-local unicast address")
	}

	if isSupportedIPv6(net.ParseIP("fe80::2333")) {
		t.Errorf("isSupportedIPv6 return true with IPv6 link-local address")
	}

	if isSupportedIPv6(net.ParseIP("ff02::2333")) {
		t.Errorf("isSupportedIPv6 return true with IPv6 link-local multicast address")
	}

	if !isSupportedIPv6(net.ParseIP("2001::1")) {
		t.Errorf("isSupportedIPv6 return false with IPv6 global unicast address")
	}
}
