// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"context"
	"errors"
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
			if err != nil {
				t.Fatalf("failed to create a new candidate: %v", err)
			}

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
			if len(agent.remoteCandidates) != 1 {
				t.Fatal("failed to add a network type to the remote candidate list")
			}

			// Length of remote candidate list for a network type must be 1
			set := agent.remoteCandidates[local.NetworkType()]
			if len(set) != 1 {
				t.Fatal("failed to add prflx candidate to remote candidate list")
			}

			c := set[0]

			if c.Type() != CandidateTypePeerReflexive {
				t.Fatal("candidate type must be prflx")
			}

			if c.Address() != "172.17.0.3" {
				t.Fatal("IP address mismatch")
			}

			if c.Port() != 999 {
				t.Fatal("Port number mismatch")
			}
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
			if err != nil {
				t.Fatalf("failed to create a new candidate: %v", err)
			}

			remote := &BadAddr{}

			// nolint: contextcheck
			agent.handleInbound(nil, local, remote)

			if len(agent.remoteCandidates) != 0 {
				t.Fatal("bad address should not be added to the remote candidate list")
			}
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
			if err != nil {
				t.Fatalf("failed to create a new candidate: %v", err)
			}

			remote := &net.UDPAddr{IP: net.ParseIP("172.17.0.3"), Port: 999}
			msg, err := stun.Build(stun.BindingSuccess, stun.NewTransactionIDSetter(tID),
				stun.NewShortTermIntegrity(agent.remotePwd),
				stun.Fingerprint,
			)
			require.NoError(t, err)

			// nolint: contextcheck
			agent.handleInbound(msg, local, remote)
			if len(agent.remoteCandidates) != 0 {
				t.Fatal("unknown remote was able to create a candidate")
			}
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

		gatherAndExchangeCandidates(aAgent, bAgent)

		accepted := make(chan struct{})
		accepting := make(chan struct{})
		var aConn *Conn

		origHdlr := aAgent.onConnectionStateChangeHdlr.Load()
		if origHdlr != nil {
			defer check(aAgent.OnConnectionStateChange(origHdlr.(func(ConnectionState)))) //nolint:forcetypeassert
		}
		check(aAgent.OnConnectionStateChange(func(s ConnectionState) {
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
			check(acceptErr)
			close(accepted)
		}()

		<-accepting

		bConn, err := bAgent.Dial(context.TODO(), aUfrag, aPwd)
		check(err)

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

	connectWithVNet(aAgent, bAgent)

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
	if err != nil {
		t.Fatalf("failed to create a new candidate: %v", err)
	}

	t.Run("Invalid Binding requests should be discarded", func(t *testing.T) {
		agent, err := NewAgent(&AgentConfig{})
		if err != nil {
			t.Fatalf("Error constructing ice.Agent")
		}
		defer func() {
			require.NoError(t, agent.Close())
		}()

		agent.handleInbound(buildMsg(stun.ClassRequest, "invalid", agent.localPwd), local, remote)
		if len(agent.remoteCandidates) == 1 {
			t.Fatal("Binding with invalid Username was able to create prflx candidate")
		}

		agent.handleInbound(buildMsg(stun.ClassRequest, agent.localUfrag+":"+agent.remoteUfrag, "Invalid"), local, remote)
		if len(agent.remoteCandidates) == 1 {
			t.Fatal("Binding with invalid MessageIntegrity was able to create prflx candidate")
		}
	})

	t.Run("Invalid Binding success responses should be discarded", func(t *testing.T) {
		a, err := NewAgent(&AgentConfig{})
		if err != nil {
			t.Fatalf("Error constructing ice.Agent")
		}
		defer func() {
			require.NoError(t, a.Close())
		}()

		a.handleInbound(buildMsg(stun.ClassSuccessResponse, a.localUfrag+":"+a.remoteUfrag, "Invalid"), local, remote)
		if len(a.remoteCandidates) == 1 {
			t.Fatal("Binding with invalid MessageIntegrity was able to create prflx candidate")
		}
	})

	t.Run("Discard non-binding messages", func(t *testing.T) {
		a, err := NewAgent(&AgentConfig{})
		if err != nil {
			t.Fatalf("Error constructing ice.Agent")
		}
		defer func() {
			require.NoError(t, a.Close())
		}()

		a.handleInbound(buildMsg(stun.ClassErrorResponse, a.localUfrag+":"+a.remoteUfrag, "Invalid"), local, remote)
		if len(a.remoteCandidates) == 1 {
			t.Fatal("non-binding message was able to create prflxRemote")
		}
	})

	t.Run("Valid bind request", func(t *testing.T) {
		a, err := NewAgent(&AgentConfig{})
		if err != nil {
			t.Fatalf("Error constructing ice.Agent")
		}
		defer func() {
			require.NoError(t, a.Close())
		}()

		err = a.loop.Run(a.loop, func(_ context.Context) {
			a.selector = &controllingSelector{agent: a, log: a.log}
			// nolint: contextcheck
			a.handleInbound(buildMsg(stun.ClassRequest, a.localUfrag+":"+a.remoteUfrag, a.localPwd), local, remote)
			if len(a.remoteCandidates) != 1 {
				t.Fatal("Binding with valid values was unable to create prflx candidate")
			}
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
			if len(agent.remoteCandidates) != 1 {
				t.Fatal("Binding with valid values (but no fingerprint) was unable to create prflx candidate")
			}
		}))
	})

	t.Run("Success with invalid TransactionID", func(t *testing.T) {
		agent, err := NewAgent(&AgentConfig{})
		if err != nil {
			t.Fatalf("Error constructing ice.Agent")
		}
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
		if err != nil {
			t.Fatalf("failed to create a new candidate: %v", err)
		}

		remote := &net.UDPAddr{IP: net.ParseIP("172.17.0.3"), Port: 999}
		tID := [stun.TransactionIDSize]byte{}
		copy(tID[:], "ABC")
		msg, err := stun.Build(stun.BindingSuccess, stun.NewTransactionIDSetter(tID),
			stun.NewShortTermIntegrity(agent.remotePwd),
			stun.Fingerprint,
		)
		require.NoError(t, err)

		agent.handleInbound(msg, local, remote)
		if len(agent.remoteCandidates) != 0 {
			t.Fatal("unknown remote was able to create a candidate")
		}
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

	if _, err = agent.Dial(ctx, "", "bar"); err != nil && !errors.Is(err, ErrRemoteUfragEmpty) {
		t.Fatal(err)
	}

	if _, err = agent.Dial(ctx, "foo", ""); err != nil && !errors.Is(err, ErrRemotePwdEmpty) {
		t.Fatal(err)
	}

	if _, err = agent.Dial(ctx, "foo", "bar"); err != nil && !errors.Is(err, ErrCanceledByCaller) {
		t.Fatal(err)
	}

	if _, err = agent.Dial(context.TODO(), "foo", "bar"); err != nil && !errors.Is(err, ErrMultipleStart) {
		t.Fatal(err)
	}
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

	isClosed := make(chan interface{})

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

	isChecking := make(chan interface{})
	isConnected := make(chan interface{})
	isDisconnected := make(chan interface{})
	isFailed := make(chan interface{})
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

	connect(aAgent, bAgent)

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
		if err != nil {
			t.Fatalf("Error constructing ice.Agent")
		}
		defer func() {
			require.NoError(t, a.Close())
		}()

		err = a.GatherCandidates()
		if !errors.Is(err, ErrNoOnCandidateHandler) {
			t.Fatal("trickle GatherCandidates succeeded without OnCandidate")
		}
	})
}

