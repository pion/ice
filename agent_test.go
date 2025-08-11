// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pion/ice/v4/internal/fakenet"
	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/pion/transport/v3/test"
	"github.com/pion/transport/v3/vnet"
	"github.com/stretchr/testify/require"
)

type BadAddr struct{}

func (ba *BadAddr) Network() string {
	return "xxx"
}

func (ba *BadAddr) String() string {
	return "yyy"
}

func TestHandlePeerReflexive(t *testing.T) { //nolint:cyclop
	defer test.CheckRoutines(t)()

	// Limit runtime in case of deadlocks
	defer test.TimeOut(time.Second * 2).Stop()

	t.Run("UDP prflx candidate from handleInbound()", func(t *testing.T) {
		agent, err := NewAgent(&AgentConfig{})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		require.NoError(t, agent.loop.Run(agent.loop, func(_ context.Context) {
			agent.selector = &controllingSelector{agent: agent, log: agent.log}

			hostConfig := CandidateHostConfig{
				Network:   "udp",
				Address:   "192.168.0.2",
				Port:      777,
				Component: 1,
			}
			local, err := NewCandidateHost(&hostConfig)
			local.conn = &fakenet.MockPacketConn{}
			require.NoError(t, err)

			remote := &net.UDPAddr{IP: net.ParseIP("172.17.0.3"), Port: 999}

			msg, err := stun.Build(stun.BindingRequest, stun.TransactionID,
				stun.NewUsername(agent.localUfrag+":"+agent.remoteUfrag),
				UseCandidate(),
				AttrControlling(agent.tieBreaker),
				PriorityAttr(local.Priority()),
				stun.NewShortTermIntegrity(agent.localPwd),
				stun.Fingerprint,
			)
			require.NoError(t, err)

			// nolint: contextcheck
			agent.handleInbound(msg, local, remote)

			// Length of remote candidate list must be one now
			require.Len(t, agent.remoteCandidates, 1)

			// Length of remote candidate list for a network type must be 1
			set := agent.remoteCandidates[local.NetworkType()]
			require.Len(t, set, 1)

			c := set[0]

			require.Equal(t, CandidateTypePeerReflexive, c.Type())
			require.Equal(t, "172.17.0.3", c.Address())
			require.Equal(t, 999, c.Port())
		}))
	})

	t.Run("Bad network type with handleInbound()", func(t *testing.T) {
		agent, err := NewAgent(&AgentConfig{})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		require.NoError(t, agent.loop.Run(agent.loop, func(_ context.Context) {
			agent.selector = &controllingSelector{agent: agent, log: agent.log}

			hostConfig := CandidateHostConfig{
				Network:   "tcp",
				Address:   "192.168.0.2",
				Port:      777,
				Component: 1,
			}
			local, err := NewCandidateHost(&hostConfig)
			require.NoError(t, err)

			remote := &BadAddr{}

			// nolint: contextcheck
			agent.handleInbound(nil, local, remote)
			require.Len(t, agent.remoteCandidates, 0)
		}))
	})

	t.Run("Success from unknown remote, prflx candidate MUST only be created via Binding Request", func(t *testing.T) {
		agent, err := NewAgent(&AgentConfig{})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		require.NoError(t, agent.loop.Run(agent.loop, func(_ context.Context) {
			agent.selector = &controllingSelector{agent: agent, log: agent.log}
			tID := [stun.TransactionIDSize]byte{}
			copy(tID[:], "ABC")
			agent.pendingBindingRequests = []bindingRequest{
				{time.Now(), tID, &net.UDPAddr{}, false},
			}

			hostConfig := CandidateHostConfig{
				Network:   "udp",
				Address:   "192.168.0.2",
				Port:      777,
				Component: 1,
			}
			local, err := NewCandidateHost(&hostConfig)
			local.conn = &fakenet.MockPacketConn{}
			require.NoError(t, err)

			remote := &net.UDPAddr{IP: net.ParseIP("172.17.0.3"), Port: 999}
			msg, err := stun.Build(stun.BindingSuccess, stun.NewTransactionIDSetter(tID),
				stun.NewShortTermIntegrity(agent.remotePwd),
				stun.Fingerprint,
			)
			require.NoError(t, err)

			// nolint: contextcheck
			agent.handleInbound(msg, local, remote)
			require.Len(t, agent.remoteCandidates, 0)
		}))
	})
}

// Assert that Agent on startup sends message, and doesn't wait for connectivityTicker to fire
// https://github.com/pion/ice/issues/15
func TestConnectivityOnStartup(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	// Create a network with two interfaces
	wan, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "0.0.0.0/0",
		LoggerFactory: logging.NewDefaultLoggerFactory(),
	})
	require.NoError(t, err)

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

	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	KeepaliveInterval := time.Hour
	cfg0 := &AgentConfig{
		NetworkTypes:      supportedNetworkTypes(),
		MulticastDNSMode:  MulticastDNSModeDisabled,
		Net:               net0,
		KeepaliveInterval: &KeepaliveInterval,
		CheckInterval:     &KeepaliveInterval,
	}

	aAgent, err := NewAgent(cfg0)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, aAgent.Close())
	}()
	require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))

	cfg1 := &AgentConfig{
		NetworkTypes:      supportedNetworkTypes(),
		MulticastDNSMode:  MulticastDNSModeDisabled,
		Net:               net1,
		KeepaliveInterval: &KeepaliveInterval,
		CheckInterval:     &KeepaliveInterval,
	}

	bAgent, err := NewAgent(cfg1)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, bAgent.Close())
	}()
	require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))

	func(aAgent, bAgent *Agent) (*Conn, *Conn) {
		// Manual signaling
		aUfrag, aPwd, err := aAgent.GetLocalUserCredentials()
		require.NoError(t, err)

		bUfrag, bPwd, err := bAgent.GetLocalUserCredentials()
		require.NoError(t, err)

		gatherAndExchangeCandidates(t, aAgent, bAgent)

		accepted := make(chan struct{})
		accepting := make(chan struct{})
		var aConn *Conn

		origHdlr := aAgent.onConnectionStateChangeHdlr.Load()
		if origHdlr != nil {
			defer require.NoError(t, aAgent.OnConnectionStateChange(origHdlr.(func(ConnectionState)))) //nolint:forcetypeassert
		}
		require.NoError(t, aAgent.OnConnectionStateChange(func(s ConnectionState) {
			if s == ConnectionStateChecking {
				close(accepting)
			}
			if origHdlr != nil {
				origHdlr.(func(ConnectionState))(s) //nolint:forcetypeassert
			}
		}))

		go func() {
			var acceptErr error
			aConn, acceptErr = aAgent.Accept(context.TODO(), bUfrag, bPwd)
			require.NoError(t, acceptErr)
			close(accepted)
		}()

		<-accepting

		bConn, err := bAgent.Dial(context.TODO(), aUfrag, aPwd)
		require.NoError(t, err)

		// Ensure accepted
		<-accepted

		return aConn, bConn
	}(aAgent, bAgent)

	// Ensure pair selected
	// Note: this assumes ConnectionStateConnected is thrown after selecting the final pair
	<-aConnected
	<-bConnected

	require.NoError(t, wan.Stop())
}

func TestConnectivityLite(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	stunServerURL := &stun.URI{
		Scheme: SchemeTypeSTUN,
		Host:   "1.2.3.4",
		Port:   3478,
		Proto:  stun.ProtoTypeUDP,
	}

	natType := &vnet.NATType{
		MappingBehavior:   vnet.EndpointIndependent,
		FilteringBehavior: vnet.EndpointIndependent,
	}
	vent, err := buildVNet(natType, natType)
	require.NoError(t, err, "should succeed")
	defer vent.close()

	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	cfg0 := &AgentConfig{
		Urls:             []*stun.URI{stunServerURL},
		NetworkTypes:     supportedNetworkTypes(),
		MulticastDNSMode: MulticastDNSModeDisabled,
		Net:              vent.net0,
	}

	aAgent, err := NewAgent(cfg0)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, aAgent.Close())
	}()
	require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))

	cfg1 := &AgentConfig{
		Urls:             []*stun.URI{},
		Lite:             true,
		CandidateTypes:   []CandidateType{CandidateTypeHost},
		NetworkTypes:     supportedNetworkTypes(),
		MulticastDNSMode: MulticastDNSModeDisabled,
		Net:              vent.net1,
	}

	bAgent, err := NewAgent(cfg1)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, bAgent.Close())
	}()
	require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))

	connectWithVNet(t, aAgent, bAgent)

	// Ensure pair selected
	// Note: this assumes ConnectionStateConnected is thrown after selecting the final pair
	<-aConnected
	<-bConnected
}

