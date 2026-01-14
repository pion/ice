// SPDX-FileCopyrightText: 2025 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/pion/transport/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/proxy"
)

// testBooleanOption is a helper function to test boolean agent options.
type booleanOptionTest struct {
	optionFunc   func() AgentOption
	getValue     func(*Agent) bool
	configSetter func(*AgentConfig, bool)
}

func testBooleanOption(t *testing.T, test booleanOptionTest, optionName string) {
	t.Helper()

	t.Run("enables "+optionName, func(t *testing.T) {
		agent, err := NewAgentWithOptions(test.optionFunc())
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.True(t, test.getValue(agent))
	})

	t.Run("default is false", func(t *testing.T) {
		agent, err := NewAgentWithOptions()
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.False(t, test.getValue(agent))
	})

	t.Run("works with config", func(t *testing.T) {
		config := &AgentConfig{
			NetworkTypes: []NetworkType{NetworkTypeUDP4},
		}
		test.configSetter(config, true)

		agent, err := NewAgent(config)
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.True(t, test.getValue(agent))
	})
}

func TestDefaultNominationValueGenerator(t *testing.T) {
	t.Run("generates incrementing values", func(t *testing.T) {
		generator := DefaultNominationValueGenerator()

		// Should generate incrementing values starting from 1
		assert.Equal(t, uint32(1), generator())
		assert.Equal(t, uint32(2), generator())
		assert.Equal(t, uint32(3), generator())
	})

	t.Run("each generator has independent counter", func(t *testing.T) {
		gen1 := DefaultNominationValueGenerator()
		gen2 := DefaultNominationValueGenerator()

		assert.Equal(t, uint32(1), gen1())
		assert.Equal(t, uint32(1), gen2()) // Should also start at 1
		assert.Equal(t, uint32(2), gen1())
		assert.Equal(t, uint32(2), gen2())
	})
}

func TestWithLite(t *testing.T) {
	t.Run("enables lite with host candidates", func(t *testing.T) {
		agent, err := NewAgentWithOptions(
			WithCandidateTypes([]CandidateType{CandidateTypeHost}),
			WithICELite(true),
		)
		require.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.True(t, agent.lite)
	})

	t.Run("default is not lite", func(t *testing.T) {
		agent, err := NewAgentWithOptions()
		require.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.False(t, agent.lite)
	})

	t.Run("config sets lite", func(t *testing.T) {
		config := &AgentConfig{
			Lite:           true,
			CandidateTypes: []CandidateType{CandidateTypeHost},
			NetworkTypes:   []NetworkType{NetworkTypeUDP4},
		}

		agent, err := NewAgent(config)
		require.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.True(t, agent.lite)
	})

	t.Run("errors when candidate types include non-host", func(t *testing.T) {
		_, err := NewAgentWithOptions(WithICELite(true))
		assert.ErrorIs(t, err, ErrLiteUsingNonHostCandidates)
	})
}

func TestWithUrls(t *testing.T) {
	stunURL, err := stun.ParseURI("stun:example.com:3478")
	require.NoError(t, err)

	input := []*stun.URI{stunURL}
	agent, err := NewAgentWithOptions(WithUrls(input))
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	require.Len(t, agent.urls, 1)
	assert.Equal(t, stunURL.String(), agent.urls[0].String())

	input[0] = nil
	require.Len(t, agent.urls, 1)
	assert.NotNil(t, agent.urls[0])
}

func TestWithPortRange(t *testing.T) {
	agent, err := NewAgentWithOptions(WithPortRange(1000, 2000))
	require.NoError(t, err)

	assert.Equal(t, uint16(1000), agent.portMin)
	assert.Equal(t, uint16(2000), agent.portMax)

	agent.Close() //nolint:gosec,errcheck
	agent, err = NewAgentWithOptions(WithPortRange(2000, 0))
	assert.NoError(t, err)
	defer agent.Close() //nolint:gosec,errcheck

	assert.Equal(t, uint16(2000), agent.portMin)
	assert.Equal(t, uint16(0), agent.portMax)
}

func TestWithTimeoutOptions(t *testing.T) {
	agent, err := NewAgentWithOptions(
		WithDisconnectedTimeout(10*time.Second),
		WithFailedTimeout(20*time.Second),
		WithKeepaliveInterval(3*time.Second),
		WithCheckInterval(150*time.Millisecond),
	)
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	assert.Equal(t, 10*time.Second, agent.disconnectedTimeout)
	assert.Equal(t, 20*time.Second, agent.failedTimeout)
	assert.Equal(t, 3*time.Second, agent.keepaliveInterval)
	assert.Equal(t, 150*time.Millisecond, agent.checkInterval)
}

func TestWithAcceptanceWaitOptions(t *testing.T) {
	agent, err := NewAgentWithOptions(
		WithHostAcceptanceMinWait(1*time.Second),
		WithSrflxAcceptanceMinWait(2*time.Second),
		WithPrflxAcceptanceMinWait(3*time.Second),
		WithRelayAcceptanceMinWait(4*time.Second),
	)
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	assert.Equal(t, 1*time.Second, agent.hostAcceptanceMinWait)
	assert.Equal(t, 2*time.Second, agent.srflxAcceptanceMinWait)
	assert.Equal(t, 3*time.Second, agent.prflxAcceptanceMinWait)
	assert.Equal(t, 4*time.Second, agent.relayAcceptanceMinWait)
}

func TestWithSTUNGatherTimeout(t *testing.T) {
	agent, err := NewAgentWithOptions(WithSTUNGatherTimeout(7 * time.Second))
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	assert.Equal(t, 7*time.Second, agent.stunGatherTimeout)
}

func TestWithIPFilterOption(t *testing.T) {
	filter := func(ip net.IP) bool {
		return ip.IsLoopback()
	}

	agent, err := NewAgentWithOptions(WithIPFilter(filter))
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	require.NotNil(t, agent.ipFilter)
	assert.True(t, agent.ipFilter(net.IPv4(127, 0, 0, 1)))
	assert.False(t, agent.ipFilter(net.IPv4(192, 0, 2, 1)))
}

func TestWithNetOption(t *testing.T) {
	stub := newStubNet(t)

	agent, err := NewAgentWithOptions(WithNet(stub))
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	assert.Equal(t, stub, agent.net)
}

