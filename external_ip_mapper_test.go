// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func makeRule(candidateType CandidateType, ips ...string) AddressRewriteRule {
	return AddressRewriteRule{
		External:        ips,
		AsCandidateType: candidateType,
	}
}

func makeLocalRule(local string, ips ...string) AddressRewriteRule {
	return AddressRewriteRule{
		External:        ips,
		Local:           local,
		AsCandidateType: CandidateTypeHost,
	}
}

func assertExternalIPStrings(
	t *testing.T,
	mapper *addressRewriteMapper,
	candidateType CandidateType,
	localIP string,
	iface string,
	expected ...string,
) {
	t.Helper()

	ips, matched, _, err := mapper.findExternalIPs(candidateType, localIP, iface)
	assert.NoError(t, err)
	assert.True(t, matched)
	assert.Len(t, ips, len(expected))
	for i, ip := range ips {
		assert.Equal(t, expected[i], ip.String())
	}
}

func assertNoExternalMapping(t *testing.T, mapper *addressRewriteMapper, candidateType CandidateType, localIP string) {
	t.Helper()

	ips, matched, _, err := mapper.findExternalIPs(candidateType, localIP, "")
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
		mapper, err := newAddressRewriteMapper(nil)
		assert.NoError(t, err)
		assert.Nil(t, mapper)
	})

	t.Run("empty rules", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{})
		assert.NoError(t, err)
		assert.Nil(t, mapper)
	})

	t.Run("default candidate type", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{makeRule(CandidateTypeUnspecified, "1.2.3.4")})
		assert.NoError(t, err)
		assert.NotNil(t, mapper)
		assert.True(t, mapper.hasCandidateType(CandidateTypeHost))

		assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.1", "eth0", "1.2.3.4")
	})

	t.Run("server reflexive candidate type", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{makeRule(CandidateTypeServerReflexive, "1.2.3.4")})
		assert.NoError(t, err)
		assert.NotNil(t, mapper)
		assert.True(t, mapper.hasCandidateType(CandidateTypeServerReflexive))
	})

	t.Run("unsupported candidate type", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{makeRule(CandidateTypePeerReflexive, "1.2.3.4")})
		assert.ErrorIs(t, err, ErrUnsupportedNAT1To1IPCandidateType)
		assert.Nil(t, mapper)
	})

	cases := []struct {
		name         string
		rules        []AddressRewriteRule
		expectMapper func(t *testing.T, mapper *addressRewriteMapper)
	}{
		{
			name: "mixed external families",
			rules: []AddressRewriteRule{
				makeRule(CandidateTypeHost, "1.2.3.4", "2001:db8::1"),
			},
			expectMapper: func(t *testing.T, mapper *addressRewriteMapper) {
				t.Helper()

				assert.NotNil(t, mapper)
				assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.1", "eth1", "1.2.3.4")
				assertExternalIPStrings(t, mapper, CandidateTypeHost, "2001:db8::10", "eth0", "2001:db8::1")
			},
		},
		{
			name: "invalid external ip",
			rules: []AddressRewriteRule{
				makeRule(CandidateTypeHost, "bad.2.3.4"),
			},
		},
		{
			name: "explicit mapping via slash rejected",
			rules: []AddressRewriteRule{
				makeRule(CandidateTypeHost, "1.2.3.4/10.0.0.1"),
			},
		},
		{
			name: "invalid local ip",
			rules: []AddressRewriteRule{
				makeLocalRule("10.0.0.bad", "1.2.3.4"),
			},
		},
		{
			name: "mixed family pair ipv6 ext ipv4 local",
			rules: []AddressRewriteRule{
				makeLocalRule("10.0.0.1", "2200::1"),
			},
			expectMapper: func(t *testing.T, mapper *addressRewriteMapper) {
				t.Helper()

				assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.1", "hosttest0", "2200::1")
			},
		},
		{
			name: "mixed family pair ipv4 ext ipv6 local",
			rules: []AddressRewriteRule{
				makeLocalRule("fe80::1", "1.2.3.4"),
			},
			expectMapper: func(t *testing.T, mapper *addressRewriteMapper) {
				t.Helper()

				assertExternalIPStrings(t, mapper, CandidateTypeHost, "fe80::1", "xdsl0", "1.2.3.4")
			},
		},
		{
			name: "implicit and explicit mix",
			rules: []AddressRewriteRule{
				makeLocalRule("10.0.0.1", "1.2.3.5"),
				makeRule(CandidateTypeHost, "1.2.3.4"),
			},
			expectMapper: func(t *testing.T, mapper *addressRewriteMapper) {
				t.Helper()

				assert.NotNil(t, mapper)

				assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.1", "", "1.2.3.5")
				assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.2", "", "1.2.3.4")
			},
		},
		{
			name: "invalid pair format",
			rules: []AddressRewriteRule{
				makeRule(CandidateTypeHost, "1.2.3.4/192.168.0.2/10.0.0.1"),
			},
		},
		{
			name: "cidr family mismatch with external",
			rules: []AddressRewriteRule{
				{
					External:        []string{"2001:db8::1"},
					AsCandidateType: CandidateTypeHost,
					CIDR:            "10.0.0.0/24",
				},
			},
			expectMapper: func(t *testing.T, mapper *addressRewriteMapper) {
				t.Helper()

				assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.5", "", "2001:db8::1")
				assertNoExternalMapping(t, mapper, CandidateTypeHost, "192.168.0.1")
				assertNoExternalMapping(t, mapper, CandidateTypeHost, "2001:db8::5")
			},
		},
		{
			name: "invalid cidr explicit mapping",
			rules: []AddressRewriteRule{
				{
					External:        []string{"1.2.3.4"},
					Local:           "10.0.0.1",
					AsCandidateType: CandidateTypeHost,
					CIDR:            "192.168.0.0/24",
				},
			},
		},
		{
			name: "invalid cidr syntax",
			rules: []AddressRewriteRule{
				{
					External:        []string{"1.2.3.4"},
					AsCandidateType: CandidateTypeHost,
					CIDR:            "not-a-cidr",
				},
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			mapper, err := newAddressRewriteMapper(tc.rules)
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
	mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
		makeRule(CandidateTypeHost, "1.2.3.4"),
		makeRule(CandidateTypeHost, "2200::1"),
	})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.1", "", "1.2.3.4")
	assertExternalIPStrings(t, mapper, CandidateTypeHost, "fe80::1", "", "2200::1")
}

