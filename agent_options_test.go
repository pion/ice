// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/proxy"
)

// testBooleanOption is a helper function to test boolean agent options.
type booleanOptionTest struct {
	optionFunc func() AgentOption
	getValue   func(*Agent) bool
}

func testBooleanOption(t *testing.T, test booleanOptionTest, optionName string) {
	t.Helper()

	t.Run("enables "+optionName, func(t *testing.T) {
		agent, err := NewAgent(test.optionFunc())
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.True(t, test.getValue(agent))
	})

	t.Run("default is false", func(t *testing.T) {
		agent, err := NewAgent()
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.False(t, test.getValue(agent))
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
		agent, err := NewAgent(WithCandidateTypes([]CandidateType{CandidateTypeHost}), WithICELite(true))
		require.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.True(t, agent.lite)
	})

	t.Run("default is not lite", func(t *testing.T) {
		agent, err := NewAgent()
		require.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.False(t, agent.lite)
	})

	t.Run("errors when candidate types include non-host", func(t *testing.T) {
		_, err := NewAgent(WithICELite(true))
		assert.ErrorIs(t, err, ErrLiteUsingNonHostCandidates)
	})
}

func TestWithUrls(t *testing.T) {
	stunURL, err := stun.ParseURI("stun:example.com:3478")
	require.NoError(t, err)

	input := []*stun.URI{stunURL}
	agent, err := NewAgent(WithUrls(input))
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	require.Len(t, agent.urls, 1)
	assert.Equal(t, stunURL.String(), agent.urls[0].String())

	input[0] = nil
	require.Len(t, agent.urls, 1)
	assert.NotNil(t, agent.urls[0])
}

func TestWithPortRange(t *testing.T) {
	agent, err := NewAgent(WithPortRange(1000, 2000))
	require.NoError(t, err)

	assert.Equal(t, uint16(1000), agent.portMin)
	assert.Equal(t, uint16(2000), agent.portMax)

	agent.Close() //nolint:gosec,errcheck
	agent, err = NewAgent(WithPortRange(2000, 0))
	assert.NoError(t, err)
	defer agent.Close() //nolint:gosec,errcheck

	assert.Equal(t, uint16(2000), agent.portMin)
	assert.Equal(t, uint16(0), agent.portMax)
}

func TestWithTimeoutOptions(t *testing.T) {
	agent, err := NewAgent(WithDisconnectedTimeout(10*time.Second), WithFailedTimeout(20*time.Second), WithKeepaliveInterval(3*time.Second), WithCheckInterval(150*time.Millisecond))
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	assert.Equal(t, 10*time.Second, agent.disconnectedTimeout)
	assert.Equal(t, 20*time.Second, agent.failedTimeout)
	assert.Equal(t, 3*time.Second, agent.keepaliveInterval)
	assert.Equal(t, 150*time.Millisecond, agent.checkInterval)
}

