// SPDX-FileCopyrightText: 2025 The Pion community <https://pion.ly>
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
	"github.com/pion/stun/v3"
	"github.com/pion/transport/v3"
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
	defer agent.Close() //nolint:errcheck

	assert.Equal(t, uint16(1000), agent.portMin)
	assert.Equal(t, uint16(2000), agent.portMax)

	_, err = NewAgentWithOptions(WithPortRange(2000, 1000))
	assert.ErrorIs(t, err, ErrPort)
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

var errStubNotImplemented = errors.New("stub not implemented")

type stubTCPMux struct{}

func (m *stubTCPMux) Close() error {
	return nil
}

func (m *stubTCPMux) GetConnByUfrag(string, bool, net.IP, logging.LeveledLogger) (net.PacketConn, error) {
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
