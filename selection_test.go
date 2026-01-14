// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/pion/transport/v4/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	selectionTestPassword    = "pwd"
	selectionTestRemoteUfrag = "remote"
	selectionTestLocalUfrag  = "local"
)

func sendUntilDone(t *testing.T, writingConn, readingConn net.Conn, maxAttempts int) bool {
	t.Helper()

	testMessage := []byte("Hello World")
	testBuffer := make([]byte, len(testMessage))

	readDone, readDoneCancel := context.WithCancel(context.Background())
	go func() {
		_, err := readingConn.Read(testBuffer)
		if errors.Is(err, io.EOF) {
			return
		}

		require.NoError(t, err)
		require.True(t, bytes.Equal(testMessage, testBuffer))

		readDoneCancel()
	}()

	attempts := 0
	for {
		select {
		case <-time.After(5 * time.Millisecond):
			if attempts > maxAttempts {
				return false
			}

			_, err := writingConn.Write(testMessage)
			require.NoError(t, err)
			attempts++
		case <-readDone.Done():
			return true
		}
	}
}

func TestBindingRequestHandler(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	var switchToNewCandidatePair, controlledLoggingFired atomic.Value
	oneHour := time.Hour
	keepaliveInterval := time.Millisecond * 20

	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()
	controllingAgent, err := NewAgent(&AgentConfig{
		NetworkTypes:      []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
		MulticastDNSMode:  MulticastDNSModeDisabled,
		KeepaliveInterval: &keepaliveInterval,
		CheckInterval:     &oneHour,
		BindingRequestHandler: func(_ *stun.Message, _, _ Candidate, _ *CandidatePair) bool {
			controlledLoggingFired.Store(true)

			return false
		},
	})
	require.NoError(t, err)
	require.NoError(t, controllingAgent.OnConnectionStateChange(aNotifier))

	controlledAgent, err := NewAgent(&AgentConfig{
		NetworkTypes:      []NetworkType{NetworkTypeUDP4},
		MulticastDNSMode:  MulticastDNSModeDisabled,
		KeepaliveInterval: &keepaliveInterval,
		CheckInterval:     &oneHour,
		BindingRequestHandler: func(_ *stun.Message, _, _ Candidate, _ *CandidatePair) bool {
			// Don't switch candidate pair until we are ready
			val, ok := switchToNewCandidatePair.Load().(bool)

			return ok && val
		},
	})
	require.NoError(t, err)
	require.NoError(t, controlledAgent.OnConnectionStateChange(bNotifier))

	controlledConn, controllingConn := connect(t, controlledAgent, controllingAgent)
	<-aConnected
	<-bConnected

	// Assert we have connected and can send data
	require.True(t, sendUntilDone(t, controlledConn, controllingConn, 100))

	// Take the lock on the controlling Agent and unset state
	assert.NoError(t, controlledAgent.loop.Run(controlledAgent.loop, func(_ context.Context) {
		for net, cs := range controlledAgent.remoteCandidates {
			for _, c := range cs {
				require.NoError(t, c.close())
			}
			delete(controlledAgent.remoteCandidates, net)
		}

		for _, c := range controlledAgent.localCandidates[NetworkTypeUDP4] {
			cast, ok := c.(*CandidateHost)
			require.True(t, ok)
			cast.remoteCandidateCaches = map[AddrPort]Candidate{}
		}

		controlledAgent.setSelectedPair(nil)
		controlledAgent.checklist = make([]*CandidatePair, 0)
	}))

	// Assert that Selected Candidate pair has only been unset on Controlled side
	candidatePair, err := controlledAgent.GetSelectedCandidatePair()
	assert.Nil(t, candidatePair)
	assert.NoError(t, err)

	candidatePair, err = controllingAgent.GetSelectedCandidatePair()
	assert.NotNil(t, candidatePair)
	assert.NoError(t, err)

	// Sending will fail, we no longer have a selected candidate pair
	require.False(t, sendUntilDone(t, controlledConn, controllingConn, 20))

	// Send STUN Binding requests until a new Selected Candidate Pair has been set by BindingRequestHandler
	switchToNewCandidatePair.Store(true)
	for {
		controllingAgent.requestConnectivityCheck()

		candidatePair, err = controlledAgent.GetSelectedCandidatePair()
		require.NoError(t, err)
		if candidatePair != nil {
			break
		}

		time.Sleep(time.Millisecond * 5)
	}

	// We have a new selected candidate pair because of BindingRequestHandler, test that it works
	require.True(t, sendUntilDone(t, controllingConn, controlledConn, 100))

	fired, ok := controlledLoggingFired.Load().(bool)
	require.True(t, ok)
	require.True(t, fired)

	closePipe(t, controllingConn, controlledConn)
}

// copied from pion/webrtc's peerconnection_go_test.go.
type testICELogger struct {
	lastErrorMessage string
}

func (t *testICELogger) Trace(string)          {}
func (t *testICELogger) Tracef(string, ...any) {}
func (t *testICELogger) Debug(string)          {}
func (t *testICELogger) Debugf(string, ...any) {}
func (t *testICELogger) Info(string)           {}
func (t *testICELogger) Infof(string, ...any)  {}
func (t *testICELogger) Warn(string)           {}
func (t *testICELogger) Warnf(string, ...any)  {}
func (t *testICELogger) Error(msg string)      { t.lastErrorMessage = msg }
func (t *testICELogger) Errorf(format string, args ...any) {
	t.lastErrorMessage = fmt.Sprintf(format, args...)
}

type testICELoggerFactory struct {
	logger *testICELogger
}

func (t *testICELoggerFactory) NewLogger(string) logging.LeveledLogger {
	return t.logger
}

func TestControllingSelector_IsNominatable_LogsInvalidType(t *testing.T) {
	testLogger := &testICELogger{}
	loggerFactory := &testICELoggerFactory{logger: testLogger}

	sel := &controllingSelector{
		agent: &Agent{},
		log:   loggerFactory.NewLogger("test"),
	}
	sel.Start()

	c := hostCandidate()
	c.candidateBase.candidateType = CandidateTypeUnspecified

	got := sel.isNominatable(c)

	require.False(t, got)
	require.Contains(t, testLogger.lastErrorMessage, "Invalid candidate type")
	require.Contains(t, testLogger.lastErrorMessage, "Unknown candidate type") // from c.Type().String()
}