func TestAddressRewriteIfaceScope(t *testing.T) {
	mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
		{
			External:        []string{"203.0.113.10"},
			Local:           "10.0.0.10",
			AsCandidateType: CandidateTypeHost,
			Iface:           "eth0",
		},
	})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	ips, matched, _, findErr := mapper.findExternalIPs(CandidateTypeHost, "10.0.0.10", "eth0")
	assert.NoError(t, findErr)
	assert.True(t, matched)
	assert.NotEmpty(t, ips)
	assert.Equal(t, "203.0.113.10", ips[0].String())

	ips, matched, _, findErr = mapper.findExternalIPs(CandidateTypeHost, "10.0.0.10", "wlan0")
	assert.NoError(t, findErr)
	assert.False(t, matched)
	assert.Nil(t, ips)
}

func TestAddressRewriteRuleOrdering(t *testing.T) {
	mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
		{
			External:        []string{"203.0.113.200"},
			AsCandidateType: CandidateTypeHost, // catch-all
		},
		{
			External:        []string{"198.51.100.5"},
			AsCandidateType: CandidateTypeHost,
			CIDR:            "10.0.0.0/24",
		},
		{
			External:        []string{"198.51.100.6"},
			AsCandidateType: CandidateTypeHost,
			Iface:           "eth0",
		},
	})
	assert.NoError(t, err)

	ips, matched, _, findErr := mapper.findExternalIPs(CandidateTypeHost, "10.0.0.5", "")
	assert.NoError(t, findErr)
	assert.True(t, matched)
	assert.Equal(t, "198.51.100.5", ips[0].String())

	ips, matched, _, findErr = mapper.findExternalIPs(CandidateTypeHost, "10.0.0.6", "eth0")
	assert.NoError(t, findErr)
	assert.True(t, matched)
	assert.Equal(t, "198.51.100.6", ips[0].String())

	ips, matched, _, findErr = mapper.findExternalIPs(CandidateTypeHost, "10.0.0.6", "wlan0")
	assert.NoError(t, findErr)
	assert.True(t, matched)
	assert.Equal(t, "203.0.113.200", ips[0].String())
}