func TestWithMulticastDNSOptions(t *testing.T) {
	agent, err := NewAgentWithOptions(
		WithMulticastDNSMode(MulticastDNSModeDisabled),
		WithMulticastDNSHostName("pion-test.local"),
	)
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	assert.Equal(t, MulticastDNSModeDisabled, agent.mDNSMode)
	assert.Equal(t, "pion-test.local", agent.mDNSName)

	_, err = NewAgentWithOptions(WithMulticastDNSHostName("invalid-host"))
	assert.ErrorIs(t, err, ErrInvalidMulticastDNSHostName)
}

func TestWithLocalCredentials(t *testing.T) {
	password := strings.Repeat("p", 16)

	agent, err := NewAgentWithOptions(WithLocalCredentials("abcd", password))
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	assert.Equal(t, "abcd", agent.localUfrag)
	assert.Equal(t, password, agent.localPwd)

	_, err = NewAgentWithOptions(WithLocalCredentials("ab", password))
	assert.ErrorIs(t, err, ErrLocalUfragInsufficientBits)

	shortPassword := strings.Repeat("p", 10)
	_, err = NewAgentWithOptions(WithLocalCredentials("abcd", shortPassword))
	assert.ErrorIs(t, err, ErrLocalPwdInsufficientBits)
}

func TestWithMuxOptions(t *testing.T) {
	tcpMux := &stubTCPMux{}
	udpMux := &stubUDPMux{}
	udpMuxSrflx := &stubUniversalUDPMux{}

	agent, err := NewAgentWithOptions(
		WithTCPMux(tcpMux),
		WithUDPMux(udpMux),
		WithUDPMuxSrflx(udpMuxSrflx),
	)
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	assert.Equal(t, tcpMux, agent.tcpMux)
	assert.Equal(t, udpMux, agent.udpMux)
	assert.Equal(t, udpMuxSrflx, agent.udpMuxSrflx)
}

func TestWithProxyDialer(t *testing.T) {
	agent, err := NewAgentWithOptions(WithProxyDialer(proxy.Direct))
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	assert.Equal(t, proxy.Direct, agent.proxyDialer)
}

func TestWithMaxBindingRequests(t *testing.T) {
	agent, err := NewAgentWithOptions(WithMaxBindingRequests(3))
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	assert.Equal(t, uint16(3), agent.maxBindingRequests)
}

func TestWithRenomination(t *testing.T) {
	t.Run("enables renomination with custom generator", func(t *testing.T) {
		counter := uint32(0)
		customGen := func() uint32 {
			counter++

			return counter * 10
		}

		agent, err := NewAgentWithOptions(WithRenomination(customGen))
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.True(t, agent.enableRenomination)
		assert.NotNil(t, agent.nominationValueGenerator)
		assert.Equal(t, uint32(10), agent.getNominationValue())
		assert.Equal(t, uint32(20), agent.getNominationValue())
	})

	t.Run("enables renomination with default generator", func(t *testing.T) {
		agent, err := NewAgentWithOptions(WithRenomination(DefaultNominationValueGenerator()))
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.True(t, agent.enableRenomination)
		assert.NotNil(t, agent.nominationValueGenerator)
		assert.Equal(t, uint32(1), agent.getNominationValue())
		assert.Equal(t, uint32(2), agent.getNominationValue())
	})

	t.Run("rejects nil generator", func(t *testing.T) {
		_, err := NewAgentWithOptions(WithRenomination(nil))
		assert.ErrorIs(t, err, ErrInvalidNominationValueGenerator)
	})

	t.Run("default agent has renomination disabled", func(t *testing.T) {
		config := &AgentConfig{
			NetworkTypes: []NetworkType{NetworkTypeUDP4},
		}

		agent, err := NewAgent(config)
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.False(t, agent.enableRenomination)
		assert.Nil(t, agent.nominationValueGenerator)
		assert.Equal(t, uint32(0), agent.getNominationValue())
	})
}

func TestWithNominationAttribute(t *testing.T) {
	t.Run("sets custom nomination attribute", func(t *testing.T) {
		agent, err := NewAgentWithOptions(WithNominationAttribute(0x0045))
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.Equal(t, stun.AttrType(0x0045), agent.nominationAttribute)
	})

	t.Run("rejects invalid attribute 0x0000", func(t *testing.T) {
		_, err := NewAgentWithOptions(WithNominationAttribute(0x0000))
		assert.ErrorIs(t, err, ErrInvalidNominationAttribute)
	})

	t.Run("default value when no option", func(t *testing.T) {
		config := &AgentConfig{
			NetworkTypes: []NetworkType{NetworkTypeUDP4},
		}

		agent, err := NewAgent(config)
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		// Should use default value 0x0030
		assert.Equal(t, stun.AttrType(0x0030), agent.nominationAttribute)
	})
}

func TestWithIncludeLoopback(t *testing.T) {
	testBooleanOption(t, booleanOptionTest{
		optionFunc:   WithIncludeLoopback,
		getValue:     func(a *Agent) bool { return a.includeLoopback },
		configSetter: func(c *AgentConfig, v bool) { c.IncludeLoopback = v },
	}, "loopback addresses")
}

func TestWithTCPPriorityOffset(t *testing.T) {
	t.Run("sets custom TCP priority offset", func(t *testing.T) {
		customOffset := uint16(50)
		agent, err := NewAgentWithOptions(WithTCPPriorityOffset(customOffset))
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.Equal(t, customOffset, agent.tcpPriorityOffset)
	})

	t.Run("default is 27", func(t *testing.T) {
		agent, err := NewAgentWithOptions()
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.Equal(t, uint16(27), agent.tcpPriorityOffset)
	})

	t.Run("works with config", func(t *testing.T) {
		customOffset := uint16(100)
		config := &AgentConfig{
			NetworkTypes:      []NetworkType{NetworkTypeUDP4},
			TCPPriorityOffset: &customOffset,
		}

		agent, err := NewAgent(config)
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.Equal(t, customOffset, agent.tcpPriorityOffset)
	})
}

func TestWithDisableActiveTCP(t *testing.T) {
	testBooleanOption(t, booleanOptionTest{
		optionFunc:   WithDisableActiveTCP,
		getValue:     func(a *Agent) bool { return a.disableActiveTCP },
		configSetter: func(c *AgentConfig, v bool) { c.DisableActiveTCP = v },
	}, "active TCP disabling")
}