func TestCandidatePairsStats(t *testing.T) { //nolint:cyclop
	defer test.CheckRoutines(t)()

	// Avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	agent, err := NewAgent(&AgentConfig{})
	if err != nil {
		t.Fatalf("Failed to create agent: %s", err)
	}
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
	if err != nil {
		t.Fatalf("Failed to construct local host candidate: %s", err)
	}

	relayConfig := &CandidateRelayConfig{
		Network:   "udp",
		Address:   "1.2.3.4",
		Port:      2340,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43210,
	}
	relayRemote, err := NewCandidateRelay(relayConfig)
	if err != nil {
		t.Fatalf("Failed to construct remote relay candidate: %s", err)
	}

	srflxConfig := &CandidateServerReflexiveConfig{
		Network:   "udp",
		Address:   "10.10.10.2",
		Port:      19218,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43212,
	}
	srflxRemote, err := NewCandidateServerReflexive(srflxConfig)
	if err != nil {
		t.Fatalf("Failed to construct remote srflx candidate: %s", err)
	}

	prflxConfig := &CandidatePeerReflexiveConfig{
		Network:   "udp",
		Address:   "10.10.10.2",
		Port:      19217,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43211,
	}
	prflxRemote, err := NewCandidatePeerReflexive(prflxConfig)
	if err != nil {
		t.Fatalf("Failed to construct remote prflx candidate: %s", err)
	}

	hostConfig = &CandidateHostConfig{
		Network:   "udp",
		Address:   "1.2.3.5",
		Port:      12350,
		Component: 1,
	}
	hostRemote, err := NewCandidateHost(hostConfig)
	if err != nil {
		t.Fatalf("Failed to construct remote host candidate: %s", err)
	}

	for _, remote := range []Candidate{relayRemote, srflxRemote, prflxRemote, hostRemote} {
		p := agent.findPair(hostLocal, remote)

		if p == nil {
			agent.addPair(hostLocal, remote)
		}
	}

	p := agent.findPair(hostLocal, prflxRemote)
	p.state = CandidatePairStateFailed

	for i := 0; i < 10; i++ {
		p.UpdateRoundTripTime(time.Duration(i+1) * time.Second)
	}

	stats := agent.GetCandidatePairsStats()
	if len(stats) != 4 {
		t.Fatal("expected 4 candidate pairs stats")
	}

	var relayPairStat, srflxPairStat, prflxPairStat, hostPairStat CandidatePairStats

	for _, cps := range stats {
		if cps.LocalCandidateID != hostLocal.ID() {
			t.Fatal("invalid local candidate id")
		}
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
			t.Fatal("invalid remote candidate ID")
		}
	}

	if relayPairStat.RemoteCandidateID != relayRemote.ID() {
		t.Fatal("missing host-relay pair stat")
	}

	if srflxPairStat.RemoteCandidateID != srflxRemote.ID() {
		t.Fatal("missing host-srflx pair stat")
	}

	if prflxPairStat.RemoteCandidateID != prflxRemote.ID() {
		t.Fatal("missing host-prflx pair stat")
	}

	if hostPairStat.RemoteCandidateID != hostRemote.ID() {
		t.Fatal("missing host-host pair stat")
	}

	if prflxPairStat.State != CandidatePairStateFailed {
		t.Fatalf("expected host-prflx pair to have state failed, it has state %s instead",
			prflxPairStat.State.String())
	}

	expectedCurrentRoundTripTime := time.Duration(10) * time.Second
	if prflxPairStat.CurrentRoundTripTime != expectedCurrentRoundTripTime.Seconds() {
		t.Fatalf("expected current round trip time to be %f, it is %f instead",
			expectedCurrentRoundTripTime.Seconds(), prflxPairStat.CurrentRoundTripTime)
	}

	expectedTotalRoundTripTime := time.Duration(55) * time.Second
	if prflxPairStat.TotalRoundTripTime != expectedTotalRoundTripTime.Seconds() {
		t.Fatalf("expected total round trip time to be %f, it is %f instead",
			expectedTotalRoundTripTime.Seconds(), prflxPairStat.TotalRoundTripTime)
	}

	if prflxPairStat.ResponsesReceived != 10 {
		t.Fatalf("expected responses received to be 10, it is %d instead",
			prflxPairStat.ResponsesReceived)
	}
}