func TestAddressRewriteModeDefaultsAndExplicit(t *testing.T) {
	mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
		makeRule(CandidateTypeHost, "1.2.3.4"),
		makeRule(CandidateTypeServerReflexive, "5.6.7.8"),
		makeRule(CandidateTypeRelay, "203.0.113.44"),
		{
			External:        []string{"9.9.9.9"},
			Local:           "10.0.0.5",
			AsCandidateType: CandidateTypeHost,
			Mode:            AddressRewriteAppend,
		},
	})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	hostRules := mapper.rulesByCandidateType[CandidateTypeHost]
	assert.Equal(t, AddressRewriteReplace, hostRules[0].mode)
	assert.Equal(t, AddressRewriteAppend, hostRules[1].mode)

	srflxRules := mapper.rulesByCandidateType[CandidateTypeServerReflexive]
	assert.Equal(t, AddressRewriteAppend, srflxRules[0].mode)

	relayRules := mapper.rulesByCandidateType[CandidateTypeRelay]
	assert.Equal(t, AddressRewriteAppend, relayRules[0].mode)

	ips, matched, mode, modeErr := mapper.findExternalIPs(CandidateTypeHost, "10.0.0.5", "")
	assert.NoError(t, modeErr)
	assert.True(t, matched)
	assert.Equal(t, AddressRewriteAppend, mode)
	assert.Len(t, ips, 1)
}

func TestAddressRewriteModeDefaultsTable(t *testing.T) {
	tests := []struct {
		name          string
		rule          AddressRewriteRule
		localIP       string
		expectMode    AddressRewriteMode
		expectAddress string
	}{
		{
			name:          "host default replace",
			rule:          makeRule(CandidateTypeHost, "203.0.113.10"),
			localIP:       "10.0.0.1",
			expectMode:    AddressRewriteReplace,
			expectAddress: "203.0.113.10",
		},
		{
			name:          "srflx default append",
			rule:          makeRule(CandidateTypeServerReflexive, "203.0.113.20"),
			localIP:       "0.0.0.0",
			expectMode:    AddressRewriteAppend,
			expectAddress: "203.0.113.20",
		},
		{
			name:          "relay default append",
			rule:          makeRule(CandidateTypeRelay, "203.0.113.30"),
			localIP:       "192.0.2.1",
			expectMode:    AddressRewriteAppend,
			expectAddress: "203.0.113.30",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			mapper, err := newAddressRewriteMapper([]AddressRewriteRule{tt.rule})
			assert.NoError(t, err)

			ips, matched, mode, findErr := mapper.findExternalIPs(tt.rule.AsCandidateType, tt.localIP, "")
			assert.NoError(t, findErr)
			assert.True(t, matched)
			assert.NotEmpty(t, ips)
			assert.Equal(t, tt.expectMode, mode)
			assert.Equal(t, tt.expectAddress, ips[0].String())
		})
	}
}

func TestCloneIPsSkipsNil(t *testing.T) {
	ipv4 := net.ParseIP("203.0.113.10")
	ipv6 := net.ParseIP("2001:db8::1")

	cloned := cloneIPs([]net.IP{ipv4, nil, ipv6})
	assert.Len(t, cloned, 2)
	assert.Equal(t, "203.0.113.10", cloned[0].String())
	assert.Equal(t, "2001:db8::1", cloned[1].String())
}

func TestIPMappingFindExternalIPs(t *testing.T) {
	t.Run("invalid mapping returns nil", func(t *testing.T) {
		m := ipMapping{}
		ips := m.findExternalIPs(net.ParseIP("10.0.0.1"))
		assert.Nil(t, ips)
	})

	t.Run("catch-all returns sole", func(t *testing.T) {
		m := ipMapping{
			ipSole: []net.IP{net.ParseIP("203.0.113.10")},
			valid:  true,
		}
		ips := m.findExternalIPs(net.ParseIP("10.0.0.1"))
		assert.Len(t, ips, 1)
		assert.Equal(t, "203.0.113.10", ips[0].String())
	})

	t.Run("no mapping found returns error", func(t *testing.T) {
		m := ipMapping{
			valid: true,
		}
		ips := m.findExternalIPs(net.ParseIP("10.0.0.1"))
		assert.Nil(t, ips)
	})
}

