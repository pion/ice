// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/stun/v3"
	transport "github.com/pion/transport/v4"
	"github.com/pion/transport/v4/test"
	"github.com/pion/turn/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/proxy"
)

var errFirewallBlocked = errors.New("firewall blocked protocol")

type firewallNet struct {
	allowUDP bool
	allowTCP bool

	mu             sync.Mutex
	listenUDPCalls int
	listenPktCalls int
	dialUDPCalls   int
	dialTCPCalls   int
	listenPktAddrs []string
	listenPktNets  []string
	listenUDPNets  []string
	listenUDPAddrs []string
	dialUDPNets    []string
	dialTCPNets    []string
}

func newFirewallNet(allowUDP, allowTCP bool) *firewallNet {
	return &firewallNet{allowUDP: allowUDP, allowTCP: allowTCP}
}

func (n *firewallNet) ListenPacket(network, address string) (net.PacketConn, error) {
	n.mu.Lock()
	n.listenPktCalls++
	n.listenPktNets = append(n.listenPktNets, network)
	n.listenPktAddrs = append(n.listenPktAddrs, address)
	n.mu.Unlock()

	if isUDPNetworkName(network) {
		if !n.allowUDP {
			return nil, errFirewallBlocked
		}

		udpAddr, err := net.ResolveUDPAddr(network, address)
		if err != nil {
			return nil, err
		}

		return newStubPacketConn(udpAddr), nil
	}

	if isTCPNetworkName(network) && !n.allowTCP {
		return nil, errFirewallBlocked
	}

	return nil, transport.ErrNotSupported
}

func (n *firewallNet) ListenUDP(network string, laddr *net.UDPAddr) (transport.UDPConn, error) {
	n.mu.Lock()
	n.listenUDPCalls++
	n.listenUDPNets = append(n.listenUDPNets, network)
	if laddr != nil {
		n.listenUDPAddrs = append(n.listenUDPAddrs, laddr.String())
	} else {
		n.listenUDPAddrs = append(n.listenUDPAddrs, "")
	}
	n.mu.Unlock()

	if !n.allowUDP {
		return nil, errFirewallBlocked
	}

	if laddr == nil {
		laddr = &net.UDPAddr{IP: net.IPv4zero, Port: 0}
	}

	return net.ListenUDP(network, laddr) //nolint:wrapcheck
}

func (n *firewallNet) ListenTCP(string, *net.TCPAddr) (transport.TCPListener, error) {
	return nil, transport.ErrNotSupported
}

func (n *firewallNet) Dial(string, string) (net.Conn, error) {
	return nil, transport.ErrNotSupported
}

func (n *firewallNet) DialUDP(network string, laddr, raddr *net.UDPAddr) (transport.UDPConn, error) {
	n.mu.Lock()
	n.dialUDPCalls++
	n.dialUDPNets = append(n.dialUDPNets, network)
	n.mu.Unlock()

	if !n.allowUDP {
		return nil, errFirewallBlocked
	}

	return net.DialUDP(network, laddr, raddr) //nolint:wrapcheck
}

func (n *firewallNet) DialTCP(network string, laddr, raddr *net.TCPAddr) (transport.TCPConn, error) {
	n.mu.Lock()
	n.dialTCPCalls++
	n.dialTCPNets = append(n.dialTCPNets, network)
	n.mu.Unlock()

	if !n.allowTCP {
		return nil, errFirewallBlocked
	}

	return net.DialTCP(network, laddr, raddr) //nolint:wrapcheck
}

func (n *firewallNet) ResolveIPAddr(network, address string) (*net.IPAddr, error) {
	return net.ResolveIPAddr(network, address)
}

func (n *firewallNet) ResolveUDPAddr(network, address string) (*net.UDPAddr, error) {
	return net.ResolveUDPAddr(network, address)
}

func (n *firewallNet) ResolveTCPAddr(network, address string) (*net.TCPAddr, error) {
	return net.ResolveTCPAddr(network, address)
}

func (n *firewallNet) Interfaces() ([]*transport.Interface, error) {
	iface := transport.NewInterface(net.Interface{
		Index: 1,
		MTU:   1500,
		Name:  "fw0",
		Flags: net.FlagUp | net.FlagLoopback,
	})
	iface.AddAddress(&net.IPNet{IP: net.IPv4(127, 0, 0, 1), Mask: net.CIDRMask(8, 32)})

	return []*transport.Interface{iface}, nil
}

