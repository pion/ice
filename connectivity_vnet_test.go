// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
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
	"github.com/pion/transport/v3/test"
	"github.com/pion/transport/v3/vnet"
	"github.com/pion/turn/v4"
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
		StaticIP: vnetSTUNServerIP, // Will be assigned to eth0
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

// Ensures that when a single local socket is advertised as multiple external endpoints (1:N),
// connectivity checks still succeed. This exercises the case where multiple candidates share
// the same PacketConn (multiple recv loops on the same underlying socket).
func TestExternalIPMapperOneToManyConnectivity(t *testing.T) {
	defer test.CheckRoutines(t)()

	// Limit runtime in case of deadlocks
	defer test.TimeOut(15 * time.Second).Stop()

	loggerFactory := logging.NewDefaultLoggerFactory()

	// Build WAN router
	wan, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "0.0.0.0/0",
		LoggerFactory: loggerFactory,
	})
	require.NoError(t, err)

	// LAN 0 behind 1:1 NAT with TWO external IPs mapping to the same local IP
	// 27.1.1.1/192.168.0.1 and 27.1.1.2/192.168.0.1
	natTypeLan0 := &vnet.NATType{Mode: vnet.NATModeNAT1To1}
	lan0, err := vnet.NewRouter(&vnet.RouterConfig{
		StaticIPs: []string{
			"27.1.1.1/192.168.0.1",
			"27.1.1.2/192.168.0.1",
		},
		CIDR:          "192.168.0.1/24",
		NATType:       natTypeLan0,
		LoggerFactory: loggerFactory,
	})
	require.NoError(t, err)

	net0, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{"192.168.0.1"}})
	require.NoError(t, err)
	require.NoError(t, lan0.AddNet(net0))
	require.NoError(t, wan.AddRouter(lan0))

	// LAN 1 without NAT (simple routed network)
	net1, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{"192.168.1.1"}})
	require.NoError(t, err)
	require.NoError(t, wan.AddNet(net1))

	// Start routers
	require.NoError(t, wan.Start())
	defer func() { require.NoError(t, wan.Stop()) }()

	// Agent A (behind LAN 0 NAT) advertises two external endpoints for its single local socket
	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	udpMapper := func(_ net.IP) []endpoint {
		return []endpoint{
			{ip: net.ParseIP("27.1.1.1"), port: 0},
			{ip: net.ParseIP("27.1.1.2"), port: 0},
		}
	}

	aCfg := &AgentConfig{
		NetworkTypes:                 supportedNetworkTypes(),
		MulticastDNSMode:             MulticastDNSModeDisabled,
		NAT1To1IPCandidateType:       CandidateTypeHost,
		HostUDPAdvertisedAddrsMapper: udpMapper,
		Net:                          net0,
	}
	aAgent, err := NewAgent(aCfg)
	require.NoError(t, err)
	require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))
	defer func() { require.NoError(t, aAgent.Close()) }()

	bCfg := &AgentConfig{
		NetworkTypes:     supportedNetworkTypes(),
		MulticastDNSMode: MulticastDNSModeDisabled,
		Net:              net1,
	}
	bAgent, err := NewAgent(bCfg)
	require.NoError(t, err)
	require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))
	defer func() { require.NoError(t, bAgent.Close()) }()

	// Connect
	ca, cb := connectWithVNet(t, aAgent, bAgent)
	defer closePipe(t, ca, cb)

	// Ensure both are connected
	<-aConnected
	<-bConnected

	// Validate that A gathered two host candidates for the mapped external IPs
	locals, err := aAgent.GetLocalCandidates()
	require.NoError(t, err)
	seen := map[string]bool{}
	for _, c := range locals {
		if c.Type() == CandidateTypeHost {
			seen[c.Address()] = true
		}
	}
	require.True(t, seen["27.1.1.1"], "expected host candidate for first external IP")
	require.True(t, seen["27.1.1.2"], "expected host candidate for second external IP")

	// Verify selected pair uses one of the external IPs (either is acceptable)
	pair, err := aAgent.GetSelectedCandidatePair()
	require.NoError(t, err)
	require.NotNil(t, pair)
	require.Contains(t, []string{"27.1.1.1", "27.1.1.2"}, pair.Local.Address())
}