func TestAddressRewriteModeHostReplaceAndAppend(t *testing.T) {
	t.Run("replace host mapping removes original", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.1"},
				Local:           "10.0.0.1",
				AsCandidateType: CandidateTypeHost,
				Mode:            AddressRewriteReplace,
			},
		})
		assert.NoError(t, err)

		ips, matched, mode, findErr := mapper.findExternalIPs(CandidateTypeHost, "10.0.0.1", "")
		assert.NoError(t, findErr)
		assert.True(t, matched)
		assert.Equal(t, AddressRewriteReplace, mode)
		assert.Equal(t, []string{"203.0.113.1"}, []string{ips[0].String()})
	})

	t.Run("append host mapping keeps original and adds new", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.1"},
				Local:           "10.0.0.1",
				AsCandidateType: CandidateTypeHost,
				Mode:            AddressRewriteAppend,
			},
		})
		assert.NoError(t, err)

		ips, matched, mode, findErr := mapper.findExternalIPs(CandidateTypeHost, "10.0.0.1", "")
		assert.NoError(t, findErr)
		assert.True(t, matched)
		assert.Equal(t, AddressRewriteAppend, mode)
		assert.Equal(t, []string{"203.0.113.1"}, []string{ips[0].String()})
	})

	t.Run("replace host mapping allows cross family", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.20"},
				Local:           "2001:db8::5",
				AsCandidateType: CandidateTypeHost,
				Mode:            AddressRewriteReplace,
			},
		})
		assert.NoError(t, err)

		ips, matched, mode, findErr := mapper.findExternalIPs(CandidateTypeHost, "2001:db8::5", "")
		assert.NoError(t, findErr)
		assert.True(t, matched)
		assert.Equal(t, AddressRewriteReplace, mode)
		assert.Equal(t, []string{"203.0.113.20"}, []string{ips[0].String()})
	})
}

func TestAddressRewriteModeSrflxReplaceAndAppend(t *testing.T) {
	t.Run("srflx default append", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"198.51.100.10"},
				Local:           "0.0.0.0",
				AsCandidateType: CandidateTypeServerReflexive,
			},
		})
		assert.NoError(t, err)

		ips, matched, mode, findErr := mapper.findExternalIPs(CandidateTypeServerReflexive, "0.0.0.0", "")
		assert.NoError(t, findErr)
		assert.True(t, matched)
		assert.Equal(t, AddressRewriteAppend, mode)
		assert.Equal(t, []string{"198.51.100.10"}, []string{ips[0].String()})
	})

	t.Run("srflx explicit replace", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"198.51.100.20"},
				Local:           "0.0.0.0",
				AsCandidateType: CandidateTypeServerReflexive,
				Mode:            AddressRewriteReplace,
			},
		})
		assert.NoError(t, err)

		ips, matched, mode, findErr := mapper.findExternalIPs(CandidateTypeServerReflexive, "0.0.0.0", "")
		assert.NoError(t, findErr)
		assert.True(t, matched)
		assert.Equal(t, AddressRewriteReplace, mode)
		assert.Equal(t, []string{"198.51.100.20"}, []string{ips[0].String()})
	})
}

func TestFindExternalIPMultipleCatchAll(t *testing.T) {
	mapper, err := newAddressRewriteMapper([]AddressRewriteRule{makeRule(CandidateTypeHost, "1.2.3.4", "5.6.7.8")})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	ips, matched, _, err := mapper.findExternalIPs(CandidateTypeHost, "10.0.0.1", "")
	assert.NoError(t, err)
	assert.True(t, matched)
	assert.Len(t, ips, 2)
	assert.Equal(t, "1.2.3.4", ips[0].String())
	assert.Equal(t, "5.6.7.8", ips[1].String())
}