func TestInboundValidity(t *testing.T) { //nolint:cyclop
	defer test.CheckRoutines(t)()

	buildMsg := func(class stun.MessageClass, username, key string) *stun.Message {
		msg, err := stun.Build(stun.NewType(stun.MethodBinding, class), stun.TransactionID,
			stun.NewUsername(username),
			stun.NewShortTermIntegrity(key),
			stun.Fingerprint,
		)
		require.NoError(t, err)

		return msg
	}

	remote := &net.UDPAddr{IP: net.ParseIP("172.17.0.3"), Port: 999}
	hostConfig := CandidateHostConfig{
		Network:   "udp",
		Address:   "192.168.0.2",
		Port:      777,
		Component: 1,
	}
	local, err := NewCandidateHost(&hostConfig)
	local.conn = &fakenet.MockPacketConn{}
	require.NoError(t, err)

	t.Run("Invalid Binding requests should be discarded", func(t *testing.T) {
		agent, err := NewAgent(&AgentConfig{})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		agent.handleInbound(buildMsg(stun.ClassRequest, "invalid", agent.localPwd), local, remote)
		require.Len(t, agent.remoteCandidates, 0)

		agent.handleInbound(buildMsg(stun.ClassRequest, agent.localUfrag+":"+agent.remoteUfrag, "Invalid"), local, remote)
		require.Len(t, agent.remoteCandidates, 0)
	})

	t.Run("Invalid Binding success responses should be discarded", func(t *testing.T) {
		a, err := NewAgent(&AgentConfig{})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, a.Close())
		}()

		a.handleInbound(buildMsg(stun.ClassSuccessResponse, a.localUfrag+":"+a.remoteUfrag, "Invalid"), local, remote)
		require.Len(t, a.remoteCandidates, 0)
	})

	t.Run("Discard non-binding messages", func(t *testing.T) {
		a, err := NewAgent(&AgentConfig{})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, a.Close())
		}()

		a.handleInbound(buildMsg(stun.ClassErrorResponse, a.localUfrag+":"+a.remoteUfrag, "Invalid"), local, remote)
		require.Len(t, a.remoteCandidates, 0)
	})

	t.Run("Valid bind request", func(t *testing.T) {
		a, err := NewAgent(&AgentConfig{})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, a.Close())
		}()

		err = a.loop.Run(a.loop, func(_ context.Context) {
			a.selector = &controllingSelector{agent: a, log: a.log}
			// nolint: contextcheck
			a.handleInbound(buildMsg(stun.ClassRequest, a.localUfrag+":"+a.remoteUfrag, a.localPwd), local, remote)
			require.Len(t, a.remoteCandidates, 1)
		})

		require.NoError(t, err)
	})

	t.Run("Valid bind without fingerprint", func(t *testing.T) {
		agent, err := NewAgent(&AgentConfig{})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		require.NoError(t, agent.loop.Run(agent.loop, func(_ context.Context) {
			agent.selector = &controllingSelector{agent: agent, log: agent.log}
			msg, err := stun.Build(stun.BindingRequest, stun.TransactionID,
				stun.NewUsername(agent.localUfrag+":"+agent.remoteUfrag),
				stun.NewShortTermIntegrity(agent.localPwd),
			)
			require.NoError(t, err)

			// nolint: contextcheck
			agent.handleInbound(msg, local, remote)
			require.Len(t, agent.remoteCandidates, 1)
		}))
	})

	t.Run("Success with invalid TransactionID", func(t *testing.T) {
		agent, err := NewAgent(&AgentConfig{})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		hostConfig := CandidateHostConfig{
			Network:   "udp",
			Address:   "192.168.0.2",
			Port:      777,
			Component: 1,
		}
		local, err := NewCandidateHost(&hostConfig)
		local.conn = &fakenet.MockPacketConn{}
		require.NoError(t, err)

		remote := &net.UDPAddr{IP: net.ParseIP("172.17.0.3"), Port: 999}
		tID := [stun.TransactionIDSize]byte{}
		copy(tID[:], "ABC")
		msg, err := stun.Build(stun.BindingSuccess, stun.NewTransactionIDSetter(tID),
			stun.NewShortTermIntegrity(agent.remotePwd),
			stun.Fingerprint,
		)
		require.NoError(t, err)

		agent.handleInbound(msg, local, remote)
		require.Len(t, agent.remoteCandidates, 0)
	})
}

func TestInvalidAgentStarts(t *testing.T) {
	defer test.CheckRoutines(t)()

	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, err = agent.Dial(ctx, "", "bar")
	require.ErrorIs(t, ErrRemoteUfragEmpty, err)

	_, err = agent.Dial(ctx, "foo", "")
	require.ErrorIs(t, ErrRemotePwdEmpty, err)

	_, err = agent.Dial(ctx, "foo", "bar")
	require.ErrorIs(t, ErrCanceledByCaller, err)

	_, err = agent.Dial(ctx, "foo", "bar")
	require.ErrorIs(t, ErrMultipleStart, err)
}

// Assert that Agent emits Connecting/Connected/Disconnected/Failed/Closed messages.
func TestConnectionStateCallback(t *testing.T) { //nolint:cyclop
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 5).Stop()

	disconnectedDuration := time.Second
	failedDuration := time.Second
	KeepaliveInterval := time.Duration(0)

	cfg := &AgentConfig{
		Urls:                []*stun.URI{},
		NetworkTypes:        supportedNetworkTypes(),
		DisconnectedTimeout: &disconnectedDuration,
		FailedTimeout:       &failedDuration,
		KeepaliveInterval:   &KeepaliveInterval,
		InterfaceFilter:     problematicNetworkInterfaces,
	}

	isClosed := make(chan any)

	aAgent, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		select {
		case <-isClosed:
			return
		default:
		}
		require.NoError(t, aAgent.Close())
	}()

	bAgent, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		select {
		case <-isClosed:
			return
		default:
		}
		require.NoError(t, bAgent.Close())
	}()

	isChecking := make(chan any)
	isConnected := make(chan any)
	isDisconnected := make(chan any)
	isFailed := make(chan any)
	err = aAgent.OnConnectionStateChange(func(c ConnectionState) {
		switch c {
		case ConnectionStateChecking:
			close(isChecking)
		case ConnectionStateConnected:
			close(isConnected)
		case ConnectionStateDisconnected:
			close(isDisconnected)
		case ConnectionStateFailed:
			close(isFailed)
		case ConnectionStateClosed:
			close(isClosed)
		default:
		}
	})
	require.NoError(t, err)

	connect(t, aAgent, bAgent)

	<-isChecking
	<-isConnected
	<-isDisconnected
	<-isFailed

	require.NoError(t, aAgent.Close())
	require.NoError(t, bAgent.Close())

	<-isClosed
}

func TestInvalidGather(t *testing.T) {
	t.Run("Gather with no OnCandidate should error", func(t *testing.T) {
		a, err := NewAgent(&AgentConfig{})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, a.Close())
		}()

		err = a.GatherCandidates()
		require.ErrorIs(t, ErrNoOnCandidateHandler, err)
	})
}

func TestCandidatePairsStats(t *testing.T) { //nolint:cyclop,gocyclo
	defer test.CheckRoutines(t)()

	// Avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	hostConfig := &CandidateHostConfig{
		Network:   "udp",
		Address:   "192.168.1.1",
		Port:      19216,
		Component: 1,
	}
	hostLocal, err := NewCandidateHost(hostConfig)
	require.NoError(t, err)

	relayConfig := &CandidateRelayConfig{
		Network:   "udp",
		Address:   "1.2.3.4",
		Port:      2340,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43210,
	}
	relayRemote, err := NewCandidateRelay(relayConfig)
	require.NoError(t, err)

	srflxConfig := &CandidateServerReflexiveConfig{
		Network:   "udp",
		Address:   "10.10.10.2",
		Port:      19218,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43212,
	}
	srflxRemote, err := NewCandidateServerReflexive(srflxConfig)
	require.NoError(t, err)

	prflxConfig := &CandidatePeerReflexiveConfig{
		Network:   "udp",
		Address:   "10.10.10.2",
		Port:      19217,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43211,
	}
	prflxRemote, err := NewCandidatePeerReflexive(prflxConfig)
	require.NoError(t, err)

	hostConfig = &CandidateHostConfig{
		Network:   "udp",
		Address:   "1.2.3.5",
		Port:      12350,
		Component: 1,
	}
	hostRemote, err := NewCandidateHost(hostConfig)
	require.NoError(t, err)

	for _, remote := range []Candidate{relayRemote, srflxRemote, prflxRemote, hostRemote} {
		p := agent.findPair(hostLocal, remote)

		if p == nil {
			p = agent.addPair(hostLocal, remote)
		}
		p.UpdateRequestReceived()
		p.UpdateRequestSent()
		p.UpdateResponseSent()
		p.UpdateRoundTripTime(time.Second)
	}

	p := agent.findPair(hostLocal, prflxRemote)
	p.state = CandidatePairStateFailed

	for i := 1; i < 10; i++ {
		p.UpdateRoundTripTime(time.Duration(i+1) * time.Second)
	}

	stats := agent.GetCandidatePairsStats()
	require.Len(t, stats, 4)

	var relayPairStat, srflxPairStat, prflxPairStat, hostPairStat CandidatePairStats

	for _, cps := range stats {
		require.Equal(t, cps.LocalCandidateID, hostLocal.ID())
		switch cps.RemoteCandidateID {
		case relayRemote.ID():
			relayPairStat = cps
		case srflxRemote.ID():
			srflxPairStat = cps
		case prflxRemote.ID():
			prflxPairStat = cps
		case hostRemote.ID():
			hostPairStat = cps
		default:
			t.Fatal("invalid remote candidate ID") //nolint
		}

		require.False(t, cps.FirstRequestTimestamp.IsZero())
		require.False(t, cps.LastRequestTimestamp.IsZero())
		require.False(t, cps.FirstResponseTimestamp.IsZero())
		require.False(t, cps.LastResponseTimestamp.IsZero())
		require.False(t, cps.FirstRequestReceivedTimestamp.IsZero())
		require.False(t, cps.LastRequestReceivedTimestamp.IsZero())
		require.NotZero(t, cps.RequestsReceived)
		require.NotZero(t, cps.RequestsSent)
		require.NotZero(t, cps.ResponsesSent)
		require.NotZero(t, cps.ResponsesReceived)
	}

	require.Equal(t, relayPairStat.RemoteCandidateID, relayRemote.ID())
	require.Equal(t, srflxPairStat.RemoteCandidateID, srflxRemote.ID())
	require.Equal(t, prflxPairStat.RemoteCandidateID, prflxRemote.ID())
	require.Equal(t, hostPairStat.RemoteCandidateID, hostRemote.ID())
	require.Equal(t, prflxPairStat.State, CandidatePairStateFailed)

	require.Equal(t, float64(10), prflxPairStat.CurrentRoundTripTime)
	require.Equal(t, float64(55), prflxPairStat.TotalRoundTripTime)
	require.Equal(t, uint64(10), prflxPairStat.ResponsesReceived)
}

