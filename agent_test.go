// +build !js

package ice

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun"
	"github.com/pion/transport/test"
	"github.com/pion/transport/vnet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockPacketConn struct {
}

func (m *mockPacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) { return 0, nil, nil }
func (m *mockPacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error)  { return 0, nil }
func (m *mockPacketConn) Close() error                                        { return nil }
func (m *mockPacketConn) LocalAddr() net.Addr                                 { return nil }
func (m *mockPacketConn) SetDeadline(t time.Time) error                       { return nil }
func (m *mockPacketConn) SetReadDeadline(t time.Time) error                   { return nil }
func (m *mockPacketConn) SetWriteDeadline(t time.Time) error                  { return nil }

func TestPairSearch(t *testing.T) {
	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 10)
	defer lim.Stop()

	var config AgentConfig
	a, err := NewAgent(&config)

	if err != nil {
		t.Fatalf("Error constructing ice.Agent")
	}

	if len(a.checklist) != 0 {
		t.Fatalf("TestPairSearch is only a valid test if a.validPairs is empty on construction")
	}

	cp := a.getBestAvailableCandidatePair()

	if cp != nil {
		t.Fatalf("No Candidate pairs should exist")
	}

	assert.NoError(t, a.Close())
}

func TestPairPriority(t *testing.T) {
	// avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	a, err := NewAgent(&AgentConfig{})
	if err != nil {
		t.Fatalf("Failed to create agent: %s", err)
	}

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
		p := a.findPair(hostLocal, remote)

		if p == nil {
			p = a.addPair(hostLocal, remote)
		}

		p.state = CandidatePairStateSucceeded
		bestPair := a.getBestValidCandidatePair()
		if bestPair.String() != (&candidatePair{remote: remote, local: hostLocal}).String() {
			t.Fatalf("Unexpected bestPair %s (expected remote: %s)", bestPair, remote)
		}
	}

	assert.NoError(t, a.Close())
}

func TestOnSelectedCandidatePairChange(t *testing.T) {
	// avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	a, err := NewAgent(&AgentConfig{})
	if err != nil {
		t.Fatalf("Failed to create agent: %s", err)
	}
	callbackCalled := make(chan struct{}, 1)
	if err = a.OnSelectedCandidatePairChange(func(local, remote Candidate) {
		close(callbackCalled)
	}); err != nil {
		t.Fatalf("Failed to set agent OnCandidatePairChange callback: %s", err)
	}

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
		Port:      12340,
		Component: 1,
		RelAddr:   "4.3.2.1",
		RelPort:   43210,
	}
	relayRemote, err := NewCandidateRelay(relayConfig)
	if err != nil {
		t.Fatalf("Failed to construct remote relay candidate: %s", err)
	}

	// select the pair
	if err = a.run(func(agent *Agent) {
		p := newCandidatePair(hostLocal, relayRemote, false)
		agent.setSelectedPair(p)
	}); err != nil {
		t.Fatalf("Failed to setValidPair(): %s", err)
	}

	// ensure that the callback fired on setting the pair
	<-callbackCalled
	assert.NoError(t, a.Close())
}

type BadAddr struct{}

func (ba *BadAddr) Network() string {
	return "xxx"
}
func (ba *BadAddr) String() string {
	return "yyy"
}

func runAgentTest(t *testing.T, config *AgentConfig, task func(a *Agent)) {
	a, err := NewAgent(config)

	if err != nil {
		t.Fatalf("Error constructing ice.Agent")
	}

	if err := a.run(task); err != nil {
		t.Fatalf("Agent run failure: %v", err)
	}

	assert.NoError(t, a.Close())
}