func (n *firewallNet) InterfaceByIndex(index int) (*transport.Interface, error) {
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

func (n *firewallNet) InterfaceByName(name string) (*transport.Interface, error) {
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

func (n *firewallNet) CreateDialer(*net.Dialer) transport.Dialer {
	return nil
}

func (n *firewallNet) CreateListenConfig(*net.ListenConfig) transport.ListenConfig {
	return nil
}

func (n *firewallNet) counts() (listenUDP, listenPacket, dialUDP, dialTCP int) {
	n.mu.Lock()
	defer n.mu.Unlock()

	return n.listenUDPCalls, n.listenPktCalls, n.dialUDPCalls, n.dialTCPCalls
}

type firewallProxyDialer struct {
	allowTCP bool

	mu        sync.Mutex
	dialCalls int
}

func (d *firewallProxyDialer) Dial(network, _ string) (net.Conn, error) {
	d.mu.Lock()
	d.dialCalls++
	d.mu.Unlock()

	if !isTCPNetworkName(network) || !d.allowTCP {
		return nil, errFirewallBlocked
	}

	return &matrixTCPConn{
		local:  &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 50000},
		remote: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 3478},
	}, nil
}

func (d *firewallProxyDialer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.dialCalls
}

type matrixTCPConn struct {
	local  net.Addr
	remote net.Addr
}

func (c *matrixTCPConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *matrixTCPConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *matrixTCPConn) Close() error                     { return nil }
func (c *matrixTCPConn) LocalAddr() net.Addr              { return c.local }
func (c *matrixTCPConn) RemoteAddr() net.Addr             { return c.remote }
func (c *matrixTCPConn) SetDeadline(time.Time) error      { return nil }
func (c *matrixTCPConn) SetReadDeadline(time.Time) error  { return nil }
func (c *matrixTCPConn) SetWriteDeadline(time.Time) error { return nil }

func isUDPNetworkName(network string) bool {
	return strings.HasPrefix(network, "udp")
}

func isTCPNetworkName(network string) bool {
	return strings.HasPrefix(network, "tcp")
}

func gatherAndCollectCandidates(t *testing.T, agent *Agent) []Candidate {
	t.Helper()

	var (
		mu         sync.Mutex
		candidates []Candidate
		done       = make(chan struct{})
	)

	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(done)

			return
		}

		mu.Lock()
		candidates = append(candidates, c)
		mu.Unlock()
	}))

	require.NoError(t, agent.GatherCandidates())

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		assert.FailNow(t, "candidate gathering did not finish in time")
	}

	mu.Lock()
	defer mu.Unlock()

	out := make([]Candidate, len(candidates))
	copy(out, candidates)

	return out
}