func TestSelectedCandidatePairStats(t *testing.T) { //nolint:cyclop
	defer test.CheckRoutines(t)()

	// Avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	agent, err := NewAgent(&AgentConfig{})
	if err != nil {
		t.Fatalf("Failed to create agent: %s", err)
	}
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
	if err != nil {
		t.Fatalf("Failed to construct local host candidate: %s", err)
	}

	srflxConfig := &CandidateServerReflexiveConfig{
		Network:   "udp",
		Address:   "10.10.10.2",
		Port:      19218,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43212,
	}
	srflxRemote, err := NewCandidateServerReflexive(srflxConfig)
	if err != nil {
		t.Fatalf("Failed to construct remote srflx candidate: %s", err)
	}

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

	if stats.LocalCandidateID != hostLocal.ID() {
		t.Fatal("invalid local candidate id")
	}
	if stats.RemoteCandidateID != srflxRemote.ID() {
		t.Fatal("invalid remote candidate id")
	}

	expectedCurrentRoundTripTime := time.Duration(10) * time.Second
	if stats.CurrentRoundTripTime != expectedCurrentRoundTripTime.Seconds() {
		t.Fatalf("expected current round trip time to be %f, it is %f instead",
			expectedCurrentRoundTripTime.Seconds(), stats.CurrentRoundTripTime)
	}

	expectedTotalRoundTripTime := time.Duration(55) * time.Second
	if stats.TotalRoundTripTime != expectedTotalRoundTripTime.Seconds() {
		t.Fatalf("expected total round trip time to be %f, it is %f instead",
			expectedTotalRoundTripTime.Seconds(), stats.TotalRoundTripTime)
	}

	if stats.ResponsesReceived != 10 {
		t.Fatalf("expected responses received to be 10, it is %d instead",
			stats.ResponsesReceived)
	}
}