func TestSelectedCandidatePairStats(t *testing.T) { //nolint:cyclop
	defer test.CheckRoutines(t)()

	// Avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	hostConfig := &CandidateHostConfig{
		Network:   "udp",
		Address:   "192.168.1.1",
		Port:      19216,
		Component: 1,
	}
	hostLocal, err := NewCandidateHost(hostConfig)
	require.NoError(t, err)

	srflxConfig := &CandidateServerReflexiveConfig{
		Network:   "udp",
		Address:   "10.10.10.2",
		Port:      19218,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43212,
	}
	srflxRemote, err := NewCandidateServerReflexive(srflxConfig)
	require.NoError(t, err)

	// no selected pair, should return not available
	_, ok := agent.GetSelectedCandidatePairStats()
	require.False(t, ok)

	// add pair and populate some RTT stats
	p := agent.findPair(hostLocal, srflxRemote)
	if p == nil {
		agent.addPair(hostLocal, srflxRemote)
		p = agent.findPair(hostLocal, srflxRemote)
	}
	for i := 0; i < 10; i++ {
		p.UpdateRoundTripTime(time.Duration(i+1) * time.Second)
	}

	// set the pair as selected
	agent.setSelectedPair(p)

	stats, ok := agent.GetSelectedCandidatePairStats()
	require.True(t, ok)

	require.Equal(t, stats.LocalCandidateID, hostLocal.ID())
	require.Equal(t, stats.RemoteCandidateID, srflxRemote.ID())
	require.Equal(t, float64(10), stats.CurrentRoundTripTime)
	require.Equal(t, float64(55), stats.TotalRoundTripTime)
	require.Equal(t, uint64(10), stats.ResponsesReceived)
}

func TestLocalCandidateStats(t *testing.T) { //nolint:cyclop
	defer test.CheckRoutines(t)()

	// Avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	hostConfig := &CandidateHostConfig{
		Network:   "udp",
		Address:   "192.168.1.1",
		Port:      19216,
		Component: 1,
	}
	hostLocal, err := NewCandidateHost(hostConfig)
	require.NoError(t, err)

	srflxConfig := &CandidateServerReflexiveConfig{
		Network:   "udp",
		Address:   "192.168.1.1",
		Port:      19217,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43212,
	}
	srflxLocal, err := NewCandidateServerReflexive(srflxConfig)
	require.NoError(t, err)

	agent.localCandidates[NetworkTypeUDP4] = []Candidate{hostLocal, srflxLocal}

	localStats := agent.GetLocalCandidatesStats()
	require.Len(t, localStats, 2)

	var hostLocalStat, srflxLocalStat CandidateStats
	for _, stats := range localStats {
		var candidate Candidate
		switch stats.ID {
		case hostLocal.ID():
			hostLocalStat = stats
			candidate = hostLocal
		case srflxLocal.ID():
			srflxLocalStat = stats
			candidate = srflxLocal
		default:
			t.Fatal("invalid local candidate ID") // nolint
		}

		require.Equal(t, stats.CandidateType, candidate.Type())
		require.Equal(t, stats.Priority, candidate.Priority())
		require.Equal(t, stats.IP, candidate.Address())
	}

	require.Equal(t, hostLocalStat.ID, hostLocal.ID())
	require.Equal(t, srflxLocalStat.ID, srflxLocal.ID())
}

func TestRemoteCandidateStats(t *testing.T) { //nolint:cyclop
	defer test.CheckRoutines(t)()

	// Avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	relayConfig := &CandidateRelayConfig{
		Network:   "udp",
		Address:   "1.2.3.4",
		Port:      12340,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43210,
	}
	relayRemote, err := NewCandidateRelay(relayConfig)
	require.NoError(t, err)

	srflxConfig := &CandidateServerReflexiveConfig{
		Network:   "udp",
		Address:   "10.10.10.2",
		Port:      19218,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43212,
	}
	srflxRemote, err := NewCandidateServerReflexive(srflxConfig)
	require.NoError(t, err)

	prflxConfig := &CandidatePeerReflexiveConfig{
		Network:   "udp",
		Address:   "10.10.10.2",
		Port:      19217,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43211,
	}
	prflxRemote, err := NewCandidatePeerReflexive(prflxConfig)
	require.NoError(t, err)

	hostConfig := &CandidateHostConfig{
		Network:   "udp",
		Address:   "1.2.3.5",
		Port:      12350,
		Component: 1,
	}
	hostRemote, err := NewCandidateHost(hostConfig)
	require.NoError(t, err)

	agent.remoteCandidates[NetworkTypeUDP4] = []Candidate{relayRemote, srflxRemote, prflxRemote, hostRemote}

	remoteStats := agent.GetRemoteCandidatesStats()
	require.Len(t, remoteStats, 4)
	var relayRemoteStat, srflxRemoteStat, prflxRemoteStat, hostRemoteStat CandidateStats
	for _, stats := range remoteStats {
		var candidate Candidate
		switch stats.ID {
		case relayRemote.ID():
			relayRemoteStat = stats
			candidate = relayRemote
		case srflxRemote.ID():
			srflxRemoteStat = stats
			candidate = srflxRemote
		case prflxRemote.ID():
			prflxRemoteStat = stats
			candidate = prflxRemote
		case hostRemote.ID():
			hostRemoteStat = stats
			candidate = hostRemote
		default:
			t.Fatal("invalid remote candidate ID") // nolint
		}

		require.Equal(t, stats.CandidateType, candidate.Type())
		require.Equal(t, stats.Priority, candidate.Priority())
		require.Equal(t, stats.IP, candidate.Address())
	}

	require.Equal(t, relayRemoteStat.ID, relayRemote.ID())
	require.Equal(t, srflxRemoteStat.ID, srflxRemote.ID())
	require.Equal(t, prflxRemoteStat.ID, prflxRemote.ID())
	require.Equal(t, hostRemoteStat.ID, hostRemote.ID())
}

