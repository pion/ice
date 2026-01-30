// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v3"
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

		a, err := NewAgent(&AgentConfig{
			Net: n,
		})
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

		router, err := vnet.NewRouter(&vnet.RouterConfig{
			CIDR:          cider,
			LoggerFactory: loggerFactory,
		})
		require.NoError(t, err)

		nw, err := vnet.NewNet(&vnet.NetConfig{})
		require.NoError(t, err)

		require.NoError(t, router.AddNet(nw))

		a, err := NewAgent(&AgentConfig{
			Net: nw,
		})
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
		router, err := vnet.NewRouter(&vnet.RouterConfig{
			CIDR:          "1.2.3.0/24",
			LoggerFactory: loggerFactory,
		})
		require.NoError(t, err)

		nw, err := vnet.NewNet(&vnet.NetConfig{})
		require.NoError(t, err)

		require.NoError(t, router.AddNet(nw))

		agent, err := NewAgent(&AgentConfig{Net: nw})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		_, localAddrs, err := localInterfaces(
			agent.net,
			agent.interfaceFilter,
			agent.ipFilter,
			[]NetworkType{NetworkTypeUDP4},
			false,
		)
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

func TestVNetGatherWithNAT1To1(t *testing.T) { //nolint:cyclop
	defer test.CheckRoutines(t)()

	loggerFactory := logging.NewDefaultLoggerFactory()
	log := loggerFactory.NewLogger("test")

	t.Run("gather 1:1 NAT external IPs as host candidates", func(t *testing.T) {
		externalIP0 := "1.2.3.4"
		externalIP1 := "1.2.3.5"
		localIP0 := "10.0.0.1"
		localIP1 := "10.0.0.2"
		map0 := fmt.Sprintf("%s/%s", externalIP0, localIP0)
		map1 := fmt.Sprintf("%s/%s", externalIP1, localIP1)

		wan, err := vnet.NewRouter(&vnet.RouterConfig{
			CIDR:          "1.2.3.0/24",
			LoggerFactory: loggerFactory,
		})
		require.NoError(t, err, "should succeed")

		lan, err := vnet.NewRouter(&vnet.RouterConfig{
			CIDR:      "10.0.0.0/24",
			StaticIPs: []string{map0, map1},
			NATType: &vnet.NATType{
				Mode: vnet.NATModeNAT1To1,
			},
			LoggerFactory: loggerFactory,
		})
		require.NoError(t, err, "should succeed")

		err = wan.AddRouter(lan)
		require.NoError(t, err, "should succeed")

		nw, err := vnet.NewNet(&vnet.NetConfig{
			StaticIPs: []string{localIP0, localIP1},
		})
		require.NoError(t, err)

		err = lan.AddNet(nw)
		require.NoError(t, err, "should succeed")

		agent, err := NewAgent(&AgentConfig{
			NetworkTypes: []NetworkType{
				NetworkTypeUDP4,
			},
			NAT1To1IPs: []string{map0, map1},
			Net:        nw,
		})
		require.NoError(t, err, "should succeed")
		defer func() {
			require.NoError(t, agent.Close())
		}()

		done := make(chan struct{})
		err = agent.OnCandidate(func(c Candidate) {
			if c == nil {
				close(done)
			}
		})
		require.NoError(t, err, "should succeed")

		err = agent.GatherCandidates()
		require.NoError(t, err, "should succeed")

		log.Debug("Wait until gathering is complete...")
		<-done
		log.Debug("Gathering is done")

		candidates, err := agent.GetLocalCandidates()
		require.NoError(t, err, "should succeed")

		require.Len(t, candidates, 2)

		lAddr := [2]*net.UDPAddr{nil, nil}
		for i, candi := range candidates {
			lAddr[i] = candi.(*CandidateHost).conn.LocalAddr().(*net.UDPAddr) //nolint:forcetypeassert
			require.Equal(t, candi.Port(), lAddr[i].Port)
		}

		if candidates[0].Address() == externalIP0 { //nolint:nestif
			require.Equal(t, candidates[1].Address(), externalIP1)
			require.Equal(t, lAddr[0].IP.String(), localIP0)
			require.Equal(t, lAddr[1].IP.String(), localIP1)
		} else if candidates[0].Address() == externalIP1 {
			require.Equal(t, candidates[1].Address(), externalIP0)
			require.Equal(t, lAddr[0].IP.String(), localIP1)
			require.Equal(t, lAddr[1].IP.String(), localIP0)
		}
	})

	t.Run("gather 1:1 NAT external IPs as srflx candidates", func(t *testing.T) {
		wan, err := vnet.NewRouter(&vnet.RouterConfig{
			CIDR:          "1.2.3.0/24",
			LoggerFactory: loggerFactory,
		})
		require.NoError(t, err, "should succeed")

		lan, err := vnet.NewRouter(&vnet.RouterConfig{
			CIDR: "10.0.0.0/24",
			StaticIPs: []string{
				"1.2.3.4/10.0.0.1",
			},
			NATType: &vnet.NATType{
				Mode: vnet.NATModeNAT1To1,
			},
			LoggerFactory: loggerFactory,
		})
		require.NoError(t, err, "should succeed")

		err = wan.AddRouter(lan)
		require.NoError(t, err, "should succeed")

		nw, err := vnet.NewNet(&vnet.NetConfig{
			StaticIPs: []string{
				"10.0.0.1",
			},
		})
		require.NoError(t, err)

		err = lan.AddNet(nw)
		require.NoError(t, err, "should succeed")

		agent, err := NewAgent(&AgentConfig{
			NetworkTypes: []NetworkType{
				NetworkTypeUDP4,
			},
			NAT1To1IPs: []string{
				"1.2.3.4",
			},
			NAT1To1IPCandidateType: CandidateTypeServerReflexive,
			Net:                    nw,
		})
		require.NoError(t, err, "should succeed")
		defer func() {
			require.NoError(t, agent.Close())
		}()

		done := make(chan struct{})
		err = agent.OnCandidate(func(c Candidate) {
			if c == nil {
				close(done)
			}
		})
		require.NoError(t, err, "should succeed")

		err = agent.GatherCandidates()
		require.NoError(t, err, "should succeed")

		log.Debug("Wait until gathering is complete...")
		<-done
		log.Debug("Gathering is done")

		candidates, err := agent.GetLocalCandidates()
		require.NoError(t, err, "should succeed")

		require.Len(t, candidates, 2)

		var candiHost *CandidateHost
		var candiSrflx *CandidateServerReflexive

		for _, candidate := range candidates {
			switch candi := candidate.(type) {
			case *CandidateHost:
				candiHost = candi
			case *CandidateServerReflexive:
				candiSrflx = candi
			default:
				t.Fatal("Unexpected candidate type") // nolint
			}
		}

		require.NotNil(t, candiHost, "should not be nil")
		require.Equal(t, "10.0.0.1", candiHost.Address(), "should match")
		require.NotNil(t, candiSrflx, "should not be nil")
		require.Equal(t, "1.2.3.4", candiSrflx.Address(), "should match")
	})
}

