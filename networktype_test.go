// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNetworkTypeParsing_Success(t *testing.T) {
	ipv4 := net.ParseIP("192.168.0.1")
	ipv6 := net.ParseIP("fe80::a3:6ff:fec4:5454")

	for _, test := range []struct {
		name      string
		inNetwork string
		inIP      net.IP
		expected  NetworkType
	}{
		{
			"lowercase UDP4",
			"udp",
			ipv4,
			NetworkTypeUDP4,
		},
		{
			"uppercase UDP4",
			"UDP",
			ipv4,
			NetworkTypeUDP4,
		},
		{
			"lowercase UDP6",
			"udp",
			ipv6,
			NetworkTypeUDP6,
		},
		{
			"uppercase UDP6",
			"UDP",
			ipv6,
			NetworkTypeUDP6,
		},
	} {
		actual, err := determineNetworkType(test.inNetwork, mustAddr(t, test.inIP))
		require.NoError(t, err)
		require.Equal(t, test.expected, actual)
	}
}

func TestNetworkTypeParsing_Failure(t *testing.T) {
	ipv6 := net.ParseIP("fe80::a3:6ff:fec4:5454")

	for _, test := range []struct {
		name      string
		inNetwork string
		inIP      net.IP
	}{
		{
			"invalid network",
			"junkNetwork",
			ipv6,
		},
	} {
		_, err := determineNetworkType(test.inNetwork, mustAddr(t, test.inIP))
		require.Error(t, err)
	}
}

func TestNetworkTypeIsUDP(t *testing.T) {
	require.True(t, NetworkTypeUDP4.IsUDP())
	require.True(t, NetworkTypeUDP6.IsUDP())
	require.False(t, NetworkTypeUDP4.IsTCP())
	require.False(t, NetworkTypeUDP6.IsTCP())
}

func TestNetworkTypeIsTCP(t *testing.T) {
	require.True(t, NetworkTypeTCP4.IsTCP())
	require.True(t, NetworkTypeTCP6.IsTCP())
	require.False(t, NetworkTypeTCP4.IsUDP())
	require.False(t, NetworkTypeTCP6.IsUDP())
}
