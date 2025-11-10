// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func makeRule(candidateType CandidateType, ips ...string) NAT1To1Rule {
	return NAT1To1Rule{
		PublicIPs:       ips,
		AsCandidateType: candidateType,
	}
}

func TestValidateIPString(t *testing.T) {
	var ip net.IP
	var isIPv4 bool
	var err error

	ip, isIPv4, err = validateIPString("1.2.3.4")
	assert.NoError(t, err)
	assert.True(t, isIPv4)
	assert.Equal(t, "1.2.3.4", ip.String())

	ip, isIPv4, err = validateIPString("2601:4567::5678")
	assert.NoError(t, err)
	assert.False(t, isIPv4)
	assert.Equal(t, "2601:4567::5678", ip.String())

	_, _, err = validateIPString("bad.6.6.6")
	assert.Error(t, err)
}

//nolint:nlreturn // test fixtures intentionally inline for clarity.
func TestNewExternalIPMapper(t *testing.T) {
	t.Run("nil rules", func(t *testing.T) {
		mapper, err := newExternalIPMapper(nil)
		assert.NoError(t, err)
		assert.Nil(t, mapper)
	})

	t.Run("empty rules", func(t *testing.T) {
		mapper, err := newExternalIPMapper([]NAT1To1Rule{})
		assert.NoError(t, err)
		assert.Nil(t, mapper)
	})

	t.Run("default candidate type", func(t *testing.T) {
		mapper, err := newExternalIPMapper([]NAT1To1Rule{makeRule(CandidateTypeUnspecified, "1.2.3.4")})
		assert.NoError(t, err)
		assert.NotNil(t, mapper)
		assert.True(t, mapper.hasCandidateType(CandidateTypeHost))

		extIP, err := mapper.findExternalIP(CandidateTypeHost, "10.0.0.1")
		assert.NoError(t, err)
		assert.Equal(t, "1.2.3.4", extIP.String())
	})

	t.Run("server reflexive candidate type", func(t *testing.T) {
		mapper, err := newExternalIPMapper([]NAT1To1Rule{makeRule(CandidateTypeServerReflexive, "1.2.3.4")})
		assert.NoError(t, err)
		assert.NotNil(t, mapper)
		assert.True(t, mapper.hasCandidateType(CandidateTypeServerReflexive))
	})

	t.Run("unsupported candidate type", func(t *testing.T) {
		mapper, err := newExternalIPMapper([]NAT1To1Rule{makeRule(CandidateTypePeerReflexive, "1.2.3.4")})
		assert.ErrorIs(t, err, ErrUnsupportedNAT1To1IPCandidateType)
		assert.Nil(t, mapper)
	})

	invalidCases := []struct {
		name         string
		rule         NAT1To1Rule
		expectMapper func(t *testing.T, mapper *externalIPMapper)
	}{
		{
			name: "duplicate ipv4 sole",
			rule: makeRule(CandidateTypeHost, "1.2.3.4", "5.6.7.8"),
		},
		{
			name: "duplicate ipv6 sole",
			rule: makeRule(CandidateTypeHost, "2201::1", "2201::0002"),
		},
		{
			name: "invalid external ip",
			rule: makeRule(CandidateTypeHost, "bad.2.3.4"),
		},
		{
			name: "invalid local ip",
			rule: makeRule(CandidateTypeHost, "1.2.3.4/10.0.0.bad"),
		},
		{
			name: "mixed family pair ipv6 ext ipv4 local",
			rule: makeRule(CandidateTypeHost, "2200::1/10.0.0.1"),
		},
		{
			name: "mixed family pair ipv4 ext ipv6 local",
			rule: makeRule(CandidateTypeHost, "1.2.3.4/fe80::1"),
		},
		{
			name: "implicit and explicit mix",
			rule: makeRule(CandidateTypeHost, "1.2.3.4", "1.2.3.5/10.0.0.1"),
			expectMapper: func(t *testing.T, mapper *externalIPMapper) {
				t.Helper()

				assert.NotNil(t, mapper)

				extIP, err := mapper.findExternalIP(CandidateTypeHost, "10.0.0.1")
				assert.NoError(t, err)
				assert.Equal(t, "1.2.3.5", extIP.String())

				extIP, err = mapper.findExternalIP(CandidateTypeHost, "10.0.0.2")
				assert.NoError(t, err)
				assert.Equal(t, "1.2.3.4", extIP.String())
			},
		},
		{
			name: "invalid pair format",
			rule: makeRule(CandidateTypeHost, "1.2.3.4/192.168.0.2/10.0.0.1"),
		},
		{
			name: "invalid cidr explicit mapping",
			rule: NAT1To1Rule{
				PublicIPs:       []string{"1.2.3.4/10.0.0.1"},
				AsCandidateType: CandidateTypeHost,
				CIDR:            "192.168.0.0/24",
			},
		},
		{
			name: "invalid cidr syntax",
			rule: NAT1To1Rule{
				PublicIPs:       []string{"1.2.3.4"},
				AsCandidateType: CandidateTypeHost,
				CIDR:            "not-a-cidr",
			},
		},
	}

	for _, tc := range invalidCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			mapper, err := newExternalIPMapper([]NAT1To1Rule{tc.rule})
			if tc.expectMapper != nil {
				assert.NoError(t, err)
				tc.expectMapper(t, mapper)
			} else {
				assert.ErrorIs(t, err, ErrInvalidNAT1To1IPMapping)
				assert.Nil(t, mapper)
			}
		})
	}
}

