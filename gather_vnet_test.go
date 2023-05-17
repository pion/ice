// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/pion/logging"
	"github.com/pion/stun"
	"github.com/pion/transport/v2/test"
	"github.com/pion/transport/v2/vnet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVNetGather(t *testing.T) {
	defer test.CheckRoutines(t)()

	loggerFactory := logging.NewDefaultLoggerFactory()

	t.Run("No local IP address", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)

		n, err := vnet.NewNet(&vnet.NetConfig{})
		require.NoError(err)

		agent, err := NewAgent(&AgentConfig{
			Net: n,
		})
		require.NoError(err)

		localIPs, err := localInterfaces(agent.net, agent.interfaceFilter, agent.ipFilter, []NetworkType{NetworkTypeUDP4}, false)
		require.NoError(err)
		require.Empty(localIPs, "Should return no local IP")

		assert.NoError(agent.Close())
	})

	t.Run("Gather a dynamic IP address", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)

		cider := "1.2.3.0/24"
		_, ipNet, err := net.ParseCIDR(cider)
		require.NoError(err, "Failed to parse CIDR")

		r, err := vnet.NewRouter(&vnet.RouterConfig{
			CIDR:          cider,
			LoggerFactory: loggerFactory,
		})
		require.NoError(err, "Failed to create a router")

		nw, err := vnet.NewNet(&vnet.NetConfig{})
		require.NoError(err, "Failed to create a Net")

		err = r.AddNet(nw)
		require.NoError(err, "Failed to add a Net to the router")

		agent, err := NewAgent(&AgentConfig{
			Net: nw,
		})
		require.NoError(err)

		localIPs, err := localInterfaces(agent.net, agent.interfaceFilter, agent.ipFilter, []NetworkType{NetworkTypeUDP4}, false)
		require.NoError(err)
		require.Len(localIPs, 1, "Should have one local IP")

		for _, ip := range localIPs {
			require.False(ip.IsLoopback(), "Should not return loopback IP")
			require.True(ipNet.Contains(ip), "Should be contained in the CIDR")
		}

		assert.NoError(agent.Close())
	})

	t.Run("listenUDP", func(t *testing.T) {
		require := require.New(t)
		assert := assert.New(t)

		r, err := vnet.NewRouter(&vnet.RouterConfig{
			CIDR:          "1.2.3.0/24",
			LoggerFactory: loggerFactory,
		})
		require.NoError(err, "Failed to create a router")

		nw, err := vnet.NewNet(&vnet.NetConfig{})
		require.NoError(err, "Failed to create a Net")

		err = r.AddNet(nw)
		require.NoError(err, "Failed to add a Net to the router")

		agent, err := NewAgent(&AgentConfig{Net: nw})
		require.NoError(err, "Failed to create agent")

		localIPs, err := localInterfaces(agent.net, agent.interfaceFilter, agent.ipFilter, []NetworkType{NetworkTypeUDP4}, false)
		require.NoError(err)
		require.NotEmpty(localIPs, "localInterfaces found no interfaces, unable to test")

		ip := localIPs[0]

		conn, err := listenUDPInPortRange(agent.net, agent.log, 0, 0, udp, &net.UDPAddr{IP: ip, Port: 0})
		require.NoError(err, "listenUDPInPortRange error with no port restriction")
		require.NotNil(conn, "listenUDPInPortRange error with no port restriction return a nil conn")

		err = conn.Close()
		require.NoError(err, "Failed to close connection")

		_, err = listenUDPInPortRange(agent.net, agent.log, 4999, 5000, udp, &net.UDPAddr{IP: ip, Port: 0})
		require.ErrorIs(err, ErrPort, "listenUDPInPortRange with invalid port range did not return ErrPort")

		conn, err = listenUDPInPortRange(agent.net, agent.log, 5000, 5000, udp, &net.UDPAddr{IP: ip, Port: 0})
		require.NoError(err, "listenUDPInPortRange error with no port restriction")
		require.NotNil(conn, "listenUDPInPortRange error with no port restriction return a nil conn")

		_, port, err := net.SplitHostPort(conn.LocalAddr().String())
		require.NoError(err)
		require.Equal("5000", port, "listenUDPInPortRange with port restriction of 5000 listened on incorrect port")

		assert.NoError(conn.Close())
		assert.NoError(agent.Close())
	})
}