func TestTransportFilteringRelayMatrix(t *testing.T) { // nolint:cyclop
	defer test.CheckRoutines(t)()

	type testCase struct {
		name string

		allowUDP bool
		allowTCP bool

		networkTypes []NetworkType
		turnScheme   stun.SchemeType
		turnProto    stun.ProtoType
		turnAllowed  []NetworkType

		expectFactoryMinCalls int
		expectRelayCandidate  bool
	}

	testCases := []testCase{
		{
			name:                  "tcp-only firewall with udp relay config and TURN/TCP gathers relay",
			allowUDP:              false,
			allowTCP:              true,
			networkTypes:          []NetworkType{NetworkTypeUDP4},
			turnScheme:            stun.SchemeTypeTURN,
			turnProto:             stun.ProtoTypeTCP,
			expectFactoryMinCalls: 1,
			expectRelayCandidate:  true,
		},
		{
			name:                  "udp-only firewall with udp-only config and TURN/UDP gathers relay",
			allowUDP:              true,
			allowTCP:              false,
			networkTypes:          []NetworkType{NetworkTypeUDP4},
			turnScheme:            stun.SchemeTypeTURN,
			turnProto:             stun.ProtoTypeUDP,
			expectFactoryMinCalls: 1,
			expectRelayCandidate:  true,
		},
		{
			name:                  "tcp-only candidate config skips relay gathering",
			allowUDP:              false,
			allowTCP:              true,
			networkTypes:          []NetworkType{NetworkTypeTCP4},
			turnScheme:            stun.SchemeTypeTURN,
			turnProto:             stun.ProtoTypeUDP,
			expectFactoryMinCalls: 0,
			expectRelayCandidate:  false,
		},
		{
			name:                  "udp relay config with TURN/TCP fails when tcp blocked by firewall",
			allowUDP:              true,
			allowTCP:              false,
			networkTypes:          []NetworkType{NetworkTypeUDP4},
			turnScheme:            stun.SchemeTypeTURN,
			turnProto:             stun.ProtoTypeTCP,
			expectFactoryMinCalls: 0,
			expectRelayCandidate:  false,
		},
		{
			name:                  "tcp-only firewall with udp relay config and TURNS/TCP gathers relay",
			allowUDP:              false,
			allowTCP:              true,
			networkTypes:          []NetworkType{NetworkTypeUDP4},
			turnScheme:            stun.SchemeTypeTURNS,
			turnProto:             stun.ProtoTypeTCP,
			expectFactoryMinCalls: 1,
			expectRelayCandidate:  true,
		},
		{
			name:                  "tcp-only config with TURN URL without transport param defaults to UDP and is filtered",
			allowUDP:              false,
			allowTCP:              true,
			networkTypes:          []NetworkType{NetworkTypeTCP4},
			turnScheme:            stun.SchemeTypeTURN,
			turnProto:             stun.ProtoTypeUnknown,
			expectFactoryMinCalls: 0,
			expectRelayCandidate:  false,
		},
		{
			name:                  "udp-only config with TURN URL without transport param defaults to UDP and gathers relay",
			allowUDP:              true,
			allowTCP:              false,
			networkTypes:          []NetworkType{NetworkTypeUDP4},
			turnScheme:            stun.SchemeTypeTURN,
			turnProto:             stun.ProtoTypeUnknown,
			expectFactoryMinCalls: 1,
			expectRelayCandidate:  true,
		},
		{
			name:                  "udp relay config with TURNS URL without transport param defaults to TCP and gathers relay",
			allowUDP:              false,
			allowTCP:              true,
			networkTypes:          []NetworkType{NetworkTypeUDP4},
			turnScheme:            stun.SchemeTypeTURNS,
			turnProto:             stun.ProtoTypeUnknown,
			expectFactoryMinCalls: 1,
			expectRelayCandidate:  true,
		},
		{
			name:                  "udp-only config with TURNS URL without transport param defaults to TCP and is filtered",
			allowUDP:              true,
			allowTCP:              false,
			networkTypes:          []NetworkType{NetworkTypeUDP4},
			turnScheme:            stun.SchemeTypeTURNS,
			turnProto:             stun.ProtoTypeUnknown,
			expectFactoryMinCalls: 0,
			expectRelayCandidate:  false,
		},
		{
			name:                  "TURN/TCP URL blocked by TURN transport option allowing UDP only",
			allowUDP:              false,
			allowTCP:              true,
			networkTypes:          []NetworkType{NetworkTypeUDP4},
			turnScheme:            stun.SchemeTypeTURN,
			turnProto:             stun.ProtoTypeTCP,
			turnAllowed:           []NetworkType{NetworkTypeUDP4},
			expectFactoryMinCalls: 0,
			expectRelayCandidate:  false,
		},
		{
			name:                  "TURNS default TCP allowed by TURN transport option",
			allowUDP:              false,
			allowTCP:              true,
			networkTypes:          []NetworkType{NetworkTypeUDP4},
			turnScheme:            stun.SchemeTypeTURNS,
			turnProto:             stun.ProtoTypeUnknown,
			turnAllowed:           []NetworkType{NetworkTypeTCP4},
			expectFactoryMinCalls: 1,
			expectRelayCandidate:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			netFW := newFirewallNet(tc.allowUDP, tc.allowTCP)
			proxyDialer := &firewallProxyDialer{allowTCP: tc.allowTCP}

			url := &stun.URI{
				Scheme:   tc.turnScheme,
				Proto:    tc.turnProto,
				Host:     "turn.example.com",
				Port:     3478,
				Username: "user",
				Password: "pass",
			}

			opts := []AgentOption{
				WithNet(netFW),
				WithProxyDialer(proxy.Dialer(proxyDialer)),
				WithCandidateTypes([]CandidateType{CandidateTypeRelay}),
				WithNetworkTypes(tc.networkTypes),
				WithMulticastDNSMode(MulticastDNSModeDisabled),
				WithIncludeLoopback(),
				WithUrls([]*stun.URI{url}),
			}
			if len(tc.turnAllowed) > 0 {
				opts = append(opts, WithTURNTransportProtocols(tc.turnAllowed))
			}

			agent, err := NewAgentWithOptions(
				opts...,
			)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, agent.Close())
			}()

			factoryCalls := 0
			agent.turnClientFactory = func(*turn.ClientConfig) (turnClient, error) {
				factoryCalls++

				return &stubTurnClient{
					relayConn: newStubPacketConn(&net.UDPAddr{IP: net.IPv4(203, 0, 113, 50), Port: 6000}),
				}, nil
			}

			candidates := gatherAndCollectCandidates(t, agent)
			relayCandidates := 0
			for _, c := range candidates {
				if c.Type() == CandidateTypeRelay {
					relayCandidates++
					require.True(t, c.NetworkType().IsUDP(), "relay endpoint must be UDP")
				}
			}

			require.GreaterOrEqual(t, factoryCalls, tc.expectFactoryMinCalls)
			if tc.expectRelayCandidate {
				require.Greater(t, relayCandidates, 0)
			} else {
				require.Equal(t, 0, relayCandidates)
			}

			_, _, _, dialTCP := netFW.counts()
			if effectiveURLProtoType(*url) == stun.ProtoTypeTCP { // nolint:nestif
				tcpAllowedByOption := len(tc.turnAllowed) == 0
				if !tcpAllowedByOption {
					for _, nt := range tc.turnAllowed {
						if nt.IsTCP() {
							tcpAllowedByOption = true

							break
						}
					}
				}

				if tcpAllowedByOption {
					require.Greater(t, proxyDialer.count()+dialTCP, 0, "expected TURN/TCP dial attempt")
				} else {
					require.Equal(t, 0, proxyDialer.count()+dialTCP, "unexpected TURN/TCP dial attempt")
				}
			}
		})
	}
}