func TestFindExternalIPHost(t *testing.T) {
	mapper, err := newExternalIPMapper([]NAT1To1Rule{makeRule(CandidateTypeHost, "1.2.3.4", "2200::1")})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	extIP, err := mapper.findExternalIP(CandidateTypeHost, "10.0.0.1")
	assert.NoError(t, err)
	assert.Equal(t, "1.2.3.4", extIP.String())

	extIP, err = mapper.findExternalIP(CandidateTypeHost, "fe80::1")
	assert.NoError(t, err)
	assert.Equal(t, "2200::1", extIP.String())
}

func TestFindExternalIPCIDRFilter(t *testing.T) {
	rule := makeRule(CandidateTypeHost, "1.2.3.4")
	rule.CIDR = "10.0.0.0/24"

	mapper, err := newExternalIPMapper([]NAT1To1Rule{rule})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	extIP, err := mapper.findExternalIP(CandidateTypeHost, "10.0.0.10")
	assert.NoError(t, err)
	assert.Equal(t, "1.2.3.4", extIP.String())

	extIP, err = mapper.findExternalIP(CandidateTypeHost, "192.168.0.1")
	assert.NoError(t, err)
	assert.Equal(t, "192.168.0.1", extIP.String())
}

func TestFindExternalIPExplicitMapping(t *testing.T) {
	mapper, err := newExternalIPMapper([]NAT1To1Rule{makeRule(
		CandidateTypeHost,
		"1.2.3.4/10.0.0.1",
		"1.2.3.5/10.0.0.2",
		"2200::1/fe80::1",
		"2200::2/fe80::2",
	)})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	extIP, err := mapper.findExternalIP(CandidateTypeHost, "10.0.0.1")
	assert.NoError(t, err)
	assert.Equal(t, "1.2.3.4", extIP.String())

	extIP, err = mapper.findExternalIP(CandidateTypeHost, "10.0.0.2")
	assert.NoError(t, err)
	assert.Equal(t, "1.2.3.5", extIP.String())

	_, err = mapper.findExternalIP(CandidateTypeHost, "10.0.0.3")
	assert.ErrorIs(t, err, ErrExternalMappedIPNotFound)

	extIP, err = mapper.findExternalIP(CandidateTypeHost, "fe80::1")
	assert.NoError(t, err)
	assert.Equal(t, "2200::1", extIP.String())

	extIP, err = mapper.findExternalIP(CandidateTypeHost, "fe80::2")
	assert.NoError(t, err)
	assert.Equal(t, "2200::2", extIP.String())

	_, err = mapper.findExternalIP(CandidateTypeHost, "fe80::3")
	assert.ErrorIs(t, err, ErrExternalMappedIPNotFound)
}

func TestFindExternalIPServerReflexive(t *testing.T) {
	mapper, err := newExternalIPMapper([]NAT1To1Rule{makeRule(CandidateTypeServerReflexive, "1.2.3.4")})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	extIP, err := mapper.findExternalIP(CandidateTypeServerReflexive, "0.0.0.0")
	assert.NoError(t, err)
	assert.Equal(t, "1.2.3.4", extIP.String())
}

