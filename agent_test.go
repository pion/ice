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

	"github.com/pion/ice/v2/internal/fakenet"
	"github.com/pion/logging"
	"github.com/pion/stun"
	"github.com/pion/transport/v2/test"
	"github.com/pion/transport/v2/vnet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type BadAddr struct{}

func (ba *BadAddr) Network() string {
	return "xxx"
}

func (ba *BadAddr) String() string {
	return "yyy"
}

func runAgentTest(t *testing.T, config *AgentConfig, task func(ctx context.Context, a *Agent)) {
	a, err := NewAgent(config)
	if err != nil {
		t.Fatalf("Error constructing ice.Agent")
	}

	if err := a.run(context.Background(), task); err != nil {
		t.Fatalf("Agent run failure: %v", err)
	}

	assert.NoError(t, a.Close())
}

func TestHandlePeerReflexive(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	// Limit runtime in case of deadlocks
	lim := test.TimeOut(time.Second * 2)
	defer lim.Stop()

	t.Run("UDP prflx candidate from handleInbound()", func(t *testing.T) {
		var config AgentConfig
		runAgentTest(t, &config, func(ctx context.Context, a *Agent) {
			a.selector = &controllingSelector{agent: a, log: a.log}

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
				stun.NewUsername(a.localUfrag+":"+a.remoteUfrag),
				UseCandidate(),
				AttrControlling(a.tieBreaker),
				PriorityAttr(local.Priority()),
				stun.NewShortTermIntegrity(a.localPwd),
				stun.Fingerprint,
			)
			if err != nil {
				t.Fatal(err)
			}

			a.handleInbound(msg, local, remote)

			// Length of remote candidate list must be one now
			if len(a.remoteCandidates) != 1 {
				t.Fatal("failed to add a network type to the remote candidate list")
			}

			// Length of remote candidate list for a network type must be 1
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
		runAgentTest(t, &config, func(ctx context.Context, a *Agent) {
			a.selector = &controllingSelector{agent: a, log: a.log}

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
		runAgentTest(t, &config, func(ctx context.Context, a *Agent) {
			a.selector = &controllingSelector{agent: a, log: a.log}
			tID := [stun.TransactionIDSize]byte{}
			copy(tID[:], "ABC")
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
			local.conn = &fakenet.MockPacketConn{}
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
// https://github.com/pion/ice/issues/15
func TestConnectivityOnStartup(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	// Create a network with two interfaces
	wan, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "0.0.0.0/0",
		LoggerFactory: logging.NewDefaultLoggerFactory(),
	})
	assert.NoError(err)

	net0, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{"192.168.0.1"},
	})
	assert.NoError(err)
	assert.NoError(wan.AddNet(net0))

	net1, err := vnet.NewNet(&vnet.NetConfig{
		StaticIPs: []string{"192.168.0.2"},
	})
	assert.NoError(err)
	assert.NoError(wan.AddNet(net1))

	assert.NoError(wan.Start())

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
	require.NoError(err)
	require.NoError(aAgent.OnConnectionStateChange(aNotifier))

	cfg1 := &AgentConfig{
		NetworkTypes:      supportedNetworkTypes(),
		MulticastDNSMode:  MulticastDNSModeDisabled,
		Net:               net1,
		KeepaliveInterval: &KeepaliveInterval,
		CheckInterval:     &KeepaliveInterval,
	}

	bAgent, err := NewAgent(cfg1)
	require.NoError(err)
	require.NoError(bAgent.OnConnectionStateChange(bNotifier))

	aConn, bConn := func(aAgent, bAgent *Agent) (*Conn, *Conn) {
		// Manual signaling
		aUfrag, aPwd, err := aAgent.GetLocalUserCredentials()
		assert.NoError(err)

		bUfrag, bPwd, err := bAgent.GetLocalUserCredentials()
		assert.NoError(err)

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

	assert.NoError(wan.Stop())
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
	local.conn = &fakenet.MockPacketConn{}
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
		assert := assert.New(t)

		a, err := NewAgent(&AgentConfig{})
		if err != nil {
			t.Fatalf("Error constructing ice.Agent")
		}

		err = a.run(context.Background(), func(ctx context.Context, a *Agent) {
			a.selector = &controllingSelector{agent: a, log: a.log}
			a.handleInbound(buildMsg(stun.ClassRequest, a.localUfrag+":"+a.remoteUfrag, a.localPwd), local, remote)
			if len(a.remoteCandidates) != 1 {
				t.Fatal("Binding with valid values was unable to create prflx candidate")
			}
		})

		assert.NoError(err)
		assert.NoError(a.Close())
	})

	t.Run("Valid bind without fingerprint", func(t *testing.T) {
		var config AgentConfig
		runAgentTest(t, &config, func(ctx context.Context, a *Agent) {
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
		assert := assert.New(t)

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
		local.conn = &fakenet.MockPacketConn{}
		if err != nil {
			t.Fatalf("failed to create a new candidate: %v", err)
		}

		remote := &net.UDPAddr{IP: net.ParseIP("172.17.0.3"), Port: 999}
		tID := [stun.TransactionIDSize]byte{}
		copy(tID[:], "ABC")
		msg, err := stun.Build(stun.BindingSuccess, stun.NewTransactionIDSetter(tID),
			stun.NewShortTermIntegrity(a.remotePwd),
			stun.Fingerprint,
		)
		assert.NoError(err)

		a.handleInbound(msg, local, remote)
		if len(a.remoteCandidates) != 0 {
			t.Fatal("unknown remote was able to create a candidate")
		}

		assert.NoError(a.Close())
	})
}

func TestInvalidAgentStarts(t *testing.T) {
	assert := assert.New(t)
	report := test.CheckRoutines(t)
	defer report()

	a, err := NewAgent(&AgentConfig{})
	assert.NoError(err)

	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	if _, err = a.Dial(ctx, "", "bar"); err != nil && !errors.Is(err, ErrRemoteUfragEmpty) {
		t.Fatal(err)
	}

	if _, err = a.Dial(ctx, "foo", ""); err != nil && !errors.Is(err, ErrRemotePwdEmpty) {
		t.Fatal(err)
	}

	if _, err = a.Dial(ctx, "foo", "bar"); err != nil && !errors.Is(err, ErrCanceledByCaller) {
		t.Fatal(err)
	}

	if _, err = a.Dial(context.TODO(), "foo", "bar"); err != nil && !errors.Is(err, ErrMultipleStart) {
		t.Fatal(err)
	}

	assert.NoError(a.Close())
}

func TestInvalidGather(t *testing.T) {
	t.Run("Gather with no OnCandidate should error", func(t *testing.T) {
		a, err := NewAgent(&AgentConfig{})
		if err != nil {
			t.Fatalf("Error constructing ice.Agent")
		}

		err = a.GatherCandidates()
		if !errors.Is(err, ErrNoOnCandidateHandler) {
			t.Fatal("trickle GatherCandidates succeeded without OnCandidate")
		}
		assert.NoError(t, a.Close())
	})
}

func TestInitExtIPMapping(t *testing.T) {
	assert := assert.New(t)
	report := test.CheckRoutines(t)
	defer report()

	// a.extIPMapper should be nil by default
	a, err := NewAgent(&AgentConfig{})
	if err != nil {
		t.Fatalf("Failed to create agent: %v", err)
	}
	if a.extIPMapper != nil {
		t.Fatal("a.extIPMapper should be nil by default")
	}
	assert.NoError(a.Close())

	// a.extIPMapper should be nil when NAT1To1IPs is a non-nil empty array
	a, err = NewAgent(&AgentConfig{
		NAT1To1IPs:             []string{},
		NAT1To1IPCandidateType: CandidateTypeHost,
	})
	if err != nil {
		t.Fatalf("Failed to create agent: %v", err)
	}
	if a.extIPMapper != nil {
		t.Fatal("a.extIPMapper should be nil by default")
	}
	assert.NoError(a.Close())

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
	assert := assert.New(t)
	report := test.CheckRoutines(t)
	defer report()

	const expectedRemovalCount = 2

	a, err := NewAgent(&AgentConfig{})
	assert.NoError(err)

	now := time.Now()
	a.pendingBindingRequests = append(a.pendingBindingRequests, bindingRequest{
		timestamp: now, // Valid
	})
	a.pendingBindingRequests = append(a.pendingBindingRequests, bindingRequest{
		timestamp: now.Add(-3900 * time.Millisecond), // Valid
	})
	a.pendingBindingRequests = append(a.pendingBindingRequests, bindingRequest{
		timestamp: now.Add(-4100 * time.Millisecond), // Invalid
	})
	a.pendingBindingRequests = append(a.pendingBindingRequests, bindingRequest{
		timestamp: now.Add(-75 * time.Hour), // Invalid
	})

	a.invalidatePendingBindingRequests(now)
	assert.Equal(expectedRemovalCount, len(a.pendingBindingRequests), "Binding invalidation due to timeout did not remove the correct number of binding requests")
	assert.NoError(a.Close())
}

// TestAgentCredentials checks if local username fragments and passwords (if set) meet RFC standard
// and ensure it's backwards compatible with previous versions of the pion/ice
func TestAgentCredentials(t *testing.T) {
	assert := assert.New(t)
	report := test.CheckRoutines(t)
	defer report()

	// Make sure to pass Travis check by disabling the logs
	log := logging.NewDefaultLoggerFactory()
	log.DefaultLogLevel = logging.LogLevelDisabled

	// Agent should not require any of the usernames and password to be set
	// If set, they should follow the default 16/128 bits random number generator strategy

	agent, err := NewAgent(&AgentConfig{LoggerFactory: log})
	assert.NoError(err)
	assert.GreaterOrEqual(len([]rune(agent.localUfrag))*8, 24)
	assert.GreaterOrEqual(len([]rune(agent.localPwd))*8, 128)
	assert.NoError(agent.Close())

	// Should honor RFC standards
	// Local values MUST be unguessable, with at least 128 bits of
	// random number generator output used to generate the password, and
	// at least 24 bits of output to generate the username fragment.

	_, err = NewAgent(&AgentConfig{LocalUfrag: "xx", LoggerFactory: log})
	assert.EqualError(err, ErrLocalUfragInsufficientBits.Error())

	_, err = NewAgent(&AgentConfig{LocalPwd: "xxxxxx", LoggerFactory: log})
	assert.EqualError(err, ErrLocalPwdInsufficientBits.Error())
}

// Assert that Agent on Failure deletes all existing candidates
// User can then do an ICE Restart to bring agent back
func TestConnectionStateFailedDeleteAllCandidates(t *testing.T) {
	assert := assert.New(t)
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 5)
	defer lim.Stop()

	oneSecond := time.Second
	KeepaliveInterval := time.Duration(0)

	cfg := &AgentConfig{
		NetworkTypes:        supportedNetworkTypes(),
		DisconnectedTimeout: &oneSecond,
		FailedTimeout:       &oneSecond,
		KeepaliveInterval:   &KeepaliveInterval,
	}

	aAgent, err := NewAgent(cfg)
	assert.NoError(err)

	bAgent, err := NewAgent(cfg)
	assert.NoError(err)

	isFailed := make(chan interface{})
	assert.NoError(aAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateFailed {
			close(isFailed)
		}
	}))

	connect(aAgent, bAgent)
	<-isFailed

	done := make(chan struct{})
	assert.NoError(aAgent.run(context.Background(), func(ctx context.Context, agent *Agent) {
		assert.Equal(len(aAgent.remoteCandidates), 0)
		assert.Equal(len(aAgent.localCandidates), 0)
		close(done)
	}))
	<-done

	assert.NoError(aAgent.Close())
	assert.NoError(bAgent.Close())
}

// Assert that the ICE Agent can go directly from Connecting -> Failed on both sides
func TestConnectionStateConnectingToFailed(t *testing.T) {
	assert := assert.New(t)
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 5)
	defer lim.Stop()

	oneSecond := time.Second
	KeepaliveInterval := time.Duration(0)

	cfg := &AgentConfig{
		DisconnectedTimeout: &oneSecond,
		FailedTimeout:       &oneSecond,
		KeepaliveInterval:   &KeepaliveInterval,
	}

	aAgent, err := NewAgent(cfg)
	assert.NoError(err)

	bAgent, err := NewAgent(cfg)
	assert.NoError(err)

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

	assert.NoError(aAgent.OnConnectionStateChange(connectionStateCheck))
	assert.NoError(bAgent.OnConnectionStateChange(connectionStateCheck))

	go func() {
		_, err := aAgent.Accept(context.TODO(), "InvalidFrag", "InvalidPwd")
		assert.Error(err)
	}()

	go func() {
		_, err := bAgent.Dial(context.TODO(), "InvalidFrag", "InvalidPwd")
		assert.Error(err)
	}()

	isChecking.Wait()
	isFailed.Wait()

	assert.NoError(aAgent.Close())
	assert.NoError(bAgent.Close())
}

func TestAgentRestart(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	oneSecond := time.Second

	t.Run("Restart During Gather", func(t *testing.T) {
		assert := assert.New(t)

		connA, connB := pipe(&AgentConfig{
			DisconnectedTimeout: &oneSecond,
			FailedTimeout:       &oneSecond,
		})

		ctx, cancel := context.WithCancel(context.Background())
		assert.NoError(connB.agent.OnConnectionStateChange(func(c ConnectionState) {
			if c == ConnectionStateFailed || c == ConnectionStateDisconnected {
				cancel()
			}
		}))

		connA.agent.gatheringState = GatheringStateGathering
		assert.NoError(connA.agent.Restart("", ""))

		<-ctx.Done()
		assert.NoError(connA.agent.Close())
		assert.NoError(connB.agent.Close())
	})

	t.Run("Restart When Closed", func(t *testing.T) {
		assert := assert.New(t)

		agent, err := NewAgent(&AgentConfig{})
		assert.NoError(err)
		assert.NoError(agent.Close())

		assert.Equal(ErrClosed, agent.Restart("", ""))
	})

	t.Run("Restart One Side", func(t *testing.T) {
		assert := assert.New(t)

		connA, connB := pipe(&AgentConfig{
			DisconnectedTimeout: &oneSecond,
			FailedTimeout:       &oneSecond,
		})

		ctx, cancel := context.WithCancel(context.Background())
		assert.NoError(connB.agent.OnConnectionStateChange(func(c ConnectionState) {
			if c == ConnectionStateFailed || c == ConnectionStateDisconnected {
				cancel()
			}
		}))
		assert.NoError(connA.agent.Restart("", ""))

		<-ctx.Done()
		assert.NoError(connA.agent.Close())
		assert.NoError(connB.agent.Close())
	})

	t.Run("Restart Both Sides", func(t *testing.T) {
		assert := assert.New(t)

		// Get all addresses of candidates concatenated
		generateCandidateAddressStrings := func(candidates []Candidate, err error) (out string) {
			assert.NoError(err)

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
		connAFirstCandidates := generateCandidateAddressStrings(connA.agent.GetLocalCandidates())
		connBFirstCandidates := generateCandidateAddressStrings(connB.agent.GetLocalCandidates())

		aNotifier, aConnected := onConnected()
		assert.NoError(connA.agent.OnConnectionStateChange(aNotifier))

		bNotifier, bConnected := onConnected()
		assert.NoError(connB.agent.OnConnectionStateChange(bNotifier))

		// Restart and Re-Signal
		assert.NoError(connA.agent.Restart("", ""))
		assert.NoError(connB.agent.Restart("", ""))

		// Exchange Candidates and Credentials
		ufrag, pwd, err := connB.agent.GetLocalUserCredentials()
		assert.NoError(err)
		assert.NoError(connA.agent.SetRemoteCredentials(ufrag, pwd))

		ufrag, pwd, err = connA.agent.GetLocalUserCredentials()
		assert.NoError(err)
		assert.NoError(connB.agent.SetRemoteCredentials(ufrag, pwd))

		gatherAndExchangeCandidates(connA.agent, connB.agent)

		// Wait until both have gone back to connected
		<-aConnected
		<-bConnected

		// Assert that we have new candidates each time
		assert.NotEqual(connAFirstCandidates, generateCandidateAddressStrings(connA.agent.GetLocalCandidates()))
		assert.NotEqual(connBFirstCandidates, generateCandidateAddressStrings(connB.agent.GetLocalCandidates()))

		assert.NoError(connA.agent.Close())
		assert.NoError(connB.agent.Close())
	})
}

func TestGetRemoteCredentials(t *testing.T) {
	assert := assert.New(t)

	var config AgentConfig
	a, err := NewAgent(&config)
	if err != nil {
		t.Fatalf("Error constructing ice.Agent: %v", err)
	}

	a.remoteUfrag = "remoteUfrag"
	a.remotePwd = "remotePwd"

	actualUfrag, actualPwd, err := a.GetRemoteUserCredentials()
	assert.NoError(err)

	assert.Equal(actualUfrag, a.remoteUfrag)
	assert.Equal(actualPwd, a.remotePwd)

	assert.NoError(a.Close())
}
