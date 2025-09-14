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
	"github.com/pion/transport/v3/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	a.remotePwd = "pwd"
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
	a.remotePwd = "pwd"
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
