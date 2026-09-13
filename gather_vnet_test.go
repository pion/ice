// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v4"
	"github.com/pion/transport/v4/test"
	"github.com/pion/transport/v4/vnet"
	"github.com/pion/turn/v5"
	"github.com/stretchr/testify/require"
)

func TestVNetGather(t *testing.T) { //nolint:cyclop
	defer test.CheckRoutines(t)()

	loggerFactory := logging.NewDefaultLoggerFactory()

	t.Run("No local IP address", func(t *testing.T) {
		n, err := vnet.NewNet(&vnet.NetConfig{})
		require.NoError(t, err)

		a, err := NewAgent(&AgentConfig{Net: n})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, a.Close())
		}()

		_, localIPs, err := localInterfaces(a.net, a.interfaceFilter, a.ipFilter, []NetworkType{NetworkTypeUDP4}, false)
		require.Len(t, localIPs, 0)
		require.NoError(t, err)
	})

	t.Run("Gather a dynamic IP address", func(t *testing.T) {
		cider := "1.2.3.0/24"
		_, ipNet, err := net.ParseCIDR(cider)
		require.NoError(t, err)

		router, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: cider, LoggerFactory: loggerFactory})
		require.NoError(t, err)

		nw, err := vnet.NewNet(&vnet.NetConfig{})
		require.NoError(t, err)

		require.NoError(t, router.AddNet(nw))

		a, err := NewAgent(&AgentConfig{Net: nw})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, a.Close())
		}()

		_, localAddrs, err := localInterfaces(a.net, a.interfaceFilter, a.ipFilter, []NetworkType{NetworkTypeUDP4}, false)
		require.Len(t, localAddrs, 1)
		require.NoError(t, err)

		for _, addr := range localAddrs {
			require.False(t, addr.addr.IsLoopback())
			require.True(t, ipNet.Contains(addr.addr.AsSlice()))
		}
	})

	t.Run("listenUDP", func(t *testing.T) {
		router, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "1.2.3.0/24", LoggerFactory: loggerFactory})
		require.NoError(t, err)

		nw, err := vnet.NewNet(&vnet.NetConfig{})
		require.NoError(t, err)

		require.NoError(t, router.AddNet(nw))

		agent, err := NewAgent(&AgentConfig{Net: nw})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		_, localAddrs, err := localInterfaces(agent.net, agent.interfaceFilter, agent.ipFilter, []NetworkType{NetworkTypeUDP4}, false)
		require.NotEqual(t, 0, len(localAddrs))
		require.NoError(t, err)

		ip := localAddrs[0].addr.AsSlice()

		conn, err := listenUDPInPortRange(agent.net, agent.log, 0, 0, udp, &net.UDPAddr{IP: ip, Port: 0})
		require.NoError(t, err)
		require.NotNil(t, conn)
		require.NoError(t, conn.Close())

		_, err = listenUDPInPortRange(agent.net, agent.log, 4999, 5000, udp, &net.UDPAddr{IP: ip, Port: 0})
		require.ErrorIs(t, ErrPort, err)

		conn, err = listenUDPInPortRange(agent.net, agent.log, 5000, 5000, udp, &net.UDPAddr{IP: ip, Port: 0})
		require.NoError(t, err)
		require.NotNil(t, conn)
		defer func() {
			require.NoError(t, conn.Close())
		}()

		_, port, err := net.SplitHostPort(conn.LocalAddr().String())

		require.NoError(t, err)
		require.Equal(t, "5000", port)
	})
}

func gatherForRewriteTest(t *testing.T, agent *Agent) []Candidate {
	t.Helper()

	done := make(chan struct{})
	var mu sync.Mutex
	var emitted []string
	require.NoError(t, agent.OnCandidate(func(candidate Candidate) {
		mu.Lock()
		defer mu.Unlock()
		if candidate == nil {
			close(done)

			return
		}
		emitted = append(emitted, candidate.Marshal())
	}))
	require.NoError(t, agent.GatherCandidates())
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "candidate gathering did not complete")
	}
	candidates, err := agent.GetLocalCandidates()
	require.NoError(t, err)
	stored := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		stored = append(stored, candidate.Marshal())
	}
	mu.Lock()
	defer mu.Unlock()
	require.ElementsMatch(t, emitted, stored, "callbacks must report every stored candidate")

	return candidates
}

