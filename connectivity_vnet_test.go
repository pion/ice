// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/pion/transport/v4/test"
	"github.com/pion/transport/v4/vnet"
	"github.com/pion/turn/v5"
	"github.com/stretchr/testify/require"
)

const (
	vnetGlobalIPA        = "27.1.1.1"
	vnetLocalIPA         = "192.168.0.1"
	vnetLocalSubnetMaskA = "24"
	vnetGlobalIPB        = "28.1.1.1"
	vnetLocalIPB         = "10.2.0.1"
	vnetLocalSubnetMaskB = "24"
	vnetSTUNServerIP     = "1.2.3.4"
	vnetSTUNServerPort   = 3478
)

type virtualNet struct {
	wan    *vnet.Router
	net0   *vnet.Net
	net1   *vnet.Net
	server *turn.Server
}

func (v *virtualNet) close() {
	v.server.Close() //nolint:errcheck,gosec
	v.wan.Stop()     //nolint:errcheck,gosec
}

func buildVNet(natType0, natType1 *vnet.NATType) (*virtualNet, error) { //nolint:cyclop
	loggerFactory := logging.NewDefaultLoggerFactory()

	// WAN
	wan, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "0.0.0.0/0",
		LoggerFactory: loggerFactory,
	})
	if err != nil {
		return nil, err
	}

	wanNet, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{vnetSTUNServerIP}, // Will be assigned to eth0
	})
	if err != nil {
		return nil, err
	}

	err = wan.AddNet(wanNet)
	if err != nil {
		return nil, err
	}

	// LAN 0
	lan0, err := vnet.NewRouter(&vnet.RouterConfig{
		StaticIPs: func() []string {
			if natType0.Mode == vnet.NATModeNAT1To1 {
				return []string{
					vnetGlobalIPA + "/" + vnetLocalIPA,
				}
			}

			return []string{
				vnetGlobalIPA,
			}
		}(),
		CIDR:          vnetLocalIPA + "/" + vnetLocalSubnetMaskA,
		NATType:       natType0,
		LoggerFactory: loggerFactory,
	})
	if err != nil {
		return nil, err
	}

	net0, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{vnetLocalIPA},
	})
	if err != nil {
		return nil, err
	}

	err = lan0.AddNet(net0)
	if err != nil {
		return nil, err
	}

	err = wan.AddRouter(lan0)
	if err != nil {
		return nil, err
	}

	// LAN 1
	lan1, err := vnet.NewRouter(&vnet.RouterConfig{
		StaticIPs: func() []string {
			if natType1.Mode == vnet.NATModeNAT1To1 {
				return []string{
					vnetGlobalIPB + "/" + vnetLocalIPB,
				}
			}

			return []string{
				vnetGlobalIPB,
			}
		}(),
		CIDR:          vnetLocalIPB + "/" + vnetLocalSubnetMaskB,
		NATType:       natType1,
		LoggerFactory: loggerFactory,
	})
	if err != nil {
		return nil, err
	}

	net1, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{vnetLocalIPB},
	})
	if err != nil {
		return nil, err
	}

	err = lan1.AddNet(net1)
	if err != nil {
		return nil, err
	}

	err = wan.AddRouter(lan1)
	if err != nil {
		return nil, err
	}

	// Start routers
	err = wan.Start()
	if err != nil {
		return nil, err
	}

	server, err := addVNetSTUN(wanNet, loggerFactory)
	if err != nil {
		return nil, err
	}

	return &virtualNet{
		wan:    wan,
		net0:   net0,
		net1:   net1,
		server: server,
	}, nil
}