func TestWithBindingRequestHandler(t *testing.T) {
	t.Run("sets binding request handler", func(t *testing.T) {
		handlerCalled := false
		handler := func(_ *stun.Message, _, _ Candidate, _ *CandidatePair) bool {
			handlerCalled = true

			return true
		}

		agent, err := NewAgentWithOptions(WithBindingRequestHandler(handler))
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.NotNil(t, agent.userBindingRequestHandler)

		// Test that the handler is actually the one we set
		// We can't directly compare functions, but we can call it
		if agent.userBindingRequestHandler != nil {
			agent.userBindingRequestHandler(nil, nil, nil, nil)
			assert.True(t, handlerCalled)
		}
	})

	t.Run("default is nil", func(t *testing.T) {
		agent, err := NewAgentWithOptions()
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.Nil(t, agent.userBindingRequestHandler)
	})

	t.Run("works with config", func(t *testing.T) {
		handlerCalled := false
		handler := func(_ *stun.Message, _, _ Candidate, _ *CandidatePair) bool {
			handlerCalled = true

			return true
		}

		config := &AgentConfig{
			NetworkTypes:          []NetworkType{NetworkTypeUDP4},
			BindingRequestHandler: handler,
		}

		agent, err := NewAgent(config)
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.NotNil(t, agent.userBindingRequestHandler)

		if agent.userBindingRequestHandler != nil {
			agent.userBindingRequestHandler(nil, nil, nil, nil)
			assert.True(t, handlerCalled)
		}
	})
}

func TestWithEnableUseCandidateCheckPriority(t *testing.T) {
	testBooleanOption(t, booleanOptionTest{
		optionFunc:   WithEnableUseCandidateCheckPriority,
		getValue:     func(a *Agent) bool { return a.enableUseCandidateCheckPriority },
		configSetter: func(c *AgentConfig, v bool) { c.EnableUseCandidateCheckPriority = v },
	}, "use candidate check priority")
}

func TestMultipleConfigOptions(t *testing.T) {
	t.Run("can apply multiple options", func(t *testing.T) {
		customOffset := uint16(100)
		handlerCalled := false
		handler := func(_ *stun.Message, _, _ Candidate, _ *CandidatePair) bool {
			handlerCalled = true

			return true
		}

		agent, err := NewAgentWithOptions(
			WithIncludeLoopback(),
			WithTCPPriorityOffset(customOffset),
			WithDisableActiveTCP(),
			WithBindingRequestHandler(handler),
			WithEnableUseCandidateCheckPriority(),
		)
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.True(t, agent.includeLoopback)
		assert.Equal(t, customOffset, agent.tcpPriorityOffset)
		assert.True(t, agent.disableActiveTCP)
		assert.NotNil(t, agent.userBindingRequestHandler)
		assert.True(t, agent.enableUseCandidateCheckPriority)

		if agent.userBindingRequestHandler != nil {
			agent.userBindingRequestHandler(nil, nil, nil, nil)
			assert.True(t, handlerCalled)
		}
	})
}

func TestWithInterfaceFilter(t *testing.T) {
	t.Run("sets interface filter", func(t *testing.T) {
		filter := func(interfaceName string) bool {
			return interfaceName == "eth0"
		}

		agent, err := NewAgentWithOptions(WithInterfaceFilter(filter))
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.NotNil(t, agent.interfaceFilter)
		assert.True(t, agent.interfaceFilter("eth0"))
		assert.False(t, agent.interfaceFilter("wlan0"))
	})

	t.Run("default is nil", func(t *testing.T) {
		agent, err := NewAgentWithOptions()
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.Nil(t, agent.interfaceFilter)
	})

	t.Run("works with config", func(t *testing.T) {
		filter := func(interfaceName string) bool {
			return interfaceName == "lo"
		}

		config := &AgentConfig{
			NetworkTypes:    []NetworkType{NetworkTypeUDP4},
			InterfaceFilter: filter,
		}

		agent, err := NewAgent(config)
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.NotNil(t, agent.interfaceFilter)
		assert.True(t, agent.interfaceFilter("lo"))
		assert.False(t, agent.interfaceFilter("eth0"))
	})
}

func TestWithLoggerFactory(t *testing.T) {
	t.Run("sets logger factory", func(t *testing.T) {
		loggerFactory := logging.NewDefaultLoggerFactory()
		loggerFactory.DefaultLogLevel = logging.LogLevelDebug

		agent, err := NewAgentWithOptions(WithLoggerFactory(loggerFactory))
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.NotNil(t, agent.log)
	})

	t.Run("default uses default logger", func(t *testing.T) {
		agent, err := NewAgentWithOptions()
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.NotNil(t, agent.log)
	})

	t.Run("works with config", func(t *testing.T) {
		loggerFactory := logging.NewDefaultLoggerFactory()
		config := &AgentConfig{
			NetworkTypes:  []NetworkType{NetworkTypeUDP4},
			LoggerFactory: loggerFactory,
		}

		agent, err := NewAgent(config)
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.NotNil(t, agent.log)
	})
}

func TestWithNetworkTypesAppliedBeforeRestart(t *testing.T) {
	t.Run("ipv6 listen skipped when network types option restricts to ipv4", func(t *testing.T) {
		stub := newStubNet(t)

		agent, err := newAgentFromConfig(&AgentConfig{
			Net: stub,
		}, WithNetworkTypes([]NetworkType{NetworkTypeUDP4}))
		require.NoError(t, err)
		defer func() { require.NoError(t, agent.Close()) }()

		assert.Zero(t, stub.udp6ListenCount, "unexpected ipv6 listen before restart")
	})
}

func TestWithCandidateTypesAffectsURLValidation(t *testing.T) {
	stunURL, err := stun.ParseURI("stun:example.com:3478")
	require.NoError(t, err)

	t.Run("default candidate types accept urls", func(t *testing.T) {
		stub := newStubNet(t)

		agent, err := newAgentFromConfig(&AgentConfig{
			Urls: []*stun.URI{stunURL},
			Net:  stub,
		})
		require.NoError(t, err)
		require.NoError(t, agent.Close())
	})

	t.Run("host only candidate types reject urls", func(t *testing.T) {
		stub := newStubNet(t)

		_, err := newAgentFromConfig(&AgentConfig{
			Urls: []*stun.URI{stunURL},
			Net:  stub,
		}, WithCandidateTypes([]CandidateType{CandidateTypeHost}))
		require.ErrorIs(t, err, ErrUselessUrlsProvided)
	})
}