func TestICELiteDisconnectedTimeoutDefault(t *testing.T) {
	explicitDisconnectedTimeout := 10 * time.Second
	explicitFailedTimeout := 10 * time.Second

	createFromOptions := func(t *testing.T, options ...AgentOption) *Agent {
		t.Helper()
		agent, err := NewAgent(options...)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, agent.Close()) })

		return agent
	}

	tests := []struct {
		name                        string
		options                     []AgentOption
		expectedDisconnectedTimeout time.Duration
		expectedFailedTimeout       time.Duration
		expectedCheckingTimeout     time.Duration
	}{
		{name: "full options keep full default", expectedDisconnectedTimeout: defaultDisconnectedTimeout, expectedFailedTimeout: defaultFailedTimeout, expectedCheckingTimeout: defaultDisconnectedTimeout + defaultFailedTimeout},
		{
			name: "lite options use lite default",
			options: []AgentOption{
				WithCandidateTypes([]CandidateType{CandidateTypeHost}),
				WithICELite(true),
			},
			expectedDisconnectedTimeout: defaultLiteDisconnectedTimeout,
			expectedFailedTimeout:       defaultFailedTimeout,
			expectedCheckingTimeout:     defaultDisconnectedTimeout + defaultFailedTimeout,
		},
		{
			name: "explicit lite default value uses explicit checking deadline",
			options: []AgentOption{
				WithCandidateTypes([]CandidateType{CandidateTypeHost}),
				WithICELite(true),
				WithDisconnectedTimeout(defaultLiteDisconnectedTimeout),
			},
			expectedDisconnectedTimeout: defaultLiteDisconnectedTimeout,
			expectedFailedTimeout:       defaultFailedTimeout,
			expectedCheckingTimeout:     defaultLiteDisconnectedTimeout + defaultFailedTimeout,
		},
		{
			name: "lite default combines with explicit failed timeout during checking",
			options: []AgentOption{
				WithCandidateTypes([]CandidateType{CandidateTypeHost}),
				WithICELite(true),
				WithFailedTimeout(explicitFailedTimeout),
			},
			expectedDisconnectedTimeout: defaultLiteDisconnectedTimeout,
			expectedFailedTimeout:       explicitFailedTimeout,
			expectedCheckingTimeout:     defaultDisconnectedTimeout + explicitFailedTimeout,
		},
		{
			name: "explicit option before lite is preserved",
			options: []AgentOption{
				WithCandidateTypes([]CandidateType{CandidateTypeHost}),
				WithDisconnectedTimeout(explicitDisconnectedTimeout),
				WithICELite(true),
			},
			expectedDisconnectedTimeout: explicitDisconnectedTimeout,
			expectedFailedTimeout:       defaultFailedTimeout,
			expectedCheckingTimeout:     explicitDisconnectedTimeout + defaultFailedTimeout,
		},
		{
			name: "explicit option after lite is preserved",
			options: []AgentOption{
				WithCandidateTypes([]CandidateType{CandidateTypeHost}),
				WithICELite(true),
				WithDisconnectedTimeout(explicitDisconnectedTimeout),
			},
			expectedDisconnectedTimeout: explicitDisconnectedTimeout,
			expectedFailedTimeout:       defaultFailedTimeout,
			expectedCheckingTimeout:     explicitDisconnectedTimeout + defaultFailedTimeout,
		},
		{
			name: "explicit zero is preserved",
			options: []AgentOption{
				WithCandidateTypes([]CandidateType{CandidateTypeHost}),
				WithICELite(true),
				WithDisconnectedTimeout(0),
			},
			expectedFailedTimeout:   defaultFailedTimeout,
			expectedCheckingTimeout: defaultFailedTimeout,
		},
		{
			name: "zero failed timeout disables initial checking timeout",
			options: []AgentOption{
				WithCandidateTypes([]CandidateType{CandidateTypeHost}),
				WithFailedTimeout(0),
			},
			expectedDisconnectedTimeout: defaultDisconnectedTimeout,
			expectedCheckingTimeout:     0,
		},
		{
			name: "zero disconnected and failed timeouts disable initial checking timeout",
			options: []AgentOption{
				WithCandidateTypes([]CandidateType{CandidateTypeHost}),
				WithDisconnectedTimeout(0),
				WithFailedTimeout(0),
			},
			expectedCheckingTimeout: 0,
		},
		{
			name: "final full mode keeps full default",
			options: []AgentOption{
				WithCandidateTypes([]CandidateType{CandidateTypeHost}),
				WithICELite(true),
				WithICELite(false),
			},
			expectedDisconnectedTimeout: defaultDisconnectedTimeout,
			expectedFailedTimeout:       defaultFailedTimeout,
			expectedCheckingTimeout:     defaultDisconnectedTimeout + defaultFailedTimeout,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			agent := createFromOptions(t, testCase.options...)
			assert.Equal(t, testCase.expectedDisconnectedTimeout, agent.disconnectedTimeout)
			assert.Equal(t, testCase.expectedCheckingTimeout, agent.initialCheckingTimeout())
			assert.Equal(t, testCase.expectedFailedTimeout, agent.failedTimeout)
			assert.Equal(t, defaultKeepaliveInterval, agent.keepaliveInterval)
		})
	}
}

func TestWithAcceptanceWaitOptions(t *testing.T) {
	agent, err := NewAgent(WithHostAcceptanceMinWait(1*time.Second), WithSrflxAcceptanceMinWait(2*time.Second), WithPrflxAcceptanceMinWait(3*time.Second), WithRelayAcceptanceMinWait(4*time.Second))
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	assert.Equal(t, 1*time.Second, agent.hostAcceptanceMinWait)
	assert.Equal(t, 2*time.Second, agent.srflxAcceptanceMinWait)
	assert.Equal(t, 3*time.Second, agent.prflxAcceptanceMinWait)
	assert.Equal(t, 4*time.Second, agent.relayAcceptanceMinWait)
}

func TestWithSTUNGatherTimeout(t *testing.T) {
	agent, err := NewAgent(WithSTUNGatherTimeout(7 * time.Second))
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	assert.Equal(t, 7*time.Second, agent.stunGatherTimeout)
}

func TestWithIPFilterOption(t *testing.T) {
	filter := func(ip net.IP) bool {
		return ip.IsLoopback()
	}

	agent, err := NewAgent(WithIPFilter(filter))
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	require.NotNil(t, agent.ipFilter)
	assert.True(t, agent.ipFilter(net.IPv4(127, 0, 0, 1)))
	assert.False(t, agent.ipFilter(net.IPv4(192, 0, 2, 1)))
}

func TestWithRemoteIPFilterOption(t *testing.T) {
	filter := func(ip net.IP) bool {
		return ip.IsPrivate()
	}

	agent, err := NewAgent(WithRemoteIPFilter(filter))
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	require.NotNil(t, agent.remoteIPFilter)
	assert.True(t, agent.remoteIPFilter(net.IPv4(192, 168, 1, 10)))
	assert.False(t, agent.remoteIPFilter(net.IPv4(203, 0, 113, 1)))
}

func TestWithNetOption(t *testing.T) {
	stub := newStubNet(t)

	agent, err := NewAgent(WithNet(stub))
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	assert.Equal(t, stub, agent.net)
}

