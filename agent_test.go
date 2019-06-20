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

	err = a.Close()

	if err != nil {
		t.Fatalf("Close agent emits error %v", err)
	}
}

func TestPairPriority(t *testing.T) {
	// avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	a, err := NewAgent(&AgentConfig{})
	if err != nil {
		t.Fatalf("Failed to create agent: %s", err)
	}

	hostLocal, err := NewCandidateHost(
		"udp",
		"192.168.1.1", 19216,
		1,
	)
	if err != nil {
		t.Fatalf("Failed to construct local host candidate: %s", err)
	}

	relayRemote, err := NewCandidateRelay(
		"udp",
		"1.2.3.4", 12340,
		1,
		"4.3.2.1", 43210,
	)
	if err != nil {
		t.Fatalf("Failed to construct remote relay candidate: %s", err)
	}

	srflxRemote, err := NewCandidateServerReflexive(
		"udp",
		"10.10.10.2", 19218,
		1,
		"4.3.2.1", 43212,
	)
	if err != nil {
		t.Fatalf("Failed to construct remote srflx candidate: %s", err)
	}

	prflxRemote, err := NewCandidatePeerReflexive(
		"udp",
		"10.10.10.2", 19217,
		1,
		"4.3.2.1", 43211,
	)
	if err != nil {
		t.Fatalf("Failed to construct remote prflx candidate: %s", err)
	}

	hostRemote, err := NewCandidateHost(
		"udp",
		"1.2.3.5", 12350,
		1,
	)
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

	if err := a.Close(); err != nil {
		t.Fatalf("Error on agent.Close(): %s", err)
	}
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

	hostLocal, err := NewCandidateHost(
		"udp",
		"192.168.1.1", 19216,
		1,
	)
	if err != nil {
		t.Fatalf("Failed to construct local host candidate: %s", err)
	}

	relayRemote, err := NewCandidateRelay(
		"udp",
		"1.2.3.4", 12340,
		1,
		"4.3.2.1", 43210,
	)
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
}