func TestHandlePeerReflexive(t *testing.T) {
	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 2)
	defer lim.Stop()

	report := test.CheckRoutines(t)
	defer report()

	t.Run("UDP pflx candidate from handleInbound()", func(t *testing.T) {
		var config AgentConfig
		runAgentTest(t, &config, func(a *Agent) {
			a.selector = &controllingSelector{agent: a, log: a.log}
			a.connectivityTicker = time.NewTicker(a.taskLoopInterval)

			hostConfig := CandidateHostConfig{
				Network:   "udp",
				Address:   "192.168.0.2",
				Port:      777,
				Component: 1,
			}
			local, err := NewCandidateHost(&hostConfig)
			local.conn = &mockPacketConn{}
			if err != nil {
				t.Fatalf("failed to create a new candidate: %v", err)
			}

			remote := &net.UDPAddr{IP: net.ParseIP("172.17.0.3"), Port: 999}

			msg, err := stun.Build(stun.BindingRequest, stun.TransactionID,
				stun.NewUsername(a.localUfrag+":"+a.remoteUfrag),
				UseCandidate,
				AttrControlling(a.tieBreaker),
				PriorityAttr(local.Priority()),
				stun.NewShortTermIntegrity(a.localPwd),
				stun.Fingerprint,
			)
			if err != nil {
				t.Fatal(err)
			}

			a.handleInbound(msg, local, remote)

			// length of remote candidate list must be one now
			if len(a.remoteCandidates) != 1 {
				t.Fatal("failed to add a network type to the remote candidate list")
			}

			// length of remote candidate list for a network type must be 1
			set := a.remoteCandidates[local.NetworkType()]
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
		})
	})

	t.Run("Bad network type with handleInbound()", func(t *testing.T) {
		var config AgentConfig
		runAgentTest(t, &config, func(a *Agent) {
			a.selector = &controllingSelector{agent: a, log: a.log}
			a.connectivityTicker = time.NewTicker(a.taskLoopInterval)

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

			a.handleInbound(nil, local, remote)

			if len(a.remoteCandidates) != 0 {
				t.Fatal("bad address should not be added to the remote candidate list")
			}
		})
	})

	t.Run("Success from unknown remote, prflx candidate MUST only be created via Binding Request", func(t *testing.T) {
		var config AgentConfig
		runAgentTest(t, &config, func(a *Agent) {
			a.selector = &controllingSelector{agent: a, log: a.log}
			a.connectivityTicker = time.NewTicker(a.taskLoopInterval)
			tID := [stun.TransactionIDSize]byte{}
			copy(tID[:], []byte("ABC"))
			a.pendingBindingRequests = []bindingRequest{
				{time.Now(), tID, &net.UDPAddr{}, false},
			}

			hostConfig := CandidateHostConfig{
				Network:   "udp",
				Address:   "192.168.0.2",
				Port:      777,
				Component: 1,
			}
			local, err := NewCandidateHost(&hostConfig)
			local.conn = &mockPacketConn{}
			if err != nil {
				t.Fatalf("failed to create a new candidate: %v", err)
			}

			remote := &net.UDPAddr{IP: net.ParseIP("172.17.0.3"), Port: 999}
			msg, err := stun.Build(stun.BindingSuccess, stun.NewTransactionIDSetter(tID),
				stun.NewShortTermIntegrity(a.remotePwd),
				stun.Fingerprint,
			)
			if err != nil {
				t.Fatal(err)
			}

			a.handleInbound(msg, local, remote)
			if len(a.remoteCandidates) != 0 {
				t.Fatal("unknown remote was able to create a candidate")
			}
		})
	})
}

// Assert that Agent on startup sends message, and doesn't wait for connectivityTicker to fire
// github.com/pion/ice/issues/15
func TestConnectivityOnStartup(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	stunServerURL := &URL{
		Scheme: SchemeTypeSTUN,
		Host:   "1.2.3.4",
		Port:   3478,
		Proto:  ProtoTypeUDP,
	}

	natType := &vnet.NATType{
		MappingBehavior:   vnet.EndpointIndependent,
		FilteringBehavior: vnet.EndpointIndependent,
	}
	v, err := buildVNet(natType, natType)
	require.NoError(t, err, "should succeed")
	defer v.close()

	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	var wg sync.WaitGroup
	wg.Add(2)

	cfg0 := &AgentConfig{
		Urls:             []*URL{stunServerURL},
		Trickle:          true,
		NetworkTypes:     supportedNetworkTypes,
		MulticastDNSMode: MulticastDNSModeDisabled,
		Net:              v.net0,

		taskLoopInterval: time.Hour,
	}

	aAgent, err := NewAgent(cfg0)
	require.NoError(t, err)
	require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))
	require.NoError(t, aAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	}))
	require.NoError(t, aAgent.GatherCandidates())

	cfg1 := &AgentConfig{
		Urls:             []*URL{stunServerURL},
		Trickle:          true,
		NetworkTypes:     supportedNetworkTypes,
		MulticastDNSMode: MulticastDNSModeDisabled,
		Net:              v.net1,
		taskLoopInterval: time.Hour,
	}

	bAgent, err := NewAgent(cfg1)
	require.NoError(t, err)
	require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))
	require.NoError(t, bAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	}))
	require.NoError(t, bAgent.GatherCandidates())

	wg.Wait()
	aConn, bConn := connectWithVNet(aAgent, bAgent)

	// Ensure pair selected
	// Note: this assumes ConnectionStateConnected is thrown after selecting the final pair
	<-aConnected
	<-bConnected

	if !closePipe(t, aConn, bConn) {
		return
	}
}