func TestWithMulticastDNSOptions(t *testing.T) {
	agent, err := NewAgent(WithMulticastDNSMode(MulticastDNSModeDisabled), WithMulticastDNSHostName("pion-test.local"))
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	assert.Equal(t, MulticastDNSModeDisabled, agent.mDNSMode)
	assert.Equal(t, "pion-test.local", agent.mDNSName)

	_, err = NewAgent(WithMulticastDNSHostName("invalid-host"))
	assert.ErrorIs(t, err, ErrInvalidMulticastDNSHostName)
}

func TestWithLocalCredentials(t *testing.T) {
	password := strings.Repeat("p", minLenPwd)

	agent, err := NewAgent(WithLocalCredentials("abcd", password))
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	assert.Equal(t, "abcd", agent.localUfrag)
	assert.Equal(t, password, agent.localPwd)

	_, err = NewAgent(WithLocalCredentials("ab", password))
	assert.ErrorIs(t, err, ErrLocalUfragInsufficientBits)

	shortPassword := strings.Repeat("p", 10)
	_, err = NewAgent(WithLocalCredentials("abcd", shortPassword))
	assert.ErrorIs(t, err, ErrLocalPwdInsufficientBits)
}

func TestLocalCredentialsLength(t *testing.T) {
	ufrag, shortUfrag := strings.Repeat("u", minLenUFrag), strings.Repeat("u", minLenUFrag-1)
	pwd, shortPwd := strings.Repeat("p", minLenPwd), strings.Repeat("p", minLenPwd-1)

	tests := []struct {
		name       string
		ufrag, pwd string
		expected   error
	}{
		{"ufrag too short", shortUfrag, pwd, ErrLocalUfragInsufficientBits},
		{"ufrag at minimum", ufrag, pwd, nil},
		{"pwd too short", ufrag, shortPwd, ErrLocalPwdInsufficientBits},
		{"pwd at minimum", ufrag, pwd, nil},
		{"stateless token", strings.Repeat("u", 24), strings.Repeat("p", 32), nil},
		{"both empty", "", "", nil},
	}

	for _, test := range tests {
		t.Run("WithLocalCredentials/"+test.name, func(t *testing.T) {
			agent, err := NewAgent(WithLocalCredentials(test.ufrag, test.pwd))
			if test.expected != nil {
				assert.ErrorIs(t, err, test.expected)

				return
			}
			require.NoError(t, err)
			defer agent.Close() //nolint:errcheck

			assert.GreaterOrEqual(t, len([]rune(agent.localUfrag)), minLenUFrag)
			assert.GreaterOrEqual(t, len([]rune(agent.localPwd)), minLenPwd)
		})

		t.Run("Restart/"+test.name, func(t *testing.T) {
			agent, err := NewAgent()
			require.NoError(t, err)
			defer agent.Close() //nolint:errcheck

			err = agent.Restart(test.ufrag, test.pwd)
			if test.expected != nil {
				assert.ErrorIs(t, err, test.expected)

				return
			}
			require.NoError(t, err)

			assert.GreaterOrEqual(t, len([]rune(agent.localUfrag)), minLenUFrag)
			assert.GreaterOrEqual(t, len([]rune(agent.localPwd)), minLenPwd)
		})
	}
}

func TestWithMuxOptions(t *testing.T) {
	tcpMux := &stubTCPMux{}
	udpMux := &stubUDPMux{}
	udpMuxSrflx := &stubUniversalUDPMux{}

	agent, err := NewAgent(WithTCPMux(tcpMux), WithUDPMux(udpMux), WithUDPMuxSrflx(udpMuxSrflx))
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	assert.Equal(t, tcpMux, agent.tcpMux)
	assert.Equal(t, udpMux, agent.udpMux)
	assert.Equal(t, udpMuxSrflx, agent.udpMuxSrflx)
}

func TestWithProxyDialer(t *testing.T) {
	agent, err := NewAgent(WithProxyDialer(proxy.Direct))
	require.NoError(t, err)
	defer agent.Close() //nolint:errcheck

	assert.Equal(t, proxy.Direct, agent.proxyDialer)
}

func TestWithMaxBindingRequests(t *testing.T) {
	agent, err := NewAgent(WithMaxBindingRequests(3))
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

		agent, err := NewAgent(WithRenomination(customGen))
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.True(t, agent.enableRenomination)
		assert.NotNil(t, agent.nominationValueGenerator)
		assert.Equal(t, uint32(10), agent.getNominationValue())
		assert.Equal(t, uint32(20), agent.getNominationValue())
	})

	t.Run("enables renomination with default generator", func(t *testing.T) {
		agent, err := NewAgent(WithRenomination(DefaultNominationValueGenerator()))
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.True(t, agent.enableRenomination)
		assert.NotNil(t, agent.nominationValueGenerator)
		assert.Equal(t, uint32(1), agent.getNominationValue())
		assert.Equal(t, uint32(2), agent.getNominationValue())
	})

	t.Run("rejects nil generator", func(t *testing.T) {
		_, err := NewAgent(WithRenomination(nil))
		assert.ErrorIs(t, err, ErrInvalidNominationValueGenerator)
	})

	t.Run("default agent has renomination disabled", func(t *testing.T) {
		config := []AgentOption{WithNetworkTypes([]NetworkType{NetworkTypeUDP4})}

		agent, err := NewAgent(config...)
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.False(t, agent.enableRenomination)
		assert.Nil(t, agent.nominationValueGenerator)
		assert.Equal(t, uint32(0), agent.getNominationValue())
	})
}

