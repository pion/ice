package ice

import (
	"context"
	"net"
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

	if len(a.validPairs) != 0 {
		t.Fatalf("TestPairSearch is only a valid test if a.validPairs is empty on construction")
	}

	cp := a.getBestValidPair()

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
		net.ParseIP("192.168.1.1"), 19216,
		1,
	)
	if err != nil {
		t.Fatalf("Failed to construct local host candidate: %s", err)
	}

	relayRemote, err := NewCandidateRelay(
		"udp",
		net.ParseIP("1.2.3.4"), 12340,
		1,
		"4.3.2.1", 43210,
	)
	if err != nil {
		t.Fatalf("Failed to construct remote relay candidate: %s", err)
	}

	srflxRemote, err := NewCandidateServerReflexive(
		"udp",
		net.ParseIP("10.10.10.2"), 19218,
		1,
		"4.3.2.1", 43212,
	)
	if err != nil {
		t.Fatalf("Failed to construct remote srflx candidate: %s", err)
	}

	prflxRemote, err := NewCandidatePeerReflexive(
		"udp",
		net.ParseIP("10.10.10.2"), 19217,
		1,
		"4.3.2.1", 43211,
	)
	if err != nil {
		t.Fatalf("Failed to construct remote prflx candidate: %s", err)
	}

	hostRemote, err := NewCandidateHost(
		"udp",
		net.ParseIP("1.2.3.5"), 12350,
		1,
	)
	if err != nil {
		t.Fatalf("Failed to construct remote host candidate: %s", err)
	}

	for _, remote := range []*Candidate{relayRemote, srflxRemote, prflxRemote, hostRemote} {
		a.addValidPair(hostLocal, remote)
		bestPair := a.getBestValidPair()
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
	if err = a.OnSelectedCandidatePairChange(func(local, remote *Candidate) {
		close(callbackCalled)
	}); err != nil {
		t.Fatalf("Failed to set agent OnCandidatePairChange callback: %s", err)
	}

	hostLocal, err := NewCandidateHost(
		"udp",
		net.ParseIP("192.168.1.1"), 19216,
		1,
	)
	if err != nil {
		t.Fatalf("Failed to construct local host candidate: %s", err)
	}

	relayRemote, err := NewCandidateRelay(
		"udp",
		net.ParseIP("1.2.3.4"), 12340,
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
			ip := net.ParseIP("192.168.0.2")
			local, err := NewCandidateHost("udp", ip, 777, 1)
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
			set := a.remoteCandidates[local.NetworkType]
			if len(set) != 1 {
				t.Fatal("failed to add prflx candidate to remote candidate list")
			}

			c := set[0]

			if c.Type != CandidateTypePeerReflexive {
				t.Fatal("candidate type must be prflx")
			}

			if !c.IP.Equal(net.ParseIP("172.17.0.3")) {
				t.Fatal("IP address mismatch")
			}

			if c.Port != 999 {
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
			ip := net.ParseIP("192.168.0.2")
			local, err := NewCandidateHost("tcp", ip, 777, 1)
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

			local, err := NewCandidateHost("udp", net.ParseIP("192.168.0.2"), 777, 1)
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
	local, err := NewCandidateHost("udp", net.ParseIP("192.168.0.2"), 777, 1)
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

		local, err := NewCandidateHost("udp", net.ParseIP("192.168.0.2"), 777, 1)
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

	timeoutDuration := time.Second
	KeepaliveInterval := time.Duration(0)
	cfg := &AgentConfig{
		Urls:              []*URL{},
		NetworkTypes:      supportedNetworkTypes,
		ConnectionTimeout: &timeoutDuration,
		KeepaliveInterval: &KeepaliveInterval,
		taskLoopInterval:  500 * time.Millisecond,
	}

	aAgent, err := NewAgent(cfg)
	if err != nil {
		t.Error(err)
	}

	bAgent, err := NewAgent(cfg)
	if err != nil {
		t.Error(err)
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