func TestWithCandidateTypesNAT1To1Validation(t *testing.T) {
	t.Run("host mapping requires host candidates", func(t *testing.T) {
		stub := newStubNet(t)

		_, err := newAgentFromConfig(&AgentConfig{
			NAT1To1IPs:             []string{"1.2.3.4"},
			NAT1To1IPCandidateType: CandidateTypeHost,
			Net:                    stub,
		}, WithCandidateTypes([]CandidateType{CandidateTypeRelay}))
		require.ErrorIs(t, err, ErrIneffectiveNAT1To1IPMappingHost)
	})

	t.Run("srflx mapping requires srflx candidates", func(t *testing.T) {
		stub := newStubNet(t)

		_, err := newAgentFromConfig(&AgentConfig{
			NAT1To1IPs:             []string{"1.2.3.4"},
			NAT1To1IPCandidateType: CandidateTypeServerReflexive,
			Net:                    stub,
		}, WithCandidateTypes([]CandidateType{CandidateTypeHost}))
		require.ErrorIs(t, err, ErrIneffectiveNAT1To1IPMappingSrflx)
	})
}

func TestWith1To1CandidateIPOptions(t *testing.T) {
	testCases := []struct {
		name             string
		rules            []AddressRewriteRule
		candidateType    CandidateType
		expectedFirstIP  string
		expectedSecondIP string
		lookupLocalIP    string
	}{
		{
			name: "host candidates",
			rules: []AddressRewriteRule{
				{
					External:        []string{"1.2.3.4"},
					AsCandidateType: CandidateTypeHost,
				},
				{
					External:        []string{"5.6.7.8"},
					AsCandidateType: CandidateTypeHost,
				},
			},
			candidateType:    CandidateTypeHost,
			expectedFirstIP:  "1.2.3.4",
			expectedSecondIP: "5.6.7.8",
			lookupLocalIP:    "10.0.0.1",
		},
		{
			name: "srflx candidates",
			rules: []AddressRewriteRule{
				{
					External:        []string{"5.6.7.8"},
					AsCandidateType: CandidateTypeServerReflexive,
				},
				{
					External:        []string{"9.9.9.9"},
					AsCandidateType: CandidateTypeServerReflexive,
				},
			},
			candidateType:    CandidateTypeServerReflexive,
			expectedFirstIP:  "5.6.7.8",
			expectedSecondIP: "9.9.9.9",
			lookupLocalIP:    "0.0.0.0",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			assertAddressRewriteOption(
				t,
				tc.rules,
				tc.candidateType,
				tc.expectedFirstIP,
				tc.expectedSecondIP,
				tc.lookupLocalIP,
			)
		})
	}
}

func assertAddressRewriteOption(
	t *testing.T,
	rules []AddressRewriteRule,
	candidateType CandidateType,
	expectedFirstIP string,
	expectedSecondIP string,
	lookupLocalIP string,
) {
	t.Helper()

	stub := newStubNet(t)

	agent, err := NewAgentWithOptions(
		WithNet(stub),
		WithAddressRewriteRules(rules...),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, agent.Close())
	})

	require.Len(t, agent.addressRewriteRules, len(rules))

	firstRule := agent.addressRewriteRules[0]
	require.Equal(t, candidateType, firstRule.AsCandidateType)
	require.Equal(t, []string{expectedFirstIP}, firstRule.External)

	secondRule := agent.addressRewriteRules[1]
	require.Equal(t, candidateType, secondRule.AsCandidateType)
	require.Equal(t, []string{expectedSecondIP}, secondRule.External)

	require.NotNil(t, agent.addressRewriteMapper)
	extIP := requireFirstExternalIP(t, agent.addressRewriteMapper, candidateType, lookupLocalIP)
	require.Equal(t, expectedFirstIP, extIP.String())
}

func requireFirstExternalIP(
	t *testing.T,
	mapper *addressRewriteMapper,
	candidateType CandidateType,
	localIP string,
) net.IP {
	t.Helper()

	ips, matched, _, err := mapper.findExternalIPs(candidateType, localIP, "")
	require.NoError(t, err)
	require.True(t, matched)
	require.NotEmpty(t, ips)

	return ips[0]
}

func requireFirstMappingIP(t *testing.T, mapping *ipMapping, localIP net.IP) net.IP {
	t.Helper()

	ips := mapping.findExternalIPs(localIP)
	require.NotEmpty(t, ips)

	return ips[0]
}

func TestWith1To1RulesOption(t *testing.T) {
	stub := newStubNet(t)
	originalRules := []AddressRewriteRule{
		{
			External:        []string{"9.9.9.9"},
			AsCandidateType: CandidateTypeHost,
		},
	}

	// With append semantics the option stacks, so call twice and ensure accumulation.
	agent, err := NewAgentWithOptions(
		WithNet(stub),
		WithAddressRewriteRules(originalRules...),
		WithAddressRewriteRules(AddressRewriteRule{
			External:        []string{"4.4.4.4"},
			AsCandidateType: CandidateTypeServerReflexive,
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, agent.Close())
	})

	require.Len(t, agent.addressRewriteRules, 2)
	require.Equal(t, []string{"9.9.9.9"}, agent.addressRewriteRules[0].External)
	require.Equal(t, []string{"4.4.4.4"}, agent.addressRewriteRules[1].External)

	// mutate the original rules after option applied, agent copy should remain unchanged.
	originalRules[0].External[0] = "0.0.0.0"
	require.Equal(t, "9.9.9.9", agent.addressRewriteRules[0].External[0])
}

func TestWith1To1RulesEmptyNoop(t *testing.T) {
	stub := newStubNet(t)

	agent, err := NewAgentWithOptions(
		WithNet(stub),
		WithAddressRewriteRules(AddressRewriteRule{
			External:        []string{"1.2.3.4"},
			AsCandidateType: CandidateTypeHost,
		}),
		WithAddressRewriteRules(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, agent.Close())
	})

	require.Len(t, agent.addressRewriteRules, 1)
	require.Equal(t, []string{"1.2.3.4"}, agent.addressRewriteRules[0].External)
	require.Equal(t, CandidateTypeHost, agent.addressRewriteRules[0].AsCandidateType)
	require.NotNil(t, agent.addressRewriteMapper)
}

func TestWithAddressRewriteRulesWarnOnConflicts(t *testing.T) {
	stub := newStubNet(t)
	logger := &recordingLogger{}
	factory := &recordingLoggerFactory{logger: logger}

	agent, err := NewAgentWithOptions(
		WithNet(stub),
		WithLoggerFactory(factory),
		WithAddressRewriteRules(AddressRewriteRule{
			External:        []string{"203.0.113.10"},
			AsCandidateType: CandidateTypeHost,
		}),
		WithAddressRewriteRules(AddressRewriteRule{
			External:        []string{"198.51.100.50"},
			AsCandidateType: CandidateTypeHost,
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, agent.Close())
	})

	require.Len(t, logger.warnings, 1)
	require.Contains(t, logger.warnings[0], "overlapping address rewrite rule")
	require.Contains(t, logger.warnings[0], "candidate=host")
	require.Contains(t, logger.warnings[0], "iface=*")
	require.Contains(t, logger.warnings[0], "networks=*")
	require.Contains(t, logger.warnings[0], "local=family:ipv4")
	require.Contains(t, logger.warnings[0], "203.0.113.10")
	require.Contains(t, logger.warnings[0], "198.51.100.50")

	ips, matched, mode, err := agent.addressRewriteMapper.findExternalIPs(CandidateTypeHost, "10.0.0.1", "")
	require.NoError(t, err)
	require.True(t, matched)
	require.Equal(t, AddressRewriteReplace, mode)
	require.NotEmpty(t, ips)
	require.Equal(t, "203.0.113.10", ips[0].String())
}