func TestVNetGatherNAT1To1SocketAddresses(t *testing.T) {
	defer test.CheckRoutines(t)()

	router, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "10.0.0.0/24", LoggerFactory: logging.NewDefaultLoggerFactory()})
	require.NoError(t, err)
	nw, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{"10.0.0.1", "10.0.0.2"}})
	require.NoError(t, err)
	require.NoError(t, router.AddNet(nw))

	agent, err := NewAgent(&AgentConfig{Net: nw, NetworkTypes: []NetworkType{NetworkTypeUDP4}, CandidateTypes: []CandidateType{CandidateTypeHost}, MulticastDNSMode: MulticastDNSModeDisabled, NAT1To1IPs: []string{"1.2.3.4/10.0.0.1", "1.2.3.5/10.0.0.2"}})
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	candidates := gatherForRewriteTest(t, agent)
	require.Len(t, candidates, 2)
	want := map[string]string{"1.2.3.4": "10.0.0.1", "1.2.3.5": "10.0.0.2"}
	for _, candidate := range candidates {
		host, ok := candidate.(*CandidateHost)
		require.True(t, ok)
		localAddr, ok := host.conn.LocalAddr().(*net.UDPAddr)
		require.True(t, ok)
		require.Contains(t, want, candidate.Address())
		require.Equal(t, want[candidate.Address()], localAddr.IP.String())
		require.Equal(t, localAddr.Port, candidate.Port())
		delete(want, candidate.Address())
	}
	require.Empty(t, want)
}

func TestGatherAddressRewriteSrflxModes(t *testing.T) {
	defer test.CheckRoutines(t)()

	router, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "10.0.0.0/24", LoggerFactory: logging.NewDefaultLoggerFactory()})
	require.NoError(t, err)
	nw, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{"10.0.0.1"}})
	require.NoError(t, err)
	require.NoError(t, router.AddNet(nw))

	for _, testCase := range []struct {
		name string
		mode AddressRewriteMode
	}{
		{name: "append", mode: AddressRewriteAppend},
		{name: "replace", mode: AddressRewriteReplace},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			mux := newMockUniversalUDPMux([]net.Addr{&net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 2345}}, &stun.XORMappedAddress{IP: net.ParseIP("198.51.100.10"), Port: 5000})
			agent, err := NewAgentWithOptions(
				WithNet(nw),
				WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
				WithCandidateTypes([]CandidateType{CandidateTypeServerReflexive}),
				WithMulticastDNSMode(MulticastDNSModeDisabled),
				WithUDPMuxSrflx(mux),
				WithUrls([]*stun.URI{{Scheme: stun.SchemeTypeSTUN, Host: "127.0.0.1", Port: 3478}}),
				WithAddressRewriteRules(AddressRewriteRule{External: []string{"203.0.113.50"}, AsCandidateType: CandidateTypeServerReflexive, Mode: testCase.mode}),
			)
			require.NoError(t, err)
			defer func() { require.NoError(t, agent.Close()) }()

			candidates := gatherForRewriteTest(t, agent)
			addresses := make([]string, 0, len(candidates))
			for _, candidate := range candidates {
				require.Equal(t, CandidateTypeServerReflexive, candidate.Type())
				addresses = append(addresses, candidate.Address())
			}
			expected := []string{"203.0.113.50"}
			if testCase.mode == AddressRewriteAppend {
				expected = append(expected, "198.51.100.10")
				require.Equal(t, 1, mux.connCount(), "append must still use STUN")
			} else {
				require.Zero(t, mux.connCount(), "replace must skip STUN")
			}
			require.ElementsMatch(t, expected, addresses)
		})
	}
}

func TestVNetGatherWithInterfaceFilter(t *testing.T) {
	defer test.CheckRoutines(t)()

	loggerFactory := logging.NewDefaultLoggerFactory()
	router, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "1.2.3.0/24", LoggerFactory: loggerFactory})
	require.NoError(t, err)

	nw, err := vnet.NewNet(&vnet.NetConfig{})
	require.NoError(t, err)
	require.NoError(t, router.AddNet(nw))

	t.Run("InterfaceFilter should exclude the interface", func(t *testing.T) {
		agent, err := NewAgent(&AgentConfig{
			Net: nw,
			InterfaceFilter: func(interfaceName string) (keep bool) {
				require.Equal(t, "eth0", interfaceName)

				return false
			},
		})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		_, localIPs, err := localInterfaces(agent.net, agent.interfaceFilter, agent.ipFilter, []NetworkType{NetworkTypeUDP4}, false)
		require.NoError(t, err)
		require.Len(t, localIPs, 0)
	})

	t.Run("IPFilter should exclude the IP", func(t *testing.T) {
		agent, err := NewAgent(&AgentConfig{
			Net: nw,
			IPFilter: func(ip net.IP) (keep bool) {
				require.Equal(t, net.IP{1, 2, 3, 1}, ip)

				return false
			},
		})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		_, localIPs, err := localInterfaces(agent.net, agent.interfaceFilter, agent.ipFilter, []NetworkType{NetworkTypeUDP4}, false)
		require.NoError(t, err)
		require.Len(t, localIPs, 0)
	})

	t.Run("InterfaceFilter should not exclude the interface", func(t *testing.T) {
		agent, err := NewAgent(&AgentConfig{
			Net: nw,
			InterfaceFilter: func(interfaceName string) (keep bool) {
				require.Equal(t, "eth0", interfaceName)

				return true
			},
		})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		_, localIPs, err := localInterfaces(agent.net, agent.interfaceFilter, agent.ipFilter, []NetworkType{NetworkTypeUDP4}, false)
		require.NoError(t, err)
		require.Len(t, localIPs, 1)
	})
}