func TestTransportFilteringSrflxMatrix(t *testing.T) {
	defer test.CheckRoutines(t)()

	serverPort := randomPort(t)
	serverListener, err := net.ListenPacket("udp4", localhostIPStr+":"+strconv.Itoa(serverPort)) // nolint: noctx
	require.NoError(t, err)
	defer func() {
		_ = serverListener.Close()
	}()

	server, err := turn.NewServer(turn.ServerConfig{
		Realm:       "pion.ly",
		AuthHandler: optimisticAuthHandler,
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn:            serverListener,
			RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: localhostIPStr},
		}},
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, server.Close())
	}()

	type testCase struct {
		name         string
		allowUDP     bool
		networkTypes []NetworkType
		turnScheme   stun.SchemeType
		turnProto    stun.ProtoType
		expectSrflx  bool
	}

	testCases := []testCase{
		{
			name:         "udp allowed and udp config with TURN/UDP gathers srflx",
			allowUDP:     true,
			networkTypes: []NetworkType{NetworkTypeUDP4},
			turnScheme:   stun.SchemeTypeTURN,
			turnProto:    stun.ProtoTypeUDP,
			expectSrflx:  true,
		},
		{
			name:         "tcp-only config with TURN/TCP does not gather srflx",
			allowUDP:     false,
			networkTypes: []NetworkType{NetworkTypeTCP4},
			turnScheme:   stun.SchemeTypeTURN,
			turnProto:    stun.ProtoTypeTCP,
			expectSrflx:  false,
		},
		{
			name:         "udp config with TURN/TCP transport is filtered for srflx",
			allowUDP:     true,
			networkTypes: []NetworkType{NetworkTypeUDP4},
			turnScheme:   stun.SchemeTypeTURN,
			turnProto:    stun.ProtoTypeTCP,
			expectSrflx:  false,
		},
		{
			name:         "udp config with TURN URL without transport param defaults to UDP and gathers srflx",
			allowUDP:     true,
			networkTypes: []NetworkType{NetworkTypeUDP4},
			turnScheme:   stun.SchemeTypeTURN,
			turnProto:    stun.ProtoTypeUnknown,
			expectSrflx:  true,
		},
		{
			name:         "udp config with TURNS URL without transport param defaults to TCP and is filtered for srflx",
			allowUDP:     true,
			networkTypes: []NetworkType{NetworkTypeUDP4},
			turnScheme:   stun.SchemeTypeTURNS,
			turnProto:    stun.ProtoTypeUnknown,
			expectSrflx:  false,
		},
		{
			name:         "udp config with explicit TURNS/TCP is filtered for srflx",
			allowUDP:     true,
			networkTypes: []NetworkType{NetworkTypeUDP4},
			turnScheme:   stun.SchemeTypeTURNS,
			turnProto:    stun.ProtoTypeTCP,
			expectSrflx:  false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			netFW := newFirewallNet(tc.allowUDP, true)
			url := &stun.URI{
				Scheme:   tc.turnScheme,
				Proto:    tc.turnProto,
				Host:     localhostIPStr,
				Port:     serverPort,
				Username: "user",
				Password: "pass",
			}

			agent, err := NewAgentWithOptions(
				WithNet(netFW),
				WithCandidateTypes([]CandidateType{CandidateTypeServerReflexive}),
				WithNetworkTypes(tc.networkTypes),
				WithMulticastDNSMode(MulticastDNSModeDisabled),
				WithIncludeLoopback(),
				WithSTUNGatherTimeout(200*time.Millisecond),
				WithUrls([]*stun.URI{url}),
			)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, agent.Close())
			}()

			candidates := gatherAndCollectCandidates(t, agent)
			srflxCandidates := 0
			for _, c := range candidates {
				if c.Type() == CandidateTypeServerReflexive {
					srflxCandidates++
				}
			}

			if tc.expectSrflx {
				require.Greater(t, srflxCandidates, 0)
			} else {
				require.Equal(t, 0, srflxCandidates)
			}
		})
	}
}