func TestWithAddressRewriteRulesConflictingModesWarningAndPrecedence(t *testing.T) {
	stub := newStubNet(t)
	logger := &recordingLogger{}
	factory := &recordingLoggerFactory{logger: logger}

	agent, err := NewAgentWithOptions(
		WithNet(stub),
		WithLoggerFactory(factory),
		WithAddressRewriteRules(
			AddressRewriteRule{
				External:        []string{"203.0.113.10"},
				AsCandidateType: CandidateTypeHost,
				Mode:            AddressRewriteReplace,
			},
			AddressRewriteRule{
				External:        []string{"198.51.100.50"},
				AsCandidateType: CandidateTypeHost,
				Mode:            AddressRewriteAppend,
			},
		),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, agent.Close())
	})

	require.NotEmpty(t, logger.warnings)
	require.Contains(t, logger.warnings[0], "overlapping address rewrite rule")

	ips, matched, mode, err := agent.addressRewriteMapper.findExternalIPs(CandidateTypeHost, "10.0.0.1", "")
	require.NoError(t, err)
	require.True(t, matched)
	require.NotEmpty(t, ips)
	require.Equal(t, "203.0.113.10", ips[0].String())
	require.Equal(t, AddressRewriteReplace, mode)
}

func TestWithAddressRewriteRulesNoFalsePositiveConflicts(t *testing.T) {
	stub := newStubNet(t)
	logger := &recordingLogger{}
	factory := &recordingLoggerFactory{logger: logger}

	agent, err := NewAgentWithOptions(
		WithNet(stub),
		WithLoggerFactory(factory),
		WithAddressRewriteRules(
			AddressRewriteRule{
				External:        []string{"203.0.113.10"},
				AsCandidateType: CandidateTypeHost,
				Networks:        []NetworkType{NetworkTypeUDP4},
			},
			AddressRewriteRule{
				External:        []string{"2001:db8::10"},
				AsCandidateType: CandidateTypeHost,
				Networks:        []NetworkType{NetworkTypeUDP6},
			},
		),
		WithAddressRewriteRules(
			AddressRewriteRule{
				External:        []string{"198.51.100.10"},
				AsCandidateType: CandidateTypeServerReflexive,
			},
		),
	)
	assert.NoError(t, err)
	if agent != nil {
		t.Cleanup(func() {
			assert.NoError(t, agent.Close())
		})
	}

	assert.Empty(t, logger.warnings)
}

func TestLegacyAndNewAddressRewriteOrdering(t *testing.T) {
	stub := newStubNet(t)

	agent, err := newAgentFromConfig(
		&AgentConfig{
			Net:        stub,
			NAT1To1IPs: []string{"203.0.113.10"},
		},
		WithAddressRewriteRules(
			AddressRewriteRule{
				External:        []string{"198.51.100.5"},
				AsCandidateType: CandidateTypeHost,
			},
		),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, agent.Close())
	})

	require.Len(t, agent.addressRewriteRules, 2)
	extIP := requireFirstExternalIP(t, agent.addressRewriteMapper, CandidateTypeHost, "10.0.0.1")
	require.Equal(t, "203.0.113.10", extIP.String())
}

func TestLegacyNAT1To1TranslationOrder(t *testing.T) {
	stub := newStubNet(t)

	agent, err := NewAgent(&AgentConfig{
		Net: stub,
		NAT1To1IPs: []string{
			"203.0.113.1/10.0.0.1",
			"203.0.113.2",
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, agent.Close())
	})

	require.Len(t, agent.addressRewriteRules, 2)

	firstRule := agent.addressRewriteRules[0]
	require.Equal(t, "203.0.113.1", firstRule.External[0])
	require.Equal(t, "10.0.0.1", firstRule.Local)

	secondRule := agent.addressRewriteRules[1]
	require.Equal(t, "203.0.113.2", secondRule.External[0])
	require.Empty(t, secondRule.Local)

	extIP := requireFirstExternalIP(t, agent.addressRewriteMapper, CandidateTypeHost, "10.0.0.2")
	require.Equal(t, "203.0.113.2", extIP.String())
}

