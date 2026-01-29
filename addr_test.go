// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"net"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

// A net.Addr type that parseAddr doesn't handle.
type unknownAddr struct{}

func (unknownAddr) Network() string { return "unknown" }
func (unknownAddr) String() string  { return "unknown-addr" }

func TestParseAddrFromIface_ErrFromParseAddr(t *testing.T) {
	in := unknownAddr{}
	ip, port, nt, err := parseAddrFromIface(in, "eth0")

	require.Error(t, err, "expected error from parseAddr for unknown net.Addr type")
	require.Zero(t, port)
	require.Zero(t, nt)
	require.True(t, !ip.IsValid(), "ip should be zero value when error is returned")
}

func TestParseAddr_ErrorBranches(t *testing.T) {
	t.Run("IPNet invalid IP -> error", func(t *testing.T) {
		// length 1 slice -> ipAddrToNetIP fails
		_, _, _, err := parseAddr(&net.IPNet{IP: net.IP{1}})
		var convErr ipConvertError
		require.ErrorAs(t, err, &convErr)
	})

	t.Run("IPAddr invalid IP -> error", func(t *testing.T) {
		_, _, _, err := parseAddr(&net.IPAddr{IP: net.IP{1}, Zone: "eth0"})
		var convErr ipConvertError
		require.ErrorAs(t, err, &convErr)
	})

	t.Run("UDPAddr invalid IP -> error", func(t *testing.T) {
		_, _, _, err := parseAddr(&net.UDPAddr{IP: net.IP{1}, Port: 3478})
		var convErr ipConvertError
		require.ErrorAs(t, err, &convErr)
	})

	t.Run("TCPAddr invalid IP -> error", func(t *testing.T) {
		_, _, _, err := parseAddr(&net.TCPAddr{IP: net.IP{1}, Port: 3478})
		var convErr ipConvertError
		require.ErrorAs(t, err, &convErr)
	})

	t.Run("Unknown net.Addr type -> addrParseError", func(t *testing.T) {
		_, _, _, err := parseAddr(unknownAddr{})
		var ap addrParseError
		require.ErrorAs(t, err, &ap)
	})
}

func TestParseAddr_IPAddr_Success(t *testing.T) {
	ip := net.ParseIP("fe80::1")
	require.NotNil(t, ip)

	gotIP, port, nt, err := parseAddr(&net.IPAddr{IP: ip, Zone: "lo0"})
	require.NoError(t, err)
	require.Equal(t, 0, port)
	require.Equal(t, NetworkType(0), nt)
	require.True(t, gotIP.Is6())
	require.Equal(t, "lo0", gotIP.Zone())
	require.Equal(t, 0, gotIP.Compare(netip.MustParseAddr("fe80::1%lo0").Unmap()))
}

func TestAddrParseError_Error(t *testing.T) {
	e := addrParseError{addr: &net.TCPAddr{}}
	require.Equal(t,
		"do not know how to parse address type *net.TCPAddr",
		e.Error(),
	)
}

func TestIPConvertError_Error(t *testing.T) {
	e := ipConvertError{ip: []byte("bad-ip")}
	require.Equal(t,
		"failed to convert IP 'bad-ip' to netip.Addr",
		e.Error(),
	)
}

func TestIPAddrToNetIP_Error_InvalidBytes(t *testing.T) {
	bad := []byte{1} // invalid length -> AddrFromSlice returns ok=false
	got, err := ipAddrToNetIP(bad, "")
	require.Equal(t, netip.Addr{}, got, "should return zero addr on error")
	require.Error(t, err)
	require.IsType(t, ipConvertError{}, err)
	require.Contains(t, err.Error(), "failed to convert IP")
}

func TestIPAddrToNetIP_OK_IPv4(t *testing.T) {
	ipv4 := []byte{1, 2, 3, 4}
	got, err := ipAddrToNetIP(ipv4, "")
	require.NoError(t, err)
	require.True(t, got.Is4())

	want := netip.AddrFrom4([4]byte{1, 2, 3, 4})
	require.Equal(t, want, got)
}

func TestAddrEqual_FirstParseError(t *testing.T) {
	a := unknownAddr{}
	b := &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 9999}

	require.False(t, addrEqual(a, b))
}

func TestAddrEqual_SecondParseError(t *testing.T) {
	a := &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 9999}
	b := unknownAddr{}

	require.False(t, addrEqual(a, b))
}

func TestAddrEqual_SameTypeIPPort(t *testing.T) {
	a := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 4242}
	b := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 4242}

	require.True(t, addrEqual(a, b))
}