func addVNetSTUN(wanNet *vnet.Net, loggerFactory logging.LoggerFactory) (*turn.Server, error) {
	// Run TURN(STUN) server
	credMap := map[string]string{}
	credMap["user"] = "pass"
	wanNetPacketConn, err := wanNet.ListenPacket("udp", fmt.Sprintf("%s:%d", vnetSTUNServerIP, vnetSTUNServerPort))
	if err != nil {
		return nil, err
	}
	server, err := turn.NewServer(turn.ServerConfig{
		AuthHandler: func(username, realm string, _ net.Addr) (key []byte, ok bool) {
			if pw, ok := credMap[username]; ok {
				return turn.GenerateAuthKey(username, realm, pw), true
			}

			return nil, false
		},
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn: wanNetPacketConn,
				RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
					RelayAddress: net.ParseIP(vnetSTUNServerIP),
					Address:      "0.0.0.0",
					Net:          wanNet,
				},
			},
		},
		Realm:         "pion.ly",
		LoggerFactory: loggerFactory,
	})
	if err != nil {
		return nil, err
	}

	return server, err
}

func connectWithVNet(t *testing.T, aAgent, bAgent *Agent) (*Conn, *Conn) {
	t.Helper()
	// Manual signaling
	aUfrag, aPwd, err := aAgent.GetLocalUserCredentials()
	require.NoError(t, err)

	bUfrag, bPwd, err := bAgent.GetLocalUserCredentials()
	require.NoError(t, err)

	gatherAndExchangeCandidates(t, aAgent, bAgent)

	accepted := make(chan struct{})
	var aConn *Conn

	go func() {
		var acceptErr error
		aConn, acceptErr = aAgent.Accept(context.TODO(), bUfrag, bPwd)
		require.NoError(t, acceptErr)
		close(accepted)
	}()

	bConn, err := bAgent.Dial(context.TODO(), aUfrag, aPwd)
	require.NoError(t, err)

	// Ensure accepted
	<-accepted

	return aConn, bConn
}

type agentTestConfig struct {
	urls                   []*stun.URI
	nat1To1IPCandidateType CandidateType
}

func pipeWithVNet(t *testing.T, vnet *virtualNet, a0TestConfig, a1TestConfig *agentTestConfig) (*Conn, *Conn) {
	t.Helper()
	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	var nat1To1IPs []string
	if a0TestConfig.nat1To1IPCandidateType != CandidateTypeUnspecified {
		nat1To1IPs = []string{
			vnetGlobalIPA,
		}
	}

	cfg0 := &AgentConfig{
		Urls:                   a0TestConfig.urls,
		NetworkTypes:           supportedNetworkTypes(),
		MulticastDNSMode:       MulticastDNSModeDisabled,
		NAT1To1IPs:             nat1To1IPs,
		NAT1To1IPCandidateType: a0TestConfig.nat1To1IPCandidateType,
		Net:                    vnet.net0,
	}

	aAgent, err := NewAgent(cfg0)
	require.NoError(t, err)
	require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))

	if a1TestConfig.nat1To1IPCandidateType != CandidateTypeUnspecified {
		nat1To1IPs = []string{
			vnetGlobalIPB,
		}
	}
	cfg1 := &AgentConfig{
		Urls:                   a1TestConfig.urls,
		NetworkTypes:           supportedNetworkTypes(),
		MulticastDNSMode:       MulticastDNSModeDisabled,
		NAT1To1IPs:             nat1To1IPs,
		NAT1To1IPCandidateType: a1TestConfig.nat1To1IPCandidateType,
		Net:                    vnet.net1,
	}

	bAgent, err := NewAgent(cfg1)
	require.NoError(t, err)
	require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))

	aConn, bConn := connectWithVNet(t, aAgent, bAgent)

	// Ensure pair selected
	// Note: this assumes ConnectionStateConnected is thrown after selecting the final pair
	<-aConnected
	<-bConnected

	return aConn, bConn
}

func pipeWithVNetUsingOptions(t *testing.T, opts0, opts1 []AgentOption) (*Conn, *Conn) {
	t.Helper()

	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	aAgent, err := NewAgentWithOptions(opts0...)
	require.NoError(t, err)
	if err = aAgent.OnConnectionStateChange(aNotifier); err != nil {
		require.NoError(t, err)
	}

	bAgent, err := NewAgentWithOptions(opts1...)
	require.NoError(t, err)
	if err = bAgent.OnConnectionStateChange(bNotifier); err != nil {
		require.NoError(t, err)
	}

	t.Cleanup(func() {
		require.NoError(t, aAgent.Close(), "failed to close agent0")
		require.NoError(t, bAgent.Close(), "failed to close agent1")
	})

	aConn, bConn := connectWithVNet(t, aAgent, bAgent)

	<-aConnected
	<-bConnected

	return aConn, bConn
}