func TestExternalIPMapperOneToManyConnectivity_UDPMux(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(15 * time.Second).Stop()
  
	// Build vnet with LAN0 behind 1:1 NAT advertising two externals
	v, err := buildVNet(&vnet.NATType{Mode: vnet.NATModeNAT1To1},
						&vnet.NATType{ /* other side routed */ })
	require.NoError(t, err)
	defer v.close()
  
	// UDPMux bound on LAN0’s local IP within vnet
	pc, err := v.net0.ListenPacket("udp4", "192.168.0.1:0")
	require.NoError(t, err)
	defer pc.Close()
	udpMux := NewUDPMuxDefault(UDPMuxParams{UDPConn: pc})
	defer udpMux.Close()
  
	// Agent A: UDPMux + advanced mapper returns two externals for same local
	aNotifier, aConnected := onConnected()
	aAgent, err := NewAgent(&AgentConfig{
	  NetworkTypes:                 []NetworkType{NetworkTypeUDP4},
	  CandidateTypes:               []CandidateType{CandidateTypeHost},
	  MulticastDNSMode:             MulticastDNSModeDisabled,
	  UDPMux:                       udpMux,
	  NAT1To1IPCandidateType:       CandidateTypeHost,
	  HostUDPAdvertisedAddrsMapper: func(_ net.IP) []endpoint {
		return []endpoint{
		  {ip: net.ParseIP("27.1.1.1"), port: 0},
		  {ip: net.ParseIP("27.1.1.2"), port: 0},
		}
	  },
	  Net: v.net0,
	})
	require.NoError(t, err)
	require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))
	defer aAgent.Close()
  
	// Agent B: normal agent on a different LAN
	bNotifier, bConnected := onConnected()
	bAgent, err := NewAgent(&AgentConfig{
	  NetworkTypes:     []NetworkType{NetworkTypeUDP4},
	  MulticastDNSMode: MulticastDNSModeDisabled,
	  Net:              v.net1,
	})
	require.NoError(t, err)
	require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))
	defer bAgent.Close()
  
	ca, cb := connectWithVNet(t, aAgent, bAgent)
	defer closePipe(t, ca, cb)
	<-aConnected
	<-bConnected
  
	locals, err := aAgent.GetLocalCandidates()
	require.NoError(t, err)
	have := map[string]bool{}
	for _, c := range locals {
	  if c.Type() == CandidateTypeHost && c.NetworkType().IsUDP() {
		have[c.Address()] = true
	  }
	}
	require.True(t, have["27.1.1.1"])
	require.True(t, have["27.1.1.2"])
  
	pair, err := aAgent.GetSelectedCandidatePair()
	require.NoError(t, err)
	require.NotNil(t, pair)
	require.Contains(t, []string{"27.1.1.1", "27.1.1.2"}, pair.Local.Address())
}

func TestExternalIPMapperOneToManyConnectivity_TCPMux(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(15 * time.Second).Stop()
  
	// Two TCP listeners on loopback
	l1, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IP{127,0,0,1}, Port: 0})
	require.NoError(t, err)
	defer l1.Close()
	l2, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IP{127,0,0,1}, Port: 0})
	require.NoError(t, err)
	defer l2.Close()
  
	portA := l1.Addr().(*net.TCPAddr).Port
	portB := l2.Addr().(*net.TCPAddr).Port
  
	mux := NewMultiTCPMuxDefault(
	  NewTCPMuxDefault(TCPMuxParams{Listener: l1, ReadBufferSize: 8}),
	  NewTCPMuxDefault(TCPMuxParams{Listener: l2, ReadBufferSize: 8}),
	)
	t.Cleanup(func(){ _ = mux.Close() })
  
	aNotifier, aConnected := onConnected()
	aAgent, err := NewAgent(&AgentConfig{
	  CandidateTypes:               []CandidateType{CandidateTypeHost},
	  NetworkTypes:                 []NetworkType{NetworkTypeTCP4},
	  IncludeLoopback:              true,
	  TCPMux:                       mux,
	  NAT1To1IPCandidateType:       CandidateTypeHost,
	  HostTCPAdvertisedAddrsMapper: func(_ net.IP) []endpoint {
		// Advertise the ports we actually listen on
		return []endpoint{
		  {ip: net.ParseIP("127.0.0.1"), port: portA},
		  {ip: net.ParseIP("127.0.0.1"), port: portB},
		}
	  },
	})
	require.NoError(t, err)
	require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))
	defer aAgent.Close()
  
	bNotifier, bConnected := onConnected()
	bAgent, err := NewAgent(&AgentConfig{
	  CandidateTypes:  []CandidateType{CandidateTypeHost},
	  NetworkTypes:    []NetworkType{NetworkTypeTCP4},
	  // Active TCP is enabled by default; B will dial A’s passive candidates
	  IncludeLoopback: true,
	})
	require.NoError(t, err)
	require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))
	defer bAgent.Close()
  
	// Standard non-vnet connect (loopback)
	ca, cb := connect(t, aAgent, bAgent)
	defer func(){ _ = ca.Close(); _ = cb.Close() }()
	<-aConnected
	<-bConnected
  
	// Verify both advertised TCP host candidates exist
	locals, err := aAgent.GetLocalCandidates()
	require.NoError(t, err)
	have := map[string]map[int]bool{"127.0.0.1": {}}
	for _, c := range locals {
	  if c.NetworkType().IsTCP() && c.Type() == CandidateTypeHost && c.TCPType() == TCPTypePassive {
		if _, ok := have[c.Address()]; !ok { have[c.Address()] = map[int]bool{} }
		have[c.Address()][c.Port()] = true
	  }
	}
	require.True(t, have["127.0.0.1"][portA])
	require.True(t, have["127.0.0.1"][portB])
  
	// Selected pair should use one of the two advertised ports
	pair, err := aAgent.GetSelectedCandidatePair()
	require.NoError(t, err)
	require.NotNil(t, pair)
	require.Equal(t, "127.0.0.1", pair.Local.Address())
	require.Contains(t, []int{portA, portB}, pair.Local.Port())
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