func TestInitExtIPMapping(t *testing.T) {
	defer test.CheckRoutines(t)()

	// agent.extIPMapper should be nil by default
	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)
	require.Nil(t, agent.extIPMapper)
	require.NoError(t, agent.Close())

	// a.extIPMapper should be nil when NAT1To1IPs is a non-nil empty array
	agent, err = NewAgent(&AgentConfig{
		NAT1To1IPs:             []string{},
		NAT1To1IPCandidateType: CandidateTypeHost,
	})
	require.NoError(t, err)
	require.Nil(t, agent.extIPMapper)
	require.NoError(t, agent.Close())

	// NewAgent should return an error when 1:1 NAT for host candidate is enabled
	// but the candidate type does not appear in the CandidateTypes.
	_, err = NewAgent(&AgentConfig{
		NAT1To1IPs:             []string{"1.2.3.4"},
		NAT1To1IPCandidateType: CandidateTypeHost,
		CandidateTypes:         []CandidateType{CandidateTypeRelay},
	})
	require.ErrorIs(t, ErrIneffectiveNAT1To1IPMappingHost, err)

	// NewAgent should return an error when 1:1 NAT for srflx candidate is enabled
	// but the candidate type does not appear in the CandidateTypes.
	_, err = NewAgent(&AgentConfig{
		NAT1To1IPs:             []string{"1.2.3.4"},
		NAT1To1IPCandidateType: CandidateTypeServerReflexive,
		CandidateTypes:         []CandidateType{CandidateTypeRelay},
	})
	require.ErrorIs(t, ErrIneffectiveNAT1To1IPMappingSrflx, err)

	// NewAgent should return an error when 1:1 NAT for host candidate is enabled
	// along with mDNS with MulticastDNSModeQueryAndGather
	_, err = NewAgent(&AgentConfig{
		NAT1To1IPs:             []string{"1.2.3.4"},
		NAT1To1IPCandidateType: CandidateTypeHost,
		MulticastDNSMode:       MulticastDNSModeQueryAndGather,
	})
	require.ErrorIs(t, ErrMulticastDNSWithNAT1To1IPMapping, err)

	// NewAgent should return if newExternalIPMapper() returns an error.
	_, err = NewAgent(&AgentConfig{
		NAT1To1IPs:             []string{"bad.2.3.4"}, // Bad IP
		NAT1To1IPCandidateType: CandidateTypeHost,
	})
	require.ErrorIs(t, ErrInvalidNAT1To1IPMapping, err)
}

func TestBindingRequestTimeout(t *testing.T) {
	defer test.CheckRoutines(t)()

	const expectedRemovalCount = 2

	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	now := time.Now()
	agent.pendingBindingRequests = append(agent.pendingBindingRequests, bindingRequest{
		timestamp: now, // Valid
	})
	agent.pendingBindingRequests = append(agent.pendingBindingRequests, bindingRequest{
		timestamp: now.Add(-3900 * time.Millisecond), // Valid
	})
	agent.pendingBindingRequests = append(agent.pendingBindingRequests, bindingRequest{
		timestamp: now.Add(-4100 * time.Millisecond), // Invalid
	})
	agent.pendingBindingRequests = append(agent.pendingBindingRequests, bindingRequest{
		timestamp: now.Add(-75 * time.Hour), // Invalid
	})

	agent.invalidatePendingBindingRequests(now)

	require.Equal(
		t,
		expectedRemovalCount,
		len(agent.pendingBindingRequests),
		"Binding invalidation due to timeout did not remove the correct number of binding requests",
	)
}

// TestAgentCredentials checks if local username fragments and passwords (if set) meet RFC standard
// and ensure it's backwards compatible with previous versions of the pion/ice.
func TestAgentCredentials(t *testing.T) {
	defer test.CheckRoutines(t)()

	// Make sure to pass Travis check by disabling the logs
	log := logging.NewDefaultLoggerFactory()
	log.DefaultLogLevel = logging.LogLevelDisabled

	// Agent should not require any of the usernames and password to be set
	// If set, they should follow the default 16/128 bits random number generator strategy

	agent, err := NewAgent(&AgentConfig{LoggerFactory: log})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()
	require.GreaterOrEqual(t, len([]rune(agent.localUfrag))*8, 24)
	require.GreaterOrEqual(t, len([]rune(agent.localPwd))*8, 128)

	// Should honor RFC standards
	// Local values MUST be unguessable, with at least 128 bits of
	// random number generator output used to generate the password, and
	// at least 24 bits of output to generate the username fragment.

	_, err = NewAgent(&AgentConfig{LocalUfrag: "xx", LoggerFactory: log})
	require.EqualError(t, err, ErrLocalUfragInsufficientBits.Error())

	_, err = NewAgent(&AgentConfig{LocalPwd: "xxxxxx", LoggerFactory: log})
	require.EqualError(t, err, ErrLocalPwdInsufficientBits.Error())
}

// Assert that Agent on Failure deletes all existing candidates
// User can then do an ICE Restart to bring agent back.
func TestConnectionStateFailedDeleteAllCandidates(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 5).Stop()

	oneSecond := time.Second
	KeepaliveInterval := time.Duration(0)

	cfg := &AgentConfig{
		NetworkTypes:        supportedNetworkTypes(),
		DisconnectedTimeout: &oneSecond,
		FailedTimeout:       &oneSecond,
		KeepaliveInterval:   &KeepaliveInterval,
	}

	aAgent, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, aAgent.Close())
	}()

	bAgent, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, bAgent.Close())
	}()

	isFailed := make(chan any)
	require.NoError(t, aAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateFailed {
			close(isFailed)
		}
	}))

	connect(t, aAgent, bAgent)
	<-isFailed

	done := make(chan struct{})
	require.NoError(t, aAgent.loop.Run(context.Background(), func(context.Context) {
		require.Equal(t, len(aAgent.remoteCandidates), 0)
		require.Equal(t, len(aAgent.localCandidates), 0)
		close(done)
	}))
	<-done
}

// Assert that the ICE Agent can go directly from Connecting -> Failed on both sides.
func TestConnectionStateConnectingToFailed(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 5).Stop()

	oneSecond := time.Second
	KeepaliveInterval := time.Duration(0)

	cfg := &AgentConfig{
		DisconnectedTimeout: &oneSecond,
		FailedTimeout:       &oneSecond,
		KeepaliveInterval:   &KeepaliveInterval,
	}

	aAgent, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, aAgent.Close())
	}()

	bAgent, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, bAgent.Close())
	}()

	var isFailed sync.WaitGroup
	var isChecking sync.WaitGroup

	isFailed.Add(2)
	isChecking.Add(2)

	connectionStateCheck := func(c ConnectionState) {
		switch c {
		case ConnectionStateFailed:
			isFailed.Done()
		case ConnectionStateChecking:
			isChecking.Done()
		case ConnectionStateCompleted:
			t.Errorf("Unexpected ConnectionState: %v", c) //nolint
		default:
		}
	}

	require.NoError(t, aAgent.OnConnectionStateChange(connectionStateCheck))
	require.NoError(t, bAgent.OnConnectionStateChange(connectionStateCheck))

	go func() {
		_, err := aAgent.Accept(context.TODO(), "InvalidFrag", "InvalidPwd")
		require.Error(t, err)
	}()

	go func() {
		_, err := bAgent.Dial(context.TODO(), "InvalidFrag", "InvalidPwd")
		require.Error(t, err)
	}()

	isChecking.Wait()
	isFailed.Wait()
}

func TestAgentRestart(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	oneSecond := time.Second

	t.Run("Restart During Gather", func(t *testing.T) {
		connA, connB := pipe(t, &AgentConfig{
			DisconnectedTimeout: &oneSecond,
			FailedTimeout:       &oneSecond,
		})
		defer closePipe(t, connA, connB)

		ctx, cancel := context.WithCancel(context.Background())
		require.NoError(t, connB.agent.OnConnectionStateChange(func(c ConnectionState) {
			if c == ConnectionStateFailed || c == ConnectionStateDisconnected {
				cancel()
			}
		}))

		connA.agent.gatheringState = GatheringStateGathering
		require.NoError(t, connA.agent.Restart("", ""))

		<-ctx.Done()
	})

	t.Run("Restart When Closed", func(t *testing.T) {
		agent, err := NewAgent(&AgentConfig{})
		require.NoError(t, err)
		require.NoError(t, agent.Close())

		require.Equal(t, ErrClosed, agent.Restart("", ""))
	})

	t.Run("Restart One Side", func(t *testing.T) {
		connA, connB := pipe(t, &AgentConfig{
			DisconnectedTimeout: &oneSecond,
			FailedTimeout:       &oneSecond,
		})
		defer closePipe(t, connA, connB)

		ctx, cancel := context.WithCancel(context.Background())
		require.NoError(t, connB.agent.OnConnectionStateChange(func(c ConnectionState) {
			if c == ConnectionStateFailed || c == ConnectionStateDisconnected {
				cancel()
			}
		}))
		require.NoError(t, connA.agent.Restart("", ""))

		<-ctx.Done()
	})

	t.Run("Restart Both Sides", func(t *testing.T) {
		// Get all addresses of candidates concatenated
		generateCandidateAddressStrings := func(candidates []Candidate, err error) (out string) {
			require.NoError(t, err)

			for _, c := range candidates {
				out += c.Address() + ":"
				out += strconv.Itoa(c.Port())
			}

			return
		}

		// Store the original candidates, confirm that after we reconnect we have new pairs
		connA, connB := pipe(t, &AgentConfig{
			DisconnectedTimeout: &oneSecond,
			FailedTimeout:       &oneSecond,
		})
		defer closePipe(t, connA, connB)
		connAFirstCandidates := generateCandidateAddressStrings(connA.agent.GetLocalCandidates())
		connBFirstCandidates := generateCandidateAddressStrings(connB.agent.GetLocalCandidates())

		aNotifier, aConnected := onConnected()
		require.NoError(t, connA.agent.OnConnectionStateChange(aNotifier))

		bNotifier, bConnected := onConnected()
		require.NoError(t, connB.agent.OnConnectionStateChange(bNotifier))

		// Restart and Re-Signal
		require.NoError(t, connA.agent.Restart("", ""))
		require.NoError(t, connB.agent.Restart("", ""))

		// Exchange Candidates and Credentials
		ufrag, pwd, err := connB.agent.GetLocalUserCredentials()
		require.NoError(t, err)
		require.NoError(t, connA.agent.SetRemoteCredentials(ufrag, pwd))

		ufrag, pwd, err = connA.agent.GetLocalUserCredentials()
		require.NoError(t, err)
		require.NoError(t, connB.agent.SetRemoteCredentials(ufrag, pwd))

		gatherAndExchangeCandidates(t, connA.agent, connB.agent)

		// Wait until both have gone back to connected
		<-aConnected
		<-bConnected

		// Assert that we have new candidates each time
		require.NotEqual(t, connAFirstCandidates, generateCandidateAddressStrings(connA.agent.GetLocalCandidates()))
		require.NotEqual(t, connBFirstCandidates, generateCandidateAddressStrings(connB.agent.GetLocalCandidates()))
	})
}