func closePipe(t *testing.T, ca *Conn, cb *Conn) {
	t.Helper()

	require.NoError(t, ca.Close())
	require.NoError(t, cb.Close())
}

func TestConnectivityVNet(t *testing.T) {
	defer test.CheckRoutines(t)()

	stunServerURL := &stun.URI{
		Scheme: stun.SchemeTypeSTUN,
		Host:   vnetSTUNServerIP,
		Port:   vnetSTUNServerPort,
		Proto:  stun.ProtoTypeUDP,
	}

	turnServerURL := &stun.URI{
		Scheme:   stun.SchemeTypeTURN,
		Host:     vnetSTUNServerIP,
		Port:     vnetSTUNServerPort,
		Username: "user",
		Password: "pass",
		Proto:    stun.ProtoTypeUDP,
	}

	t.Run("Full-cone NATs on both ends", func(t *testing.T) {
		loggerFactory := logging.NewDefaultLoggerFactory()
		log := loggerFactory.NewLogger("test")

		// buildVNet with a Full-cone NATs both LANs
		natType := &vnet.NATType{
			MappingBehavior:   vnet.EndpointIndependent,
			FilteringBehavior: vnet.EndpointIndependent,
		}
		vnet, err := buildVNet(natType, natType)

		require.NoError(t, err, "should succeed")
		defer vnet.close()

		log.Debug("Connecting...")
		a0TestConfig := &agentTestConfig{
			urls: []*stun.URI{
				stunServerURL,
			},
		}
		a1TestConfig := &agentTestConfig{
			urls: []*stun.URI{
				stunServerURL,
			},
		}
		ca, cb := pipeWithVNet(t, vnet, a0TestConfig, a1TestConfig)

		time.Sleep(1 * time.Second)

		log.Debug("Closing...")
		closePipe(t, ca, cb)
	})

	t.Run("Symmetric NATs on both ends", func(t *testing.T) {
		loggerFactory := logging.NewDefaultLoggerFactory()
		log := loggerFactory.NewLogger("test")

		// buildVNet with a Symmetric NATs for both LANs
		natType := &vnet.NATType{
			MappingBehavior:   vnet.EndpointAddrPortDependent,
			FilteringBehavior: vnet.EndpointAddrPortDependent,
		}
		vnet, err := buildVNet(natType, natType)

		require.NoError(t, err, "should succeed")
		defer vnet.close()

		log.Debug("Connecting...")
		a0TestConfig := &agentTestConfig{
			urls: []*stun.URI{
				stunServerURL,
				turnServerURL,
			},
		}
		a1TestConfig := &agentTestConfig{
			urls: []*stun.URI{
				stunServerURL,
			},
		}
		ca, cb := pipeWithVNet(t, vnet, a0TestConfig, a1TestConfig)

		log.Debug("Closing...")
		closePipe(t, ca, cb)
	})

	t.Run("1:1 NAT with host candidate vs Symmetric NATs", func(t *testing.T) {
		loggerFactory := logging.NewDefaultLoggerFactory()
		log := loggerFactory.NewLogger("test")

		// Agent0 is behind 1:1 NAT
		natType0 := &vnet.NATType{
			Mode: vnet.NATModeNAT1To1,
		}
		// Agent1 is behind a symmetric NAT
		natType1 := &vnet.NATType{
			MappingBehavior:   vnet.EndpointAddrPortDependent,
			FilteringBehavior: vnet.EndpointAddrPortDependent,
		}
		vnet, err := buildVNet(natType0, natType1)

		require.NoError(t, err, "should succeed")
		defer vnet.close()

		log.Debug("Connecting...")
		a0TestConfig := &agentTestConfig{
			urls:                   []*stun.URI{},
			nat1To1IPCandidateType: CandidateTypeHost, // Use 1:1 NAT IP as a host candidate
		}
		a1TestConfig := &agentTestConfig{
			urls: []*stun.URI{},
		}
		ca, cb := pipeWithVNet(t, vnet, a0TestConfig, a1TestConfig)

		log.Debug("Closing...")
		closePipe(t, ca, cb)
	})

	t.Run("1:1 NAT with srflx candidate vs Symmetric NATs", func(t *testing.T) {
		loggerFactory := logging.NewDefaultLoggerFactory()
		log := loggerFactory.NewLogger("test")

		// Agent0 is behind 1:1 NAT
		natType0 := &vnet.NATType{
			Mode: vnet.NATModeNAT1To1,
		}
		// Agent1 is behind a symmetric NAT
		natType1 := &vnet.NATType{
			MappingBehavior:   vnet.EndpointAddrPortDependent,
			FilteringBehavior: vnet.EndpointAddrPortDependent,
		}
		vnet, err := buildVNet(natType0, natType1)

		require.NoError(t, err, "should succeed")
		defer vnet.close()

		log.Debug("Connecting...")
		a0TestConfig := &agentTestConfig{
			urls:                   []*stun.URI{},
			nat1To1IPCandidateType: CandidateTypeServerReflexive, // Use 1:1 NAT IP as a srflx candidate
		}
		a1TestConfig := &agentTestConfig{
			urls: []*stun.URI{},
		}
		ca, cb := pipeWithVNet(t, vnet, a0TestConfig, a1TestConfig)

		log.Debug("Closing...")
		closePipe(t, ca, cb)
	})
}