func TestVNetGatherWithInterfaceFilter(t *testing.T) {
	defer test.CheckRoutines(t)()

	loggerFactory := logging.NewDefaultLoggerFactory()
	router, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "1.2.3.0/24",
		LoggerFactory: loggerFactory,
	})
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

		_, localIPs, err := localInterfaces(
			agent.net,
			agent.interfaceFilter,
			agent.ipFilter,
			[]NetworkType{NetworkTypeUDP4},
			false,
		)
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

		_, localIPs, err := localInterfaces(
			agent.net,
			agent.interfaceFilter,
			agent.ipFilter,
			[]NetworkType{NetworkTypeUDP4},
			false,
		)
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

		_, localIPs, err := localInterfaces(
			agent.net,
			agent.interfaceFilter,
			agent.ipFilter,
			[]NetworkType{NetworkTypeUDP4},
			false,
		)
		require.NoError(t, err)
		require.Len(t, localIPs, 1)
	})
}

func TestGatherRelayWithVNet(t *testing.T) {
	defer test.CheckRoutines(t)()

	loggerFactory := logging.NewDefaultLoggerFactory()

	router, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "10.0.0.0/24",
		LoggerFactory: loggerFactory,
	})
	require.NoError(t, err)

	clientNet, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{"10.0.0.2"},
	})
	require.NoError(t, err)

	serverNet, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{"10.0.0.3"},
	})
	require.NoError(t, err)

	require.NoError(t, router.AddNet(clientNet))
	require.NoError(t, router.AddNet(serverNet))
	require.NoError(t, router.Start())
	defer func() {
		require.NoError(t, router.Stop())
	}()

	turnAddr := &net.UDPAddr{
		IP:   net.IPv4(10, 0, 0, 3),
		Port: 3478,
	}
	serverConn, err := serverNet.ListenPacket("udp4", turnAddr.String())
	require.NoError(t, err)

	relayGenerator := &turn.RelayAddressGeneratorStatic{
		RelayAddress: turnAddr.IP,
		Address:      turnAddr.IP.String(),
		Net:          serverNet,
	}

	const (
		turnRealm = "pion.ly"
		turnUser  = "user"
		turnPass  = "pass"
	)

	server, err := turn.NewServer(turn.ServerConfig{
		LoggerFactory: loggerFactory,
		Realm:         turnRealm,
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn:            serverConn,
				RelayAddressGenerator: relayGenerator,
			},
		},
		AuthHandler: func(username, realm string, srcAddr net.Addr) ([]byte, bool) {
			if username != turnUser {
				return nil, false
			}

			return turn.GenerateAuthKey(username, realm, turnPass), true
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
		WithUrls([]*stun.URI{
			{
				Scheme:   stun.SchemeTypeTURN,
				Host:     turnAddr.IP.String(),
				Port:     turnAddr.Port,
				Username: turnUser,
				Password: turnPass,
				Proto:    stun.ProtoTypeUDP,
			},
		}),
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

	turnServerURL := &stun.URI{
		Scheme:   stun.SchemeTypeTURN,
		Host:     vnetSTUNServerIP,
		Port:     vnetSTUNServerPort,
		Username: "user",
		Password: "pass",
		Proto:    stun.ProtoTypeUDP,
	}

	// buildVNet with a Symmetric NATs for both LANs
	natType := &vnet.NATType{
		MappingBehavior:   vnet.EndpointAddrPortDependent,
		FilteringBehavior: vnet.EndpointAddrPortDependent,
	}
	v, err := buildVNet(natType, natType)

	require.NoError(t, err, "should succeed")
	defer v.close()

	cfg0 := &AgentConfig{
		Urls: []*stun.URI{
			turnServerURL,
		},
		NetworkTypes:     supportedNetworkTypes(),
		MulticastDNSMode: MulticastDNSModeDisabled,
		NAT1To1IPs:       []string{vnetGlobalIPA},
		Net:              v.net0,
	}
	aAgent, err := NewAgent(cfg0)
	require.NoError(t, err, "should succeed")
	defer func() {
		// Assert relay conn leak on close.
		require.NoError(t, aAgent.Close())
	}()

	aAgent.gatherCandidatesRelay(context.Background(), []*stun.URI{turnServerURL})
}