func TestGetRemoteCredentials(t *testing.T) {
	var config AgentConfig
	agent, err := NewAgent(&config)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	agent.remoteUfrag = "remoteUfrag"
	agent.remotePwd = "remotePwd"

	actualUfrag, actualPwd, err := agent.GetRemoteUserCredentials()
	require.NoError(t, err)

	require.Equal(t, actualUfrag, agent.remoteUfrag)
	require.Equal(t, actualPwd, agent.remotePwd)
}

func TestGetRemoteCandidates(t *testing.T) {
	var config AgentConfig

	agent, err := NewAgent(&config)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	expectedCandidates := []Candidate{}

	for i := 0; i < 5; i++ {
		cfg := CandidateHostConfig{
			Network:   "udp",
			Address:   "192.168.0.2",
			Port:      1000 + i,
			Component: 1,
		}

		cand, errCand := NewCandidateHost(&cfg)
		require.NoError(t, errCand)

		expectedCandidates = append(expectedCandidates, cand)

		agent.addRemoteCandidate(cand)
	}

	actualCandidates, err := agent.GetRemoteCandidates()
	require.NoError(t, err)
	require.ElementsMatch(t, expectedCandidates, actualCandidates)
}

// Ensure that when HostUDPAdvertisedAddrsMapper returns no endpoints, agents fail to connect.
func TestAdvancedMapperUDPConnectFail(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(5 * time.Second).Stop()

	disconnected := time.Second
	failed := time.Second
	KeepaliveInterval := time.Duration(0)

	mkAgent := func() *Agent {
		a, err := NewAgent(&AgentConfig{
			CandidateTypes:               []CandidateType{CandidateTypeHost},
			NetworkTypes:                 []NetworkType{NetworkTypeUDP4},
			IncludeLoopback:              true,
			DisconnectedTimeout:          &disconnected,
			FailedTimeout:                &failed,
			KeepaliveInterval:            &KeepaliveInterval,
			NAT1To1IPCandidateType:       CandidateTypeHost,
			HostUDPAdvertisedAddrsMapper: func(net.IP) []endpoint { return []endpoint{} },
		})
		require.NoError(t, err)

		return a
	}

	aAgent := mkAgent()
	bAgent := mkAgent()
	defer func() {
		require.NoError(t, aAgent.Close())
		require.NoError(t, bAgent.Close())
	}()

	aFailed := make(chan struct{})
	bFailed := make(chan struct{})
	require.NoError(t, aAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateFailed {
			select {
			case <-aFailed:
			default:
				close(aFailed)
			}
		}
	}))
	require.NoError(t, bAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateFailed {
			select {
			case <-bFailed:
			default:
				close(bFailed)
			}
		}
	}))

	// Gather (should produce zero candidates) and exchange
	gatherAndExchangeCandidates(t, aAgent, bAgent)
	la, err := aAgent.GetLocalCandidates()
	require.NoError(t, err)
	require.Len(t, la, 0)
	lb, err := bAgent.GetLocalCandidates()
	require.NoError(t, err)
	require.Len(t, lb, 0)

	// Attempt to connect with timeouts; expect errors
	bUfrag, bPwd, err := bAgent.GetLocalUserCredentials()
	require.NoError(t, err)
	aCtx, aCancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer aCancel()
	var aErr error
	doneAccept := make(chan struct{})
	go func() {
		_, aErr = aAgent.Accept(aCtx, bUfrag, bPwd)
		close(doneAccept)
	}()

	aUfrag, aPwd, err := aAgent.GetLocalUserCredentials()
	require.NoError(t, err)
	bCtx, bCancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer bCancel()
	_, bErr := bAgent.Dial(bCtx, aUfrag, aPwd)
	require.Error(t, bErr)
	<-doneAccept
	require.Error(t, aErr)

	// Observe failed state
	select {
	case <-aFailed:
	case <-time.After(3 * time.Second):
		t.Fatal("aAgent did not reach Failed state")
	}
	select {
	case <-bFailed:
	case <-time.After(3 * time.Second):
		t.Fatal("bAgent did not reach Failed state")
	}
}

// Ensure that when HostTCPAdvertisedAddrsMapper returns no endpoints, agents fail to connect over TCP.
func TestAdvancedMapperTCPConnectFail(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(5 * time.Second).Stop()

	disconnected := time.Second
	failed := time.Second
	KeepaliveInterval := time.Duration(0)

	// Create TCPMux listeners for both agents
	mkTCPMux := func() TCPMux {
		l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: 0})
		require.NoError(t, err)
		t.Cleanup(func() { _ = l.Close() })
		return NewTCPMuxDefault(TCPMuxParams{Listener: l, ReadBufferSize: 8})
	}

	mkAgent := func(mux TCPMux) *Agent {
		a, err := NewAgent(&AgentConfig{
			CandidateTypes:               []CandidateType{CandidateTypeHost},
			NetworkTypes:                 []NetworkType{NetworkTypeTCP4},
			IncludeLoopback:              true,
			TCPMux:                       mux,
			DisconnectedTimeout:          &disconnected,
			FailedTimeout:                &failed,
			KeepaliveInterval:            &KeepaliveInterval,
			NAT1To1IPCandidateType:       CandidateTypeHost,
			HostTCPAdvertisedAddrsMapper: func(net.IP) []endpoint { return []endpoint{} },
		})
		require.NoError(t, err)

		return a
	}

	muxA := mkTCPMux()
	muxB := mkTCPMux()
	aAgent := mkAgent(muxA)
	bAgent := mkAgent(muxB)
	defer func() {
		require.NoError(t, aAgent.Close())
		require.NoError(t, bAgent.Close())
		require.NoError(t, muxA.Close())
		require.NoError(t, muxB.Close())
	}()

	aFailed := make(chan struct{})
	bFailed := make(chan struct{})
	require.NoError(t, aAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateFailed {
			select {
			case <-aFailed:
			default:
				close(aFailed)
			}
		}
	}))
	require.NoError(t, bAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateFailed {
			select {
			case <-bFailed:
			default:
				close(bFailed)
			}
		}
	}))

	// Gather (should produce zero candidates) and exchange
	gatherAndExchangeCandidates(t, aAgent, bAgent)
	la, err := aAgent.GetLocalCandidates()
	require.NoError(t, err)
	require.Len(t, la, 0)
	lb, err := bAgent.GetLocalCandidates()
	require.NoError(t, err)
	require.Len(t, lb, 0)

	// Attempt to connect with timeouts; expect errors
	bUfrag, bPwd, err := bAgent.GetLocalUserCredentials()
	require.NoError(t, err)
	aCtx, aCancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer aCancel()
	var aErr error
	doneAccept := make(chan struct{})
	go func() {
		_, aErr = aAgent.Accept(aCtx, bUfrag, bPwd)
		close(doneAccept)
	}()

	aUfrag, aPwd, err := aAgent.GetLocalUserCredentials()
	require.NoError(t, err)
	bCtx, bCancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer bCancel()
	_, bErr := bAgent.Dial(bCtx, aUfrag, aPwd)
	require.Error(t, bErr)
	<-doneAccept
	require.Error(t, aErr)

	// Observe failed state
	select {
	case <-aFailed:
	case <-time.After(3 * time.Second):
		t.Fatal("aAgent did not reach Failed state (TCP)")
	}
	select {
	case <-bFailed:
	case <-time.After(3 * time.Second):
		t.Fatal("bAgent did not reach Failed state (TCP)")
	}
}