func TestGatherRelayWithVNet(t *testing.T) {
	defer test.CheckRoutines(t)()

	loggerFactory := logging.NewDefaultLoggerFactory()

	router, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "10.0.0.0/24", LoggerFactory: loggerFactory})
	require.NoError(t, err)

	clientNet, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{"10.0.0.2"}})
	require.NoError(t, err)

	serverNet, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{"10.0.0.3"}})
	require.NoError(t, err)

	require.NoError(t, router.AddNet(clientNet))
	require.NoError(t, router.AddNet(serverNet))
	require.NoError(t, router.Start())
	defer func() {
		require.NoError(t, router.Stop())
	}()

	turnAddr := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 3), Port: 3478}
	serverConn, err := serverNet.ListenPacket("udp4", turnAddr.String())
	require.NoError(t, err)

	relayGenerator := &turn.RelayAddressGeneratorStatic{RelayAddress: turnAddr.IP, Address: turnAddr.IP.String(), Net: serverNet}

	const (
		turnRealm = "pion.ly"
		turnUser  = "user"
		turnPass  = "pass"
	)

	server, err := turn.NewServer(turn.ServerConfig{
		LoggerFactory:     loggerFactory,
		Realm:             turnRealm,
		PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: serverConn, RelayAddressGenerator: relayGenerator}},
		AuthHandler: func(ra *turn.RequestAttributes) (userID string, key []byte, ok bool) {
			if ra.Username != turnUser {
				return "", nil, false
			}

			return ra.Username, turn.GenerateAuthKey(ra.Username, ra.Realm, turnPass), true
		},
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, server.Close())
	}()

	agent, err := NewAgentWithOptions(
		WithNet(clientNet),
		WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
		WithCandidateTypes([]CandidateType{CandidateTypeRelay}),
		WithUrls([]*stun.URI{{Scheme: stun.SchemeTypeTURN, Host: turnAddr.IP.String(), Port: turnAddr.Port, Username: turnUser, Password: turnPass, Proto: stun.ProtoTypeUDP}}),
		WithMulticastDNSMode(MulticastDNSModeDisabled),
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	relayCandidates := make(chan Candidate, 1)
	done := make(chan struct{})
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(done)

			return
		}

		if c.Type() == CandidateTypeRelay {
			select {
			case relayCandidates <- c:
			default:
			}
		}
	}))

	require.NoError(t, agent.GatherCandidates())

	select {
	case cand := <-relayCandidates:
		require.Equal(t, CandidateTypeRelay, cand.Type())
		require.Equal(t, "10.0.0.3", cand.Address())
	case <-done:
		require.Fail(t, "gathering finished without relay candidate")
	case <-time.After(5 * time.Second):
		require.Fail(t, "timeout waiting for relay candidate")
	}
}

func TestVNetGather_TURNConnectionLeak(t *testing.T) {
	defer test.CheckRoutines(t)()

	turnServerURL := &stun.URI{Scheme: stun.SchemeTypeTURN, Host: vnetSTUNServerIP, Port: vnetSTUNServerPort, Username: "user", Password: "pass", Proto: stun.ProtoTypeUDP}

	// buildVNet with a Symmetric NATs for both LANs
	natType := &vnet.NATType{MappingBehavior: vnet.EndpointAddrPortDependent, FilteringBehavior: vnet.EndpointAddrPortDependent}
	v, err := buildVNet(natType, natType)

	require.NoError(t, err, "should succeed")
	defer v.close()

	cfg0 := &AgentConfig{Urls: []*stun.URI{turnServerURL}, NetworkTypes: supportedNetworkTypes(), MulticastDNSMode: MulticastDNSModeDisabled, NAT1To1IPs: []string{vnetGlobalIPA}, Net: v.net0}
	aAgent, err := NewAgent(cfg0)
	require.NoError(t, err, "should succeed")
	defer func() {
		// Assert relay conn leak on close.
		require.NoError(t, aAgent.Close())
	}()

	aAgent.gatherCandidatesRelay(context.Background(), []*stun.URI{turnServerURL}, aAgent.gatherGeneration)
}
