package ice

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/vnet"
	"github.com/pion/turn"
	"github.com/stretchr/testify/assert"
)

type virtualNet struct {
	wan    *vnet.Router
	net0   *vnet.Net
	net1   *vnet.Net
	server *turn.Server
}

func (v *virtualNet) close() {
	v.server.Close() // nolint:errcheck
	v.wan.Stop()
}

func buildVNet(natType *vnet.NATType) (*virtualNet, error) {
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
		StaticIP:      "27.1.1.1", // this router's external IP on eth0
		CIDR:          "192.168.0.0/24",
		NATType:       natType,
		LoggerFactory: loggerFactory,
	})
	if err != nil {
		return nil, err
	}

	net0 := vnet.NewNet(&vnet.NetConfig{})
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
		StaticIP:      "28.1.1.1", // this router's external IP on eth0
		CIDR:          "10.2.0.0/24",
		NATType:       natType,
		LoggerFactory: loggerFactory,
	})
	if err != nil {
		return nil, err
	}

	net1 := vnet.NewNet(&vnet.NetConfig{})
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
	server := turn.NewServer(&turn.ServerConfig{
		AuthHandler: func(username string, srcAddr net.Addr) (password string, ok bool) {
			if pw, ok := credMap[username]; ok {
				return pw, true
			}
			return "", false
		},
		Realm:         "pion.ly",
		Net:           wanNet,
		LoggerFactory: loggerFactory,
	})
	err = server.AddListeningIPAddr("1.2.3.4")
	if err != nil {
		return nil, err
	}

	err = server.Start()
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

func pipeWithVNet(v *virtualNet, urls0, urls1 []*URL) (*Conn, *Conn) {
	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	var wg sync.WaitGroup
	wg.Add(2)

	cfg0 := &AgentConfig{
		Urls:             urls0,
		Trickle:          true,
		NetworkTypes:     supportedNetworkTypes,
		MulticastDNSMode: MulticastDNSModeDisabled,
		Net:              v.net0,
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

	cfg1 := &AgentConfig{
		Urls:             urls1,
		Trickle:          true,
		NetworkTypes:     supportedNetworkTypes,
		MulticastDNSMode: MulticastDNSModeDisabled,
		Net:              v.net1,
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
	if !assert.NoError(t, err, "should succeed") {
		return false
	}
	return true
}

func TestConnectivityVNet(t *testing.T) {
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

	t.Run("Full-cone NATs", func(t *testing.T) {
		loggerFactory := logging.NewDefaultLoggerFactory()
		log := loggerFactory.NewLogger("test")

		// buildVNet with Full-cone NATs
		v, err := buildVNet(&vnet.NATType{
			MappingBehavior:   vnet.EndpointIndependent,
			FilteringBehavior: vnet.EndpointIndependent,
		})

		if !assert.NoError(t, err, "should succeed") {
			return
		}
		defer v.close()

		log.Debug("Connecting...")
		urls0 := []*URL{
			stunServerURL,
		}

		urls1 := []*URL{
			stunServerURL,
		}
		ca, cb := pipeWithVNet(v, urls0, urls1)

		time.Sleep(1 * time.Second)

		log.Debug("Closing...")
		if !closePipe(t, ca, cb) {
			return
		}
	})

	t.Run("Symmetric NATs", func(t *testing.T) {
		loggerFactory := logging.NewDefaultLoggerFactory()
		log := loggerFactory.NewLogger("test")

		// buildVNet with Symmetric NATs
		v, err := buildVNet(&vnet.NATType{
			MappingBehavior:   vnet.EndpointAddrPortDependent,
			FilteringBehavior: vnet.EndpointAddrPortDependent,
		})

		if !assert.NoError(t, err, "should succeed") {
			return
		}
		defer v.close()

		log.Debug("Connecting...")
		urls0 := []*URL{
			stunServerURL,
			turnServerURL,
		}

		urls1 := []*URL{
			stunServerURL,
		}
		ca, cb := pipeWithVNet(v, urls0, urls1)

		log.Debug("Closing...")
		if !closePipe(t, ca, cb) {
			return
		}
	})
}