// When both agents advertise multiple UDP endpoints that are unroutable, they gather candidates
// but still fail to connect.
func TestAdvancedMapperUDPAdvertisedButUnreachableConnectFail(t *testing.T) { //nolint:dupl
	defer test.CheckRoutines(t)()

	defer test.TimeOut(8 * time.Second).Stop()

	disconnected := time.Second
	failed := time.Second
	KeepaliveInterval := time.Duration(0)

	ip1 := net.ParseIP("198.18.10.1")
	ip2 := net.ParseIP("198.18.10.2")
	ip3 := net.ParseIP("198.18.10.3")
	ip4 := net.ParseIP("198.18.10.4")
	p1, p2, p3, p4 := 52001, 52002, 52003, 52004

	mkAgent := func(hostUDPAdvertisedAddrsMapper func(net.IP) []endpoint) *Agent {
		a, err := NewAgent(&AgentConfig{
			CandidateTypes:         []CandidateType{CandidateTypeHost},
			NetworkTypes:           []NetworkType{NetworkTypeUDP4},
			IncludeLoopback:        true,
			DisconnectedTimeout:    &disconnected,
			FailedTimeout:          &failed,
			KeepaliveInterval:      &KeepaliveInterval,
			NAT1To1IPCandidateType: CandidateTypeHost,
			HostUDPAdvertisedAddrsMapper: hostUDPAdvertisedAddrsMapper,
		})
		require.NoError(t, err)

		return a
	}

	aAgent := mkAgent(func(net.IP) []endpoint {
		return []endpoint{{ip: ip1, port: p1}, {ip: ip2, port: p2}, {ip: ip3, port: p3}}
	})
	bAgent := mkAgent(func(net.IP) []endpoint {
		return []endpoint{{ip: ip4, port: p4}}
	})
	defer func() {
		require.NoError(t, aAgent.Close())
		require.NoError(t, bAgent.Close())
	}()

	aFailed := make(chan struct{})
	bFailed := make(chan struct{})
	require.NoError(t, aAgent.OnConnectionStateChange(func(c ConnectionState) {
		t.Log("aAgent connection state changed to", c)
		if c == ConnectionStateFailed {
			select {
			case <-aFailed:
			default:
				close(aFailed)
			}
		}
	}))
	require.NoError(t, bAgent.OnConnectionStateChange(func(c ConnectionState) {
		t.Log("bAgent connection state changed to", c)
		if c == ConnectionStateFailed {
			select {
			case <-bFailed:
			default:
				close(bFailed)
			}
		}
	}))

	// Gather and exchange
	gatherAndExchangeCandidates(t, aAgent, bAgent)
	la, err := aAgent.GetLocalCandidates()
	require.NoError(t, err)
	require.Equal(t, 3, len(la))
	bAgent.GetLocalCandidates()
	lb, err := bAgent.GetLocalCandidates()
	require.NoError(t, err)
	require.Equal(t, 1, len(lb))

	// Attempt connect; expect timeouts/errors due to unroutable advertised addresses.
	bUfrag, bPwd, err := bAgent.GetLocalUserCredentials()
	require.NoError(t, err)
	aCtx, aCancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer aCancel()
	var aErr error
	doneAccept := make(chan struct{})
	go func() {
		_, aErr = aAgent.Accept(aCtx, bUfrag, bPwd)
		close(doneAccept)
	}()

	aUfrag, aPwd, err := aAgent.GetLocalUserCredentials()
	require.NoError(t, err)
	bCtx, bCancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer bCancel()
	_, bErr := bAgent.Dial(bCtx, aUfrag, aPwd)
	require.Error(t, bErr)
	<-doneAccept
	require.Error(t, aErr)

	// Observe failed state
	select {
	case <-aFailed:
	case <-time.After(3 * time.Second):
		t.Fatal("aAgent did not reach Failed state (UDP advertised unreachable)")
	}
	select {
	case <-bFailed:
	case <-time.After(3 * time.Second):
		t.Fatal("bAgent did not reach Failed state (UDP advertised unreachable)")
	}
}

// When both agents advertise multiple TCP endpoints that are unroutable, they gather candidates
// but still fail to connect.
func TestAdvancedMapperTCPAdvertisedButUnreachableConnectFail(t *testing.T) { //nolint:dupl
	defer test.CheckRoutines(t)()

	defer test.TimeOut(8 * time.Second).Stop()

	disconnected := time.Second
	failed := time.Second
	KeepaliveInterval := time.Duration(0)

	// Create TCPMux listeners for both agents
	mkTCPMux := func() TCPMux {
		l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: 0})
		require.NoError(t, err)
		t.Cleanup(func() { _ = l.Close() })
		return NewTCPMuxDefault(TCPMuxParams{Listener: l, ReadBufferSize: 8})
	}

	ip1 := net.ParseIP("203.0.113.10")
	ip2 := net.ParseIP("203.0.113.11")
	ip3 := net.ParseIP("203.0.113.12")
	p1, p2, p3 := 53001, 53002, 53003

	mkAgent := func(mux TCPMux) *Agent {
		a, err := NewAgent(&AgentConfig{
			CandidateTypes:         []CandidateType{CandidateTypeHost},
			NetworkTypes:           []NetworkType{NetworkTypeTCP4},
			IncludeLoopback:        true,
			TCPMux:                 mux,
			DisconnectedTimeout:    &disconnected,
			FailedTimeout:          &failed,
			KeepaliveInterval:      &KeepaliveInterval,
			NAT1To1IPCandidateType: CandidateTypeHost,
			HostTCPAdvertisedAddrsMapper: func(net.IP) []endpoint {
				return []endpoint{{ip: ip1, port: p1}, {ip: ip2, port: p2}, {ip: ip3, port: p3}}
			},
		})
		require.NoError(t, err)

		return a
	}

	muxA := mkTCPMux()
	muxB := mkTCPMux()
	aAgent := mkAgent(muxA)
	bAgent := mkAgent(muxB)
	defer func() {
		require.NoError(t, aAgent.Close())
		require.NoError(t, bAgent.Close())
		require.NoError(t, muxA.Close())
		require.NoError(t, muxB.Close())
	}()

	aFailed := make(chan struct{})
	bFailed := make(chan struct{})
	require.NoError(t, aAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateFailed {
			select {
			case <-aFailed:
			default:
				close(aFailed)
			}
		}
	}))
	require.NoError(t, bAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateFailed {
			select {
			case <-bFailed:
			default:
				close(bFailed)
			}
		}
	}))

	// Gather and exchange
	gatherAndExchangeCandidates(t, aAgent, bAgent)
	la, err := aAgent.GetLocalCandidates()
	require.NoError(t, err)
	// Only passive TCP host candidates should be counted (active TCP may be spawned automatically)
	var tcpCount int
	for _, c := range la {
		if c.NetworkType().IsTCP() && c.Type() == CandidateTypeHost && c.TCPType() == TCPTypePassive {
			tcpCount++
		}
	}
	require.Equal(t, 3, tcpCount)

	lb, err := bAgent.GetLocalCandidates()
	require.NoError(t, err)
	tcpCount = 0
	for _, c := range lb {
		if c.NetworkType().IsTCP() && c.Type() == CandidateTypeHost && c.TCPType() == TCPTypePassive {
			tcpCount++
		}
	}
	require.Equal(t, 3, tcpCount)

	// Attempt connect; expect timeouts/errors due to unroutable advertised addresses
	bUfrag, bPwd, err := bAgent.GetLocalUserCredentials()
	require.NoError(t, err)
	aCtx, aCancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer aCancel()
	var aErr error
	doneAccept := make(chan struct{})
	go func() {
		_, aErr = aAgent.Accept(aCtx, bUfrag, bPwd)
		close(doneAccept)
	}()

	aUfrag, aPwd, err := aAgent.GetLocalUserCredentials()
	require.NoError(t, err)
	bCtx, bCancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer bCancel()
	_, bErr := bAgent.Dial(bCtx, aUfrag, aPwd)
	require.Error(t, bErr)
	<-doneAccept
	require.Error(t, aErr)

	// Observe failed state
	select {
	case <-aFailed:
	case <-time.After(3 * time.Second):
		t.Fatal("aAgent did not reach Failed state (TCP advertised unreachable)")
	}
	select {
	case <-bFailed:
	case <-time.After(3 * time.Second):
		t.Fatal("bAgent did not reach Failed state (TCP advertised unreachable)")
	}
}

func TestGetLocalCandidates(t *testing.T) {
	var config AgentConfig

	agent, err := NewAgent(&config)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	dummyConn := &net.UDPConn{}
	expectedCandidates := []Candidate{}

	for i := 0; i < 5; i++ {
		cfg := CandidateHostConfig{
			Network:   "udp",
			Address:   "192.168.0.2",
			Port:      1000 + i,
			Component: 1,
		}

		cand, errCand := NewCandidateHost(&cfg)
		require.NoError(t, errCand)

		expectedCandidates = append(expectedCandidates, cand)

		err = agent.addCandidate(context.Background(), cand, dummyConn)
		require.NoError(t, err)
	}

	actualCandidates, err := agent.GetLocalCandidates()
	require.NoError(t, err)
	require.ElementsMatch(t, expectedCandidates, actualCandidates)
}