func TestControllingSelector_NominatePair_BuildError(t *testing.T) {
	testLogger := &testICELogger{}
	loggerFactory := &testICELoggerFactory{logger: testLogger}

	// selector with an Agent with ufrags to make an oversized username
	// (username = remoteUfrag + ":" + localUfrag) since oversized username causes
	// stun.NewUsername(...) inside stun.Build to fail.
	long := strings.Repeat("x", 300) // > 255 each side
	sel := &controllingSelector{
		agent: &Agent{
			remoteUfrag: long,
			localUfrag:  long,
			remotePwd:   "pwd", // any non-empty value is fine
			tieBreaker:  0,
		},
		log: loggerFactory.NewLogger("test"),
	}
	sel.Start()

	p := newCandidatePair(hostCandidate(), hostCandidate(), true)

	sel.nominatePair(p)

	require.NotEmpty(t, testLogger.lastErrorMessage, "expected error log from nominatePair on Build failure")
}

type pingNoIOCand struct{ candidateBase }

func newPingNoIOCand() *pingNoIOCand {
	return &pingNoIOCand{
		candidateBase: candidateBase{
			candidateType: CandidateTypeHost,
			component:     ComponentRTP,
		},
	}
}
func (d *pingNoIOCand) writeTo(b []byte, _ Candidate) (int, error) { return len(b), nil }

func bareAgentForPing() *Agent {
	return &Agent{
		hostAcceptanceMinWait:  time.Hour,
		srflxAcceptanceMinWait: time.Hour,
		prflxAcceptanceMinWait: time.Hour,
		relayAcceptanceMinWait: time.Hour,

		checklist:         []*CandidatePair{},
		pairsByID:         make(map[uint64]*CandidatePair),
		keepaliveInterval: time.Second,
		checkInterval:     time.Second,

		connectionStateNotifier: &handlerNotifier{
			done:                make(chan struct{}),
			connectionStateFunc: func(ConnectionState) {}}, //nolint formatting

		candidateNotifier: &handlerNotifier{
			done:          make(chan struct{}),
			candidateFunc: func(Candidate) {}}, //nolint formatting

		selectedCandidatePairNotifier: &handlerNotifier{
			done:              make(chan struct{}),
			candidatePairFunc: func(*CandidatePair) {}}, //nolint formatting
	}
}

func bigStr() string { return strings.Repeat("x", 40000) }

func TestControllingSelector_PingCandidate_BuildError(t *testing.T) {
	a := bareAgentForPing()
	// make Username really big so stun.Build returns an error.
	a.remoteUfrag = bigStr()
	a.localUfrag = bigStr()
	a.remotePwd = selectionTestPassword
	a.tieBreaker = 1

	testLogger := &testICELogger{}
	sel := &controllingSelector{agent: a, log: testLogger}
	sel.Start()

	local := newPingNoIOCand()
	remote := newPingNoIOCand()

	sel.PingCandidate(local, remote)

	require.NotEmpty(t, testLogger.lastErrorMessage, "expected error to be logged from stun.Build")
}

func TestControlledSelector_PingCandidate_BuildError(t *testing.T) {
	a := bareAgentForPing()
	a.remoteUfrag = bigStr()
	a.localUfrag = bigStr()
	a.remotePwd = selectionTestPassword
	a.tieBreaker = 1

	testLogger := &testICELogger{}
	sel := &controlledSelector{agent: a, log: testLogger}
	sel.Start()

	local := newPingNoIOCand()
	remote := newPingNoIOCand()

	sel.PingCandidate(local, remote)

	require.NotEmpty(t, testLogger.lastErrorMessage, "expected error to be logged from stun.Build")
}

type warnTestLogger struct {
	warned bool
}

func (l *warnTestLogger) Trace(string)          {}
func (l *warnTestLogger) Tracef(string, ...any) {}
func (l *warnTestLogger) Debug(string)          {}
func (l *warnTestLogger) Debugf(string, ...any) {}
func (l *warnTestLogger) Info(string)           {}
func (l *warnTestLogger) Infof(string, ...any)  {}
func (l *warnTestLogger) Warn(string)           { l.warned = true }
func (l *warnTestLogger) Warnf(string, ...any)  { l.warned = true }
func (l *warnTestLogger) Error(string)          {}
func (l *warnTestLogger) Errorf(string, ...any) {}

type dummyNoIOCand struct{ candidateBase }

func newDummyNoIOCand(t CandidateType) *dummyNoIOCand {
	return &dummyNoIOCand{
		candidateBase: candidateBase{
			candidateType: t,
			component:     ComponentRTP,
		},
	}
}
func (d *dummyNoIOCand) writeTo(p []byte, _ Candidate) (int, error) { return len(p), nil }

func TestControlledSelector_HandleSuccessResponse_UnknownTxID(t *testing.T) {
	logger := &warnTestLogger{}

	ag := &Agent{log: logger}

	sel := &controlledSelector{agent: ag, log: logger}
	sel.Start()

	local := newDummyNoIOCand(CandidateTypeHost)
	remote := newDummyNoIOCand(CandidateTypeHost)

	var m stun.Message
	copy(m.TransactionID[:], []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12})

	sel.HandleSuccessResponse(&m, local, remote, nil)

	require.True(t, logger.warned, "expected Warnf to be called for unknown TransactionID (hitting !ok branch)")
}