func TestWithNominationAttribute(t *testing.T) {
	t.Run("sets custom nomination attribute", func(t *testing.T) {
		agent, err := NewAgent(WithNominationAttribute(0x0045))
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.Equal(t, stun.AttrType(0x0045), agent.nominationAttribute)
	})

	t.Run("rejects invalid attribute 0x0000", func(t *testing.T) {
		_, err := NewAgent(WithNominationAttribute(0x0000))
		assert.ErrorIs(t, err, ErrInvalidNominationAttribute)
	})

	t.Run("default value when no option", func(t *testing.T) {
		config := []AgentOption{WithNetworkTypes([]NetworkType{NetworkTypeUDP4})}

		agent, err := NewAgent(config...)
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		// Should use default value 0xC001
		assert.Equal(t, stun.AttrType(0xC001), agent.nominationAttribute)
	})
}

func TestWithIncludeLoopback(t *testing.T) {
	testBooleanOption(t, booleanOptionTest{
		optionFunc: WithIncludeLoopback,
		getValue:   func(a *Agent) bool { return a.includeLoopback },
	}, "loopback addresses")
}

func TestWithTCPPriorityOffset(t *testing.T) {
	t.Run("sets custom TCP priority offset", func(t *testing.T) {
		customOffset := uint16(50)
		agent, err := NewAgent(WithTCPPriorityOffset(customOffset))
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.Equal(t, customOffset, agent.tcpPriorityOffset)
	})

	t.Run("default is 27", func(t *testing.T) {
		agent, err := NewAgent()
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.Equal(t, uint16(27), agent.tcpPriorityOffset)
	})
}

func TestWithDisableActiveTCP(t *testing.T) {
	testBooleanOption(t, booleanOptionTest{
		optionFunc: WithDisableActiveTCP,
		getValue:   func(a *Agent) bool { return a.disableActiveTCP },
	}, "active TCP disabling")
}

func TestWithBindingRequestHandler(t *testing.T) {
	t.Run("sets binding request handler", func(t *testing.T) {
		handlerCalled := false
		handler := func(_ *stun.Message, _, _ Candidate, _ *CandidatePair) bool {
			handlerCalled = true

			return true
		}

		agent, err := NewAgent(WithBindingRequestHandler(handler))
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
		agent, err := NewAgent()
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.Nil(t, agent.userBindingRequestHandler)
	})
}

func TestWithEnableUseCandidateCheckPriority(t *testing.T) {
	testBooleanOption(t, booleanOptionTest{
		optionFunc: WithEnableUseCandidateCheckPriority,
		getValue:   func(a *Agent) bool { return a.enableUseCandidateCheckPriority },
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

		agent, err := NewAgent(WithIncludeLoopback(), WithTCPPriorityOffset(customOffset), WithDisableActiveTCP(), WithBindingRequestHandler(handler), WithEnableUseCandidateCheckPriority())
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
			return interfaceName == "eth0" // nolint:goconst
		}

		agent, err := NewAgent(WithInterfaceFilter(filter))
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.NotNil(t, agent.interfaceFilter)
		assert.True(t, agent.interfaceFilter("eth0")) // nolint:goconst
		assert.False(t, agent.interfaceFilter("wlan0"))
	})

	t.Run("default is nil", func(t *testing.T) {
		agent, err := NewAgent()
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.Nil(t, agent.interfaceFilter)
	})
}

func TestWithLoggerFactory(t *testing.T) {
	t.Run("sets logger factory", func(t *testing.T) {
		loggerFactory := logging.NewDefaultLoggerFactory()
		loggerFactory.DefaultLogLevel = logging.LogLevelDebug

		agent, err := NewAgent(WithLoggerFactory(loggerFactory))
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.Equal(t, loggerFactory, agent.loggerFactory)
		assert.NotNil(t, agent.log)
	})

	t.Run("default uses default logger", func(t *testing.T) {
		agent, err := NewAgent()
		assert.NoError(t, err)
		defer agent.Close() //nolint:errcheck

		assert.NotNil(t, agent.log)
	})
}

func TestWithNetworkTypesAppliedBeforeRestart(t *testing.T) {
	t.Run("ipv6 listen skipped when network types option restricts to ipv4", func(t *testing.T) {
		stub := newStubNet(t)

		agent, err := NewAgent(WithNet(stub), WithNetworkTypes([]NetworkType{NetworkTypeUDP4}))
		require.NoError(t, err)
		defer func() { require.NoError(t, agent.Close()) }()

		assert.Zero(t, stub.udp6ListenCount, "unexpected ipv6 listen before restart")
	})
}