func TestLocalCandidateStats(t *testing.T) { //nolint:cyclop
	defer test.CheckRoutines(t)()

	// Avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	agent, err := NewAgent(&AgentConfig{})
	if err != nil {
		t.Fatalf("Failed to create agent: %s", err)
	}
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
	if err != nil {
		t.Fatalf("Failed to construct local host candidate: %s", err)
	}

	srflxConfig := &CandidateServerReflexiveConfig{
		Network:   "udp",
		Address:   "192.168.1.1",
		Port:      19217,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43212,
	}
	srflxLocal, err := NewCandidateServerReflexive(srflxConfig)
	if err != nil {
		t.Fatalf("Failed to construct local srflx candidate: %s", err)
	}

	agent.localCandidates[NetworkTypeUDP4] = []Candidate{hostLocal, srflxLocal}

	localStats := agent.GetLocalCandidatesStats()
	if len(localStats) != 2 {
		t.Fatalf("expected 2 local candidates stats, got %d instead", len(localStats))
	}

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
			t.Fatal("invalid local candidate ID")
		}

		if stats.CandidateType != candidate.Type() {
			t.Fatal("invalid stats CandidateType")
		}

		if stats.Priority != candidate.Priority() {
			t.Fatal("invalid stats CandidateType")
		}

		if stats.IP != candidate.Address() {
			t.Fatal("invalid stats IP")
		}
	}

	if hostLocalStat.ID != hostLocal.ID() {
		t.Fatal("missing host local stat")
	}

	if srflxLocalStat.ID != srflxLocal.ID() {
		t.Fatal("missing srflx local stat")
	}
}

