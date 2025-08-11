// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExternalIPMapper(t *testing.T) { //nolint:maintidx
	t.Run("validateIPString", func(t *testing.T) {
		var ip net.IP
		var isIPv4 bool
		var err error

		ip, isIPv4, err = validateIPString("1.2.3.4")
		require.NoError(t, err, "should succeed")
		require.True(t, isIPv4, "should be true")
		require.Equal(t, "1.2.3.4", ip.String(), "should be true")

		ip, isIPv4, err = validateIPString("2601:4567::5678")
		require.NoError(t, err, "should succeed")
		require.False(t, isIPv4, "should be false")
		require.Equal(t, "2601:4567::5678", ip.String(), "should be true")

		_, _, err = validateIPString("bad.6.6.6")
		require.Error(t, err, "should fail")
	})

	t.Run("newExternalIPMapper", func(t *testing.T) {
		var mapper *externalIPMapper
		var err error

		// ips being nil should succeed but mapper will be nil also
		mapper, err = newExternalIPMapper(CandidateTypeUnspecified, nil)
		require.NoError(t, err, "should succeed")
		require.Nil(t, mapper, "should be nil")

		// ips being empty should succeed but mapper will still be nil
		mapper, err = newExternalIPMapper(CandidateTypeUnspecified, []string{})
		require.NoError(t, err, "should succeed")
		require.Nil(t, mapper, "should be nil")

		// IPv4 with no explicit local IP, defaults to CandidateTypeHost
		mapper, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4",
		})
		require.NoError(t, err, "should succeed")
		require.NotNil(t, mapper, "should not be nil")
		require.Equal(t, CandidateTypeHost, mapper.candidateType, "should match")
		require.NotNil(t, mapper.ipv4Mapping.ipSole)
		require.Nil(t, mapper.ipv6Mapping.ipSole)
		require.Equal(t, 0, len(mapper.ipv4Mapping.ipMap), "should match")
		require.Equal(t, 0, len(mapper.ipv6Mapping.ipMap), "should match")

		// IPv4 with no explicit local IP, using CandidateTypeServerReflexive
		mapper, err = newExternalIPMapper(CandidateTypeServerReflexive, []string{
			"1.2.3.4",
		})
		require.NoError(t, err, "should succeed")
		require.NotNil(t, mapper, "should not be nil")
		require.Equal(t, CandidateTypeServerReflexive, mapper.candidateType, "should match")
		require.NotNil(t, mapper.ipv4Mapping.ipSole)
		require.Nil(t, mapper.ipv6Mapping.ipSole)
		require.Equal(t, 0, len(mapper.ipv4Mapping.ipMap), "should match")
		require.Equal(t, 0, len(mapper.ipv6Mapping.ipMap), "should match")

		// IPv4 with no explicit local IP, defaults to CandidateTypeHost
		mapper, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"2601:4567::5678",
		})
		require.NoError(t, err, "should succeed")
		require.NotNil(t, mapper, "should not be nil")
		require.Equal(t, CandidateTypeHost, mapper.candidateType, "should match")
		require.Nil(t, mapper.ipv4Mapping.ipSole)
		require.NotNil(t, mapper.ipv6Mapping.ipSole)
		require.Equal(t, 0, len(mapper.ipv4Mapping.ipMap), "should match")
		require.Equal(t, 0, len(mapper.ipv6Mapping.ipMap), "should match")

		// IPv4 and IPv6 in the mix
		mapper, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4",
			"2601:4567::5678",
		})
		require.NoError(t, err, "should succeed")
		require.NotNil(t, mapper, "should not be nil")
		require.Equal(t, CandidateTypeHost, mapper.candidateType, "should match")
		require.NotNil(t, mapper.ipv4Mapping.ipSole)
		require.NotNil(t, mapper.ipv6Mapping.ipSole)
		require.Equal(t, 0, len(mapper.ipv4Mapping.ipMap), "should match")
		require.Equal(t, 0, len(mapper.ipv6Mapping.ipMap), "should match")

		// Unsupported candidate type - CandidateTypePeerReflexive
		mapper, err = newExternalIPMapper(CandidateTypePeerReflexive, []string{
			"1.2.3.4",
		})
		require.Error(t, err, "should fail")
		require.Nil(t, mapper, "should be nil")

		// Unsupported candidate type - CandidateTypeRelay
		mapper, err = newExternalIPMapper(CandidateTypePeerReflexive, []string{
			"1.2.3.4",
		})
		require.Error(t, err, "should fail")
		require.Nil(t, mapper, "should be nil")

		// Cannot duplicate mapping IPv4 family
		mapper, err = newExternalIPMapper(CandidateTypeServerReflexive, []string{
			"1.2.3.4",
			"5.6.7.8",
		})
		require.Error(t, err, "should fail")
		require.Nil(t, mapper, "should be nil")

		// Cannot duplicate mapping IPv6 family
		mapper, err = newExternalIPMapper(CandidateTypeServerReflexive, []string{
			"2201::1",
			"2201::0002",
		})
		require.Error(t, err, "should fail")
		require.Nil(t, mapper, "should be nil")

		// Invalide external IP string
		mapper, err = newExternalIPMapper(CandidateTypeServerReflexive, []string{
			"bad.2.3.4",
		})
		require.Error(t, err, "should fail")
		require.Nil(t, mapper, "should be nil")

		// Invalide local IP string
		mapper, err = newExternalIPMapper(CandidateTypeServerReflexive, []string{
			"1.2.3.4/10.0.0.bad",
		})
		require.Error(t, err, "should fail")
		require.Nil(t, mapper, "should be nil")
	})

	t.Run("newExternalIPMapper with explicit local IP", func(t *testing.T) {
		var mapper *externalIPMapper
		var err error

		// IPv4 with  explicit local IP, defaults to CandidateTypeHost
		mapper, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4/10.0.0.1",
		})
		require.NoError(t, err, "should succeed")
		require.NotNil(t, mapper, "should not be nil")
		require.Equal(t, CandidateTypeHost, mapper.candidateType, "should match")
		require.Nil(t, mapper.ipv4Mapping.ipSole)
		require.Nil(t, mapper.ipv6Mapping.ipSole)
		require.Equal(t, 1, len(mapper.ipv4Mapping.ipMap), "should match")
		require.Equal(t, 0, len(mapper.ipv6Mapping.ipMap), "should match")

		// Cannot assign two ext IPs for one local IPv4
		mapper, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4/10.0.0.1",
			"1.2.3.5/10.0.0.1",
		})
		require.Error(t, err, "should fail")
		require.Nil(t, mapper, "should be nil")

		// Cannot assign two ext IPs for one local IPv6
		mapper, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"2200::1/fe80::1",
			"2200::0002/fe80::1",
		})
		require.Error(t, err, "should fail")
		require.Nil(t, mapper, "should be nil")

		// Cannot mix different IP family in a pair (1)
		mapper, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"2200::1/10.0.0.1",
		})
		require.Error(t, err, "should fail")
		require.Nil(t, mapper, "should be nil")

		// Cannot mix different IP family in a pair (2)
		mapper, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4/fe80::1",
		})
		require.Error(t, err, "should fail")
		require.Nil(t, mapper, "should be nil")

		// Invalid pair
		mapper, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4/192.168.0.2/10.0.0.1",
		})
		require.Error(t, err, "should fail")
		require.Nil(t, mapper, "should be nil")
	})

	t.Run("newExternalIPMapper with implicit and explicit local IP", func(t *testing.T) {
		// Mixing implicit and explicit local IPs not allowed
		_, err := newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4",
			"1.2.3.5/10.0.0.1",
		})
		require.Error(t, err, "should fail")

		// Mixing implicit and explicit local IPs not allowed
		_, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.5/10.0.0.1",
			"1.2.3.4",
		})
		require.Error(t, err, "should fail")
	})

	t.Run("findExternalIP without explicit local IP", func(t *testing.T) {
		var mapper *externalIPMapper
		var err error
		var extIP net.IP

		// IPv4 with  explicit local IP, defaults to CandidateTypeHost
		mapper, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4",
			"2200::1",
		})
		require.NoError(t, err, "should succeed")
		require.NotNil(t, mapper, "should not be nil")
		require.NotNil(t, mapper.ipv4Mapping.ipSole)
		require.NotNil(t, mapper.ipv6Mapping.ipSole)

		// Find external IPv4
		extIP, err = mapper.findExternalIP("10.0.0.1")
		require.NoError(t, err, "should succeed")
		require.Equal(t, "1.2.3.4", extIP.String(), "should match")

		// Find external IPv6
		extIP, err = mapper.findExternalIP("fe80::0001") // Use '0001' instead of '1' on purpose
		require.NoError(t, err, "should succeed")
		require.Equal(t, "2200::1", extIP.String(), "should match")

		// Bad local IP string
		_, err = mapper.findExternalIP("really.bad")
		require.Error(t, err, "should fail")
	})

	t.Run("findExternalIP with explicit local IP", func(t *testing.T) {
		var mapper *externalIPMapper
		var err error
		var extIP net.IP

		// IPv4 with  explicit local IP, defaults to CandidateTypeHost
		mapper, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4/10.0.0.1",
			"1.2.3.5/10.0.0.2",
			"2200::1/fe80::1",
			"2200::2/fe80::2",
		})
		require.NoError(t, err, "should succeed")
		require.NotNil(t, mapper, "should not be nil")

		// Find external IPv4
		extIP, err = mapper.findExternalIP("10.0.0.1")
		require.NoError(t, err, "should succeed")
		require.Equal(t, "1.2.3.4", extIP.String(), "should match")

		extIP, err = mapper.findExternalIP("10.0.0.2")
		require.NoError(t, err, "should succeed")
		require.Equal(t, "1.2.3.5", extIP.String(), "should match")

		_, err = mapper.findExternalIP("10.0.0.3")
		require.Error(t, err, "should fail")

		// Find external IPv6
		extIP, err = mapper.findExternalIP("fe80::0001") // Use '0001' instead of '1' on purpose
		require.NoError(t, err, "should succeed")
		require.Equal(t, "2200::1", extIP.String(), "should match")

		extIP, err = mapper.findExternalIP("fe80::0002") // Use '0002' instead of '2' on purpose
		require.NoError(t, err, "should succeed")
		require.Equal(t, "2200::2", extIP.String(), "should match")

		_, err = mapper.findExternalIP("fe80::3")
		require.Error(t, err, "should fail")

		// Bad local IP string
		_, err = mapper.findExternalIP("really.bad")
		require.Error(t, err, "should fail")
	})

	t.Run("findExternalIP with empty map", func(t *testing.T) {
		var mapper *externalIPMapper
		var err error

		mapper, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4",
		})
		require.NoError(t, err, "should succeed")

		// Attempt to find IPv6 that does not exist in the map
		extIP, err := mapper.findExternalIP("fe80::1")
		require.NoError(t, err, "should succeed")
		require.Equal(t, "fe80::1", extIP.String(), "should match")

		mapper, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"2200::1",
		})
		require.NoError(t, err, "should succeed")

		// Attempt to find IPv4 that does not exist in the map
		extIP, err = mapper.findExternalIP("10.0.0.1")
		require.NoError(t, err, "should succeed")
		require.Equal(t, "10.0.0.1", extIP.String(), "should match")
	})

	t.Run("newExternalIPMapperAdvanced", func(t *testing.T) {
		// Both mappers nil -> returns nil mapper
		mapper, err := newExternalIPMapperAdvanced(CandidateTypeUnspecified, nil, nil)
		require.NoError(t, err)
		require.Nil(t, mapper)

		// Unsupported candidate types -> error
		mapper, err = newExternalIPMapperAdvanced(CandidateTypePeerReflexive, nil, func(net.IP) []endpoint { return nil })
		require.Error(t, err)
		require.Nil(t, mapper)
		mapper, err = newExternalIPMapperAdvanced(CandidateTypeRelay, func(net.IP) []endpoint { return nil }, nil)
		require.Error(t, err)
		require.Nil(t, mapper)

		// Only UDP mapper provided -> TCP defaults to identity mapping, CandidateType defaults to Host
		udpMappedIP := net.ParseIP("1.2.3.4")
		udpMappedPort := 12345
		mapper, err = newExternalIPMapperAdvanced(CandidateTypeUnspecified,
			func(_ net.IP) []endpoint { return []endpoint{{ip: udpMappedIP, port: udpMappedPort}} },
			nil,
		)
		require.NoError(t, err)
		require.NotNil(t, mapper)
		require.Equal(t, CandidateTypeHost, mapper.candidateType)

		eps, err := mapper.findExternalEndpoints(udp, net.IP{10, 0, 0, 1})
		require.NoError(t, err)
		require.Len(t, eps, 1)
		require.Equal(t, udpMappedIP.String(), eps[0].ip.String())
		require.Equal(t, udpMappedPort, eps[0].port)

		// For TCP, identity mapping should be used
		local := net.IP{10, 0, 0, 1}
		eps, err = mapper.findExternalEndpoints(tcp, local)
		require.NoError(t, err)
		require.Len(t, eps, 1)
		require.Equal(t, local.String(), eps[0].ip.String())
		require.Equal(t, 0, eps[0].port)

		// Both mappers provided and CandidateTypeServerReflexive
		tcpMappedIP := net.ParseIP("5.6.7.8")
		tcpMappedPort := 23456
		mapper, err = newExternalIPMapperAdvanced(CandidateTypeServerReflexive,
			func(_ net.IP) []endpoint { return []endpoint{{ip: udpMappedIP, port: udpMappedPort}} },
			func(_ net.IP) []endpoint { return []endpoint{{ip: tcpMappedIP, port: tcpMappedPort}} },
		)
		require.NoError(t, err)
		require.NotNil(t, mapper)
		require.Equal(t, CandidateTypeServerReflexive, mapper.candidateType)

		eps, err = mapper.findExternalEndpoints(udp, net.IP{192, 168, 0, 10})
		require.NoError(t, err)
		require.Len(t, eps, 1)
		require.Equal(t, udpMappedIP.String(), eps[0].ip.String())
		require.Equal(t, udpMappedPort, eps[0].port)

		eps, err = mapper.findExternalEndpoints(tcp, net.IP{192, 168, 0, 11})
		require.NoError(t, err)
		require.Len(t, eps, 1)
		require.Equal(t, tcpMappedIP.String(), eps[0].ip.String())
		require.Equal(t, tcpMappedPort, eps[0].port)

		// Unknown network -> error
		eps, err = mapper.findExternalEndpoints("sctp", net.IP{1, 1, 1, 1})
		require.Error(t, err)
		require.Nil(t, eps)
	})
}