func TestConnectivityVNetWithAddressRewriteRuleOptions(t *testing.T) {
	defer test.CheckRoutines(t)()

	t.Run("host candidate mapping with options", func(t *testing.T) {
		natType0 := &vnet.NATType{Mode: vnet.NATModeNAT1To1}
		natType1 := &vnet.NATType{
			MappingBehavior:   vnet.EndpointAddrPortDependent,
			FilteringBehavior: vnet.EndpointAddrPortDependent,
		}

		vnet, err := buildVNet(natType0, natType1)
		require.NoError(t, err)
		defer vnet.close()

		agent0Opts := []AgentOption{
			WithNet(vnet.net0),
			WithNetworkTypes(supportedNetworkTypes()),
			WithMulticastDNSMode(MulticastDNSModeDisabled),
			WithAddressRewriteRules(AddressRewriteRule{
				External:        []string{vnetGlobalIPA},
				AsCandidateType: CandidateTypeHost,
			}),
		}
		agent1Opts := []AgentOption{
			WithNet(vnet.net1),
			WithNetworkTypes(supportedNetworkTypes()),
			WithMulticastDNSMode(MulticastDNSModeDisabled),
		}

		ca, cb := pipeWithVNetUsingOptions(t, agent0Opts, agent1Opts)
		closePipe(t, ca, cb)
	})

	t.Run("srflx candidate mapping with options", func(t *testing.T) {
		natType0 := &vnet.NATType{Mode: vnet.NATModeNAT1To1}
		natType1 := &vnet.NATType{
			MappingBehavior:   vnet.EndpointAddrPortDependent,
			FilteringBehavior: vnet.EndpointAddrPortDependent,
		}

		vnet, err := buildVNet(natType0, natType1)
		require.NoError(t, err)
		defer vnet.close()

		agent0Opts := []AgentOption{
			WithNet(vnet.net0),
			WithNetworkTypes(supportedNetworkTypes()),
			WithMulticastDNSMode(MulticastDNSModeDisabled),
			WithAddressRewriteRules(AddressRewriteRule{
				External:        []string{vnetGlobalIPA},
				AsCandidateType: CandidateTypeServerReflexive,
			}),
		}
		agent1Opts := []AgentOption{
			WithNet(vnet.net1),
			WithNetworkTypes(supportedNetworkTypes()),
			WithMulticastDNSMode(MulticastDNSModeDisabled),
		}

		ca, cb := pipeWithVNetUsingOptions(t, agent0Opts, agent1Opts)
		closePipe(t, ca, cb)
	})
}