func TestHandlePeerReflexive(t *testing.T) {
	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 2)
	defer lim.Stop()

	t.Run("UDP pflx candidate from handleInbound()", func(t *testing.T) {
		var config AgentConfig
		runAgentTest(t, &config, func(a *Agent) {
			a.selector = &controllingSelector{agent: a, log: a.log}
			local, err := NewCandidateHost("udp", "192.168.0.2", 777, 1)
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

			err = a.Close()
			if err != nil {
				t.Fatalf("Close agent emits error %v", err)
			}
		})
	})

	t.Run("Bad network type with handleInbound()", func(t *testing.T) {
		var config AgentConfig
		runAgentTest(t, &config, func(a *Agent) {
			a.selector = &controllingSelector{agent: a, log: a.log}
			local, err := NewCandidateHost("tcp", "192.168.0.2", 777, 1)
			if err != nil {
				t.Fatalf("failed to create a new candidate: %v", err)
			}

			remote := &BadAddr{}

			a.handleInbound(nil, local, remote)

			if len(a.remoteCandidates) != 0 {
				t.Fatal("bad address should not be added to the remote candidate list")
			}

			err = a.Close()
			if err != nil {
				t.Fatalf("Close agent emits error %v", err)
			}
		})
	})

	t.Run("Success from unknown remote, prflx candidate MUST only be created via Binding Request", func(t *testing.T) {
		var config AgentConfig
		runAgentTest(t, &config, func(a *Agent) {
			a.selector = &controllingSelector{agent: a, log: a.log}
			tID := [stun.TransactionIDSize]byte{}
			copy(tID[:], []byte("ABC"))
			a.pendingBindingRequests = []bindingRequest{
				{tID, &net.UDPAddr{}, false},
			}

			local, err := NewCandidateHost("udp", "192.168.0.2", 777, 1)
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

// Assert that Agent on startup sends message, and doesn't wait for taskloop to start
// github.com/pion/ice/issues/15
func TestConnectivityOnStartup(t *testing.T) {
	lim := test.TimeOut(time.Second * 5)
	defer lim.Stop()

	cfg := &AgentConfig{
		Urls:             []*URL{},
		Trickle:          false,
		NetworkTypes:     supportedNetworkTypes,
		taskLoopInterval: time.Hour,
		LoggerFactory:    logging.NewDefaultLoggerFactory(),
	}

	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	aAgent, err := NewAgent(cfg)
	if err != nil {
		t.Error(err)
	}
	err = aAgent.OnConnectionStateChange(aNotifier)
	if err != nil {
		panic(err)
	}

	bAgent, err := NewAgent(cfg)
	if err != nil {
		t.Error(err)
	}
	err = bAgent.OnConnectionStateChange(bNotifier)
	if err != nil {
		panic(err)
	}

	connect(aAgent, bAgent)

	<-aConnected
	<-bConnected
}

func TestInboundValidity(t *testing.T) {
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
	local, err := NewCandidateHost("udp", "192.168.0.2", 777, 1)
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
	})

	t.Run("Valid bind request", func(t *testing.T) {
		a, err := NewAgent(&AgentConfig{})
		if err != nil {
			t.Fatalf("Error constructing ice.Agent")
		}

		err = a.run(func(a *Agent) {
			a.selector = &controllingSelector{agent: a, log: a.log}
			a.handleInbound(buildMsg(stun.ClassRequest, a.localUfrag+":"+a.remoteUfrag, a.localPwd), local, remote)
			if len(a.remoteCandidates) != 1 {
				t.Fatal("Binding with valid values was unable to create prflx candidate")
			}
		})

		if err != nil {
			t.Fatalf("Agent run failure: %v", err)
		}
	})

	t.Run("Valid bind without fingerprint", func(t *testing.T) {
		var config AgentConfig
		runAgentTest(t, &config, func(a *Agent) {
			a.selector = &controllingSelector{agent: a, log: a.log}
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

		local, err := NewCandidateHost("udp", "192.168.0.2", 777, 1)
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
		if err != nil {
			t.Fatal(err)
		}

		a.handleInbound(msg, local, remote)
		if len(a.remoteCandidates) != 0 {
			t.Fatal("unknown remote was able to create a candidate")
		}
	})

}

func TestInvalidAgentStarts(t *testing.T) {
	a, err := NewAgent(&AgentConfig{})
	if err != nil {
		t.Fatal(err)
	}

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
}

// Assert that Agent emits Connecting/Connected/Disconnected/Closed messages
func TestConnectionStateCallback(t *testing.T) {
	lim := test.TimeOut(time.Second * 5)
	defer lim.Stop()

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
	if err = aAgent.Close(); err != nil {
		t.Error(err)
	}

	if err = bAgent.Close(); err != nil {
		t.Error(err)
	}

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
	})
}

func TestCandidatePairStats(t *testing.T) {
	// avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	a, err := NewAgent(&AgentConfig{})
	if err != nil {
		t.Fatalf("Failed to create agent: %s", err)
	}

	hostLocal, err := NewCandidateHost(
		"udp",
		"192.168.1.1", 19216,
		1,
	)
	if err != nil {
		t.Fatalf("Failed to construct local host candidate: %s", err)
	}

	relayRemote, err := NewCandidateRelay(
		"udp",
		"1.2.3.4", 2340,
		1,
		"4.3.2.1", 43210,
	)
	if err != nil {
		t.Fatalf("Failed to construct remote relay candidate: %s", err)
	}

	srflxRemote, err := NewCandidateServerReflexive(
		"udp",
		"10.10.10.2", 19218,
		1,
		"4.3.2.1", 43212,
	)
	if err != nil {
		t.Fatalf("Failed to construct remote srflx candidate: %s", err)
	}

	prflxRemote, err := NewCandidatePeerReflexive(
		"udp",
		"10.10.10.2", 19217,
		1,
		"4.3.2.1", 43211,
	)
	if err != nil {
		t.Fatalf("Failed to construct remote prflx candidate: %s", err)
	}

	hostRemote, err := NewCandidateHost(
		"udp",
		"1.2.3.5", 12350,
		1,
	)
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

	if err := a.Close(); err != nil {
		t.Fatalf("Error on agent.Close(): %s", err)
	}
}

func TestLocalCandidateStats(t *testing.T) {
	// avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	a, err := NewAgent(&AgentConfig{})
	if err != nil {
		t.Fatalf("Failed to create agent: %s", err)
	}

	hostLocal, err := NewCandidateHost(
		"udp",
		"192.168.1.1", 19216,
		1,
	)
	if err != nil {
		t.Fatalf("Failed to construct local host candidate: %s", err)
	}

	srflxLocal, err := NewCandidateServerReflexive(
		"udp",
		"192.168.1.1", 19217,
		1,
		"4.3.2.1", 43212,
	)
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

	if err := a.Close(); err != nil {
		t.Fatalf("Error on agent.Close(): %s", err)
	}
}

func TestRemoteCandidateStats(t *testing.T) {
	// avoid deadlocks?
	defer test.TimeOut(1 * time.Second).Stop()

	a, err := NewAgent(&AgentConfig{})
	if err != nil {
		t.Fatalf("Failed to create agent: %s", err)
	}

	relayRemote, err := NewCandidateRelay(
		"udp",
		"1.2.3.4", 12340,
		1,
		"4.3.2.1", 43210,
	)
	if err != nil {
		t.Fatalf("Failed to construct remote relay candidate: %s", err)
	}

	srflxRemote, err := NewCandidateServerReflexive(
		"udp",
		"10.10.10.2", 19218,
		1,
		"4.3.2.1", 43212,
	)
	if err != nil {
		t.Fatalf("Failed to construct remote srflx candidate: %s", err)
	}

	prflxRemote, err := NewCandidatePeerReflexive(
		"udp",
		"10.10.10.2", 19217,
		1,
		"4.3.2.1", 43211,
	)
	if err != nil {
		t.Fatalf("Failed to construct remote prflx candidate: %s", err)
	}

	hostRemote, err := NewCandidateHost(
		"udp",
		"1.2.3.5", 12350,
		1,
	)
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

	if err := a.Close(); err != nil {
		t.Fatalf("Error on agent.Close(): %s", err)
	}
}