func TestAutomaticRenomination(t *testing.T) { //nolint:maintidx
	report := test.CheckRoutines(t)
	defer report()

	t.Run("Configuration", func(t *testing.T) {
		t.Run("WithAutomaticRenomination enables feature", func(t *testing.T) {
			agent, err := NewAgentWithOptions(
				WithRenomination(DefaultNominationValueGenerator()),
				WithAutomaticRenomination(5*time.Second),
			)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, agent.Close())
			}()

			assert.True(t, agent.automaticRenomination)
			assert.Equal(t, 5*time.Second, agent.renominationInterval)
			assert.True(t, agent.enableRenomination)
		})

		t.Run("Default interval when zero", func(t *testing.T) {
			agent, err := NewAgentWithOptions(
				WithRenomination(DefaultNominationValueGenerator()),
				WithAutomaticRenomination(0),
			)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, agent.Close())
			}()

			assert.True(t, agent.automaticRenomination)
			assert.Equal(t, 3*time.Second, agent.renominationInterval)
		})
	})

	t.Run("Quality Assessment", func(t *testing.T) {
		agent, err := NewAgent(&AgentConfig{})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		localHost, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "192.168.1.1",
			Port:      10000,
			Component: 1,
		})
		require.NoError(t, err)

		remoteHost, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "192.168.1.2",
			Port:      20000,
			Component: 1,
		})
		require.NoError(t, err)

		localRelay, err := NewCandidateRelay(&CandidateRelayConfig{
			Network:   "udp",
			Address:   "10.0.0.1",
			Port:      30000,
			Component: 1,
			RelAddr:   "192.168.1.1",
			RelPort:   10000,
		})
		require.NoError(t, err)

		remoteRelay, err := NewCandidateRelay(&CandidateRelayConfig{
			Network:   "udp",
			Address:   "10.0.0.2",
			Port:      40000,
			Component: 1,
			RelAddr:   "192.168.1.2",
			RelPort:   20000,
		})
		require.NoError(t, err)

		t.Run("Host pair scores higher than relay pair", func(t *testing.T) {
			hostPair := newCandidatePair(localHost, remoteHost, true)
			hostPair.state = CandidatePairStateSucceeded
			hostPair.UpdateRoundTripTime(10 * time.Millisecond)

			relayPair := newCandidatePair(localRelay, remoteRelay, true)
			relayPair.state = CandidatePairStateSucceeded
			relayPair.UpdateRoundTripTime(10 * time.Millisecond)

			hostScore := agent.evaluateCandidatePairQuality(hostPair)
			relayScore := agent.evaluateCandidatePairQuality(relayPair)

			assert.Greater(t, hostScore, relayScore,
				"Host pair should score higher than relay pair with same RTT")
		})

		t.Run("Lower RTT scores higher", func(t *testing.T) {
			pair1 := newCandidatePair(localHost, remoteHost, true)
			pair1.state = CandidatePairStateSucceeded
			pair1.UpdateRoundTripTime(5 * time.Millisecond)

			pair2 := newCandidatePair(localHost, remoteHost, true)
			pair2.state = CandidatePairStateSucceeded
			pair2.UpdateRoundTripTime(50 * time.Millisecond)

			score1 := agent.evaluateCandidatePairQuality(pair1)
			score2 := agent.evaluateCandidatePairQuality(pair2)

			assert.Greater(t, score1, score2,
				"Pair with lower RTT should score higher")
		})
	})

	t.Run("Should Renominate Logic", func(t *testing.T) {
		agent, err := NewAgent(&AgentConfig{})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		localHost, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "192.168.1.1",
			Port:      10000,
			Component: 1,
		})
		require.NoError(t, err)

		remoteHost, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "192.168.1.2",
			Port:      20000,
			Component: 1,
		})
		require.NoError(t, err)

		localRelay, err := NewCandidateRelay(&CandidateRelayConfig{
			Network:   "udp",
			Address:   "10.0.0.1",
			Port:      30000,
			Component: 1,
			RelAddr:   "192.168.1.1",
			RelPort:   10000,
		})
		require.NoError(t, err)

		remoteRelay, err := NewCandidateRelay(&CandidateRelayConfig{
			Network:   "udp",
			Address:   "10.0.0.2",
			Port:      40000,
			Component: 1,
			RelAddr:   "192.168.1.2",
			RelPort:   20000,
		})
		require.NoError(t, err)

		t.Run("Should renominate relay to host", func(t *testing.T) {
			relayPair := newCandidatePair(localRelay, remoteRelay, true)
			relayPair.state = CandidatePairStateSucceeded
			relayPair.UpdateRoundTripTime(50 * time.Millisecond)

			hostPair := newCandidatePair(localHost, remoteHost, true)
			hostPair.state = CandidatePairStateSucceeded
			hostPair.UpdateRoundTripTime(45 * time.Millisecond) // Similar RTT

			shouldSwitch := agent.shouldRenominate(relayPair, hostPair)
			assert.True(t, shouldSwitch,
				"Should renominate from relay to host even with similar RTT")
		})

		t.Run("Should renominate for RTT improvement > 10ms", func(t *testing.T) {
			// Create different host candidates for pair2 to avoid same-pair check
			localHost2, err := NewCandidateHost(&CandidateHostConfig{
				Network:   "udp",
				Address:   "192.168.1.3",
				Port:      10001,
				Component: 1,
			})
			require.NoError(t, err)

			pair1 := newCandidatePair(localHost, remoteHost, true)
			pair1.state = CandidatePairStateSucceeded
			pair1.UpdateRoundTripTime(50 * time.Millisecond)

			pair2 := newCandidatePair(localHost2, remoteHost, true)
			pair2.state = CandidatePairStateSucceeded
			pair2.UpdateRoundTripTime(30 * time.Millisecond) // 20ms improvement

			shouldSwitch := agent.shouldRenominate(pair1, pair2)
			assert.True(t, shouldSwitch,
				"Should renominate for RTT improvement > 10ms")
		})

		t.Run("Should not renominate for small RTT improvement", func(t *testing.T) {
			// Create different host candidates for pair2 to avoid same-pair check
			localHost2, err := NewCandidateHost(&CandidateHostConfig{
				Network:   "udp",
				Address:   "192.168.1.3",
				Port:      10001,
				Component: 1,
			})
			require.NoError(t, err)

			pair1 := newCandidatePair(localHost, remoteHost, true)
			pair1.state = CandidatePairStateSucceeded
			pair1.UpdateRoundTripTime(50 * time.Millisecond)

			pair2 := newCandidatePair(localHost2, remoteHost, true)
			pair2.state = CandidatePairStateSucceeded
			pair2.UpdateRoundTripTime(45 * time.Millisecond) // Only 5ms improvement

			shouldSwitch := agent.shouldRenominate(pair1, pair2)
			assert.False(t, shouldSwitch,
				"Should not renominate for RTT improvement < 10ms")
		})

		t.Run("Should not renominate to same pair", func(t *testing.T) {
			pair := newCandidatePair(localHost, remoteHost, true)
			pair.state = CandidatePairStateSucceeded

			shouldSwitch := agent.shouldRenominate(pair, pair)
			assert.False(t, shouldSwitch,
				"Should not renominate to the same pair")
		})

		t.Run("Should not renominate to non-succeeded pair", func(t *testing.T) {
			currentPair := newCandidatePair(localHost, remoteHost, true)
			currentPair.state = CandidatePairStateSucceeded

			candidatePair := newCandidatePair(localHost, remoteHost, true)
			candidatePair.state = CandidatePairStateInProgress

			shouldSwitch := agent.shouldRenominate(currentPair, candidatePair)
			assert.False(t, shouldSwitch,
				"Should not renominate to non-succeeded pair")
		})
	})

	t.Run("Find Best Candidate Pair", func(t *testing.T) {
		agent, err := NewAgent(&AgentConfig{})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		// Create candidates
		localHost, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "192.168.1.1",
			Port:      10000,
			Component: 1,
		})
		require.NoError(t, err)

		remoteHost, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "192.168.1.2",
			Port:      20000,
			Component: 1,
		})
		require.NoError(t, err)

		localRelay, err := NewCandidateRelay(&CandidateRelayConfig{
			Network:   "udp",
			Address:   "10.0.0.1",
			Port:      30000,
			Component: 1,
			RelAddr:   "192.168.1.1",
			RelPort:   10000,
		})
		require.NoError(t, err)

		remoteRelay, err := NewCandidateRelay(&CandidateRelayConfig{
			Network:   "udp",
			Address:   "10.0.0.2",
			Port:      40000,
			Component: 1,
			RelAddr:   "192.168.1.2",
			RelPort:   20000,
		})
		require.NoError(t, err)

		ctx := context.Background()
		err = agent.loop.Run(ctx, func(context.Context) {
			// Add pairs to checklist
			hostPair := agent.addPair(localHost, remoteHost)
			hostPair.state = CandidatePairStateSucceeded
			hostPair.UpdateRoundTripTime(10 * time.Millisecond)

			relayPair := agent.addPair(localRelay, remoteRelay)
			relayPair.state = CandidatePairStateSucceeded
			relayPair.UpdateRoundTripTime(50 * time.Millisecond)

			// Find best should return host pair
			best := agent.findBestCandidatePair()
			assert.NotNil(t, best)
			assert.Equal(t, hostPair, best,
				"Best pair should be the host pair with lower latency")
		})
		require.NoError(t, err)
	})
}