func TestConnectivityLite(t *testing.T) {
	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	report := test.CheckRoutines(t)
	defer report()

	stunServerURL := &URL{
		Scheme: SchemeTypeSTUN,
		Host:   "1.2.3.4",
		Port:   3478,
		Proto:  ProtoTypeUDP,
	}

	natType := &vnet.NATType{
		MappingBehavior:   vnet.EndpointIndependent,
		FilteringBehavior: vnet.EndpointIndependent,
	}
	v, err := buildVNet(natType, natType)
	require.NoError(t, err, "should succeed")
	defer v.close()

	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	var wg sync.WaitGroup
	wg.Add(2)

	cfg0 := &AgentConfig{
		Urls:             []*URL{stunServerURL},
		Trickle:          true,
		NetworkTypes:     supportedNetworkTypes,
		MulticastDNSMode: MulticastDNSModeDisabled,
		Net:              v.net0,
	}

	aAgent, err := NewAgent(cfg0)
	require.NoError(t, err)
	require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))
	require.NoError(t, aAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	}))
	require.NoError(t, aAgent.GatherCandidates())

	cfg1 := &AgentConfig{
		Urls:             []*URL{},
		Trickle:          true,
		Lite:             true,
		CandidateTypes:   []CandidateType{CandidateTypeHost},
		NetworkTypes:     supportedNetworkTypes,
		MulticastDNSMode: MulticastDNSModeDisabled,
		Net:              v.net1,
	}

	bAgent, err := NewAgent(cfg1)
	require.NoError(t, err)
	require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))
	require.NoError(t, bAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	}))
	require.NoError(t, bAgent.GatherCandidates())

	wg.Wait()
	aConn, bConn := connectWithVNet(aAgent, bAgent)

	// Ensure pair selected
	// Note: this assumes ConnectionStateConnected is thrown after selecting the final pair
	<-aConnected
	<-bConnected

	if !closePipe(t, aConn, bConn) {
		return
	}
}