func TestRemoteCandidateStats(t *testing.T) { //nolint:cyclop
	defer test.CheckRoutines(t)()

	// Avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	agent, err := NewAgent(&AgentConfig{})
	if err != nil {
		t.Fatalf("Failed to create agent: %s", err)
	}
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
	if err != nil {
		t.Fatalf("Failed to construct remote relay candidate: %s", err)
	}

	srflxConfig := &CandidateServerReflexiveConfig{
		Network:   "udp",
		Address:   "10.10.10.2",
		Port:      19218,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43212,
	}
	srflxRemote, err := NewCandidateServerReflexive(srflxConfig)
	if err != nil {
		t.Fatalf("Failed to construct remote srflx candidate: %s", err)
	}

	prflxConfig := &CandidatePeerReflexiveConfig{
		Network:   "udp",
		Address:   "10.10.10.2",
		Port:      19217,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43211,
	}
	prflxRemote, err := NewCandidatePeerReflexive(prflxConfig)
	if err != nil {
		t.Fatalf("Failed to construct remote prflx candidate: %s", err)
	}

	hostConfig := &CandidateHostConfig{
		Network:   "udp",
		Address:   "1.2.3.5",
		Port:      12350,
		Component: 1,
	}
	hostRemote, err := NewCandidateHost(hostConfig)
	if err != nil {
		t.Fatalf("Failed to construct remote host candidate: %s", err)
	}

	agent.remoteCandidates[NetworkTypeUDP4] = []Candidate{relayRemote, srflxRemote, prflxRemote, hostRemote}

	remoteStats := agent.GetRemoteCandidatesStats()
	if len(remoteStats) != 4 {
		t.Fatalf("expected 4 remote candidates stats, got %d instead", len(remoteStats))
	}
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
			t.Fatal("invalid remote candidate ID")
		}

		if stats.CandidateType != candidate.Type() {
			t.Fatal("invalid stats CandidateType")
		}

		if stats.Priority != candidate.Priority() {
			t.Fatal("invalid stats CandidateType")
		}

		if stats.IP != candidate.Address() {
			t.Fatal("invalid stats IP")
		}
	}

	if relayRemoteStat.ID != relayRemote.ID() {
		t.Fatal("missing relay remote stat")
	}

	if srflxRemoteStat.ID != srflxRemote.ID() {
		t.Fatal("missing srflx remote stat")
	}

	if prflxRemoteStat.ID != prflxRemote.ID() {
		t.Fatal("missing prflx remote stat")
	}

	if hostRemoteStat.ID != hostRemote.ID() {
		t.Fatal("missing host remote stat")
	}
}