func TestTransportFilteringHostMatrix(t *testing.T) {
	defer test.CheckRoutines(t)()

	type testCase struct {
		name         string
		networkTypes []NetworkType
		allowUDP     bool
		useTCPMux    bool
		expectHost   bool
		expectTCP    bool
	}

	testCases := []testCase{
		{
			name:         "udp config gathers udp host candidate",
			networkTypes: []NetworkType{NetworkTypeUDP4},
			allowUDP:     true,
			useTCPMux:    false,
			expectHost:   true,
			expectTCP:    false,
		},
		{
			name:         "tcp config gathers tcp host candidate with tcp mux",
			networkTypes: []NetworkType{NetworkTypeTCP4},
			allowUDP:     false,
			useTCPMux:    true,
			expectHost:   true,
			expectTCP:    true,
		},
		{
			name:         "tcp config without tcp mux yields no host candidate",
			networkTypes: []NetworkType{NetworkTypeTCP4},
			allowUDP:     false,
			useTCPMux:    false,
			expectHost:   false,
			expectTCP:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			netFW := newFirewallNet(tc.allowUDP, true)

			opts := []AgentOption{
				WithNet(netFW),
				WithCandidateTypes([]CandidateType{CandidateTypeHost}),
				WithNetworkTypes(tc.networkTypes),
				WithMulticastDNSMode(MulticastDNSModeDisabled),
				WithIncludeLoopback(),
			}
			if tc.useTCPMux {
				opts = append(opts, WithTCPMux(&boundTCPMux{
					localAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 34567},
				}))
			}

			agent, err := NewAgentWithOptions(opts...)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, agent.Close())
			}()

			candidates := gatherAndCollectCandidates(t, agent)
			hostCandidates := 0
			tcpHosts := 0
			for _, c := range candidates {
				if c.Type() != CandidateTypeHost {
					continue
				}

				hostCandidates++
				if c.NetworkType().IsTCP() {
					tcpHosts++
				}
			}

			if tc.expectHost {
				require.Greater(t, hostCandidates, 0)
			} else {
				require.Equal(t, 0, hostCandidates)
			}

			if tc.expectTCP {
				require.Greater(t, tcpHosts, 0)
			}
		})
	}
}

func TestTransportFilteringRelayTCPOnlyFirewallUDPRelayConfigTURNTCP(t *testing.T) {
	defer test.CheckRoutines(t)()

	netFW := newFirewallNet(false, true)
	proxyDialer := &firewallProxyDialer{allowTCP: true}

	agent, err := NewAgentWithOptions(
		WithNet(netFW),
		WithProxyDialer(proxy.Dialer(proxyDialer)),
		WithCandidateTypes([]CandidateType{CandidateTypeRelay}),
		WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
		WithMulticastDNSMode(MulticastDNSModeDisabled),
		WithIncludeLoopback(),
		WithUrls([]*stun.URI{{
			Scheme:   stun.SchemeTypeTURN,
			Proto:    stun.ProtoTypeTCP,
			Host:     "turn.example.com",
			Port:     3478,
			Username: "user",
			Password: "pass",
		}}),
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	factoryCalls := 0
	agent.turnClientFactory = func(*turn.ClientConfig) (turnClient, error) {
		factoryCalls++

		return &stubTurnClient{
			relayConn: newStubPacketConn(&net.UDPAddr{IP: net.IPv4(203, 0, 113, 77), Port: 6100}),
		}, nil
	}

	candidates := gatherAndCollectCandidates(t, agent)

	require.GreaterOrEqual(t, proxyDialer.count(), 1)
	require.GreaterOrEqual(t, factoryCalls, 1)

	relayCandidates := 0
	for _, c := range candidates {
		if c.Type() != CandidateTypeRelay {
			continue
		}
		relayCandidates++
		require.True(t, c.NetworkType().IsUDP())
	}
	require.Greater(t, relayCandidates, 0)
}
