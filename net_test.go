// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"errors"
	"net"
	"net/netip"
	"sort"
	"strings"
	"testing"

	"github.com/pion/logging"
	"github.com/pion/transport/v4"
	"github.com/pion/transport/v4/stdnet"
	"github.com/stretchr/testify/require"
)

func TestIsSupportedIPv6Partial(t *testing.T) {
	require.False(t, isSupportedIPv6Partial(net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1}))
	require.False(t, isSupportedIPv6Partial(net.ParseIP("fec0::2333")))
	require.True(t, isSupportedIPv6Partial(net.ParseIP("fe80::2333")))
	require.True(t, isSupportedIPv6Partial(net.ParseIP("ff02::2333")))
	require.True(t, isSupportedIPv6Partial(net.ParseIP("2001::1")))
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
		t.Fatal(ipConvertError{ip}) // nolint
	}

	return addr
}

type errInterfacesNet struct {
	transport.Net
	retErr error
}

func (e *errInterfacesNet) Interfaces() ([]*transport.Interface, error) {
	return nil, e.retErr
}

var errBoom = errors.New("boom")

func TestLocalInterfaces_ErrorFromInterfaces(t *testing.T) {
	base, err := stdnet.NewNet()
	require.NoError(t, err)

	wrapped := &errInterfacesNet{
		Net:    base,
		retErr: errBoom,
	}

	ifaces, addrs, gotErr := localInterfaces(
		wrapped,
		nil,
		nil,
		nil,
		false,
	)

	require.ErrorIs(t, gotErr, wrapped.retErr)
	require.Nil(t, ifaces, "expected nil iface slice on error")
	require.NotNil(t, addrs, "ipAddrs should be a non-nil empty slice")
	require.Len(t, addrs, 0)
}

type fixedInterfacesNet struct {
	transport.Net
	list []*transport.Interface
}

func (f *fixedInterfacesNet) Interfaces() ([]*transport.Interface, error) {
	return f.list, nil
}

func TestLocalInterfaces_SkipInterfaceDown(t *testing.T) {
	base, err := stdnet.NewNet()
	require.NoError(t, err)

	sysIfaces, err := base.Interfaces()
	require.NoError(t, err)
	if len(sysIfaces) == 0 {
		t.Skip("no system network interfaces available")
	}

	clone := *sysIfaces[0]
	clone.Flags &^= net.FlagUp

	wrapped := &fixedInterfacesNet{
		Net:  base,
		list: []*transport.Interface{&clone},
	}

	ifcs, addrs, ierr := localInterfaces(
		wrapped,
		nil,
		nil,
		nil,
		false,
	)
	require.NoError(t, ierr)
	require.Len(t, ifcs, 0, "down interfaces must be skipped")
	require.Len(t, addrs, 0, "no addresses should be collected from a down interface")
}

func TestLocalInterfaces_SkipLoopbackAddrs_WhenIncludeLoopbackFalse(t *testing.T) {
	base, err := stdnet.NewNet()
	require.NoError(t, err)

	sysIfaces, err := base.Interfaces()
	require.NoError(t, err)

	var loop *transport.Interface
	for _, ifc := range sysIfaces {
		if ifc.Flags&net.FlagLoopback != 0 {
			loop = ifc

			break
		}
	}
	if loop == nil {
		t.Skip("no loopback interface found on this system")
	}

	// clone the loopback iface and clear the Loopback flag so the outer check
	// doesn't drop it to force the inner `(ipAddr.IsLoopback() && !includeLoopback)`.
	cloned := *loop
	cloned.Flags |= net.FlagUp
	cloned.Flags &^= net.FlagLoopback

	wrapped := &fixedInterfacesNet{
		Net:  base,
		list: []*transport.Interface{&cloned},
	}

	ifaces, addrs, ierr := localInterfaces(
		wrapped,
		nil,   // interfaceFilter
		nil,   // ipFilter
		nil,   // networkTypes
		false, // includeLoopback
	)
	require.NoError(t, ierr)

	// don't assert on the number of interfaces because some systems may
	// report the iface as having addresses in a way that causes it to be included.
	// assert that all loopback addresses were skipped.
	for _, a := range addrs {
		require.False(t, a.addr.IsLoopback(), "loopback addresses must be skipped when includeLoopback=false")
	}

	_ = ifaces // intentionally don't assert on this, see above comment
}

// Captures ListenUDP attempts and always fails so the loop exhausts.
type listenUDPCaptor struct {
	transport.Net
	attempts []int
}

func (c *listenUDPCaptor) ListenUDP(network string, laddr *net.UDPAddr) (transport.UDPConn, error) {
	c.attempts = append(c.attempts, laddr.Port)

	return nil, errBoom
}

func TestListenUDPInPortRange_DefaultsPortMinTo1024(t *testing.T) {
	base, err := stdnet.NewNet()
	require.NoError(t, err)

	captor := &listenUDPCaptor{Net: base}
	logger := logging.NewDefaultLoggerFactory().NewLogger("ice-test")

	// portMin == 0 (should become 1024), portMax small to keep the loop short.
	_, err = listenUDPInPortRange(
		captor,
		logger,
		1030, // portMax
		0,    // portMin -> becomes 1024
		udp4,
		&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0},
	)
	require.ErrorIs(t, err, ErrPort)

	// should have attempted exactly [1024..1030] in some order.
	sort.Ints(captor.attempts)
	require.Equal(t, []int{1024, 1025, 1026, 1027, 1028, 1029, 1030}, captor.attempts)
}

func TestListenUDPInPortRange_DefaultsPortMaxToFFFF(t *testing.T) {
	base, err := stdnet.NewNet()
	require.NoError(t, err)

	captor := &listenUDPCaptor{Net: base}
	logger := logging.NewDefaultLoggerFactory().NewLogger("ice-test")

	// portMax == 0 (should become 0xFFFF). Use portMin=65535 so the range is 1 port.
	_, err = listenUDPInPortRange(
		captor,
		logger,
		0,     // portMax -> becomes 65535
		65535, // portMin
		udp4,
		&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0},
	)
	require.ErrorIs(t, err, ErrPort)

	require.Equal(t, []int{65535}, captor.attempts)
}