func TestConnectivityVNetNAT1To1SharedFoundation(t *testing.T) {
	defer test.CheckRoutines(t)()

	natType0 := &vnet.NATType{Mode: vnet.NATModeNAT1To1}
	natType1 := &vnet.NATType{}

	vnet, err := buildVNet(natType0, natType1)
	require.NoError(t, err)
	defer vnet.close()

	agent, err := NewAgentWithOptions(
		WithNet(vnet.net0),
		WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
		WithMulticastDNSMode(MulticastDNSModeDisabled),
		WithAddressRewriteRules(
			AddressRewriteRule{
				External:        []string{vnetGlobalIPA},
				AsCandidateType: CandidateTypeHost,
			},
			AddressRewriteRule{
				External:        []string{vnetGlobalIPA},
				AsCandidateType: CandidateTypeServerReflexive,
			},
		),
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	require.NoError(t, agent.OnCandidate(func(Candidate) {}))
	require.NoError(t, agent.GatherCandidates())

	require.Eventually(t, func() bool {
		state, stateErr := agent.GetGatheringState()
		require.NoError(t, stateErr)

		return state == GatheringStateComplete
	}, time.Second, 10*time.Millisecond)

	foundationSeen := make(map[string]struct{})
	typeCount := make(map[CandidateType]int)

	for _, candidates := range agent.localCandidates {
		for _, cand := range candidates {
			if cand.Address() != vnetGlobalIPA {
				continue
			}

			if cand.Type() != CandidateTypeHost && cand.Type() != CandidateTypeServerReflexive {
				continue
			}

			foundation := cand.Foundation()
			_, dup := foundationSeen[foundation]
			require.Falsef(t, dup, "duplicate foundation %s for %s candidate", foundation, cand.Type())
			foundationSeen[foundation] = struct{}{}
			typeCount[cand.Type()]++
		}
	}

	require.Equal(t, 1, typeCount[CandidateTypeHost], "expected exactly one host candidate for %s", vnetGlobalIPA)
	require.Equal(
		t,
		1,
		typeCount[CandidateTypeServerReflexive],
		"expected exactly one srflx candidate for %s",
		vnetGlobalIPA,
	)
}

// TestDisconnectedToConnected requires that an agent can go to disconnected,
// and then return to connected successfully.
func TestDisconnectedToConnected(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 10).Stop()

	loggerFactory := logging.NewDefaultLoggerFactory()

	// Create a network with two interfaces
	wan, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "0.0.0.0/0",
		LoggerFactory: loggerFactory,
	})
	require.NoError(t, err)

	var dropAllData uint64
	wan.AddChunkFilter(func(vnet.Chunk) bool {
		return atomic.LoadUint64(&dropAllData) != 1
	})

	net0, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{"192.168.0.1"},
	})
	require.NoError(t, err)
	require.NoError(t, wan.AddNet(net0))

	net1, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{"192.168.0.2"},
	})
	require.NoError(t, err)
	require.NoError(t, wan.AddNet(net1))

	require.NoError(t, wan.Start())

	disconnectTimeout := time.Second
	keepaliveInterval := time.Millisecond * 20

	// Create two agents and connect them
	controllingAgent, err := NewAgent(&AgentConfig{
		NetworkTypes:        supportedNetworkTypes(),
		MulticastDNSMode:    MulticastDNSModeDisabled,
		Net:                 net0,
		DisconnectedTimeout: &disconnectTimeout,
		KeepaliveInterval:   &keepaliveInterval,
		CheckInterval:       &keepaliveInterval,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, controllingAgent.Close())
	}()

	controlledAgent, err := NewAgent(&AgentConfig{
		NetworkTypes:        supportedNetworkTypes(),
		MulticastDNSMode:    MulticastDNSModeDisabled,
		Net:                 net1,
		DisconnectedTimeout: &disconnectTimeout,
		KeepaliveInterval:   &keepaliveInterval,
		CheckInterval:       &keepaliveInterval,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, controlledAgent.Close())
	}()

	controllingStateChanges := make(chan ConnectionState, 100)
	require.NoError(t, controllingAgent.OnConnectionStateChange(func(c ConnectionState) {
		controllingStateChanges <- c
	}))

	controlledStateChanges := make(chan ConnectionState, 100)
	require.NoError(t, controlledAgent.OnConnectionStateChange(func(c ConnectionState) {
		controlledStateChanges <- c
	}))

	connectWithVNet(t, controllingAgent, controlledAgent)
	blockUntilStateSeen := func(expectedState ConnectionState, stateQueue chan ConnectionState) {
		for s := range stateQueue {
			if s == expectedState {
				return
			}
		}
	}

	// Assert we have gone to connected
	blockUntilStateSeen(ConnectionStateConnected, controllingStateChanges)
	blockUntilStateSeen(ConnectionStateConnected, controlledStateChanges)

	// Drop all packets, and block until we have gone to disconnected
	atomic.StoreUint64(&dropAllData, 1)
	blockUntilStateSeen(ConnectionStateDisconnected, controllingStateChanges)
	blockUntilStateSeen(ConnectionStateDisconnected, controlledStateChanges)

	// Allow all packets through again, block until we have gone to connected
	atomic.StoreUint64(&dropAllData, 0)
	blockUntilStateSeen(ConnectionStateConnected, controllingStateChanges)
	blockUntilStateSeen(ConnectionStateConnected, controlledStateChanges)

	require.NoError(t, wan.Stop())
}