func TestCloseInConnectionStateCallback(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 5).Stop()

	disconnectedDuration := time.Second
	failedDuration := time.Second
	KeepaliveInterval := time.Duration(0)
	CheckInterval := 500 * time.Millisecond

	cfg := &AgentConfig{
		Urls:                []*stun.URI{},
		NetworkTypes:        supportedNetworkTypes(),
		DisconnectedTimeout: &disconnectedDuration,
		FailedTimeout:       &failedDuration,
		KeepaliveInterval:   &KeepaliveInterval,
		CheckInterval:       &CheckInterval,
	}

	aAgent, err := NewAgent(cfg)
	require.NoError(t, err)
	var aAgentClosed bool
	defer func() {
		if aAgentClosed {
			return
		}
		require.NoError(t, aAgent.Close())
	}()

	bAgent, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, bAgent.Close())
	}()

	isClosed := make(chan any)
	isConnected := make(chan any)
	err = aAgent.OnConnectionStateChange(func(c ConnectionState) {
		switch c {
		case ConnectionStateConnected:
			<-isConnected
			require.NoError(t, aAgent.Close())
			aAgentClosed = true
		case ConnectionStateClosed:
			close(isClosed)
		default:
		}
	})
	require.NoError(t, err)

	connect(t, aAgent, bAgent)
	close(isConnected)

	<-isClosed
}

func TestRunTaskInConnectionStateCallback(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 5).Stop()

	oneSecond := time.Second
	KeepaliveInterval := time.Duration(0)
	CheckInterval := 50 * time.Millisecond

	cfg := &AgentConfig{
		Urls:                []*stun.URI{},
		NetworkTypes:        supportedNetworkTypes(),
		DisconnectedTimeout: &oneSecond,
		FailedTimeout:       &oneSecond,
		KeepaliveInterval:   &KeepaliveInterval,
		CheckInterval:       &CheckInterval,
	}

	aAgent, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, aAgent.Close())
	}()
	bAgent, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, bAgent.Close())
	}()

	isComplete := make(chan any)
	err = aAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateConnected {
			_, _, errCred := aAgent.GetLocalUserCredentials()
			require.NoError(t, errCred)
			require.NoError(t, aAgent.Restart("", ""))
			close(isComplete)
		}
	})
	require.NoError(t, err)

	connect(t, aAgent, bAgent)

	<-isComplete
}

func TestRunTaskInSelectedCandidatePairChangeCallback(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 5).Stop()

	oneSecond := time.Second
	KeepaliveInterval := time.Duration(0)
	CheckInterval := 50 * time.Millisecond

	cfg := &AgentConfig{
		Urls:                []*stun.URI{},
		NetworkTypes:        supportedNetworkTypes(),
		DisconnectedTimeout: &oneSecond,
		FailedTimeout:       &oneSecond,
		KeepaliveInterval:   &KeepaliveInterval,
		CheckInterval:       &CheckInterval,
	}

	aAgent, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, aAgent.Close())
	}()
	bAgent, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, bAgent.Close())
	}()

	isComplete := make(chan any)
	isTested := make(chan any)
	err = aAgent.OnSelectedCandidatePairChange(func(Candidate, Candidate) {
		go func() {
			_, _, errCred := aAgent.GetLocalUserCredentials()
			require.NoError(t, errCred)
			close(isTested)
		}()
	})
	require.NoError(t, err)

	err = aAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateConnected {
			close(isComplete)
		}
	})
	require.NoError(t, err)

	connect(t, aAgent, bAgent)

	<-isComplete
	<-isTested
}

// Assert that a Lite agent goes to disconnected and failed.
func TestLiteLifecycle(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	aNotifier, aConnected := onConnected()

	aAgent, err := NewAgent(&AgentConfig{
		NetworkTypes:     supportedNetworkTypes(),
		MulticastDNSMode: MulticastDNSModeDisabled,
	})
	require.NoError(t, err)
	var aClosed bool
	defer func() {
		if aClosed {
			return
		}
		require.NoError(t, aAgent.Close())
	}()
	require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))

	disconnectedDuration := time.Second
	failedDuration := time.Second
	KeepaliveInterval := time.Duration(0)
	CheckInterval := 500 * time.Millisecond
	bAgent, err := NewAgent(&AgentConfig{
		Lite:                true,
		CandidateTypes:      []CandidateType{CandidateTypeHost},
		NetworkTypes:        supportedNetworkTypes(),
		MulticastDNSMode:    MulticastDNSModeDisabled,
		DisconnectedTimeout: &disconnectedDuration,
		FailedTimeout:       &failedDuration,
		KeepaliveInterval:   &KeepaliveInterval,
		CheckInterval:       &CheckInterval,
	})
	require.NoError(t, err)
	var bClosed bool
	defer func() {
		if bClosed {
			return
		}
		require.NoError(t, bAgent.Close())
	}()

	bConnected := make(chan any)
	bDisconnected := make(chan any)
	bFailed := make(chan any)

	require.NoError(t, bAgent.OnConnectionStateChange(func(c ConnectionState) {
		switch c {
		case ConnectionStateConnected:
			close(bConnected)
		case ConnectionStateDisconnected:
			close(bDisconnected)
		case ConnectionStateFailed:
			close(bFailed)
		default:
		}
	}))

	connectWithVNet(t, bAgent, aAgent)

	<-aConnected
	<-bConnected
	require.NoError(t, aAgent.Close())
	aClosed = true

	<-bDisconnected
	<-bFailed
	require.NoError(t, bAgent.Close())
	bClosed = true
}

func TestNilCandidate(t *testing.T) {
	a, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)

	require.NoError(t, a.AddRemoteCandidate(nil))
	require.NoError(t, a.Close())
}

func TestNilCandidatePair(t *testing.T) {
	a, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, a.Close())
	}()

	a.setSelectedPair(nil)
}

func TestGetSelectedCandidatePair(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	wan, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "0.0.0.0/0",
		LoggerFactory: logging.NewDefaultLoggerFactory(),
	})
	require.NoError(t, err)

	net, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{"192.168.0.1"},
	})
	require.NoError(t, err)
	require.NoError(t, wan.AddNet(net))

	require.NoError(t, wan.Start())

	cfg := &AgentConfig{
		NetworkTypes: supportedNetworkTypes(),
		Net:          net,
	}

	aAgent, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, aAgent.Close())
	}()

	bAgent, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, bAgent.Close())
	}()

	aAgentPair, err := aAgent.GetSelectedCandidatePair()
	require.NoError(t, err)
	require.Nil(t, aAgentPair)

	bAgentPair, err := bAgent.GetSelectedCandidatePair()
	require.NoError(t, err)
	require.Nil(t, bAgentPair)

	connect(t, aAgent, bAgent)

	aAgentPair, err = aAgent.GetSelectedCandidatePair()
	require.NoError(t, err)
	require.NotNil(t, aAgentPair)

	bAgentPair, err = bAgent.GetSelectedCandidatePair()
	require.NoError(t, err)
	require.NotNil(t, bAgentPair)

	require.True(t, bAgentPair.Local.Equal(aAgentPair.Remote))
	require.True(t, bAgentPair.Remote.Equal(aAgentPair.Local))

	require.NoError(t, wan.Stop())
}