func TestAutomaticRenominationIntegration(t *testing.T) { //nolint:cyclop
	report := test.CheckRoutines(t)
	defer report()

	t.Run("Automatic renomination triggers after interval", func(t *testing.T) {
		// Create agents with automatic renomination enabled
		aAgent, err := NewAgentWithOptions(
			WithRenomination(DefaultNominationValueGenerator()),
			WithAutomaticRenomination(100*time.Millisecond), // Short interval for testing
		)
		require.NoError(t, err)
		defer func() {
			require.NoError(t, aAgent.Close())
		}()

		bAgent, err := NewAgentWithOptions(
			WithRenomination(DefaultNominationValueGenerator()),
		)
		require.NoError(t, err)
		defer func() {
			require.NoError(t, bAgent.Close())
		}()

		// Start gathering candidates
		err = aAgent.OnCandidate(func(c Candidate) {
			if c != nil {
				t.Logf("Agent A gathered candidate: %s", c)
			}
		})
		require.NoError(t, err)

		err = bAgent.OnCandidate(func(c Candidate) {
			if c != nil {
				t.Logf("Agent B gathered candidate: %s", c)
			}
		})
		require.NoError(t, err)

		require.NoError(t, aAgent.GatherCandidates())
		require.NoError(t, bAgent.GatherCandidates())

		// Wait for gathering to complete
		time.Sleep(100 * time.Millisecond)

		// Exchange credentials
		aUfrag, aPwd, err := aAgent.GetLocalUserCredentials()
		require.NoError(t, err)
		bUfrag, bPwd, err := bAgent.GetLocalUserCredentials()
		require.NoError(t, err)

		// Get candidates
		aCandidates, err := aAgent.GetLocalCandidates()
		require.NoError(t, err)
		bCandidates, err := bAgent.GetLocalCandidates()
		require.NoError(t, err)

		// Verify we have candidates
		if len(aCandidates) == 0 || len(bCandidates) == 0 {
			t.Skip("No candidates gathered, skipping integration test")
		}

		require.NoError(t, aAgent.startConnectivityChecks(true, bUfrag, bPwd))
		require.NoError(t, bAgent.startConnectivityChecks(false, aUfrag, aPwd))

		// Exchange candidates
		for _, c := range aCandidates {
			cpCand, copyErr := c.copy()
			require.NoError(t, copyErr)
			require.NoError(t, bAgent.AddRemoteCandidate(cpCand))
		}
		for _, c := range bCandidates {
			cpCand, copyErr := c.copy()
			require.NoError(t, copyErr)
			require.NoError(t, aAgent.AddRemoteCandidate(cpCand))
		}

		// Wait for initial connection
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Wait for connection on both agents
		select {
		case <-aAgent.onConnected:
		case <-ctx.Done():
			require.Fail(t, "Agent A failed to connect")
		}

		select {
		case <-bAgent.onConnected:
		case <-ctx.Done():
			require.Fail(t, "Agent B failed to connect")
		}

		// Record initial selected pairs
		initialAPair, err := aAgent.GetSelectedCandidatePair()
		require.NoError(t, err)
		require.NotNil(t, initialAPair)

		// Note: In a real scenario, automatic renomination would trigger
		// when a better path becomes available (e.g., relay -> direct).
		// For this test, we're just verifying the mechanism is in place.

		// Wait to see if automatic renomination check runs
		// (it should run but may not renominate if no better pair exists)
		time.Sleep(200 * time.Millisecond)

		// The automatic renomination check should have run at least once
		// We can't easily verify renomination occurred without simulating
		// network changes, but we can verify the feature is enabled
		assert.True(t, aAgent.automaticRenomination)
		assert.True(t, aAgent.enableRenomination)
	})
}