func TestLegacyAddressRewriteParityWithRules(t *testing.T) {
	t.Run("host candidate parity", func(t *testing.T) {
		legacyStub := newStubNet(t)
		legacyAgent, err := NewAgent(&AgentConfig{
			Net: legacyStub,
			NAT1To1IPs: []string{
				"203.0.113.10",
				"198.51.100.20/10.0.0.20",
			},
		})
		assert.NoError(t, err)
		if legacyAgent != nil {
			t.Cleanup(func() {
				assert.NoError(t, legacyAgent.Close())
			})
		}

		modernStub := newStubNet(t)
		modernAgent, err := NewAgentWithOptions(
			WithNet(modernStub),
			WithAddressRewriteRules(
				AddressRewriteRule{
					External:        []string{"203.0.113.10"},
					AsCandidateType: CandidateTypeHost,
				},
			),
			WithAddressRewriteRules(
				AddressRewriteRule{
					External:        []string{"198.51.100.20"},
					Local:           "10.0.0.20",
					AsCandidateType: CandidateTypeHost,
				},
			),
		)
		assert.NoError(t, err)
		if modernAgent != nil {
			t.Cleanup(func() {
				assert.NoError(t, modernAgent.Close())
			})
		}

		for _, loc := range []string{"10.0.0.20", "10.0.0.21"} {
			legacyIPs, legacyMatched, _, legacyErr := legacyAgent.addressRewriteMapper.findExternalIPs(
				CandidateTypeHost, loc, "",
			)
			assert.NoError(t, legacyErr)
			assert.True(t, legacyMatched)
			assert.NotEmpty(t, legacyIPs)

			modernIPs, modernMatched, _, modernErr := modernAgent.addressRewriteMapper.findExternalIPs(
				CandidateTypeHost, loc, "",
			)
			assert.NoError(t, modernErr)
			assert.True(t, modernMatched)
			assert.NotEmpty(t, modernIPs)

			assert.Equal(t, legacyIPs[0].String(), modernIPs[0].String())
		}
	})

	t.Run("srflx candidate parity", func(t *testing.T) {
		legacyStub := newStubNet(t)
		legacyAgent, err := NewAgent(&AgentConfig{
			Net:                    legacyStub,
			NAT1To1IPs:             []string{"198.51.100.77"},
			NAT1To1IPCandidateType: CandidateTypeServerReflexive,
		})
		assert.NoError(t, err)
		if legacyAgent != nil {
			t.Cleanup(func() {
				assert.NoError(t, legacyAgent.Close())
			})
		}

		modernStub := newStubNet(t)
		modernAgent, err := NewAgentWithOptions(
			WithNet(modernStub),
			WithAddressRewriteRules(
				AddressRewriteRule{
					External:        []string{"198.51.100.77"},
					AsCandidateType: CandidateTypeServerReflexive,
				},
			),
		)
		assert.NoError(t, err)
		if modernAgent != nil {
			t.Cleanup(func() {
				assert.NoError(t, modernAgent.Close())
			})
		}

		legacyIPs, legacyMatched, _, legacyErr := legacyAgent.addressRewriteMapper.findExternalIPs(
			CandidateTypeServerReflexive,
			"0.0.0.0",
			"",
		)
		assert.NoError(t, legacyErr)
		assert.True(t, legacyMatched)
		assert.NotEmpty(t, legacyIPs)

		modernIPs, modernMatched, _, modernErr := modernAgent.addressRewriteMapper.findExternalIPs(
			CandidateTypeServerReflexive,
			"0.0.0.0",
			"",
		)
		assert.NoError(t, modernErr)
		assert.True(t, modernMatched)
		assert.NotEmpty(t, modernIPs)

		assert.Equal(t, legacyIPs[0].String(), modernIPs[0].String())
	})
}

func TestOverlapWarningPerCandidateType(t *testing.T) {
	stub := newStubNet(t)
	logger := &recordingLogger{}
	factory := &recordingLoggerFactory{logger: logger}

	agent, err := NewAgentWithOptions(
		WithNet(stub),
		WithLoggerFactory(factory),
		WithAddressRewriteRules(
			AddressRewriteRule{
				External:        []string{"203.0.113.10"},
				AsCandidateType: CandidateTypeHost,
			},
		),
		WithAddressRewriteRules(
			AddressRewriteRule{
				External:        []string{"198.51.100.10"},
				AsCandidateType: CandidateTypeServerReflexive,
			},
			AddressRewriteRule{
				External:        []string{"198.51.100.20"},
				AsCandidateType: CandidateTypeServerReflexive,
			},
		),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, agent.Close())
	})

	require.Len(t, logger.warnings, 1)
	require.Contains(t, logger.warnings[0], "candidate=srflx")
}

func TestWithNAT1To1IPValidation(t *testing.T) {
	t.Run("dedupe and trim host IPs", func(t *testing.T) {
		stub := newStubNet(t)

		agent, err := NewAgentWithOptions(
			WithNet(stub),
			WithAddressRewriteRules(AddressRewriteRule{
				External:        []string{" 203.0.113.1 ", "203.0.113.1", "203.0.113.2 "},
				AsCandidateType: CandidateTypeHost,
			}),
		)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})

		require.Len(t, agent.addressRewriteRules, 1)
		require.Equal(t, []string{"203.0.113.1", "203.0.113.2"}, agent.addressRewriteRules[0].External)
	})

	t.Run("reject hostname entry", func(t *testing.T) {
		stub := newStubNet(t)

		agent, err := NewAgentWithOptions(
			WithNet(stub),
			WithAddressRewriteRules(AddressRewriteRule{
				External:        []string{"example.com"},
				AsCandidateType: CandidateTypeHost,
			}),
		)
		require.Nil(t, agent)
		require.ErrorIs(t, err, ErrInvalidNAT1To1IPMapping)
	})

	t.Run("reject slash mapping in address rewrite rules", func(t *testing.T) {
		stub := newStubNet(t)

		agent, err := NewAgentWithOptions(
			WithNet(stub),
			WithAddressRewriteRules(AddressRewriteRule{
				External:        []string{"203.0.113.1/10.0.0.1"},
				AsCandidateType: CandidateTypeHost,
			}),
		)
		require.Nil(t, agent)
		require.ErrorIs(t, err, ErrInvalidNAT1To1IPMapping)
	})

	t.Run("reject invalid rule entry", func(t *testing.T) {
		stub := newStubNet(t)

		agent, err := NewAgentWithOptions(
			WithNet(stub),
			WithAddressRewriteRules(AddressRewriteRule{
				External:        []string{"1.2.3.4", "bad-ip"},
				AsCandidateType: CandidateTypeHost,
			}),
		)
		require.Nil(t, agent)
		require.ErrorIs(t, err, ErrInvalidNAT1To1IPMapping)
	})
}

func TestWithAddressRewriteRulesIPv6(t *testing.T) {
	stub := newStubNet(t)

	agent, err := NewAgentWithOptions(
		WithNet(stub),
		WithAddressRewriteRules(AddressRewriteRule{
			External:        []string{"2001:db8::2"},
			Local:           "2001:db8:1::2",
			AsCandidateType: CandidateTypeHost,
			Networks:        []NetworkType{NetworkTypeUDP6},
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, agent.Close())
	})

	require.Len(t, agent.addressRewriteRules, 1)
	require.Equal(t, []NetworkType{NetworkTypeUDP6}, agent.addressRewriteRules[0].Networks)
	require.NotNil(t, agent.addressRewriteMapper)

	mappings := agent.addressRewriteMapper.rulesByCandidateType[CandidateTypeHost]
	require.Len(t, mappings, 1)
	require.True(t, mappings[0].ipv6Mapping.valid)
	_, ok := mappings[0].ipv6Mapping.ipMap["2001:db8:1::2"]
	require.True(t, ok)
	for key := range mappings[0].ipv6Mapping.ipMap {
		t.Logf("stored ipv6 mapping key: %q", key)
	}
	locIP := net.ParseIP("2001:db8:1::2")
	require.NotNil(t, locIP)
	t.Logf("parsed ipv6 string: %q", locIP.String())
	directExt := requireFirstMappingIP(t, &mappings[0].ipv6Mapping, locIP)
	require.Equal(t, "2001:db8::2", directExt.String())

	mapper, err := newAddressRewriteMapper(agent.addressRewriteRules)
	require.NoError(t, err)
	extIP := requireFirstExternalIP(t, mapper, CandidateTypeHost, "2001:db8:1::2")
	require.Equal(t, "2001:db8::2", extIP.String())

	_, matched, _, err := mapper.findExternalIPs(CandidateTypeHost, "2001:db8:1::3", "")
	require.NoError(t, err)
	require.False(t, matched)
}