func TestVNetGatherWithNAT1To1(t *testing.T) {
	defer test.CheckRoutines(t)()

	loggerFactory := logging.NewDefaultLoggerFactory()

	t.Run("gather 1:1 NAT external IPs as host candidates", func(t *testing.T) {
		require := require.New(t)

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
		require.NoError(err)

		lan, err := vnet.NewRouter(&vnet.RouterConfig{
			CIDR:      "10.0.0.0/24",
			StaticIPs: []string{map0, map1},
			NATType: &vnet.NATType{
				Mode: vnet.NATModeNAT1To1,
			},
			LoggerFactory: loggerFactory,
		})
		require.NoError(err)

		require.NoError(wan.AddRouter(lan))

		nw, err := vnet.NewNet(&vnet.NetConfig{
			StaticIPs: []string{localIP0, localIP1},
		})
		require.NoError(err, "Failed to create a Net")

		require.NoError(lan.AddNet(nw))

		agent, err := NewAgent(&AgentConfig{
			NetworkTypes: []NetworkType{
				NetworkTypeUDP4,
			},
			NAT1To1IPs: []string{map0, map1},
			Net:        nw,
		})
		require.NoError(err)
		defer agent.Close() //nolint:errcheck

		candidateNotifier, candidatesCh := onCandidateNotifier()
		require.NoError(agent.OnCandidate(candidateNotifier))

		require.NoError(agent.GatherCandidates())

		t.Log("Wait until gathering is complete...")
		for c := range candidatesCh {
			t.Logf("Found candidate: %s", c)
		}
		t.Log("Gathering is done")

		candidates, err := agent.GetLocalCandidates()
		require.NoError(err)
		require.Len(candidates, 2, "There must be two candidates")

		lAddr := [2]*net.UDPAddr{nil, nil}
		for i, cand := range candidates {
			lAddr[i] = cand.(*CandidateHost).conn.LocalAddr().(*net.UDPAddr) //nolint:forcetypeassert
			require.Equal(cand.Port(), lAddr[i].Port, "Unexpected candidate port")
		}

		if candidates[0].Address() == externalIP0 {
			require.Equal(candidates[1].Address(), externalIP1, "Unexpected candidate IP")
			require.Equal(lAddr[0].IP.String(), localIP0, "Unexpected listen IP")
			require.Equal(lAddr[1].IP.String(), localIP1, "Unexpected listen IP")
		} else if candidates[0].Address() == externalIP1 {
			require.Equal(candidates[1].Address(), externalIP0, "Unexpected candidate IP")
			require.Equal(lAddr[0].IP.String(), localIP1, "Unexpected listen IP")
			require.Equal(lAddr[1].IP.String(), localIP0, "Unexpected listen IP")
		}
	})

	t.Run("gather 1:1 NAT external IPs as srflx candidates", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)

		wan, err := vnet.NewRouter(&vnet.RouterConfig{
			CIDR:          "1.2.3.0/24",
			LoggerFactory: loggerFactory,
		})
		require.NoError(err)

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
		require.NoError(err)

		require.NoError(wan.AddRouter(lan))

		nw, err := vnet.NewNet(&vnet.NetConfig{
			StaticIPs: []string{
				"10.0.0.1",
			},
		})
		require.NoError(err, "Failed to create a Net")

		require.NoError(lan.AddNet(nw))

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
		require.NoError(err)
		defer agent.Close() //nolint:errcheck

		candidateNotifier, candidatesCh := onCandidateNotifier()
		require.NoError(agent.OnCandidate(candidateNotifier))

		require.NoError(agent.GatherCandidates())

		t.Log("Wait until gathering is complete...")
		for c := range candidatesCh {
			t.Logf("Found candidate: %s", c)
		}
		t.Log("Gathering is done")

		candidates, err := agent.GetLocalCandidates()
		require.NoError(err)
		require.Len(candidates, 2, "Expected two candidates")

		var candiHost *CandidateHost
		var candiSrflx *CandidateServerReflexive

		for _, candidate := range candidates {
			switch candi := candidate.(type) {
			case *CandidateHost:
				candiHost = candi
			case *CandidateServerReflexive:
				candiSrflx = candi
			default:
				require.Fail("Unexpected candidate type")
			}
		}

		assert.NotNil(candiHost)
		assert.Equal("10.0.0.1", candiHost.Address())
		assert.NotNil(candiSrflx)
		assert.Equal("1.2.3.4", candiSrflx.Address())
	})
}

func TestVNetGatherWithInterfaceFilter(t *testing.T) {
	defer test.CheckRoutines(t)()

	loggerFactory := logging.NewDefaultLoggerFactory()
	r, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "1.2.3.0/24",
		LoggerFactory: loggerFactory,
	})
	require.NoError(t, err, "Failed to create a router")

	nw, err := vnet.NewNet(&vnet.NetConfig{})
	require.NoError(t, err, "Failed to create a Net")

	err = r.AddNet(nw)
	require.NoError(t, err, "Failed to add a Net to the router")

	t.Run("InterfaceFilter should exclude the interface", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)

		agent, err := NewAgent(&AgentConfig{
			Net: nw,
			InterfaceFilter: func(interfaceName string) bool {
				assert.Equal("eth0", interfaceName)
				return false
			},
		})
		require.NoError(err)

		localIPs, err := localInterfaces(agent.net, agent.interfaceFilter, agent.ipFilter, []NetworkType{NetworkTypeUDP4}, false)
		require.NoError(err)
		require.Empty(localIPs, "InterfaceFilter should have excluded everything")

		assert.NoError(agent.Close())
	})

	t.Run("IPFilter should exclude the IP", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)

		agent, err := NewAgent(&AgentConfig{
			Net: nw,
			IPFilter: func(ip net.IP) bool {
				assert.Equal(net.IP{1, 2, 3, 1}, ip)
				return false
			},
		})
		require.NoError(err)

		localIPs, err := localInterfaces(agent.net, agent.interfaceFilter, agent.ipFilter, []NetworkType{NetworkTypeUDP4}, false)
		require.NoError(err)
		require.Empty(localIPs, "IPFilter should have excluded everything")

		assert.NoError(agent.Close())
	})

	t.Run("InterfaceFilter should not exclude the interface", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)

		agent, err := NewAgent(&AgentConfig{
			Net: nw,
			InterfaceFilter: func(interfaceName string) bool {
				assert.Equal("eth0", interfaceName)
				return true
			},
		})
		require.NoError(err)

		localIPs, err := localInterfaces(agent.net, agent.interfaceFilter, agent.ipFilter, []NetworkType{NetworkTypeUDP4}, false)
		require.NoError(err)
		require.NotEmpty(localIPs, "InterfaceFilter should not have excluded anything")

		assert.NoError(agent.Close())
	})
}

func TestVNetGather_TURNConnectionLeak(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

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
	v, err := newVirtualNet(natType, natType)
	require.NoError(err)

	defer func() {
		require.NoError(v.Close())
	}()

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
	require.NoError(err)

	aAgent.gatherCandidatesRelay(context.Background(), []*stun.URI{turnServerURL})
	// Assert relay conn leak on close.
	assert.NoError(aAgent.Close())
}