func TestWithNetworkTypes(t *testing.T) {
	t.Run("applies option", func(t *testing.T) {
		agent, err := NewAgent(WithNetworkTypes([]NetworkType{NetworkTypeUDP4, NetworkTypeTCP4}))
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		require.Equal(t, []NetworkType{NetworkTypeUDP4, NetworkTypeTCP4}, agent.networkTypes)
	})

	t.Run("deduplicates values", func(t *testing.T) {
		agent, err := NewAgent(WithNetworkTypes([]NetworkType{NetworkTypeUDP4, NetworkTypeUDP4, NetworkTypeTCP4}))
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		require.Equal(t, []NetworkType{NetworkTypeUDP4, NetworkTypeTCP4}, agent.networkTypes)
	})

	t.Run("rejects unsupported value", func(t *testing.T) {
		_, err := NewAgent(WithNetworkTypes([]NetworkType{NetworkType(0)}))
		require.ErrorIs(t, err, ErrProtoType)
	})

	t.Run("rejects unsupported value from config", func(t *testing.T) {
		_, err := NewAgent(WithNetworkTypes([]NetworkType{NetworkType(0)}))
		require.ErrorIs(t, err, ErrProtoType)
	})
}

func TestWithTURNTransportProtocols(t *testing.T) {
	t.Run("applies option", func(t *testing.T) {
		agent, err := NewAgent(WithTURNTransportProtocols([]NetworkType{NetworkTypeTCP4}))
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		require.Equal(t, []NetworkType{NetworkTypeTCP4}, agent.turnTransportProtocols)
	})

	t.Run("deduplicates protocols", func(t *testing.T) {
		agent, err := NewAgent(WithTURNTransportProtocols([]NetworkType{NetworkTypeTCP4, NetworkTypeTCP4, NetworkTypeUDP4}))
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		require.Equal(t, []NetworkType{NetworkTypeTCP4, NetworkTypeUDP4}, agent.turnTransportProtocols)
	})

	t.Run("rejects unsupported proto", func(t *testing.T) {
		_, err := NewAgent(WithTURNTransportProtocols([]NetworkType{NetworkType(0)}))
		require.ErrorIs(t, err, ErrProtoType)
	})
}

func TestWithCandidateTypesAffectsURLValidation(t *testing.T) {
	stunURL, err := stun.ParseURI("stun:example.com:3478")
	require.NoError(t, err)

	t.Run("default candidate types accept urls", func(t *testing.T) {
		stub := newStubNet(t)

		agent, err := NewAgent(WithUrls([]*stun.URI{stunURL}), WithNet(stub))
		require.NoError(t, err)
		require.NoError(t, agent.Close())
	})

	t.Run("host only candidate types reject urls", func(t *testing.T) {
		stub := newStubNet(t)

		_, err := NewAgent(WithUrls([]*stun.URI{stunURL}), WithNet(stub), WithCandidateTypes([]CandidateType{CandidateTypeHost}))
		require.ErrorIs(t, err, ErrUselessUrlsProvided)
	})
}

func TestWithAddressRewriteRulesAccumulatesAndCopies(t *testing.T) {
	rules := []AddressRewriteRule{{External: []string{" 203.0.113.1 ", "203.0.113.1", "203.0.113.2 "}, Local: " 10.0.0.1 ", Networks: []NetworkType{NetworkTypeUDP4}}}
	agent, err := NewAgent(
		WithNet(newStubNet(t)),
		WithMulticastDNSMode(MulticastDNSModeDisabled),
		WithAddressRewriteRules(rules...),
		WithAddressRewriteRules(AddressRewriteRule{External: []string{"198.51.100.1"}, AsCandidateType: CandidateTypeServerReflexive, Mode: AddressRewriteAppend}),
		WithAddressRewriteRules(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, agent.Close()) })

	// Callers can reuse their slices without changing the agent's configuration.
	rules[0].External[0] = "192.0.2.1"
	rules[0].Networks[0] = NetworkTypeUDP6
	rules[0].Local = "10.0.0.2"

	require.Len(t, agent.addressRewriteRules, 2)
	require.Equal(t, []string{"203.0.113.1", "203.0.113.2"}, agent.addressRewriteRules[0].External)
	require.Equal(t, "10.0.0.1", agent.addressRewriteRules[0].Local)
	require.Equal(t, []NetworkType{NetworkTypeUDP4}, agent.addressRewriteRules[0].Networks)
	require.Equal(t, []string{"198.51.100.1"}, agent.addressRewriteRules[1].External)

	for _, testCase := range []struct {
		typ      CandidateType
		expected []string
	}{
		{CandidateTypeHost, []string{"203.0.113.1", "203.0.113.2"}},
		{CandidateTypeServerReflexive, []string{"198.51.100.1"}},
	} {
		ips, matched, _, lookupErr := agent.addressRewriteMapper.findExternalIPs(testCase.typ, "10.0.0.1", "")
		require.NoError(t, lookupErr)
		require.True(t, matched)
		require.Equal(t, testCase.expected, ips)
	}
}