func TestFindExternalIPCIDRFilter(t *testing.T) {
	rule := makeRule(CandidateTypeHost, "1.2.3.4")
	rule.CIDR = "10.0.0.0/24"

	mapper, err := newAddressRewriteMapper([]AddressRewriteRule{rule})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.10", "", "1.2.3.4")
	assertNoExternalMapping(t, mapper, CandidateTypeHost, "192.168.0.1")
}

func TestFindExternalIPExplicitMapping(t *testing.T) {
	mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
		makeLocalRule("10.0.0.1", "1.2.3.4"),
		makeLocalRule("10.0.0.2", "1.2.3.5"),
		makeLocalRule("fe80::1", "2200::1"),
		makeLocalRule("fe80::2", "2200::2"),
	})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.1", "", "1.2.3.4")
	assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.2", "", "1.2.3.5")
	assertNoExternalMapping(t, mapper, CandidateTypeHost, "10.0.0.3")
	assertExternalIPStrings(t, mapper, CandidateTypeHost, "fe80::1", "", "2200::1")
	assertExternalIPStrings(t, mapper, CandidateTypeHost, "fe80::2", "", "2200::2")
	assertNoExternalMapping(t, mapper, CandidateTypeHost, "fe80::3")
}

func TestFindExternalIPServerReflexive(t *testing.T) {
	mapper, err := newAddressRewriteMapper([]AddressRewriteRule{makeRule(CandidateTypeServerReflexive, "1.2.3.4")})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	assertExternalIPStrings(t, mapper, CandidateTypeServerReflexive, "0.0.0.0", "", "1.2.3.4")
}

func TestFindExternalIPFallbackAndErrors(t *testing.T) {
	t.Run("fallback to local address when candidate type missing", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{makeRule(CandidateTypeServerReflexive, "1.2.3.4")})
		assert.NoError(t, err)
		assert.NotNil(t, mapper)

		assertNoExternalMapping(t, mapper, CandidateTypeHost, "10.0.0.1")
	})

	t.Run("invalid local ip", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{makeRule(CandidateTypeHost, "1.2.3.4")})
		assert.NoError(t, err)
		assert.NotNil(t, mapper)

		_, _, _, err = mapper.findExternalIPs(CandidateTypeHost, "really.bad", "")
		assert.Error(t, err)
	})

	t.Run("append with zero externals returns error but keeps original upstream", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        nil,
				Local:           "10.0.0.11",
				AsCandidateType: CandidateTypeHost,
				Mode:            AddressRewriteAppend,
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, mapper)

		_, matched, mode, err := mapper.findExternalIPs(CandidateTypeHost, "10.0.0.11", "")
		assert.True(t, matched)
		assert.Equal(t, AddressRewriteAppend, mode)
		assert.NoError(t, err)
	})
}