func TestInitExtIPMapping(t *testing.T) {
	defer test.CheckRoutines(t)()

	// agent.extIPMapper should be nil by default
	agent, err := NewAgent(&AgentConfig{})
	if err != nil {
		t.Fatalf("Failed to create agent: %v", err)
	}
	if agent.extIPMapper != nil {
		require.NoError(t, agent.Close())
		t.Fatal("a.extIPMapper should be nil by default")
	}
	require.NoError(t, agent.Close())

	// a.extIPMapper should be nil when NAT1To1IPs is a non-nil empty array
	agent, err = NewAgent(&AgentConfig{
		NAT1To1IPs:             []string{},
		NAT1To1IPCandidateType: CandidateTypeHost,
	})
	if err != nil {
		require.NoError(t, agent.Close())
		t.Fatalf("Failed to create agent: %v", err)
	}
	if agent.extIPMapper != nil {
		require.NoError(t, agent.Close())
		t.Fatal("a.extIPMapper should be nil by default")
	}
	require.NoError(t, agent.Close())

	// NewAgent should return an error when 1:1 NAT for host candidate is enabled
	// but the candidate type does not appear in the CandidateTypes.
	_, err = NewAgent(&AgentConfig{
		NAT1To1IPs:             []string{"1.2.3.4"},
		NAT1To1IPCandidateType: CandidateTypeHost,
		CandidateTypes:         []CandidateType{CandidateTypeRelay},
	})
	if !errors.Is(err, ErrIneffectiveNAT1To1IPMappingHost) {
		t.Fatalf("Unexpected error: %v", err)
	}

	// NewAgent should return an error when 1:1 NAT for srflx candidate is enabled
	// but the candidate type does not appear in the CandidateTypes.
	_, err = NewAgent(&AgentConfig{
		NAT1To1IPs:             []string{"1.2.3.4"},
		NAT1To1IPCandidateType: CandidateTypeServerReflexive,
		CandidateTypes:         []CandidateType{CandidateTypeRelay},
	})
	if !errors.Is(err, ErrIneffectiveNAT1To1IPMappingSrflx) {
		t.Fatalf("Unexpected error: %v", err)
	}

	// NewAgent should return an error when 1:1 NAT for host candidate is enabled
	// along with mDNS with MulticastDNSModeQueryAndGather
	_, err = NewAgent(&AgentConfig{
		NAT1To1IPs:             []string{"1.2.3.4"},
		NAT1To1IPCandidateType: CandidateTypeHost,
		MulticastDNSMode:       MulticastDNSModeQueryAndGather,
	})
	if !errors.Is(err, ErrMulticastDNSWithNAT1To1IPMapping) {
		t.Fatalf("Unexpected error: %v", err)
	}

	// NewAgent should return if newExternalIPMapper() returns an error.
	_, err = NewAgent(&AgentConfig{
		NAT1To1IPs:             []string{"bad.2.3.4"}, // Bad IP
		NAT1To1IPCandidateType: CandidateTypeHost,
	})
	if !errors.Is(err, ErrInvalidNAT1To1IPMapping) {
		t.Fatalf("Unexpected error: %v", err)
	}
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

	isFailed := make(chan interface{})
	require.NoError(t, aAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateFailed {
			close(isFailed)
		}
	}))

	connect(aAgent, bAgent)
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
			t.Errorf("Unexpected ConnectionState: %v", c)
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
		connA, connB := pipe(&AgentConfig{
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
		connA, connB := pipe(&AgentConfig{
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
		connA, connB := pipe(&AgentConfig{
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

		gatherAndExchangeCandidates(connA.agent, connB.agent)

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
	if err != nil {
		t.Fatalf("Error constructing ice.Agent: %v", err)
	}
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
	if err != nil {
		t.Fatalf("Error constructing ice.Agent: %v", err)
	}
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

func TestGetLocalCandidates(t *testing.T) {
	var config AgentConfig

	agent, err := NewAgent(&config)
	if err != nil {
		t.Fatalf("Error constructing ice.Agent: %v", err)
	}
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

	isClosed := make(chan interface{})
	isConnected := make(chan interface{})
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

	connect(aAgent, bAgent)
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
	check(err)
	defer func() {
		require.NoError(t, aAgent.Close())
	}()
	bAgent, err := NewAgent(cfg)
	check(err)
	defer func() {
		require.NoError(t, bAgent.Close())
	}()

	isComplete := make(chan interface{})
	err = aAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateConnected {
			_, _, errCred := aAgent.GetLocalUserCredentials()
			require.NoError(t, errCred)
			require.NoError(t, aAgent.Restart("", ""))
			close(isComplete)
		}
	})
	require.NoError(t, err)

	connect(aAgent, bAgent)

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
	check(err)
	defer func() {
		require.NoError(t, aAgent.Close())
	}()
	bAgent, err := NewAgent(cfg)
	check(err)
	defer func() {
		require.NoError(t, bAgent.Close())
	}()

	isComplete := make(chan interface{})
	isTested := make(chan interface{})
	if err = aAgent.OnSelectedCandidatePairChange(func(Candidate, Candidate) {
		go func() {
			_, _, errCred := aAgent.GetLocalUserCredentials()
			require.NoError(t, errCred)
			close(isTested)
		}()
	}); err != nil {
		t.Error(err)
	}
	if err = aAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateConnected {
			close(isComplete)
		}
	}); err != nil {
		t.Error(err)
	}

	connect(aAgent, bAgent)

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

	bConnected := make(chan interface{})
	bDisconnected := make(chan interface{})
	bFailed := make(chan interface{})

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

	connectWithVNet(bAgent, aAgent)

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

	connect(aAgent, bAgent)

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

			connect(aAgent, bAgent)

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
				if !tc.isExpectedToSwitch {
					require.True(t, aAgent.getSelectedPair().Remote.Equal(expectNewSelectedCandidate))
				} else {
					t.Fatal("No selected candidate pair")
				}
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
		check(agent.OnConnectionStateChange(func(cs ConnectionState) {
			if cs == ConnectionStateConnected {
				connected.Done()
				closeNow.Wait()

				go func() {
					if err := agent.GracefulClose(); err != nil {
						require.NoError(t, err)
					}
					*agentClosed = true
					closed.Done()
				}()
			}
		}))
	}

	closeHdlr(aAgent, &aAgentClosed)
	closeHdlr(bAgent, &bAgentClosed)

	t.Log("connecting agents")
	_, _ = connect(aAgent, bAgent)

	t.Log("waiting for them to confirm connection in callback")
	connected.Wait()

	t.Log("tell them to close themselves in the same callback and wait")
	closeNow.Done()
	closed.Wait()
}

func TestSetCandidatesUfrag(t *testing.T) {
	var config AgentConfig

	agent, err := NewAgent(&config)
	if err != nil {
		t.Fatalf("Error constructing ice.Agent: %v", err)
	}
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
