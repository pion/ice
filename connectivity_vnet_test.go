// +build !js

package ice

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/test"
	"github.com/pion/transport/vnet"
	"github.com/pion/turn/v2"
	"github.com/stretchr/testify/assert"
)

type virtualNet struct {
	wan    *vnet.Router
	net0   *vnet.Net
	net1   *vnet.Net
	server *turn.Server
}

func (v *virtualNet) close() {
	v.server.Close() // nolint:errcheck,gosec
	v.wan.Stop()     // nolint:errcheck,gosec
}

func buildVNet(natType0, natType1 *vnet.NATType) (*virtualNet, error) {
	loggerFactory := logging.NewDefaultLoggerFactory()

	// WAN
	wan, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "0.0.0.0/0",
		LoggerFactory: loggerFactory,
	})
	if err != nil {
		return nil, err
	}

	wanNet := vnet.NewNet(&vnet.NetConfig{
		StaticIP: "1.2.3.4", // will be assigned to eth0
	})

	err = wan.AddNet(wanNet)
	if err != nil {
		return nil, err
	}

	// LAN 0
	lan0, err := vnet.NewRouter(&vnet.RouterConfig{
		StaticIPs: func() []string {
			if natType0.Mode == vnet.NATModeNAT1To1 {
				return []string{
					"27.1.1.1/192.168.0.1",
				}
			}
			return []string{
				"27.1.1.1",
			}
		}(),
		CIDR:          "192.168.0.0/24",
		NATType:       natType0,
		LoggerFactory: loggerFactory,
	})
	if err != nil {
		return nil, err
	}

	net0 := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{"192.168.0.1"},
	})
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
					"28.1.1.1/10.2.0.1",
				}
			}
			return []string{
				"28.1.1.1",
			}
		}(),
		CIDR:          "10.2.0.0/24",
		NATType:       natType1,
		LoggerFactory: loggerFactory,
	})
	if err != nil {
		return nil, err
	}

	net1 := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{"10.2.0.1"},
	})
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

	// Run TURN(STUN) server
	credMap := map[string]string{}
	credMap["user"] = "pass"
	wanNetPacketConn, err := wanNet.ListenPacket("udp", "1.2.3.4:3478")
	if err != nil {
		return nil, err
	}
	server, err := turn.NewServer(turn.ServerConfig{
		AuthHandler: func(username, realm string, srcAddr net.Addr) (key []byte, ok bool) {
			if pw, ok := credMap[username]; ok {
				return turn.GenerateAuthKey(username, realm, pw), true
			}
			return nil, false
		},
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn: wanNetPacketConn,
				RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
					RelayAddress: net.ParseIP("1.2.3.4"),
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

	return &virtualNet{
		wan:    wan,
		net0:   net0,
		net1:   net1,
		server: server,
	}, nil
}

func connectWithVNet(aAgent, bAgent *Agent) (*Conn, *Conn) {
	loggerFactory := logging.NewDefaultLoggerFactory()
	log := loggerFactory.NewLogger("test")

	// Manual signaling
	aUfrag, aPwd := aAgent.GetLocalUserCredentials()
	bUfrag, bPwd := bAgent.GetLocalUserCredentials()

	candidates, err := aAgent.GetLocalCandidates()
	check(err)
	for _, c := range candidates {
		log.Debugf("agent a candidate: %v", c.String())
		check(bAgent.AddRemoteCandidate(copyCandidate(c)))
	}

	candidates, err = bAgent.GetLocalCandidates()
	check(err)
	for _, c := range candidates {
		log.Debugf("agent b candidate: %v", c.String())
		check(aAgent.AddRemoteCandidate(copyCandidate(c)))
	}

	accepted := make(chan struct{})
	var aConn *Conn

	go func() {
		var acceptErr error
		aConn, acceptErr = aAgent.Accept(context.TODO(), bUfrag, bPwd)
		check(acceptErr)
		close(accepted)
	}()

	bConn, err := bAgent.Dial(context.TODO(), aUfrag, aPwd)
	check(err)

	// Ensure accepted
	<-accepted
	return aConn, bConn
}

type agentTestConfig struct {
	urls                   []*URL
	nat1To1IPCandidateType CandidateType
}

func pipeWithVNet(v *virtualNet, a0TestConfig, a1TestConfig *agentTestConfig) (*Conn, *Conn) {
	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	var wg sync.WaitGroup
	wg.Add(2)

	var nat1To1IPs []string
	if a0TestConfig.nat1To1IPCandidateType != CandidateTypeUnspecified {
		nat1To1IPs = []string{
			"27.1.1.1",
		}
	}

	cfg0 := &AgentConfig{
		Urls:                   a0TestConfig.urls,
		Trickle:                true,
		NetworkTypes:           supportedNetworkTypes,
		MulticastDNSMode:       MulticastDNSModeDisabled,
		NAT1To1IPs:             nat1To1IPs,
		NAT1To1IPCandidateType: a0TestConfig.nat1To1IPCandidateType,
		Net:                    v.net0,
	}

	aAgent, err := NewAgent(cfg0)
	if err != nil {
		panic(err)
	}
	err = aAgent.OnConnectionStateChange(aNotifier)
	if err != nil {
		panic(err)
	}
	err = aAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	})
	if err != nil {
		panic(err)
	}
	err = aAgent.GatherCandidates()
	if err != nil {
		panic(err)
	}

	if a1TestConfig.nat1To1IPCandidateType != CandidateTypeUnspecified {
		nat1To1IPs = []string{
			"28.1.1.1",
		}
	}
	cfg1 := &AgentConfig{
		Urls:                   a1TestConfig.urls,
		Trickle:                true,
		NetworkTypes:           supportedNetworkTypes,
		MulticastDNSMode:       MulticastDNSModeDisabled,
		NAT1To1IPs:             nat1To1IPs,
		NAT1To1IPCandidateType: a1TestConfig.nat1To1IPCandidateType,
		Net:                    v.net1,
	}

	bAgent, err := NewAgent(cfg1)
	if err != nil {
		panic(err)
	}
	err = bAgent.OnConnectionStateChange(bNotifier)
	if err != nil {
		panic(err)
	}
	err = bAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	})
	if err != nil {
		panic(err)
	}
	err = bAgent.GatherCandidates()
	if err != nil {
		panic(err)
	}

	wg.Wait()
	aConn, bConn := connectWithVNet(aAgent, bAgent)

	// Ensure pair selected
	// Note: this assumes ConnectionStateConnected is thrown after selecting the final pair
	<-aConnected
	<-bConnected

	return aConn, bConn
}