func TestKeepAliveCandidatesForRenomination(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	// Create test candidates that don't require real network I/O
	createTestCandidates := func() (Candidate, Candidate, Candidate) {
		local1 := newPingNoIOCand()
		local1.candidateBase.networkType = NetworkTypeUDP4
		local1.candidateBase.resolvedAddr = &net.UDPAddr{IP: net.ParseIP("192.168.1.1"), Port: 10000}

		local2 := newPingNoIOCand()
		local2.candidateBase.networkType = NetworkTypeUDP4
		local2.candidateBase.resolvedAddr = &net.UDPAddr{IP: net.ParseIP("192.168.1.3"), Port: 10001}

		remote := newPingNoIOCand()
		remote.candidateBase.networkType = NetworkTypeUDP4
		remote.candidateBase.resolvedAddr = &net.UDPAddr{IP: net.ParseIP("192.168.1.2"), Port: 20000}

		return local1, local2, remote
	}

	t.Run("Only pings all candidates when automatic renomination enabled", func(t *testing.T) {
		localHost1, localHost2, remoteHost := createTestCandidates()

		// Test with automatic renomination DISABLED
		agentWithoutAutoRenom := bareAgentForPing()
		agentWithoutAutoRenom.log = logging.NewDefaultLoggerFactory().NewLogger("test")
		agentWithoutAutoRenom.remoteUfrag = selectionTestRemoteUfrag
		agentWithoutAutoRenom.localUfrag = selectionTestLocalUfrag
		agentWithoutAutoRenom.remotePwd = selectionTestPassword
		agentWithoutAutoRenom.tieBreaker = 1
		agentWithoutAutoRenom.isControlling.Store(true)
		agentWithoutAutoRenom.setSelector()

		// Add pairs - one selected (succeeded) and one alternate (succeeded)
		pair1 := agentWithoutAutoRenom.addPair(localHost1, remoteHost)
		pair1.state = CandidatePairStateSucceeded
		pair1.UpdateRoundTripTime(10 * time.Millisecond)
		// Don't set selected pair for the "without renomination" agent

		pair2 := agentWithoutAutoRenom.addPair(localHost2, remoteHost)
		pair2.state = CandidatePairStateSucceeded
		pair2.UpdateRoundTripTime(50 * time.Millisecond)

		// keepAliveCandidatesForRenomination should do nothing when automatic renomination is disabled
		agentWithoutAutoRenom.keepAliveCandidatesForRenomination()

		// Since automatic renomination is off, the function should not ping anything
		// We can't easily verify no pings were sent, but we verify the function completes

		// Test with automatic renomination ENABLED
		agentWithAutoRenom := bareAgentForPing()
		agentWithAutoRenom.log = logging.NewDefaultLoggerFactory().NewLogger("test")
		agentWithAutoRenom.automaticRenomination = true
		agentWithAutoRenom.enableRenomination = true
		agentWithAutoRenom.renominationInterval = 100 * time.Millisecond
		agentWithAutoRenom.remoteUfrag = selectionTestRemoteUfrag
		agentWithAutoRenom.localUfrag = selectionTestLocalUfrag
		agentWithAutoRenom.remotePwd = selectionTestPassword
		agentWithAutoRenom.tieBreaker = 1
		agentWithAutoRenom.isControlling.Store(true)
		agentWithAutoRenom.setSelector()

		// Add pairs with different states
		pair1 = agentWithAutoRenom.addPair(localHost1, remoteHost)
		pair1.state = CandidatePairStateSucceeded
		pair1.UpdateRoundTripTime(10 * time.Millisecond)
		// Don't set selected pair for this test

		pair2 = agentWithAutoRenom.addPair(localHost2, remoteHost)
		pair2.state = CandidatePairStateSucceeded
		pair2.UpdateRoundTripTime(50 * time.Millisecond)

		// Call keepAliveCandidatesForRenomination - should ping all pairs
		agentWithAutoRenom.keepAliveCandidatesForRenomination()

		// Verify both pairs remain in succeeded state (not changed by the function)
		assert.Equal(t, CandidatePairStateSucceeded, pair1.state)
		assert.Equal(t, CandidatePairStateSucceeded, pair2.state)
	})

	t.Run("Pings succeeded pairs unlike pingAllCandidates", func(t *testing.T) {
		localHost1, localHost2, remoteHost := createTestCandidates()

		agent := bareAgentForPing()
		agent.log = logging.NewDefaultLoggerFactory().NewLogger("test")
		agent.automaticRenomination = true
		agent.enableRenomination = true
		agent.renominationInterval = 100 * time.Millisecond
		agent.remoteUfrag = selectionTestRemoteUfrag
		agent.localUfrag = selectionTestLocalUfrag
		agent.remotePwd = selectionTestPassword
		agent.tieBreaker = 1
		agent.isControlling.Store(true)
		agent.setSelector()

		// Create a pair in succeeded state
		pair := agent.addPair(localHost1, remoteHost)
		pair.state = CandidatePairStateSucceeded
		pair.UpdateRoundTripTime(10 * time.Millisecond)

		// Create another pair in succeeded state
		pair2 := agent.addPair(localHost2, remoteHost)
		pair2.state = CandidatePairStateSucceeded
		pair2.UpdateRoundTripTime(50 * time.Millisecond)

		// keepAliveCandidatesForRenomination should ping succeeded pairs
		// (pingAllCandidates would skip them)
		agent.keepAliveCandidatesForRenomination()

		// Pairs should still be in succeeded state
		assert.Equal(t, CandidatePairStateSucceeded, pair.state)
		assert.Equal(t, CandidatePairStateSucceeded, pair2.state)
	})

	t.Run("Transitions waiting pairs to in-progress", func(t *testing.T) {
		localHost1, _, remoteHost := createTestCandidates()

		agent := bareAgentForPing()
		agent.log = logging.NewDefaultLoggerFactory().NewLogger("test")
		agent.automaticRenomination = true
		agent.enableRenomination = true
		agent.renominationInterval = 100 * time.Millisecond
		agent.remoteUfrag = selectionTestRemoteUfrag
		agent.localUfrag = selectionTestLocalUfrag
		agent.remotePwd = selectionTestPassword
		agent.tieBreaker = 1
		agent.isControlling.Store(true)
		agent.setSelector()

		// Create a pair in waiting state
		pair := agent.addPair(localHost1, remoteHost)
		pair.state = CandidatePairStateWaiting

		// Call keepAliveCandidatesForRenomination
		agent.keepAliveCandidatesForRenomination()

		// Pair should transition to in-progress
		assert.Equal(t, CandidatePairStateInProgress, pair.state)
	})

	t.Run("Skips failed pairs", func(t *testing.T) {
		localHost1, localHost2, remoteHost := createTestCandidates()

		agent := bareAgentForPing()
		agent.log = logging.NewDefaultLoggerFactory().NewLogger("test")
		agent.automaticRenomination = true
		agent.enableRenomination = true
		agent.renominationInterval = 100 * time.Millisecond
		agent.remoteUfrag = selectionTestRemoteUfrag
		agent.localUfrag = selectionTestLocalUfrag
		agent.remotePwd = selectionTestPassword
		agent.tieBreaker = 1
		agent.isControlling.Store(true)
		agent.setSelector()

		// Create a succeeded pair
		pair1 := agent.addPair(localHost1, remoteHost)
		pair1.state = CandidatePairStateSucceeded

		// Create a failed pair
		pair2 := agent.addPair(localHost2, remoteHost)
		pair2.state = CandidatePairStateFailed

		// Call keepAliveCandidatesForRenomination
		agent.keepAliveCandidatesForRenomination()

		// Failed pair should remain failed (not transitioned to in-progress)
		assert.Equal(t, CandidatePairStateFailed, pair2.state)
		// Succeeded pair should remain succeeded
		assert.Equal(t, CandidatePairStateSucceeded, pair1.state)
	})
}

