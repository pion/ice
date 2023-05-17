// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNetworkTypeParsing_Success(t *testing.T) {
	assert := assert.New(t)

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
		actual, err := determineNetworkType(test.inNetwork, test.inIP)
		assert.NoError(err)
		assert.Equalf(actual, test.expected, "NetworkTypeParsing: %s", test.name)
	}
}

func TestNetworkTypeParsing_Failure(t *testing.T) {
	assert := assert.New(t)

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
		_, err := determineNetworkType(test.inNetwork, test.inIP)
		assert.Errorf(err, "determineNetworkType should fail for %s", test.name)
	}
}

func TestNetworkTypeIsUDP(t *testing.T) {
	assert := assert.New(t)

	assert.True(NetworkTypeUDP4.IsUDP())
	assert.True(NetworkTypeUDP6.IsUDP())
	assert.False(NetworkTypeUDP4.IsTCP())
	assert.False(NetworkTypeUDP6.IsTCP())
}

func TestNetworkTypeIsTCP(t *testing.T) {
	assert := assert.New(t)

	assert.True(NetworkTypeTCP4.IsTCP())
	assert.True(NetworkTypeTCP6.IsTCP())
	assert.False(NetworkTypeTCP4.IsUDP())
	assert.False(NetworkTypeTCP6.IsUDP())
}