func TestWithAddressRewriteRulesOverlapWarnings(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		candidateType CandidateType
		networks      []NetworkType
		networkScope  string
	}{
		{"host wildcard", CandidateTypeHost, nil, "*"},
		{"srflx wildcard", CandidateTypeServerReflexive, nil, "*"},
		{"host UDP4", CandidateTypeHost, []NetworkType{NetworkTypeUDP4}, "udp4"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			logger := &recordingLogger{}
			agent, err := NewAgent(
				WithNet(newStubNet(t)),
				WithMulticastDNSMode(MulticastDNSModeDisabled),
				WithLoggerFactory(&recordingLoggerFactory{logger: logger}),
				WithAddressRewriteRules(AddressRewriteRule{External: []string{"203.0.113.10"}, AsCandidateType: testCase.candidateType, Mode: AddressRewriteReplace, Networks: testCase.networks}),
				WithAddressRewriteRules(AddressRewriteRule{External: []string{"198.51.100.50"}, AsCandidateType: testCase.candidateType, Mode: AddressRewriteAppend, Networks: testCase.networks}),
			)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, agent.Close()) })

			require.Len(t, logger.warnings, 1)
			for _, fragment := range []string{
				"overlapping address rewrite rule", "candidate=" + testCase.candidateType.String(),
				"iface=*", "cidr=*", "networks=" + testCase.networkScope, "local=family:ipv4", "203.0.113.10", "198.51.100.50",
			} {
				require.Contains(t, logger.warnings[0], fragment)
			}

			ips, matched, mode, err := agent.addressRewriteMapper.findExternalIPs(testCase.candidateType, "10.0.0.1", "")
			require.NoError(t, err)
			require.True(t, matched)
			require.Equal(t, []string{"203.0.113.10"}, ips)
			require.Equal(t, AddressRewriteReplace, mode)
		})
	}

	t.Run("disjoint scopes do not warn", func(t *testing.T) {
		logger := &recordingLogger{}
		agent, err := NewAgent(
			WithNet(newStubNet(t)),
			WithMulticastDNSMode(MulticastDNSModeDisabled),
			WithLoggerFactory(&recordingLoggerFactory{logger: logger}),
			WithAddressRewriteRules(
				AddressRewriteRule{External: []string{"203.0.113.10"}, Networks: []NetworkType{NetworkTypeUDP4}},
				AddressRewriteRule{External: []string{"2001:db8::10"}, Networks: []NetworkType{NetworkTypeUDP6}},
				AddressRewriteRule{External: []string{"198.51.100.10"}, AsCandidateType: CandidateTypeServerReflexive, Networks: []NetworkType{NetworkTypeUDP4}},
			),
		)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, agent.Close()) })
		require.Empty(t, logger.warnings)
	})
}