// Agent.Write should use the best valid pair if a selected pair is not yet available.
func TestWriteUseValidPair(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 10).Stop()

	loggerFactory := logging.NewDefaultLoggerFactory()

	// Create a network with two interfaces
	wan, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "0.0.0.0/0",
		LoggerFactory: loggerFactory,
	})
	require.NoError(t, err)

	wan.AddChunkFilter(func(c vnet.Chunk) bool {
		if stun.IsMessage(c.UserData()) {
			m := &stun.Message{
				Raw: c.UserData(),
			}
			if decErr := m.Decode(); decErr != nil {
				return false
			} else if m.Contains(stun.AttrUseCandidate) {
				return false
			}
		}

		return true
	})

	net0, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{"192.168.0.1"},
	})
	require.NoError(t, err)
	require.NoError(t, wan.AddNet(net0))

	net1, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{"192.168.0.2"},
	})
	require.NoError(t, err)
	require.NoError(t, wan.AddNet(net1))

	require.NoError(t, wan.Start())

	// Create two agents and connect them
	controllingAgent, err := NewAgent(&AgentConfig{
		NetworkTypes:     supportedNetworkTypes(),
		MulticastDNSMode: MulticastDNSModeDisabled,
		Net:              net0,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, controllingAgent.Close())
	}()

	controlledAgent, err := NewAgent(&AgentConfig{
		NetworkTypes:     supportedNetworkTypes(),
		MulticastDNSMode: MulticastDNSModeDisabled,
		Net:              net1,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, controlledAgent.Close())
	}()

	gatherAndExchangeCandidates(t, controllingAgent, controlledAgent)

	controllingUfrag, controllingPwd, err := controllingAgent.GetLocalUserCredentials()
	require.NoError(t, err)

	controlledUfrag, controlledPwd, err := controlledAgent.GetLocalUserCredentials()
	require.NoError(t, err)

	require.NoError(t, controllingAgent.startConnectivityChecks(true, controlledUfrag, controlledPwd))
	require.NoError(t, controlledAgent.startConnectivityChecks(false, controllingUfrag, controllingPwd))

	testMessage := []byte("Test Message")
	go func() {
		for {
			if _, writeErr := (&Conn{agent: controllingAgent}).Write(testMessage); writeErr != nil {
				return
			}

			time.Sleep(20 * time.Millisecond)
		}
	}()

	readBuf := make([]byte, len(testMessage))
	_, err = (&Conn{agent: controlledAgent}).Read(readBuf)
	require.NoError(t, err)

	require.Equal(t, readBuf, testMessage)

	require.NoError(t, wan.Stop())
}