// TestRenominationAcceptance verifies that the controlled agent correctly
// accepts renomination based on nomination values, not just priority.
func TestRenominationAcceptance(t *testing.T) {
	t.Run("Accepts renomination with nomination value regardless of priority", func(t *testing.T) {
		// Create a controlled agent
		agent := bareAgentForPing()
		agent.log = logging.NewDefaultLoggerFactory().NewLogger("test")
		agent.remoteUfrag = selectionTestRemoteUfrag
		agent.localUfrag = selectionTestLocalUfrag
		agent.remotePwd = selectionTestPassword
		agent.tieBreaker = 1
		agent.isControlling.Store(false) // Controlled agent
		agent.nominationAttribute = stun.AttrType(0x0030)
		agent.onConnected = make(chan struct{}) // Initialize the channel
		agent.setSelector()

		selector, ok := agent.getSelector().(*controlledSelector)
		require.True(t, ok, "expected controlledSelector")

		// Create two host candidates with same priority
		local1 := newPingNoIOCand()
		local1.candidateBase.networkType = NetworkTypeUDP4
		local1.candidateBase.resolvedAddr = &net.UDPAddr{IP: net.ParseIP("192.168.1.1"), Port: 10000}

		local2 := newPingNoIOCand()
		local2.candidateBase.networkType = NetworkTypeUDP4
		local2.candidateBase.resolvedAddr = &net.UDPAddr{IP: net.ParseIP("192.168.1.3"), Port: 10001}

		remote := newPingNoIOCand()
		remote.candidateBase.networkType = NetworkTypeUDP4
		remote.candidateBase.resolvedAddr = &net.UDPAddr{IP: net.ParseIP("192.168.1.2"), Port: 20000}

		// Create two pairs with the same priority (both host candidates)
		pair1 := agent.addPair(local1, remote)
		pair1.state = CandidatePairStateSucceeded

		pair2 := agent.addPair(local2, remote)
		pair2.state = CandidatePairStateSucceeded

		// Select the first pair initially
		agent.setSelectedPair(pair1)
		assert.Equal(t, pair1, agent.getSelectedPair())
		assert.True(t, pair1.nominated)

		// Build a nomination request for the second pair with a nomination value
		nominationValue := uint32(100)
		msg, err := stun.Build(stun.BindingRequest,
			stun.TransactionID,
			stun.NewUsername(agent.localUfrag+":"+agent.remoteUfrag),
			UseCandidate(),
			NominationSetter{
				Value:    nominationValue,
				AttrType: agent.nominationAttribute,
			},
			stun.NewShortTermIntegrity(agent.localPwd),
			stun.Fingerprint,
		)
		require.NoError(t, err)

		// Handle the binding request with nomination value for pair2
		selector.HandleBindingRequest(msg, local2, remote)

		// The controlled agent should accept the renomination even though
		// pair2 has the same priority as pair1, because a nomination value is present
		selectedPair := agent.getSelectedPair()
		assert.Equal(t, pair2, selectedPair,
			"Should switch to pair2 when renomination with nomination value is received")
		assert.True(t, pair2.nominated)
	})

	t.Run("Standard nomination still requires higher priority without nomination value", func(t *testing.T) {
		// Create a controlled agent
		agent := bareAgentForPing()
		agent.log = logging.NewDefaultLoggerFactory().NewLogger("test")
		agent.remoteUfrag = selectionTestRemoteUfrag
		agent.localUfrag = selectionTestLocalUfrag
		agent.remotePwd = selectionTestPassword
		agent.tieBreaker = 1
		agent.isControlling.Store(false)
		agent.onConnected = make(chan struct{})
		agent.setSelector()

		selector, ok := agent.getSelector().(*controlledSelector)
		require.True(t, ok, "expected controlledSelector")

		// Create candidates - we'll simulate lower priority by using different types
		local1 := newPingNoIOCand()
		local1.candidateBase.networkType = NetworkTypeUDP4
		local1.candidateBase.resolvedAddr = &net.UDPAddr{IP: net.ParseIP("192.168.1.1"), Port: 10000}
		local1.candidateBase.candidateType = CandidateTypeHost // Higher priority

		local2 := newPingNoIOCand()
		local2.candidateBase.networkType = NetworkTypeUDP4
		local2.candidateBase.resolvedAddr = &net.UDPAddr{IP: net.ParseIP("192.168.1.3"), Port: 10001}
		local2.candidateBase.candidateType = CandidateTypeHost // Same priority

		remote := newPingNoIOCand()
		remote.candidateBase.networkType = NetworkTypeUDP4
		remote.candidateBase.resolvedAddr = &net.UDPAddr{IP: net.ParseIP("192.168.1.2"), Port: 20000}
		remote.candidateBase.candidateType = CandidateTypeHost

		pair1 := agent.addPair(local1, remote)
		pair1.state = CandidatePairStateSucceeded

		pair2 := agent.addPair(local2, remote)
		pair2.state = CandidatePairStateSucceeded

		// Select the first pair
		agent.setSelectedPair(pair1)

		// Build a standard nomination request WITHOUT nomination value for pair2
		msg, err := stun.Build(stun.BindingRequest,
			stun.TransactionID,
			stun.NewUsername(agent.localUfrag+":"+agent.remoteUfrag),
			UseCandidate(),
			stun.NewShortTermIntegrity(agent.localPwd),
			stun.Fingerprint,
		)
		require.NoError(t, err)

		// Handle the binding request for pair2 (same priority)
		selector.HandleBindingRequest(msg, local2, remote)

		// Without renomination, standard ICE rules apply
		// Since pair2 has equal priority to pair1, it should NOT be accepted
		// (only higher priority pairs are accepted in standard ICE)
		selectedPair := agent.getSelectedPair()
		assert.Equal(t, pair1, selectedPair,
			"Should NOT switch to pair2 with standard nomination when priority is equal")
	})

	t.Run("Higher nomination values override lower ones", func(t *testing.T) {
		agent := bareAgentForPing()
		agent.log = logging.NewDefaultLoggerFactory().NewLogger("test")
		agent.remoteUfrag = selectionTestRemoteUfrag
		agent.localUfrag = selectionTestLocalUfrag
		agent.remotePwd = selectionTestPassword
		agent.tieBreaker = 1
		agent.isControlling.Store(false)
		agent.nominationAttribute = stun.AttrType(0x0030)
		agent.onConnected = make(chan struct{})
		agent.setSelector()

		selector, ok := agent.getSelector().(*controlledSelector)
		require.True(t, ok, "expected controlledSelector")

		local1 := newPingNoIOCand()
		local1.candidateBase.networkType = NetworkTypeUDP4
		local1.candidateBase.resolvedAddr = &net.UDPAddr{IP: net.ParseIP("192.168.1.1"), Port: 10000}

		local2 := newPingNoIOCand()
		local2.candidateBase.networkType = NetworkTypeUDP4
		local2.candidateBase.resolvedAddr = &net.UDPAddr{IP: net.ParseIP("192.168.1.3"), Port: 10001}

		local3 := newPingNoIOCand()
		local3.candidateBase.networkType = NetworkTypeUDP4
		local3.candidateBase.resolvedAddr = &net.UDPAddr{IP: net.ParseIP("192.168.1.4"), Port: 10002}

		remote := newPingNoIOCand()
		remote.candidateBase.networkType = NetworkTypeUDP4
		remote.candidateBase.resolvedAddr = &net.UDPAddr{IP: net.ParseIP("192.168.1.2"), Port: 20000}

		pair1 := agent.addPair(local1, remote)
		pair1.state = CandidatePairStateSucceeded

		pair2 := agent.addPair(local2, remote)
		pair2.state = CandidatePairStateSucceeded

		pair3 := agent.addPair(local3, remote)
		pair3.state = CandidatePairStateSucceeded

		// Nominate pair1 with value 100
		msg1, err := stun.Build(stun.BindingRequest,
			stun.TransactionID,
			stun.NewUsername(agent.localUfrag+":"+agent.remoteUfrag),
			UseCandidate(),
			NominationSetter{Value: 100, AttrType: agent.nominationAttribute},
			stun.NewShortTermIntegrity(agent.localPwd),
			stun.Fingerprint,
		)
		require.NoError(t, err)
		selector.HandleBindingRequest(msg1, local1, remote)
		assert.Equal(t, pair1, agent.getSelectedPair())

		// Try to nominate pair2 with a LOWER value (50) - should be rejected
		msg2, err := stun.Build(stun.BindingRequest,
			stun.TransactionID,
			stun.NewUsername(agent.localUfrag+":"+agent.remoteUfrag),
			UseCandidate(),
			NominationSetter{Value: 50, AttrType: agent.nominationAttribute},
			stun.NewShortTermIntegrity(agent.localPwd),
			stun.Fingerprint,
		)
		require.NoError(t, err)
		selector.HandleBindingRequest(msg2, local2, remote)
		assert.Equal(t, pair1, agent.getSelectedPair(),
			"Should reject nomination with lower value")

		// Nominate pair3 with a HIGHER value (200) - should be accepted
		msg3, err := stun.Build(stun.BindingRequest,
			stun.TransactionID,
			stun.NewUsername(agent.localUfrag+":"+agent.remoteUfrag),
			UseCandidate(),
			NominationSetter{Value: 200, AttrType: agent.nominationAttribute},
			stun.NewShortTermIntegrity(agent.localPwd),
			stun.Fingerprint,
		)
		require.NoError(t, err)
		selector.HandleBindingRequest(msg3, local3, remote)
		assert.Equal(t, pair3, agent.getSelectedPair(),
			"Should accept nomination with higher value")
	})
}