func TestAddressRewriteMapper(t *testing.T) {
	type lookup struct {
		name     string
		local    string
		iface    string
		external []string
		mode     AddressRewriteMode
	}
	tests := []struct {
		name          string
		rules         []AddressRewriteRule
		candidateType CandidateType
		lookups       []lookup
	}{
		{
			name: "precedence",
			rules: []AddressRewriteRule{
				{External: []string{"203.0.113.200", "relay.example"}, Mode: AddressRewriteReplace},
				{External: []string{"203.0.113.201", "relay.example"}, Mode: AddressRewriteAppend},
				{External: []string{"203.0.113.100", "relay.example"}, CIDR: "10.0.0.0/24", Mode: AddressRewriteAppend},
				{External: []string{"203.0.113.50", "relay.example"}, Iface: "eth0", Mode: AddressRewriteReplace},
				{External: []string{"203.0.113.51", "relay.example"}, Iface: "eth0", CIDR: "10.0.0.0/24", Mode: AddressRewriteAppend},
				{External: []string{"203.0.113.5", "relay.example"}, Local: "10.0.0.5", Mode: AddressRewriteReplace},
				{External: []string{"203.0.113.6", "relay.example"}, Local: "10.0.0.5", Mode: AddressRewriteAppend},
			},
			candidateType: CandidateTypeHost,
			lookups: []lookup{
				{name: "first explicit mapping outranks earlier CIDR", local: "10.0.0.5", external: []string{"203.0.113.5", "relay.example"}, mode: AddressRewriteReplace},
				{name: "explicit mapping outranks interface and CIDR", local: "10.0.0.5", iface: "eth0", external: []string{"203.0.113.5", "relay.example"}, mode: AddressRewriteReplace},
				{name: "CIDR outranks catch-all without interface", local: "10.0.0.25", external: []string{"203.0.113.100", "relay.example"}, mode: AddressRewriteAppend},
				{name: "interface and CIDR outrank interface alone", local: "10.0.0.25", iface: "eth0", external: []string{"203.0.113.51", "relay.example"}, mode: AddressRewriteAppend},
				{name: "interface outranks catch-all outside CIDR", local: "172.16.0.1", iface: "eth0", external: []string{"203.0.113.50", "relay.example"}, mode: AddressRewriteReplace},
				{name: "first catch-all wins equal specificity", local: "172.16.0.1", iface: "wlan0", external: []string{"203.0.113.200", "relay.example"}, mode: AddressRewriteReplace},
				{name: "unscoped CIDR has equal priority with interface lookup", local: "10.0.0.25", iface: "wlan0", external: []string{"203.0.113.200", "relay.example"}, mode: AddressRewriteReplace},
			},
		},
		{
			name:          "nil networks allow both families and preserve address order",
			rules:         []AddressRewriteRule{{External: []string{"203.0.113.2", "2001:db8::2", "203.0.113.1", "2001:db8::1", "relay.example"}, Networks: nil}},
			candidateType: CandidateTypeHost,
			lookups: []lookup{
				{local: "10.0.0.1", external: []string{"203.0.113.2", "203.0.113.1", "relay.example"}, mode: AddressRewriteReplace},
				{local: "2001:db8:1::1", external: []string{"2001:db8::2", "2001:db8::1", "relay.example"}, mode: AddressRewriteReplace},
			},
		},
		{
			name:          "explicit IPv4 local maps to IPv6 external",
			rules:         []AddressRewriteRule{{External: []string{"2001:db8::1", "relay.example"}, Local: " ::ffff:10.0.0.1 ", Networks: []NetworkType{NetworkTypeUDP4}}},
			candidateType: CandidateTypeHost,
			lookups: []lookup{
				{local: "10.0.0.1", external: []string{"2001:db8::1", "relay.example"}, mode: AddressRewriteReplace},
				{local: "10.0.0.2"},
				{local: "2001:db8:1::1"},
			},
		},
		{
			name:          "empty explicit mapping still matches",
			rules:         []AddressRewriteRule{{Local: "10.0.0.1"}},
			candidateType: CandidateTypeHost,
			lookups: []lookup{
				{local: "10.0.0.1", mode: AddressRewriteReplace},
				{local: "10.0.0.2"},
			},
		},
		{
			name:          "explicit IPv6 local maps to IPv4 external",
			rules:         []AddressRewriteRule{{External: []string{"203.0.113.1", "relay.example"}, Local: "2001:db8:1::1", Networks: []NetworkType{NetworkTypeUDP6}}},
			candidateType: CandidateTypeHost,
			lookups: []lookup{
				{local: "2001:db8:1::1", external: []string{"203.0.113.1", "relay.example"}, mode: AddressRewriteReplace},
				{local: "2001:db8:1::2"},
				{local: "10.0.0.1"},
			},
		},
		{
			name:          "IPv4 CIDR determines local family for IPv6 external",
			rules:         []AddressRewriteRule{{External: []string{"2001:db8::1", "relay.example"}, CIDR: "10.0.0.0/24"}},
			candidateType: CandidateTypeHost,
			lookups: []lookup{
				{local: "10.0.0.1", external: []string{"2001:db8::1", "relay.example"}, mode: AddressRewriteReplace},
				{local: "10.0.1.1"},
				{local: "2001:db8:1::1"},
			},
		},
		{
			name:          "IPv6 CIDR determines local family for IPv4 external",
			rules:         []AddressRewriteRule{{External: []string{"203.0.113.1", "relay.example"}, CIDR: "2001:db8:1::/64"}},
			candidateType: CandidateTypeHost,
			lookups: []lookup{
				{local: "2001:db8:1::1", external: []string{"203.0.113.1", "relay.example"}, mode: AddressRewriteReplace},
				{local: "2001:db8:2::1"},
				{local: "10.0.0.1"},
			},
		},
		{
			name:          "empty networks allow both families",
			rules:         []AddressRewriteRule{{External: []string{"203.0.113.1", "2001:db8::1", "relay.example"}, Networks: []NetworkType{}}},
			candidateType: CandidateTypeHost,
			lookups: []lookup{
				{local: "10.0.0.1", external: []string{"203.0.113.1", "relay.example"}, mode: AddressRewriteReplace},
				{local: "2001:db8:1::1", external: []string{"2001:db8::1", "relay.example"}, mode: AddressRewriteReplace},
			},
		},
		{
			name:          "UDP4 excludes IPv6",
			rules:         []AddressRewriteRule{{External: []string{"203.0.113.1", "2001:db8::1", "relay.example"}, Networks: []NetworkType{NetworkTypeUDP4}}},
			candidateType: CandidateTypeHost,
			lookups: []lookup{
				{local: "10.0.0.1", external: []string{"203.0.113.1", "relay.example"}, mode: AddressRewriteReplace},
				{local: "2001:db8:1::1"},
			},
		},
		{
			name:          "UDP6 excludes IPv4",
			rules:         []AddressRewriteRule{{External: []string{"203.0.113.1", "2001:db8::1", "relay.example"}, Networks: []NetworkType{NetworkTypeUDP6}}},
			candidateType: CandidateTypeHost,
			lookups: []lookup{
				{local: "10.0.0.1"},
				{local: "2001:db8:1::1", external: []string{"2001:db8::1", "relay.example"}, mode: AddressRewriteReplace},
			},
		},
		{
			name:          "TCP4 excludes IPv6",
			rules:         []AddressRewriteRule{{External: []string{"203.0.113.1", "2001:db8::1", "relay.example"}, Networks: []NetworkType{NetworkTypeTCP4}}},
			candidateType: CandidateTypeHost,
			lookups: []lookup{
				{local: "10.0.0.1", external: []string{"203.0.113.1", "relay.example"}, mode: AddressRewriteReplace},
				{local: "2001:db8:1::1"},
			},
		},
		{
			name:          "TCP6 excludes IPv4",
			rules:         []AddressRewriteRule{{External: []string{"203.0.113.1", "2001:db8::1", "relay.example"}, Networks: []NetworkType{NetworkTypeTCP6}}},
			candidateType: CandidateTypeHost,
			lookups: []lookup{
				{local: "10.0.0.1"},
				{local: "2001:db8:1::1", external: []string{"2001:db8::1", "relay.example"}, mode: AddressRewriteReplace},
			},
		},
		{
			name: "unspecified defaults to host replace", rules: []AddressRewriteRule{{External: []string{"203.0.113.1", "relay.example"}, AsCandidateType: CandidateTypeUnspecified}},
			candidateType: CandidateTypeHost, lookups: []lookup{{local: "10.0.0.1", external: []string{"203.0.113.1", "relay.example"}, mode: AddressRewriteReplace}},
		},
		{
			name: "host defaults to replace", rules: []AddressRewriteRule{{External: []string{"203.0.113.1", "relay.example"}, AsCandidateType: CandidateTypeHost}},
			candidateType: CandidateTypeHost, lookups: []lookup{{local: "10.0.0.1", external: []string{"203.0.113.1", "relay.example"}, mode: AddressRewriteReplace}},
		},
		{
			name: "srflx defaults to append", rules: []AddressRewriteRule{{External: []string{"203.0.113.1", "relay.example"}, AsCandidateType: CandidateTypeServerReflexive}},
			candidateType: CandidateTypeServerReflexive, lookups: []lookup{{local: "10.0.0.1", external: []string{"203.0.113.1", "relay.example"}, mode: AddressRewriteAppend}},
		},
		{
			name: "relay defaults to append", rules: []AddressRewriteRule{{External: []string{"203.0.113.1", "relay.example"}, AsCandidateType: CandidateTypeRelay}},
			candidateType: CandidateTypeRelay, lookups: []lookup{{local: "10.0.0.1", external: []string{"203.0.113.1", "relay.example"}, mode: AddressRewriteAppend}},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			mapper, err := newAddressRewriteMapper(testCase.rules)
			require.NoError(t, err)
			require.NotNil(t, mapper)
			for _, lookup := range testCase.lookups {
				name := lookup.name
				if name == "" {
					name = lookup.local
				}
				t.Run(name, func(t *testing.T) {
					ips, matched, mode, err := mapper.findExternalIPs(testCase.candidateType, lookup.local, lookup.iface)
					require.NoError(t, err)
					require.Equal(t, lookup.mode != addressRewriteModeUnspecified, matched)
					require.Equal(t, lookup.mode, mode)
					require.Equal(t, lookup.external, ips)
					if len(ips) > 0 {
						ips[0] = "mutated.example"
						ips, _, _, err = mapper.findExternalIPs(testCase.candidateType, lookup.local, lookup.iface)
						require.NoError(t, err)
						require.Equal(t, lookup.external, ips)
					}
				})
			}
		})
	}
}

