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

func assertExternalIPStrings(
	t *testing.T, mapper *externalIPMapper, candidateType CandidateType, localIP string, expected ...string,
) {
	t.Helper()

	ips, matched, err := mapper.findExternalIPs(candidateType, localIP)
	assert.NoError(t, err)
	assert.True(t, matched)
	assert.Len(t, ips, len(expected))
	for i, ip := range ips {
		assert.Equal(t, expected[i], ip.String())
	}
}

func assertExternalIPError(
	t *testing.T, mapper *externalIPMapper, candidateType CandidateType, localIP string, expected error,
) {
	t.Helper()

	_, matched, err := mapper.findExternalIPs(candidateType, localIP)
	assert.True(t, matched)
	assert.ErrorIs(t, err, expected)
}

func assertNoExternalMapping(t *testing.T, mapper *externalIPMapper, candidateType CandidateType, localIP string) {
	t.Helper()

	ips, matched, err := mapper.findExternalIPs(candidateType, localIP)
	assert.NoError(t, err)
	assert.False(t, matched)
	assert.Nil(t, ips)
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

		assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.1", "1.2.3.4")
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

				assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.1", "1.2.3.5")
				assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.2", "1.2.3.4")
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

	assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.1", "1.2.3.4")
	assertExternalIPStrings(t, mapper, CandidateTypeHost, "fe80::1", "2200::1")
}

func TestFindExternalIPMultipleCatchAll(t *testing.T) {
	mapper, err := newExternalIPMapper([]NAT1To1Rule{makeRule(CandidateTypeHost, "1.2.3.4", "5.6.7.8")})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	ips, matched, err := mapper.findExternalIPs(CandidateTypeHost, "10.0.0.1")
	assert.NoError(t, err)
	assert.True(t, matched)
	assert.Len(t, ips, 2)
	assert.Equal(t, "1.2.3.4", ips[0].String())
	assert.Equal(t, "5.6.7.8", ips[1].String())
}

func TestFindExternalIPCIDRFilter(t *testing.T) {
	rule := makeRule(CandidateTypeHost, "1.2.3.4")
	rule.CIDR = "10.0.0.0/24"

	mapper, err := newExternalIPMapper([]NAT1To1Rule{rule})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.10", "1.2.3.4")
	assertNoExternalMapping(t, mapper, CandidateTypeHost, "192.168.0.1")
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

	assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.1", "1.2.3.4")
	assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.2", "1.2.3.5")
	assertExternalIPError(t, mapper, CandidateTypeHost, "10.0.0.3", ErrExternalMappedIPNotFound)
	assertExternalIPStrings(t, mapper, CandidateTypeHost, "fe80::1", "2200::1")
	assertExternalIPStrings(t, mapper, CandidateTypeHost, "fe80::2", "2200::2")
	assertExternalIPError(t, mapper, CandidateTypeHost, "fe80::3", ErrExternalMappedIPNotFound)
}

func TestFindExternalIPServerReflexive(t *testing.T) {
	mapper, err := newExternalIPMapper([]NAT1To1Rule{makeRule(CandidateTypeServerReflexive, "1.2.3.4")})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	assertExternalIPStrings(t, mapper, CandidateTypeServerReflexive, "0.0.0.0", "1.2.3.4")
}

func TestFindExternalIPFallbackAndErrors(t *testing.T) {
	t.Run("fallback to local address when candidate type missing", func(t *testing.T) {
		mapper, err := newExternalIPMapper([]NAT1To1Rule{makeRule(CandidateTypeServerReflexive, "1.2.3.4")})
		assert.NoError(t, err)
		assert.NotNil(t, mapper)

		assertNoExternalMapping(t, mapper, CandidateTypeHost, "10.0.0.1")
	})

	t.Run("invalid local ip", func(t *testing.T) {
		mapper, err := newExternalIPMapper([]NAT1To1Rule{makeRule(CandidateTypeHost, "1.2.3.4")})
		assert.NoError(t, err)
		assert.NotNil(t, mapper)

		_, _, err = mapper.findExternalIPs(CandidateTypeHost, "really.bad")
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

	assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.2", "203.0.113.2")
	assertNoExternalMapping(t, mapper, CandidateTypeHost, "2001:db8:1::1")

	mapper, err = newExternalIPMapper([]NAT1To1Rule{
		{
			PublicIPs:       []string{"2001:db8::6/2001:db8:2::6"},
			AsCandidateType: CandidateTypeServerReflexive,
			Networks:        []NetworkType{NetworkTypeUDP6},
		},
	})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	assertExternalIPStrings(t, mapper, CandidateTypeServerReflexive, "2001:db8:2::6", "2001:db8::6")
	assertNoExternalMapping(t, mapper, CandidateTypeServerReflexive, "192.0.2.10")

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

		assertExternalIPStrings(t, mixedMapper, CandidateTypeHost, "10.10.10.10", "203.0.113.99")
		assertExternalIPStrings(t, mixedMapper, CandidateTypeHost, "2001:db8:1::99", "2001:db8::99")
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

		assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.5", "203.0.113.10")
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

		assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.5", "203.0.113.30")
		assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.10", "203.0.113.40")
		assertNoExternalMapping(t, mapper, CandidateTypeHost, "192.0.2.20")
	})
}