func TestExternalIPMapperNetworksFilter(t *testing.T) {
	mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
		{
			External:        []string{"203.0.113.2"},
			Local:           "10.0.0.2",
			AsCandidateType: CandidateTypeHost,
			Networks:        []NetworkType{NetworkTypeUDP4},
		},
	})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.2", "", "203.0.113.2")
	assertNoExternalMapping(t, mapper, CandidateTypeHost, "2001:db8:1::1")

	mapper, err = newAddressRewriteMapper([]AddressRewriteRule{
		{
			External:        []string{"2001:db8::6"},
			Local:           "2001:db8:2::6",
			AsCandidateType: CandidateTypeServerReflexive,
			Networks:        []NetworkType{NetworkTypeUDP6},
		},
	})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	assertExternalIPStrings(t, mapper, CandidateTypeServerReflexive, "2001:db8:2::6", "", "2001:db8::6")
	assertNoExternalMapping(t, mapper, CandidateTypeServerReflexive, "192.0.2.10")

	t.Run("nil and empty networks are equivalent", func(t *testing.T) {
		nilNetworks, nilErr := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.5"},
				Local:           "10.0.0.5",
				AsCandidateType: CandidateTypeHost,
			},
		})
		assert.NoError(t, nilErr)
		assert.NotNil(t, nilNetworks)
		assertExternalIPStrings(t, nilNetworks, CandidateTypeHost, "10.0.0.5", "", "203.0.113.5")

		emptyNetworks, emptyErr := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.5"},
				Local:           "10.0.0.5",
				AsCandidateType: CandidateTypeHost,
				Networks:        []NetworkType{},
			},
		})
		assert.NoError(t, emptyErr)
		assert.NotNil(t, emptyNetworks)
		assertExternalIPStrings(t, emptyNetworks, CandidateTypeHost, "10.0.0.5", "", "203.0.113.5")
	})

	t.Run("nil/empty allow ipv6 while udp4 excludes it", func(t *testing.T) {
		nilMapper, nilErr := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"2001:db8::50"},
				Local:           "2001:db8::50",
				AsCandidateType: CandidateTypeHost,
			},
		})
		assert.NoError(t, nilErr)
		assert.NotNil(t, nilMapper)
		assertExternalIPStrings(t, nilMapper, CandidateTypeHost, "2001:db8::50", "", "2001:db8::50")

		emptyMapper, emptyErr := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"2001:db8::50"},
				Local:           "2001:db8::50",
				AsCandidateType: CandidateTypeHost,
				Networks:        []NetworkType{},
			},
		})
		assert.NoError(t, emptyErr)
		assert.NotNil(t, emptyMapper)
		assertExternalIPStrings(t, emptyMapper, CandidateTypeHost, "2001:db8::50", "", "2001:db8::50")

		udp4Mapper, udp4Err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"2001:db8::50"},
				Local:           "2001:db8::50",
				AsCandidateType: CandidateTypeHost,
				Networks:        []NetworkType{NetworkTypeUDP4},
			},
		})
		assert.NoError(t, udp4Err)
		assert.Nil(t, udp4Mapper)
	})

	t.Run("mixed family rule respects each address family", func(t *testing.T) {
		mixedMapper, mixedErr := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.99"},
				AsCandidateType: CandidateTypeHost,
			},
			{
				External:        []string{"2001:db8::99"},
				Local:           "2001:db8:1::99",
				AsCandidateType: CandidateTypeHost,
			},
		})
		assert.NoError(t, mixedErr)
		assert.NotNil(t, mixedMapper)

		assertExternalIPStrings(t, mixedMapper, CandidateTypeHost, "10.10.10.10", "", "203.0.113.99")
		assertExternalIPStrings(t, mixedMapper, CandidateTypeHost, "2001:db8:1::99", "", "2001:db8::99")
	})
}

func TestAddressRewritePrecedenceMatrix(t *testing.T) {
	mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
		{
			External:        []string{"203.0.113.200"},
			AsCandidateType: CandidateTypeHost,
			CIDR:            "10.0.0.0/24",
		},
		{
			External:        []string{"198.51.100.200"},
			AsCandidateType: CandidateTypeHost,
		},
		{
			External:        []string{"192.0.2.50"},
			Local:           "10.0.0.50",
			AsCandidateType: CandidateTypeHost,
		},
	})
	assert.NoError(t, err)
	assert.NotNil(t, mapper)

	assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.50", "", "192.0.2.50")
	assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.25", "", "203.0.113.200")
	assertExternalIPStrings(t, mapper, CandidateTypeHost, "172.16.0.1", "", "198.51.100.200")
}

func TestExternalIPMapperRuleOrderAndSpecificity(t *testing.T) {
	t.Run("earliest matching rule wins", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.10"},
				Local:           "10.0.0.5",
				AsCandidateType: CandidateTypeHost,
			},
			{
				External:        []string{"198.51.100.10"},
				Local:           "10.0.0.5",
				AsCandidateType: CandidateTypeHost,
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, mapper)

		assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.5", "", "203.0.113.10")
	})

	t.Run("specific mapping outranks cidr and catch-all", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.30"},
				Local:           "10.0.0.5",
				AsCandidateType: CandidateTypeHost,
				CIDR:            "10.0.0.0/24",
			},
			{
				External:        []string{"203.0.113.40"},
				AsCandidateType: CandidateTypeHost,
				CIDR:            "10.0.0.0/24",
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, mapper)

		assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.5", "", "203.0.113.30")
		assertExternalIPStrings(t, mapper, CandidateTypeHost, "10.0.0.10", "", "203.0.113.40")
		assertNoExternalMapping(t, mapper, CandidateTypeHost, "192.0.2.20")
	})
}