func TestAddressRewriteRulesRejectWithMDNSQueryAndGather(t *testing.T) {
	agent := &Agent{
		candidateTypes: []CandidateType{CandidateTypeHost},
		mDNSMode:       MulticastDNSModeQueryAndGather,
		addressRewriteRules: []AddressRewriteRule{
			{
				External:        []string{"203.0.113.200"},
				AsCandidateType: CandidateTypeHost,
			},
		},
		log: logging.NewDefaultLoggerFactory().NewLogger("test"),
	}

	err := applyAddressRewriteMapping(agent)
	assert.ErrorIs(t, err, ErrMulticastDNSWithNAT1To1IPMapping)
}

func TestAgentAddressRewriteModeIntegration(t *testing.T) {
	stub := newStubNet(t)

	t.Run("defaults host replace srflx append", func(t *testing.T) {
		agent, err := NewAgentWithOptions(
			WithNet(stub),
			WithAddressRewriteRules(
				AddressRewriteRule{
					External:        []string{"203.0.113.10"},
					AsCandidateType: CandidateTypeHost,
				},
				AddressRewriteRule{
					External:        []string{"198.51.100.10"},
					Local:           "0.0.0.0",
					AsCandidateType: CandidateTypeServerReflexive,
				},
			),
		)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})

		require.Len(t, agent.addressRewriteRules, 2)
		assert.Equal(t, AddressRewriteReplace, agent.addressRewriteRules[0].Mode)
		assert.Equal(t, AddressRewriteAppend, agent.addressRewriteRules[1].Mode)
	})

	t.Run("host append honored", func(t *testing.T) {
		agent, err := NewAgentWithOptions(
			WithNet(stub),
			WithAddressRewriteRules(
				AddressRewriteRule{
					External:        []string{"203.0.113.20"},
					Local:           "10.0.0.5",
					AsCandidateType: CandidateTypeHost,
					Mode:            AddressRewriteAppend,
				},
			),
		)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})

		require.Len(t, agent.addressRewriteRules, 1)
		assert.Equal(t, AddressRewriteAppend, agent.addressRewriteRules[0].Mode)
	})

	t.Run("srflx replace honored", func(t *testing.T) {
		agent, err := NewAgentWithOptions(
			WithNet(stub),
			WithAddressRewriteRules(
				AddressRewriteRule{
					External:        []string{"198.51.100.50"},
					Local:           "0.0.0.0",
					AsCandidateType: CandidateTypeServerReflexive,
					Mode:            AddressRewriteReplace,
				},
			),
		)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})

		require.Len(t, agent.addressRewriteRules, 1)
		assert.Equal(t, AddressRewriteReplace, agent.addressRewriteRules[0].Mode)
	})
}

func TestAddressRewriteModeOverrides(t *testing.T) {
	t.Run("host append preserves local candidate", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"203.0.113.99"},
				Local:           "10.0.0.99",
				AsCandidateType: CandidateTypeHost,
				Mode:            AddressRewriteAppend,
			},
		})
		assert.NoError(t, err)

		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logging.NewDefaultLoggerFactory().NewLogger("test"),
		}

		local := netip.MustParseAddr("10.0.0.99")
		mapped, ok := agent.applyHostAddressRewrite(local, []netip.Addr{local}, "")
		assert.True(t, ok)
		assert.Equal(t, []netip.Addr{
			local,
			netip.MustParseAddr("203.0.113.99"),
		}, mapped)
	})

	t.Run("srflx replace overrides default append", func(t *testing.T) {
		mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
			{
				External:        []string{"198.51.100.99"},
				Local:           "0.0.0.0",
				AsCandidateType: CandidateTypeServerReflexive,
				Mode:            AddressRewriteReplace,
			},
		})
		assert.NoError(t, err)

		agent := &Agent{
			addressRewriteMapper: mapper,
			log:                  logging.NewDefaultLoggerFactory().NewLogger("test"),
		}

		assert.True(t, agent.addressRewriteMapper.shouldReplace(CandidateTypeServerReflexive))

		ips, matched, mode, err := agent.addressRewriteMapper.findExternalIPs(
			CandidateTypeServerReflexive,
			"0.0.0.0",
			"",
		)
		assert.NoError(t, err)
		assert.True(t, matched)
		assert.Equal(t, AddressRewriteReplace, mode)
		assert.Equal(t, "198.51.100.99", ips[0].String())
	})
}

func TestAddressRewriteMixedFamilyApplication(t *testing.T) {
	logger := logging.NewDefaultLoggerFactory().NewLogger("test")

	mapper, err := newAddressRewriteMapper([]AddressRewriteRule{
		{
			External:        []string{"203.0.113.123"},
			Local:           "2001:db8::123",
			AsCandidateType: CandidateTypeHost,
			Mode:            AddressRewriteReplace,
		},
	})
	assert.NoError(t, err)

	agent := &Agent{
		addressRewriteMapper: mapper,
		log:                  logger,
	}

	local := netip.MustParseAddr("2001:db8::123")
	mapped, ok := agent.applyHostAddressRewrite(local, []netip.Addr{local}, "")
	assert.True(t, ok)
	assert.Len(t, mapped, 1)
	assert.Equal(t, netip.MustParseAddr("203.0.113.123"), mapped[0])
}

type recordingLogger struct {
	warnings []string
}

func (l *recordingLogger) Trace(string) {}

func (l *recordingLogger) Tracef(string, ...any) {}

func (l *recordingLogger) Debug(string) {}

func (l *recordingLogger) Debugf(string, ...any) {}

func (l *recordingLogger) Info(string) {}

func (l *recordingLogger) Infof(string, ...any) {}

func (l *recordingLogger) Warn(msg string) {
	l.warnings = append(l.warnings, msg)
}

func (l *recordingLogger) Warnf(format string, args ...any) {
	l.warnings = append(l.warnings, fmt.Sprintf(format, args...))
}

func (l *recordingLogger) Error(string) {}

func (l *recordingLogger) Errorf(string, ...any) {}

type recordingLoggerFactory struct {
	logger *recordingLogger
}

