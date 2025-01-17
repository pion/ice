// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsSupportedIPv6Partial(t *testing.T) {
	if isSupportedIPv6Partial(net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1}) {
		t.Errorf("isSupportedIPv6Partial returned true with IPv4-compatible IPv6 address")
	}

	if isSupportedIPv6Partial(net.ParseIP("fec0::2333")) {
		t.Errorf("isSupportedIPv6Partial returned true with IPv6 site-local unicast address")
	}

	if !isSupportedIPv6Partial(net.ParseIP("fe80::2333")) {
		t.Errorf("isSupportedIPv6Partial returned false with IPv6 link-local address")
	}

	if !isSupportedIPv6Partial(net.ParseIP("ff02::2333")) {
		t.Errorf("isSupportedIPv6Partial returned false with IPv6 link-local multicast address")
	}

	if !isSupportedIPv6Partial(net.ParseIP("2001::1")) {
		t.Errorf("isSupportedIPv6Partial returned false with IPv6 global unicast address")
	}
}

func TestCreateAddr(t *testing.T) {
	ipv4 := mustAddr(t, net.IP{127, 0, 0, 1})
	ipv6 := mustAddr(t, net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1})
	port := 9000

	require.Equal(t, &net.UDPAddr{IP: ipv4.AsSlice(), Port: port}, createAddr(NetworkTypeUDP4, ipv4, port))
	require.Equal(t, &net.UDPAddr{IP: ipv6.AsSlice(), Port: port}, createAddr(NetworkTypeUDP6, ipv6, port))
	require.Equal(t, &net.TCPAddr{IP: ipv4.AsSlice(), Port: port}, createAddr(NetworkTypeTCP4, ipv4, port))
	require.Equal(t, &net.TCPAddr{IP: ipv6.AsSlice(), Port: port}, createAddr(NetworkTypeTCP6, ipv6, port))
}

func problematicNetworkInterfaces(s string) (keep bool) {
	defaultDockerBridgeNetwork := strings.Contains(s, "docker")
	customDockerBridgeNetwork := strings.Contains(s, "br-")

	// Apple filters
	accessPoint := strings.Contains(s, "ap")
	appleWirelessDirectLink := strings.Contains(s, "awdl")
	appleLowLatencyWLANInterface := strings.Contains(s, "llw")
	appleTunnelingInterface := strings.Contains(s, "utun")

	return !defaultDockerBridgeNetwork &&
		!customDockerBridgeNetwork &&
		!accessPoint &&
		!appleWirelessDirectLink &&
		!appleLowLatencyWLANInterface &&
		!appleTunnelingInterface
}

func mustAddr(t *testing.T, ip net.IP) netip.Addr {
	t.Helper()
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		t.Fatal(ipConvertError{ip})
	}

	return addr
}