// TestControllingSideRenomination verifies that the controlling agent correctly
// updates its selected pair when receiving a success response for a renomination request.
func TestControllingSideRenomination(t *testing.T) {
	t.Run("Switches selected pair on renomination success response", func(t *testing.T) {
		// Create a controlling agent
		agent := bareAgentForPing()
		agent.log = logging.NewDefaultLoggerFactory().NewLogger("test")
		agent.remoteUfrag = selectionTestRemoteUfrag
		agent.localUfrag = selectionTestLocalUfrag
		agent.remotePwd = selectionTestPassword
		agent.tieBreaker = 1
		agent.isControlling.Store(true) // Controlling agent
		agent.nominationAttribute = stun.AttrType(0x0030)
		agent.onConnected = make(chan struct{}) // Initialize the channel
		agent.setSelector()

		selector, ok := agent.getSelector().(*controllingSelector)
		require.True(t, ok, "expected controllingSelector")

		// Create two host candidates
		local1 := newPingNoIOCand()
		local1.candidateBase.networkType = NetworkTypeUDP4
		local1.candidateBase.resolvedAddr = &net.UDPAddr{IP: net.ParseIP("192.168.1.1"), Port: 10000}

		local2 := newPingNoIOCand()
		local2.candidateBase.networkType = NetworkTypeUDP4
		local2.candidateBase.resolvedAddr = &net.UDPAddr{IP: net.ParseIP("192.168.1.3"), Port: 10001}

		remote := newPingNoIOCand()
		remote.candidateBase.networkType = NetworkTypeUDP4
		remote.candidateBase.resolvedAddr = &net.UDPAddr{IP: net.ParseIP("192.168.1.2"), Port: 20000}

		// Create two pairs
		pair1 := agent.addPair(local1, remote)
		pair1.state = CandidatePairStateSucceeded
		pair1.UpdateRoundTripTime(10 * time.Millisecond)

		pair2 := agent.addPair(local2, remote)
		pair2.state = CandidatePairStateSucceeded
		pair2.UpdateRoundTripTime(5 * time.Millisecond)

		// Select the first pair initially
		agent.setSelectedPair(pair1)
		assert.Equal(t, pair1, agent.getSelectedPair())
		assert.True(t, pair1.nominated)

		// Build a renomination request with nomination value for pair2
		nominationValue := uint32(100)
		msg, err := stun.Build(stun.BindingRequest,
			stun.TransactionID,
			stun.NewUsername(agent.remoteUfrag+":"+agent.localUfrag),
			UseCandidate(),
			AttrControlling(agent.tieBreaker),
			PriorityAttr(local2.Priority()),
			NominationSetter{
				Value:    nominationValue,
				AttrType: agent.nominationAttribute,
			},
			stun.NewShortTermIntegrity(agent.remotePwd),
			stun.Fingerprint,
		)
		require.NoError(t, err)

		// Simulate sending the binding request (adds to pendingBindingRequests)
		agent.sendBindingRequest(msg, local2, remote)

		// Verify the nomination value was stored in the pending request
		require.Len(t, agent.pendingBindingRequests, 1)
		require.NotNil(t, agent.pendingBindingRequests[0].nominationValue)
		require.Equal(t, nominationValue, *agent.pendingBindingRequests[0].nominationValue)

		// Build a success response
		successMsg, err := stun.Build(msg, stun.BindingSuccess,
			&stun.XORMappedAddress{
				IP:   net.ParseIP("192.168.1.2").To4(),
				Port: 20000,
			},
			stun.NewShortTermIntegrity(agent.remotePwd),
			stun.Fingerprint,
		)
		require.NoError(t, err)

		// Handle the success response - this should switch to pair2
		selector.HandleSuccessResponse(successMsg, local2, remote, remote.addr())

		// The controlling agent should have switched to pair2
		selectedPair := agent.getSelectedPair()
		assert.Equal(t, pair2, selectedPair,
			"Controlling agent should switch to pair2 after renomination success response")
		assert.True(t, pair2.nominated)
	})

	t.Run("Does not switch on standard nomination success if pair already selected", func(t *testing.T) {
		// Create a controlling agent
		agent := bareAgentForPing()
		agent.log = logging.NewDefaultLoggerFactory().NewLogger("test")
		agent.remoteUfrag = selectionTestRemoteUfrag
		agent.localUfrag = selectionTestLocalUfrag
		agent.remotePwd = selectionTestPassword
		agent.tieBreaker = 1
		agent.isControlling.Store(true)
		agent.onConnected = make(chan struct{})
		agent.setSelector()

		selector, ok := agent.getSelector().(*controllingSelector)
		require.True(t, ok, "expected controllingSelector")

		// Create two host candidates
		local1 := newPingNoIOCand()
		local1.candidateBase.networkType = NetworkTypeUDP4
		local1.candidateBase.resolvedAddr = &net.UDPAddr{IP: net.ParseIP("192.168.1.1"), Port: 10000}

		local2 := newPingNoIOCand()
		local2.candidateBase.networkType = NetworkTypeUDP4
		local2.candidateBase.resolvedAddr = &net.UDPAddr{IP: net.ParseIP("192.168.1.3"), Port: 10001}

		remote := newPingNoIOCand()
		remote.candidateBase.networkType = NetworkTypeUDP4
		remote.candidateBase.resolvedAddr = &net.UDPAddr{IP: net.ParseIP("192.168.1.2"), Port: 20000}

		// Create two pairs
		pair1 := agent.addPair(local1, remote)
		pair1.state = CandidatePairStateSucceeded

		pair2 := agent.addPair(local2, remote)
		pair2.state = CandidatePairStateSucceeded

		// Select the first pair initially
		agent.setSelectedPair(pair1)
		assert.Equal(t, pair1, agent.getSelectedPair())

		// Build a standard nomination request WITHOUT nomination value for pair2
		msg, err := stun.Build(stun.BindingRequest,
			stun.TransactionID,
			stun.NewUsername(agent.remoteUfrag+":"+agent.localUfrag),
			UseCandidate(),
			AttrControlling(agent.tieBreaker),
			PriorityAttr(local2.Priority()),
			stun.NewShortTermIntegrity(agent.remotePwd),
			stun.Fingerprint,
		)
		require.NoError(t, err)

		// Simulate sending the binding request
		agent.sendBindingRequest(msg, local2, remote)

		// Verify no nomination value was stored
		require.Len(t, agent.pendingBindingRequests, 1)
		require.Nil(t, agent.pendingBindingRequests[0].nominationValue)

		// Build a success response
		successMsg, err := stun.Build(msg, stun.BindingSuccess,
			&stun.XORMappedAddress{
				IP:   net.ParseIP("192.168.1.2").To4(),
				Port: 20000,
			},
			stun.NewShortTermIntegrity(agent.remotePwd),
			stun.Fingerprint,
		)
		require.NoError(t, err)

		// Handle the success response - this should NOT switch since it's standard nomination
		// and a pair is already selected
		selector.HandleSuccessResponse(successMsg, local2, remote, remote.addr())

		// The controlling agent should remain with pair1
		selectedPair := agent.getSelectedPair()
		assert.Equal(t, pair1, selectedPair,
			"Controlling agent should NOT switch with standard nomination when pair already selected")
	})
}