func TestAddressRewritePortValidation(t *testing.T) {
	for _, ports := range [][2]int{{1234, 0}, {-1, 4321}, {1234, 65536}} {
		err := WithAddressRewriteRules(AddressRewriteRule{
			External:     []string{"203.0.113.1"},
			OriginalPort: ports[0],
			NewPort:      ports[1],
		})(&Agent{})
		require.ErrorIs(t, err, ErrInvalidAddressRewriteMapping)
	}
}

type recordingLogger struct {
	warnings []string
}

func (l *recordingLogger) Trace(string)          {}
func (l *recordingLogger) Tracef(string, ...any) {}
func (l *recordingLogger) Debug(string)          {}
func (l *recordingLogger) Debugf(string, ...any) {}
func (l *recordingLogger) Info(string)           {}
func (l *recordingLogger) Infof(string, ...any)  {}
func (l *recordingLogger) Error(string)          {}
func (l *recordingLogger) Errorf(string, ...any) {}

func (l *recordingLogger) Warn(msg string) {
	l.warnings = append(l.warnings, msg)
}

func (l *recordingLogger) Warnf(format string, args ...any) {
	l.warnings = append(l.warnings, fmt.Sprintf(format, args...))
}

type recordingLoggerFactory struct {
	logger *recordingLogger
}

func (f *recordingLoggerFactory) NewLogger(string) logging.LeveledLogger {
	return f.logger
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
	iface := transport.NewInterface(net.Interface{Index: 1, MTU: 1500, Name: "stub0", Flags: net.FlagUp})
	iface.AddAddress(&net.IPNet{IP: net.IPv4(192, 0, 2, 1), Mask: net.CIDRMask(24, 32)})

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

func TestWithInsecureSkipVerify(t *testing.T) {
	agent, err := NewAgent(WithNet(newStubNet(t)), WithMulticastDNSMode(MulticastDNSModeDisabled))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, agent.Close()) })
	require.False(t, agent.insecureSkipVerify)
	require.ErrorIs(t, WithInsecureSkipVerify(true)(agent), ErrAgentOptionNotUpdatable)

	insecureAgent, err := NewAgent(WithNet(newStubNet(t)), WithMulticastDNSMode(MulticastDNSModeDisabled), WithInsecureSkipVerify(true))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, insecureAgent.Close()) })
	require.True(t, insecureAgent.insecureSkipVerify)
}
