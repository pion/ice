// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExternalIPMapper(t *testing.T) {
	t.Run("validateIPString", func(t *testing.T) {
		assert := assert.New(t)

		var ip net.IP
		var isIPv4 bool
		var err error

		ip, isIPv4, err = validateIPString("1.2.3.4")
		assert.NoError(err)
		assert.True(isIPv4)
		assert.Equal("1.2.3.4", ip.String())

		ip, isIPv4, err = validateIPString("2601:4567::5678")
		assert.NoError(err)
		assert.False(isIPv4)
		assert.Equal("2601:4567::5678", ip.String())

		_, _, err = validateIPString("bad.6.6.6")
		assert.Error(err)
	})

	t.Run("newExternalIPMapper", func(t *testing.T) {
		assert := assert.New(t)

		var m *externalIPMapper
		var err error

		// ips being nil should succeed but mapper will be nil also
		m, err = newExternalIPMapper(CandidateTypeUnspecified, nil)
		assert.NoError(err)
		assert.Nil(m)

		// ips being empty should succeed but mapper will still be nil
		m, err = newExternalIPMapper(CandidateTypeUnspecified, []string{})
		assert.NoError(err)
		assert.Nil(m)

		// IPv4 with no explicit local IP, defaults to CandidateTypeHost
		m, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4",
		})
		assert.NoError(err)
		assert.NotNil(m)
		assert.Equal(CandidateTypeHost, m.candidateType)
		assert.NotNil(m.ipv4Mapping.ipSole)
		assert.Nil(m.ipv6Mapping.ipSole)
		assert.Equal(0, len(m.ipv4Mapping.ipMap))
		assert.Equal(0, len(m.ipv6Mapping.ipMap))

		// IPv4 with no explicit local IP, using CandidateTypeServerReflexive
		m, err = newExternalIPMapper(CandidateTypeServerReflexive, []string{
			"1.2.3.4",
		})
		assert.NoError(err)
		assert.NotNil(m)
		assert.Equal(CandidateTypeServerReflexive, m.candidateType)
		assert.NotNil(m.ipv4Mapping.ipSole)
		assert.Nil(m.ipv6Mapping.ipSole)
		assert.Equal(0, len(m.ipv4Mapping.ipMap))
		assert.Equal(0, len(m.ipv6Mapping.ipMap))

		// IPv4 with no explicit local IP, defaults to CandidateTypeHost
		m, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"2601:4567::5678",
		})
		assert.NoError(err)
		assert.NotNil(m)
		assert.Equal(CandidateTypeHost, m.candidateType)
		assert.Nil(m.ipv4Mapping.ipSole)
		assert.NotNil(m.ipv6Mapping.ipSole)
		assert.Equal(0, len(m.ipv4Mapping.ipMap))
		assert.Equal(0, len(m.ipv6Mapping.ipMap))

		// IPv4 and IPv6 in the mix
		m, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4",
			"2601:4567::5678",
		})
		assert.NoError(err)
		assert.NotNil(m)
		assert.Equal(CandidateTypeHost, m.candidateType)
		assert.NotNil(m.ipv4Mapping.ipSole)
		assert.NotNil(m.ipv6Mapping.ipSole)
		assert.Equal(0, len(m.ipv4Mapping.ipMap))
		assert.Equal(0, len(m.ipv6Mapping.ipMap))

		// Unsupported candidate type - CandidateTypePeerReflexive
		m, err = newExternalIPMapper(CandidateTypePeerReflexive, []string{
			"1.2.3.4",
		})
		assert.Error(err)
		assert.Nil(m)

		// Unsupported candidate type - CandidateTypeRelay
		m, err = newExternalIPMapper(CandidateTypePeerReflexive, []string{
			"1.2.3.4",
		})
		assert.Error(err)
		assert.Nil(m)

		// Cannot duplicate mapping IPv4 family
		m, err = newExternalIPMapper(CandidateTypeServerReflexive, []string{
			"1.2.3.4",
			"5.6.7.8",
		})
		assert.Error(err)
		assert.Nil(m)

		// Cannot duplicate mapping IPv6 family
		m, err = newExternalIPMapper(CandidateTypeServerReflexive, []string{
			"2201::1",
			"2201::0002",
		})
		assert.Error(err)
		assert.Nil(m)

		// Invalide external IP string
		m, err = newExternalIPMapper(CandidateTypeServerReflexive, []string{
			"bad.2.3.4",
		})
		assert.Error(err)
		assert.Nil(m)

		// Invalide local IP string
		m, err = newExternalIPMapper(CandidateTypeServerReflexive, []string{
			"1.2.3.4/10.0.0.bad",
		})
		assert.Error(err)
		assert.Nil(m)
	})

	t.Run("newExternalIPMapper with explicit local IP", func(t *testing.T) {
		assert := assert.New(t)

		var m *externalIPMapper
		var err error

		// IPv4 with  explicit local IP, defaults to CandidateTypeHost
		m, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4/10.0.0.1",
		})
		assert.NoError(err)
		assert.NotNil(m)
		assert.Equal(CandidateTypeHost, m.candidateType)
		assert.Nil(m.ipv4Mapping.ipSole)
		assert.Nil(m.ipv6Mapping.ipSole)
		assert.Equal(1, len(m.ipv4Mapping.ipMap))
		assert.Equal(0, len(m.ipv6Mapping.ipMap))

		// Cannot assign two ext IPs for one local IPv4
		m, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4/10.0.0.1",
			"1.2.3.5/10.0.0.1",
		})
		assert.Error(err)
		assert.Nil(m)

		// Cannot assign two ext IPs for one local IPv6
		m, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"2200::1/fe80::1",
			"2200::0002/fe80::1",
		})
		assert.Error(err)
		assert.Nil(m)

		// Cannot mix different IP family in a pair (1)
		m, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"2200::1/10.0.0.1",
		})
		assert.Error(err)
		assert.Nil(m)

		// Cannot mix different IP family in a pair (2)
		m, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4/fe80::1",
		})
		assert.Error(err)
		assert.Nil(m)

		// Invalid pair
		m, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4/192.168.0.2/10.0.0.1",
		})
		assert.Error(err)
		assert.Nil(m)
	})

	t.Run("newExternalIPMapper with implicit and explicit local IP", func(t *testing.T) {
		assert := assert.New(t)

		// Mixing implicit and explicit local IPs not allowed
		_, err := newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4",
			"1.2.3.5/10.0.0.1",
		})
		assert.Error(err)

		// Mixing implicit and explicit local IPs not allowed
		_, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.5/10.0.0.1",
			"1.2.3.4",
		})
		assert.Error(err)
	})

	t.Run("findExternalIP without explicit local IP", func(t *testing.T) {
		assert := assert.New(t)

		var m *externalIPMapper
		var err error
		var extIP net.IP

		// IPv4 with  explicit local IP, defaults to CandidateTypeHost
		m, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4",
			"2200::1",
		})
		assert.NoError(err)
		assert.NotNil(m)
		assert.NotNil(m.ipv4Mapping.ipSole)
		assert.NotNil(m.ipv6Mapping.ipSole)

		// Find external IPv4
		extIP, err = m.findExternalIP("10.0.0.1")
		assert.NoError(err)
		assert.Equal("1.2.3.4", extIP.String())

		// Find external IPv6
		extIP, err = m.findExternalIP("fe80::0001") // Use '0001' instead of '1' on purpose
		assert.NoError(err)
		assert.Equal("2200::1", extIP.String())

		// Bad local IP string
		_, err = m.findExternalIP("really.bad")
		assert.Error(err)
	})

	t.Run("findExternalIP with explicit local IP", func(t *testing.T) {
		assert := assert.New(t)

		var m *externalIPMapper
		var err error
		var extIP net.IP

		// IPv4 with  explicit local IP, defaults to CandidateTypeHost
		m, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4/10.0.0.1",
			"1.2.3.5/10.0.0.2",
			"2200::1/fe80::1",
			"2200::2/fe80::2",
		})
		assert.NoError(err)
		assert.NotNil(m)

		// Find external IPv4
		extIP, err = m.findExternalIP("10.0.0.1")
		assert.NoError(err)
		assert.Equal("1.2.3.4", extIP.String())

		extIP, err = m.findExternalIP("10.0.0.2")
		assert.NoError(err)
		assert.Equal("1.2.3.5", extIP.String())

		_, err = m.findExternalIP("10.0.0.3")
		assert.Error(err)

		// Find external IPv6
		extIP, err = m.findExternalIP("fe80::0001") // Use '0001' instead of '1' on purpose
		assert.NoError(err)
		assert.Equal("2200::1", extIP.String())

		extIP, err = m.findExternalIP("fe80::0002") // Use '0002' instead of '2' on purpose
		assert.NoError(err)
		assert.Equal("2200::2", extIP.String())

		_, err = m.findExternalIP("fe80::3")
		assert.Error(err)

		// Bad local IP string
		_, err = m.findExternalIP("really.bad")
		assert.Error(err)
	})

	t.Run("findExternalIP with empty map", func(t *testing.T) {
		assert := assert.New(t)

		var m *externalIPMapper
		var err error

		m, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"1.2.3.4",
		})
		assert.NoError(err)

		// Attempt to find IPv6 that does not exist in the map
		extIP, err := m.findExternalIP("fe80::1")
		assert.NoError(err)
		assert.Equal("fe80::1", extIP.String())

		m, err = newExternalIPMapper(CandidateTypeUnspecified, []string{
			"2200::1",
		})
		assert.NoError(err)

		// Attempt to find IPv4 that does not exist in the map
		extIP, err = m.findExternalIP("10.0.0.1")
		assert.NoError(err)
		assert.Equal("10.0.0.1", extIP.String())
	})
}