func TestFindExternalIPFallbackAndErrors(t *testing.T) {
	t.Run("fallback to local address when candidate type missing", func(t *testing.T) {
		mapper, err := newExternalIPMapper([]NAT1To1Rule{makeRule(CandidateTypeServerReflexive, "1.2.3.4")})
		assert.NoError(t, err)
		assert.NotNil(t, mapper)

		extIP, err := mapper.findExternalIP(CandidateTypeHost, "10.0.0.1")
		assert.NoError(t, err)
		assert.Equal(t, "10.0.0.1", extIP.String())
	})

	t.Run("invalid local ip", func(t *testing.T) {
		mapper, err := newExternalIPMapper([]NAT1To1Rule{makeRule(CandidateTypeHost, "1.2.3.4")})
		assert.NoError(t, err)
		assert.NotNil(t, mapper)

		_, err = mapper.findExternalIP(CandidateTypeHost, "really.bad")
		assert.Error(t, err)
	})
}

func TestExternalIPMapperNetworksFilter(t *testing.T) {
	mapper, err := newExternalIPMapper([]NAT1To1Rule{
		{
			PublicIPs:       []string{"203.0.113.2/10.0.0.2"},
			AsCandidateType: CandidateTypeHost,
			Networks:        []NetworkType{NetworkTypeUDP4},
		},
	})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	extIP, err := mapper.findExternalIP(CandidateTypeHost, "10.0.0.2")
	assert.NoError(t, err)
	assert.Equal(t, "203.0.113.2", extIP.String())

	extIP, err = mapper.findExternalIP(CandidateTypeHost, "2001:db8:1::1")
	assert.NoError(t, err)
	assert.Equal(t, "2001:db8:1::1", extIP.String())

	mapper, err = newExternalIPMapper([]NAT1To1Rule{
		{
			PublicIPs:       []string{"2001:db8::6/2001:db8:2::6"},
			AsCandidateType: CandidateTypeServerReflexive,
			Networks:        []NetworkType{NetworkTypeUDP6},
		},
	})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	extIP, err = mapper.findExternalIP(CandidateTypeServerReflexive, "2001:db8:2::6")
	assert.NoError(t, err)
	assert.Equal(t, "2001:db8::6", extIP.String())

	extIP, err = mapper.findExternalIP(CandidateTypeServerReflexive, "192.0.2.10")
	assert.NoError(t, err)
	assert.Equal(t, "192.0.2.10", extIP.String())

	t.Run("mixed family rule respects each address family", func(t *testing.T) {
		mixedMapper, mixedErr := newExternalIPMapper([]NAT1To1Rule{
			{
				PublicIPs: []string{
					"203.0.113.99",
					"2001:db8::99/2001:db8:1::99",
				},
				AsCandidateType: CandidateTypeHost,
			},
		})
		assert.NoError(t, mixedErr)
		assert.NotNil(t, mixedMapper)

		extIP, err := mixedMapper.findExternalIP(CandidateTypeHost, "10.10.10.10")
		assert.NoError(t, err)
		assert.Equal(t, "203.0.113.99", extIP.String())

		extIP, err = mixedMapper.findExternalIP(CandidateTypeHost, "2001:db8:1::99")
		assert.NoError(t, err)
		assert.Equal(t, "2001:db8::99", extIP.String())
	})
}

func TestExternalIPMapperRuleOrderAndSpecificity(t *testing.T) {
	t.Run("earliest matching rule wins", func(t *testing.T) {
		mapper, err := newExternalIPMapper([]NAT1To1Rule{
			{
				PublicIPs:       []string{"203.0.113.10/10.0.0.5"},
				AsCandidateType: CandidateTypeHost,
			},
			{
				PublicIPs:       []string{"198.51.100.10/10.0.0.5"},
				AsCandidateType: CandidateTypeHost,
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, mapper)

		extIP, err := mapper.findExternalIP(CandidateTypeHost, "10.0.0.5")
		assert.NoError(t, err)
		assert.Equal(t, "203.0.113.10", extIP.String())
	})

	t.Run("specific mapping outranks cidr and catch-all", func(t *testing.T) {
		mapper, err := newExternalIPMapper([]NAT1To1Rule{
			{
				PublicIPs:       []string{"203.0.113.30/10.0.0.5", "203.0.113.40"},
				AsCandidateType: CandidateTypeHost,
				CIDR:            "10.0.0.0/24",
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, mapper)

		extIP, err := mapper.findExternalIP(CandidateTypeHost, "10.0.0.5")
		assert.NoError(t, err)
		assert.Equal(t, "203.0.113.30", extIP.String())

		extIP, err = mapper.findExternalIP(CandidateTypeHost, "10.0.0.10")
		assert.NoError(t, err)
		assert.Equal(t, "203.0.113.40", extIP.String())

		extIP, err = mapper.findExternalIP(CandidateTypeHost, "192.0.2.20")
		assert.NoError(t, err)
		assert.Equal(t, "192.0.2.20", extIP.String())
	})
}