func closePipe(t *testing.T, ca *Conn, cb *Conn) bool {
	err := ca.Close()
	if !assert.NoError(t, err, "should succeed") {
		return false
	}
	err = cb.Close()
	return assert.NoError(t, err, "should succeed")
}

func TestConnectivityVNet(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	stunServerURL := &URL{
		Scheme: SchemeTypeSTUN,
		Host:   "1.2.3.4",
		Port:   3478,
		Proto:  ProtoTypeUDP,
	}

	turnServerURL := &URL{
		Scheme:   SchemeTypeTURN,
		Host:     "1.2.3.4",
		Port:     3478,
		Username: "user",
		Password: "pass",
		Proto:    ProtoTypeUDP,
	}

	t.Run("Full-cone NATs on both ends", func(t *testing.T) {
		loggerFactory := logging.NewDefaultLoggerFactory()
		log := loggerFactory.NewLogger("test")

		// buildVNet with a Full-cone NATs both LANs
		natType := &vnet.NATType{
			MappingBehavior:   vnet.EndpointIndependent,
			FilteringBehavior: vnet.EndpointIndependent,
		}
		v, err := buildVNet(natType, natType)

		if !assert.NoError(t, err, "should succeed") {
			return
		}
		defer v.close()

		log.Debug("Connecting...")
		a0TestConfig := &agentTestConfig{
			urls: []*URL{
				stunServerURL,
			},
		}
		a1TestConfig := &agentTestConfig{
			urls: []*URL{
				stunServerURL,
			},
		}
		ca, cb := pipeWithVNet(v, a0TestConfig, a1TestConfig)

		time.Sleep(1 * time.Second)

		log.Debug("Closing...")
		if !closePipe(t, ca, cb) {
			return
		}
	})

	t.Run("Symmetric NATs on both ends", func(t *testing.T) {
		loggerFactory := logging.NewDefaultLoggerFactory()
		log := loggerFactory.NewLogger("test")

		// buildVNet with a Symmetric NATs for both LANs
		natType := &vnet.NATType{
			MappingBehavior:   vnet.EndpointAddrPortDependent,
			FilteringBehavior: vnet.EndpointAddrPortDependent,
		}
		v, err := buildVNet(natType, natType)

		if !assert.NoError(t, err, "should succeed") {
			return
		}
		defer v.close()

		log.Debug("Connecting...")
		a0TestConfig := &agentTestConfig{
			urls: []*URL{
				stunServerURL,
				turnServerURL,
			},
		}
		a1TestConfig := &agentTestConfig{
			urls: []*URL{
				stunServerURL,
			},
		}
		ca, cb := pipeWithVNet(v, a0TestConfig, a1TestConfig)

		log.Debug("Closing...")
		if !closePipe(t, ca, cb) {
			return
		}
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
		v, err := buildVNet(natType0, natType1)

		if !assert.NoError(t, err, "should succeed") {
			return
		}
		defer v.close()

		log.Debug("Connecting...")
		a0TestConfig := &agentTestConfig{
			urls:                   []*URL{},
			nat1To1IPCandidateType: CandidateTypeHost, // Use 1:1 NAT IP as a host candidate
		}
		a1TestConfig := &agentTestConfig{
			urls: []*URL{},
		}
		ca, cb := pipeWithVNet(v, a0TestConfig, a1TestConfig)

		log.Debug("Closing...")
		if !closePipe(t, ca, cb) {
			return
		}
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
		v, err := buildVNet(natType0, natType1)

		if !assert.NoError(t, err, "should succeed") {
			return
		}
		defer v.close()

		log.Debug("Connecting...")
		a0TestConfig := &agentTestConfig{
			urls:                   []*URL{},
			nat1To1IPCandidateType: CandidateTypeServerReflexive, // Use 1:1 NAT IP as a srflx candidate
		}
		a1TestConfig := &agentTestConfig{
			urls: []*URL{},
		}
		ca, cb := pipeWithVNet(v, a0TestConfig, a1TestConfig)

		log.Debug("Closing...")
		if !closePipe(t, ca, cb) {
			return
		}
	})
}