func TestInboundValidity(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	buildMsg := func(class stun.MessageClass, username, key string) *stun.Message {
		msg, err := stun.Build(stun.NewType(stun.MethodBinding, class), stun.TransactionID,
			stun.NewUsername(username),
			stun.NewShortTermIntegrity(key),
			stun.Fingerprint,
		)
		if err != nil {
			t.Fatal(err)
		}

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
	local.conn = &mockPacketConn{}
	if err != nil {
		t.Fatalf("failed to create a new candidate: %v", err)
	}

	t.Run("Invalid Binding requests should be discarded", func(t *testing.T) {
		a, err := NewAgent(&AgentConfig{})
		if err != nil {
			t.Fatalf("Error constructing ice.Agent")
		}

		a.handleInbound(buildMsg(stun.ClassRequest, "invalid", a.localPwd), local, remote)
		if len(a.remoteCandidates) == 1 {
			t.Fatal("Binding with invalid Username was able to create prflx candidate")
		}

		a.handleInbound(buildMsg(stun.ClassRequest, a.localUfrag+":"+a.remoteUfrag, "Invalid"), local, remote)
		if len(a.remoteCandidates) == 1 {
			t.Fatal("Binding with invalid MessageIntegrity was able to create prflx candidate")
		}

		assert.NoError(t, a.Close())
	})

	t.Run("Invalid Binding success responses should be discarded", func(t *testing.T) {
		a, err := NewAgent(&AgentConfig{})
		if err != nil {
			t.Fatalf("Error constructing ice.Agent")
		}

		a.handleInbound(buildMsg(stun.ClassSuccessResponse, a.localUfrag+":"+a.remoteUfrag, "Invalid"), local, remote)
		if len(a.remoteCandidates) == 1 {
			t.Fatal("Binding with invalid MessageIntegrity was able to create prflx candidate")
		}

		assert.NoError(t, a.Close())
	})

	t.Run("Discard non-binding messages", func(t *testing.T) {
		a, err := NewAgent(&AgentConfig{})
		if err != nil {
			t.Fatalf("Error constructing ice.Agent")
		}

		a.handleInbound(buildMsg(stun.ClassErrorResponse, a.localUfrag+":"+a.remoteUfrag, "Invalid"), local, remote)
		if len(a.remoteCandidates) == 1 {
			t.Fatal("non-binding message was able to create prflxRemote")
		}

		assert.NoError(t, a.Close())
	})

	t.Run("Valid bind request", func(t *testing.T) {
		a, err := NewAgent(&AgentConfig{})
		if err != nil {
			t.Fatalf("Error constructing ice.Agent")
		}

		err = a.run(func(a *Agent) {
			a.selector = &controllingSelector{agent: a, log: a.log}
			a.connectivityTicker = time.NewTicker(a.taskLoopInterval)
			a.handleInbound(buildMsg(stun.ClassRequest, a.localUfrag+":"+a.remoteUfrag, a.localPwd), local, remote)
			if len(a.remoteCandidates) != 1 {
				t.Fatal("Binding with valid values was unable to create prflx candidate")
			}
		})

		assert.NoError(t, err)
		assert.NoError(t, a.Close())
	})

	t.Run("Valid bind without fingerprint", func(t *testing.T) {
		var config AgentConfig
		runAgentTest(t, &config, func(a *Agent) {
			a.selector = &controllingSelector{agent: a, log: a.log}
			a.connectivityTicker = time.NewTicker(a.taskLoopInterval)
			msg, err := stun.Build(stun.BindingRequest, stun.TransactionID,
				stun.NewUsername(a.localUfrag+":"+a.remoteUfrag),
				stun.NewShortTermIntegrity(a.localPwd),
			)
			if err != nil {
				t.Fatal(err)
			}

			a.handleInbound(msg, local, remote)
			if len(a.remoteCandidates) != 1 {
				t.Fatal("Binding with valid values (but no fingerprint) was unable to create prflx candidate")
			}
		})
	})

	t.Run("Success with invalid TransactionID", func(t *testing.T) {
		a, err := NewAgent(&AgentConfig{})
		if err != nil {
			t.Fatalf("Error constructing ice.Agent")
		}

		hostConfig := CandidateHostConfig{
			Network:   "udp",
			Address:   "192.168.0.2",
			Port:      777,
			Component: 1,
		}
		local, err := NewCandidateHost(&hostConfig)
		local.conn = &mockPacketConn{}
		if err != nil {
			t.Fatalf("failed to create a new candidate: %v", err)
		}

		remote := &net.UDPAddr{IP: net.ParseIP("172.17.0.3"), Port: 999}
		tID := [stun.TransactionIDSize]byte{}
		copy(tID[:], []byte("ABC"))
		msg, err := stun.Build(stun.BindingSuccess, stun.NewTransactionIDSetter(tID),
			stun.NewShortTermIntegrity(a.remotePwd),
			stun.Fingerprint,
		)
		assert.NoError(t, err)

		a.handleInbound(msg, local, remote)
		if len(a.remoteCandidates) != 0 {
			t.Fatal("unknown remote was able to create a candidate")
		}

		assert.NoError(t, a.Close())
	})
}

func TestInvalidAgentStarts(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	a, err := NewAgent(&AgentConfig{})
	assert.NoError(t, err)

	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	if _, err = a.Dial(ctx, "", "bar"); err != nil && err != ErrRemoteUfragEmpty {
		t.Fatal(err)
	}

	if _, err = a.Dial(ctx, "foo", ""); err != nil && err != ErrRemotePwdEmpty {
		t.Fatal(err)
	}

	if _, err = a.Dial(ctx, "foo", "bar"); err != nil && err != ErrCanceledByCaller {
		t.Fatal(err)
	}

	if _, err = a.Dial(context.TODO(), "foo", "bar"); err != nil && err != ErrMultipleStart {
		t.Fatal(err)
	}

	assert.NoError(t, a.Close())
}

// Assert that Agent emits Connecting/Connected/Disconnected/Closed messages
func TestConnectionStateCallback(t *testing.T) {
	lim := test.TimeOut(time.Second * 5)
	defer lim.Stop()

	report := test.CheckRoutines(t)
	defer report()

	var wg sync.WaitGroup
	wg.Add(2)

	timeoutDuration := time.Second
	KeepaliveInterval := time.Duration(0)
	cfg := &AgentConfig{
		Urls:              []*URL{},
		Trickle:           true,
		NetworkTypes:      supportedNetworkTypes,
		ConnectionTimeout: &timeoutDuration,
		KeepaliveInterval: &KeepaliveInterval,
		taskLoopInterval:  500 * time.Millisecond,
	}

	aAgent, err := NewAgent(cfg)
	if err != nil {
		t.Error(err)
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

	bAgent, err := NewAgent(cfg)
	if err != nil {
		t.Error(err)
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

	isChecking := make(chan interface{})
	isConnected := make(chan interface{})
	isDisconnected := make(chan interface{})
	isClosed := make(chan interface{})
	err = aAgent.OnConnectionStateChange(func(c ConnectionState) {
		switch c {
		case ConnectionStateChecking:
			close(isChecking)
		case ConnectionStateConnected:
			close(isConnected)
		case ConnectionStateDisconnected:
			close(isDisconnected)
		case ConnectionStateClosed:
			close(isClosed)
		}
	})
	if err != nil {
		t.Error(err)
	}

	wg.Wait()
	connect(aAgent, bAgent)

	<-isChecking
	<-isConnected
	<-isDisconnected

	assert.NoError(t, aAgent.Close())
	assert.NoError(t, bAgent.Close())

	<-isClosed
}

func TestInvalidGather(t *testing.T) {
	t.Run("Gather with Trickle enable and no OnCandidate should error", func(t *testing.T) {
		a, err := NewAgent(&AgentConfig{Trickle: true})
		if err != nil {
			t.Fatalf("Error constructing ice.Agent")
		}

		err = a.GatherCandidates()
		if err != ErrNoOnCandidateHandler {
			t.Fatal("trickle GatherCandidates succeeded without OnCandidate")
		}
		assert.NoError(t, a.Close())
	})
}

func TestCandidatePairStats(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	// avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	a, err := NewAgent(&AgentConfig{})
	if err != nil {
		t.Fatalf("Failed to create agent: %s", err)
	}

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
		p := a.findPair(hostLocal, remote)

		if p == nil {
			a.addPair(hostLocal, remote)
		}
	}

	p := a.findPair(hostLocal, prflxRemote)
	p.state = CandidatePairStateFailed

	stats := a.GetCandidatePairsStats()
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
		t.Fatalf("expected host-prfflx pair to have state failed, it has state %s instead",
			prflxPairStat.State.String())
	}

	assert.NoError(t, a.Close())
}

func TestLocalCandidateStats(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	// avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	a, err := NewAgent(&AgentConfig{})
	if err != nil {
		t.Fatalf("Failed to create agent: %s", err)
	}

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

	a.localCandidates[NetworkTypeUDP4] = []Candidate{hostLocal, srflxLocal}

	localStats := a.GetLocalCandidatesStats()
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

	assert.NoError(t, a.Close())
}

func TestRemoteCandidateStats(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	// avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	a, err := NewAgent(&AgentConfig{})
	if err != nil {
		t.Fatalf("Failed to create agent: %s", err)
	}

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

	a.remoteCandidates[NetworkTypeUDP4] = []Candidate{relayRemote, srflxRemote, prflxRemote, hostRemote}

	remoteStats := a.GetRemoteCandidatesStats()
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

	assert.NoError(t, a.Close())
}

func TestInitExtIPMapping(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	// a.extIPMapper should be nil by default
	a, err := NewAgent(&AgentConfig{
		Trickle: true, // to avoid starting gathering candidates
	})
	if err != nil {
		t.Fatalf("Failed to create agent: %v", err)
	}
	if a.extIPMapper != nil {
		t.Fatal("a.extIPMapper should be nil by default")
	}
	assert.NoError(t, a.Close())

	// a.extIPMapper should be nil when NAT1To1IPs is a non-nil empty array
	a, err = NewAgent(&AgentConfig{
		NAT1To1IPs:             []string{},
		NAT1To1IPCandidateType: CandidateTypeHost,
		Trickle:                true, // to avoid starting gathering candidates
	})
	if err != nil {
		t.Fatalf("Failed to create agent: %v", err)
	}
	if a.extIPMapper != nil {
		t.Fatal("a.extIPMapper should be nil by default")
	}
	assert.NoError(t, a.Close())

	// NewAgent should return an error when 1:1 NAT for host candidate is enabled
	// but the candidate type does not appear in the CandidateTypes.
	_, err = NewAgent(&AgentConfig{
		NAT1To1IPs:             []string{"1.2.3.4"},
		NAT1To1IPCandidateType: CandidateTypeHost,
		CandidateTypes:         []CandidateType{CandidateTypeRelay},
		Trickle:                true, // to avoid starting gathering candidates
	})
	if err != ErrIneffectiveNAT1To1IPMappingHost {
		t.Fatalf("Unexpected error: %v", err)
	}

	// NewAgent should return an error when 1:1 NAT for srflx candidate is enabled
	// but the candidate type does not appear in the CandidateTypes.
	_, err = NewAgent(&AgentConfig{
		NAT1To1IPs:             []string{"1.2.3.4"},
		NAT1To1IPCandidateType: CandidateTypeServerReflexive,
		CandidateTypes:         []CandidateType{CandidateTypeRelay},
		Trickle:                true, // to avoid starting gathering candidates
	})
	if err != ErrIneffectiveNAT1To1IPMappingSrflx {
		t.Fatalf("Unexpected error: %v", err)
	}

	// NewAgent should return an error when 1:1 NAT for host candidate is enabled
	// along with mDNS with MulticastDNSModeQueryAndGather
	_, err = NewAgent(&AgentConfig{
		NAT1To1IPs:             []string{"1.2.3.4"},
		NAT1To1IPCandidateType: CandidateTypeHost,
		MulticastDNSMode:       MulticastDNSModeQueryAndGather,
		Trickle:                true, // to avoid starting gathering candidates
	})
	if err != ErrMulticastDNSWithNAT1To1IPMapping {
		t.Fatalf("Unexpected error: %v", err)
	}

	// NewAgent should return if newExternalIPMapper() returns an error.
	_, err = NewAgent(&AgentConfig{
		NAT1To1IPs:             []string{"bad.2.3.4"}, // bad IP
		NAT1To1IPCandidateType: CandidateTypeHost,
		Trickle:                true, // to avoid starting gathering candidates
	})
	if err != ErrInvalidNAT1To1IPMapping {
		t.Fatalf("Unexpected error: %v", err)
	}
}

func TestBindingRequestTimeout(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	const expectedRemovalCount = 2

	a, err := NewAgent(&AgentConfig{})
	assert.NoError(t, err)

	now := time.Now()
	a.pendingBindingRequests = append(a.pendingBindingRequests, bindingRequest{
		timestamp: now,
	})
	a.pendingBindingRequests = append(a.pendingBindingRequests, bindingRequest{
		timestamp: now.Add(-25 * time.Millisecond),
	})
	a.pendingBindingRequests = append(a.pendingBindingRequests, bindingRequest{
		timestamp: now.Add(-750 * time.Millisecond),
	})
	a.pendingBindingRequests = append(a.pendingBindingRequests, bindingRequest{
		timestamp: now.Add(-75 * time.Hour),
	})

	a.invalidatePendingBindingRequests(now)
	assert.Equal(t, len(a.pendingBindingRequests), expectedRemovalCount, "Binding invalidation due to timeout did not remove the correct number of binding requests")
	assert.NoError(t, a.Close())
}

// TestAgentCredentials checks if local username fragments and passwords (if set) meet RFC standard
// and ensure it's backwards compatible with previous versions of the pion/ice
func TestAgentCredentials(t *testing.T) {
	// Make sure to pass travis check by disabling the logs
	log := logging.NewDefaultLoggerFactory()
	log.DefaultLogLevel = logging.LogLevelDisabled

	// Agent should not require any of the usernames and password to be set
	// If set, they should follow the default 16/128 bits random number generator strategy

	agent, err := NewAgent(&AgentConfig{Trickle: true, LoggerFactory: log})
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len([]rune(agent.localUfrag))*8, 24)
	assert.GreaterOrEqual(t, len([]rune(agent.localPwd))*8, 128)
	assert.NoError(t, agent.Close())

	// Should honor RFC standards
	// Local values MUST be unguessable, with at least 128 bits of
	// random number generator output used to generate the password, and
	// at least 24 bits of output to generate the username fragment.

	_, err = NewAgent(&AgentConfig{Trickle: true, LocalUfrag: "xx", LoggerFactory: log})
	assert.EqualError(t, err, ErrLocalUfragInsufficientBits.Error())

	_, err = NewAgent(&AgentConfig{Trickle: true, LocalPwd: "xxxxxx", LoggerFactory: log})
	assert.EqualError(t, err, ErrLocalPwdInsufficientBits.Error())
}