func TestAcceptAggressiveNomination(t *testing.T) { //nolint:cyclop
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	// Create a network with two interfaces
	wan, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "0.0.0.0/0",
		LoggerFactory: logging.NewDefaultLoggerFactory(),
	})
	require.NoError(t, err)

	net0, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{"192.168.0.1"},
	})
	require.NoError(t, err)
	require.NoError(t, wan.AddNet(net0))

	net1, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{"192.168.0.2", "192.168.0.3", "192.168.0.4"},
	})
	require.NoError(t, err)
	require.NoError(t, wan.AddNet(net1))

	require.NoError(t, wan.Start())

	testCases := []struct {
		name                            string
		isLite                          bool
		enableUseCandidateCheckPriority bool
		useHigherPriority               bool
		isExpectedToSwitch              bool
	}{
		{"should accept higher priority - full agent", false, false, true, true},
		{"should not accept lower priority - full agent", false, false, false, false},
		{"should accept higher priority - no use-candidate priority check - lite agent", true, false, true, true},
		{"should accept lower priority - no use-candidate priority check - lite agent", true, false, false, true},
		{"should accept higher priority - use-candidate priority check - lite agent", true, true, true, true},
		{"should not accept lower priority - use-candidate priority check - lite agent", true, true, false, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			aNotifier, aConnected := onConnected()
			bNotifier, bConnected := onConnected()

			KeepaliveInterval := time.Hour
			cfg0 := &AgentConfig{
				NetworkTypes:                    []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				MulticastDNSMode:                MulticastDNSModeDisabled,
				Net:                             net0,
				KeepaliveInterval:               &KeepaliveInterval,
				CheckInterval:                   &KeepaliveInterval,
				Lite:                            tc.isLite,
				EnableUseCandidateCheckPriority: tc.enableUseCandidateCheckPriority,
			}
			if tc.isLite {
				cfg0.CandidateTypes = []CandidateType{CandidateTypeHost}
			}

			var aAgent, bAgent *Agent
			aAgent, err = NewAgent(cfg0)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, aAgent.Close())
			}()
			require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))

			cfg1 := &AgentConfig{
				NetworkTypes:      []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				MulticastDNSMode:  MulticastDNSModeDisabled,
				Net:               net1,
				KeepaliveInterval: &KeepaliveInterval,
				CheckInterval:     &KeepaliveInterval,
			}

			bAgent, err = NewAgent(cfg1)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, bAgent.Close())
			}()
			require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))

			connect(t, aAgent, bAgent)

			// Ensure pair selected
			// Note: this assumes ConnectionStateConnected is thrown after selecting the final pair
			<-aConnected
			<-bConnected

			// Send new USE-CANDIDATE message with priority to update the selected pair
			buildMsg := func(class stun.MessageClass, username, key string, priority uint32) *stun.Message {
				msg, err1 := stun.Build(stun.NewType(stun.MethodBinding, class), stun.TransactionID,
					stun.NewUsername(username),
					stun.NewShortTermIntegrity(key),
					UseCandidate(),
					PriorityAttr(priority),
					stun.Fingerprint,
				)
				require.NoError(t, err1)

				return msg
			}

			selectedCh := make(chan Candidate, 1)
			var expectNewSelectedCandidate Candidate
			err = aAgent.OnSelectedCandidatePairChange(func(_, remote Candidate) {
				selectedCh <- remote
			})
			require.NoError(t, err)
			var bcandidates []Candidate
			bcandidates, err = bAgent.GetLocalCandidates()
			require.NoError(t, err)

			for _, cand := range bcandidates {
				if cand != bAgent.getSelectedPair().Local { //nolint:nestif
					if expectNewSelectedCandidate == nil {
					expected_change_priority:
						for _, candidates := range aAgent.remoteCandidates {
							for _, candidate := range candidates {
								if candidate.Equal(cand) {
									if tc.useHigherPriority {
										candidate.(*CandidateHost).priorityOverride += 1000 //nolint:forcetypeassert
									} else {
										candidate.(*CandidateHost).priorityOverride -= 1000 //nolint:forcetypeassert
									}

									break expected_change_priority
								}
							}
						}
						if tc.isExpectedToSwitch {
							expectNewSelectedCandidate = cand
						} else {
							expectNewSelectedCandidate = aAgent.getSelectedPair().Remote
						}
					} else {
						// a smaller change for other candidates other the new expected one
					change_priority:
						for _, candidates := range aAgent.remoteCandidates {
							for _, candidate := range candidates {
								if candidate.Equal(cand) {
									if tc.useHigherPriority {
										candidate.(*CandidateHost).priorityOverride += 500 //nolint:forcetypeassert
									} else {
										candidate.(*CandidateHost).priorityOverride -= 500 //nolint:forcetypeassert
									}

									break change_priority
								}
							}
						}
					}
					_, err = cand.writeTo(
						buildMsg(
							stun.ClassRequest,
							aAgent.localUfrag+":"+aAgent.remoteUfrag,
							aAgent.localPwd,
							cand.Priority(),
						).Raw,
						bAgent.getSelectedPair().Remote,
					)
					require.NoError(t, err)
				}
			}

			time.Sleep(1 * time.Second)
			select {
			case selected := <-selectedCh:
				require.True(t, selected.Equal(expectNewSelectedCandidate))
			default:
				require.False(t, tc.isExpectedToSwitch)
				require.True(t, aAgent.getSelectedPair().Remote.Equal(expectNewSelectedCandidate))
			}
		})
	}

	require.NoError(t, wan.Stop())
}

// Close can deadlock but GracefulClose must not.
func TestAgentGracefulCloseDeadlock(t *testing.T) {
	defer test.CheckRoutinesStrict(t)()
	defer test.TimeOut(time.Second * 5).Stop()

	config := &AgentConfig{
		NetworkTypes: supportedNetworkTypes(),
	}
	aAgent, err := NewAgent(config)
	require.NoError(t, err)
	var aAgentClosed bool
	defer func() {
		if aAgentClosed {
			return
		}
		require.NoError(t, aAgent.Close())
	}()

	bAgent, err := NewAgent(config)
	require.NoError(t, err)
	var bAgentClosed bool
	defer func() {
		if bAgentClosed {
			return
		}
		require.NoError(t, bAgent.Close())
	}()

	var connected, closeNow, closed sync.WaitGroup
	connected.Add(2)
	closeNow.Add(1)
	closed.Add(2)
	closeHdlr := func(agent *Agent, agentClosed *bool) {
		require.NoError(t, agent.OnConnectionStateChange(func(cs ConnectionState) {
			if cs == ConnectionStateConnected {
				connected.Done()
				closeNow.Wait()

				go func() {
					require.NoError(t, agent.GracefulClose())
					*agentClosed = true
					closed.Done()
				}()
			}
		}))
	}

	closeHdlr(aAgent, &aAgentClosed)
	closeHdlr(bAgent, &bAgentClosed)

	t.Log("connecting agents")
	_, _ = connect(t, aAgent, bAgent)

	t.Log("waiting for them to confirm connection in callback")
	connected.Wait()

	t.Log("tell them to close themselves in the same callback and wait")
	closeNow.Done()
	closed.Wait()
}

func TestSetCandidatesUfrag(t *testing.T) {
	var config AgentConfig

	agent, err := NewAgent(&config)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	dummyConn := &net.UDPConn{}

	for i := 0; i < 5; i++ {
		cfg := CandidateHostConfig{
			Network:   "udp",
			Address:   "192.168.0.2",
			Port:      1000 + i,
			Component: 1,
		}

		cand, errCand := NewCandidateHost(&cfg)
		require.NoError(t, errCand)

		err = agent.addCandidate(context.Background(), cand, dummyConn)
		require.NoError(t, err)
	}

	actualCandidates, err := agent.GetLocalCandidates()
	require.NoError(t, err)

	for _, candidate := range actualCandidates {
		ext, ok := candidate.GetExtension("ufrag")

		require.True(t, ok)
		require.Equal(t, agent.localUfrag, ext.Value)
	}
}

func TestAlwaysSentKeepAlive(t *testing.T) { //nolint:cyclop
	defer test.CheckRoutines(t)()

	// Avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	log := logging.NewDefaultLoggerFactory().NewLogger("agent")
	agent.selector = &controllingSelector{agent: agent, log: log}
	pair := makeCandidatePair(t)
	s, ok := pair.Local.(*CandidateHost)
	require.True(t, ok)
	s.conn = &fakenet.MockPacketConn{}
	agent.setSelectedPair(pair)

	pair.Remote.seen(false)

	lastSent := pair.Local.LastSent()
	agent.checkKeepalive()
	newLastSent := pair.Local.LastSent()
	require.NotEqual(t, lastSent, newLastSent)
	lastSent = newLastSent

	// sleep, so there is difference in sent time of local candidate
	time.Sleep(10 * time.Millisecond)
	agent.checkKeepalive()
	newLastSent = pair.Local.LastSent()
	require.NotEqual(t, lastSent, newLastSent)
}

func TestRoleConflict(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	runTest := func(doDial bool) {
		cfg := &AgentConfig{
			NetworkTypes:     supportedNetworkTypes(),
			MulticastDNSMode: MulticastDNSModeDisabled,
			InterfaceFilter:  problematicNetworkInterfaces,
		}

		aAgent, err := NewAgent(cfg)
		require.NoError(t, err)

		bAgent, err := NewAgent(cfg)
		require.NoError(t, err)

		isConnected := make(chan any)
		err = aAgent.OnConnectionStateChange(func(c ConnectionState) {
			if c == ConnectionStateConnected {
				close(isConnected)
			}
		})
		require.NoError(t, err)

		gatherAndExchangeCandidates(t, aAgent, bAgent)

		go func() {
			ufrag, pwd, routineErr := bAgent.GetLocalUserCredentials()
			require.NoError(t, routineErr)

			if doDial {
				_, routineErr = aAgent.Dial(context.TODO(), ufrag, pwd)
			} else {
				_, routineErr = aAgent.Accept(context.TODO(), ufrag, pwd)
			}
			require.NoError(t, routineErr)
		}()

		ufrag, pwd, err := aAgent.GetLocalUserCredentials()
		require.NoError(t, err)

		if doDial {
			_, err = bAgent.Dial(context.TODO(), ufrag, pwd)
		} else {
			_, err = bAgent.Accept(context.TODO(), ufrag, pwd)
		}
		require.NoError(t, err)

		<-isConnected

		require.NoError(t, aAgent.Close())
		require.NoError(t, bAgent.Close())
	}

	t.Run("Controlling", func(t *testing.T) {
		runTest(true)
	})

	t.Run("Controlled", func(t *testing.T) {
		runTest(false)
	})
}