func (f *recordingLoggerFactory) NewLogger(string) logging.LeveledLogger {
	return f.logger
}

func TestAgentConfigNAT1To1IPs(t *testing.T) {
	testCases := []struct {
		name          string
		config        AgentConfig
		candidateType CandidateType
		localIP       string
		expectedIP    string
	}{
		{
			name: "host candidate default type",
			config: AgentConfig{
				NAT1To1IPs: []string{"1.2.3.4"},
			},
			candidateType: CandidateTypeHost,
			localIP:       "10.0.0.1",
			expectedIP:    "1.2.3.4",
		},
		{
			name: "srflx candidate explicit type",
			config: AgentConfig{
				NAT1To1IPs:             []string{"5.6.7.8"},
				NAT1To1IPCandidateType: CandidateTypeServerReflexive,
			},
			candidateType: CandidateTypeServerReflexive,
			localIP:       "0.0.0.0",
			expectedIP:    "5.6.7.8",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			stub := newStubNet(t)
			config := tc.config
			config.Net = stub

			agent, err := NewAgent(&config)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, agent.Close())
			})

			require.NotNil(t, agent.addressRewriteMapper)
			extIP := requireFirstExternalIP(t, agent.addressRewriteMapper, tc.candidateType, tc.localIP)
			require.Equal(t, tc.expectedIP, extIP.String())
		})
	}

	t.Run("deprecated multiple config IPs reject", func(t *testing.T) {
		stub := newStubNet(t)

		//nolint:godox
		// TODO: remove once AgentConfig.NAT1To1IPs is deprecated.
		agent, err := NewAgent(&AgentConfig{
			Net:        stub,
			NAT1To1IPs: []string{"1.2.3.4", "5.6.7.8"},
		})
		require.ErrorIs(t, err, ErrInvalidNAT1To1IPMapping)
		require.Nil(t, agent)
	})

	t.Run("legacy config allows slash pair syntax", func(t *testing.T) {
		stub := newStubNet(t)

		agent, err := NewAgent(&AgentConfig{
			Net:        stub,
			NAT1To1IPs: []string{"203.0.113.20/10.0.0.20"},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, agent.Close())
		})

		extIP := requireFirstExternalIP(t, agent.addressRewriteMapper, CandidateTypeHost, "10.0.0.20")
		require.Equal(t, "203.0.113.20", extIP.String())
	})
}

var errStubNotImplemented = errors.New("stub not implemented")

type stubTCPMux struct{}

func (m *stubTCPMux) Close() error {
	return nil
}

func (m *stubTCPMux) GetConnByUfrag(string, bool, net.IP) (net.PacketConn, error) {
	return nil, errStubNotImplemented
}

func (m *stubTCPMux) RemoveConnByUfrag(string) {}

type stubUDPMux struct{}

func (m *stubUDPMux) Close() error {
	return nil
}

func (m *stubUDPMux) GetConn(string, net.Addr) (net.PacketConn, error) {
	return nil, errStubNotImplemented
}

func (m *stubUDPMux) RemoveConnByUfrag(string) {}

func (m *stubUDPMux) GetListenAddresses() []net.Addr {
	return nil
}

type stubUniversalUDPMux struct {
	stubUDPMux
}

func (m *stubUniversalUDPMux) GetXORMappedAddr(net.Addr, time.Duration) (*stun.XORMappedAddress, error) {
	return nil, errStubNotImplemented
}

func (m *stubUniversalUDPMux) GetRelayedAddr(net.Addr, time.Duration) (*net.Addr, error) {
	return nil, errStubNotImplemented
}

func (m *stubUniversalUDPMux) GetConnForURL(string, string, net.Addr) (net.PacketConn, error) {
	return nil, errStubNotImplemented
}

type stubNet struct {
	t               *testing.T
	udp6ListenCount int
}

func newStubNet(t *testing.T) *stubNet {
	t.Helper()

	return &stubNet{t: t}
}

func (n *stubNet) ListenPacket(network, address string) (net.PacketConn, error) {
	return nil, transport.ErrNotSupported
}

func (n *stubNet) ListenUDP(network string, locAddr *net.UDPAddr) (transport.UDPConn, error) {
	if network == "udp6" {
		n.udp6ListenCount++
	}

	return nil, fmt.Errorf("stub net does not listen on %s", network) //nolint:err113
}

func (n *stubNet) ListenTCP(network string, laddr *net.TCPAddr) (transport.TCPListener, error) {
	return nil, transport.ErrNotSupported
}

func (n *stubNet) Dial(network, address string) (net.Conn, error) {
	return nil, transport.ErrNotSupported
}

func (n *stubNet) DialUDP(network string, laddr, raddr *net.UDPAddr) (transport.UDPConn, error) {
	return nil, transport.ErrNotSupported
}

func (n *stubNet) DialTCP(network string, laddr, raddr *net.TCPAddr) (transport.TCPConn, error) {
	return nil, transport.ErrNotSupported
}

func (n *stubNet) ResolveIPAddr(network, address string) (*net.IPAddr, error) {
	return net.ResolveIPAddr(network, address)
}

func (n *stubNet) ResolveUDPAddr(network, address string) (*net.UDPAddr, error) {
	return net.ResolveUDPAddr(network, address)
}

func (n *stubNet) ResolveTCPAddr(network, address string) (*net.TCPAddr, error) {
	return net.ResolveTCPAddr(network, address)
}

func (n *stubNet) Interfaces() ([]*transport.Interface, error) {
	iface := transport.NewInterface(net.Interface{
		Index: 1,
		MTU:   1500,
		Name:  "stub0",
		Flags: net.FlagUp,
	})
	iface.AddAddress(&net.IPNet{
		IP:   net.IPv4(192, 0, 2, 1),
		Mask: net.CIDRMask(24, 32),
	})

	return []*transport.Interface{iface}, nil
}

func (n *stubNet) InterfaceByIndex(index int) (*transport.Interface, error) {
	ifaces, err := n.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		if iface.Index == index {
			return iface, nil
		}
	}

	return nil, transport.ErrInterfaceNotFound
}

func (n *stubNet) InterfaceByName(name string) (*transport.Interface, error) {
	ifaces, err := n.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		if iface.Name == name {
			return iface, nil
		}
	}

	return nil, transport.ErrInterfaceNotFound
}

func (n *stubNet) CreateDialer(dialer *net.Dialer) transport.Dialer {
	return nil
}

func (n *stubNet) CreateListenConfig(listenerConfig *net.ListenConfig) transport.ListenConfig {
	return nil
}
