// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/stun"
	"github.com/pion/transport/v2/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStressDuplex(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 20).Stop()

	// Run the test
	stressDuplex(t)
}

func testTimeout(t *testing.T, c *Conn, timeout time.Duration) {
	assert := assert.New(t)
	require := require.New(t)

	const pollRate = 100 * time.Millisecond
	const margin = 20 * time.Millisecond // Allow 20msec error in time
	ticker := time.NewTicker(pollRate)
	defer func() {
		ticker.Stop()
		err := c.Close()
		assert.NoError(err)
	}()

	startedAt := time.Now()

	for cnt := time.Duration(0); cnt <= timeout+defaultKeepaliveInterval+pollRate; cnt += pollRate {
		<-ticker.C

		var cs ConnectionState

		err := c.agent.run(context.Background(), func(ctx context.Context, agent *Agent) {
			cs = agent.connectionState
		})
		if err != nil {
			// We should never get here.
			panic(err)
		}

		if cs != ConnectionStateConnected {
			elapsed := time.Since(startedAt)
			if assert.False(elapsed+margin < timeout, "Connection timed out %f msec early", elapsed.Seconds()*1000) {
				t.Logf("Connection timed out in %f msec", elapsed.Seconds()*1000)
				return
			}
		}
	}
	require.Failf("", "Connection failed to time out in time. (expected timeout: %v)", timeout)
}

func TestTimeout(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 20).Stop()

	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	t.Run("WithoutDisconnectTimeout", func(t *testing.T) {
		ca, cb := pipe(nil)
		err := cb.Close()
		if err != nil {
			// We should never get here.
			panic(err)
		}

		testTimeout(t, ca, defaultDisconnectedTimeout)
	})

	t.Run("WithDisconnectTimeout", func(t *testing.T) {
		ca, cb := pipeWithTimeout(5*time.Second, 3*time.Second)
		err := cb.Close()
		if err != nil {
			// We should never get here.
			panic(err)
		}

		testTimeout(t, ca, 5*time.Second)
	})
}

func TestReadClosed(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 20).Stop()

	ca, cb := pipe(nil)

	err := ca.Close()
	if err != nil {
		// We should never get here.
		panic(err)
	}

	err = cb.Close()
	if err != nil {
		// We should never get here.
		panic(err)
	}

	empty := make([]byte, 10)
	_, err = ca.Read(empty)
	require.Error(t, err, "Reading from a closed channel should return an error")
}

func stressDuplex(t *testing.T) {
	require := require.New(t)

	ca, cb := pipe(nil)

	defer func() {
		require.NoError(ca.Close())
		require.NoError(cb.Close())
	}()

	opt := test.Options{
		MsgSize:  10,
		MsgCount: 1, // Order not reliable due to UDP & potentially multiple candidate pairs.
	}

	require.NoError(test.StressDuplex(ca, cb, opt))
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func gatherAndExchangeCandidates(aAgent, bAgent *Agent) {
	var wg sync.WaitGroup
	wg.Add(2)

	check(aAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	}))
	check(aAgent.GatherCandidates())

	check(bAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	}))
	check(bAgent.GatherCandidates())

	wg.Wait()

	candidates, err := aAgent.GetLocalCandidates()
	check(err)
	for _, c := range candidates {
		candidateCopy, copyErr := c.copy()
		check(copyErr)
		check(bAgent.AddRemoteCandidate(candidateCopy))
	}

	candidates, err = bAgent.GetLocalCandidates()
	check(err)
	for _, c := range candidates {
		candidateCopy, copyErr := c.copy()
		check(copyErr)
		check(aAgent.AddRemoteCandidate(candidateCopy))
	}
}

func connect(aAgent, bAgent *Agent) (*Conn, *Conn) {
	gatherAndExchangeCandidates(aAgent, bAgent)

	accepted := make(chan struct{})
	var aConn *Conn

	go func() {
		var acceptErr error
		bUfrag, bPwd, acceptErr := bAgent.GetLocalUserCredentials()
		check(acceptErr)
		aConn, acceptErr = aAgent.Accept(context.TODO(), bUfrag, bPwd)
		check(acceptErr)
		close(accepted)
	}()
	aUfrag, aPwd, err := aAgent.GetLocalUserCredentials()
	check(err)
	bConn, err := bAgent.Dial(context.TODO(), aUfrag, aPwd)
	check(err)

	// Ensure accepted
	<-accepted
	return aConn, bConn
}

func pipe(defaultConfig *AgentConfig) (*Conn, *Conn) {
	var urls []*stun.URI

	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	cfg := &AgentConfig{}
	if defaultConfig != nil {
		*cfg = *defaultConfig
	}

	cfg.Urls = urls
	cfg.NetworkTypes = supportedNetworkTypes()

	aAgent, err := NewAgent(cfg)
	check(err)
	check(aAgent.OnConnectionStateChange(aNotifier))

	bAgent, err := NewAgent(cfg)
	check(err)

	check(bAgent.OnConnectionStateChange(bNotifier))

	aConn, bConn := connect(aAgent, bAgent)

	// Ensure pair selected
	// Note: this assumes ConnectionStateConnected is thrown after selecting the final pair
	<-aConnected
	<-bConnected

	return aConn, bConn
}

func pipeWithTimeout(disconnectTimeout time.Duration, iceKeepalive time.Duration) (*Conn, *Conn) {
	var urls []*stun.URI

	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	cfg := &AgentConfig{
		Urls:                urls,
		DisconnectedTimeout: &disconnectTimeout,
		KeepaliveInterval:   &iceKeepalive,
		NetworkTypes:        supportedNetworkTypes(),
	}

	aAgent, err := NewAgent(cfg)
	check(err)
	check(aAgent.OnConnectionStateChange(aNotifier))

	bAgent, err := NewAgent(cfg)
	check(err)
	check(bAgent.OnConnectionStateChange(bNotifier))

	aConn, bConn := connect(aAgent, bAgent)

	// Ensure pair selected
	// Note: this assumes ConnectionStateConnected is thrown after selecting the final pair
	<-aConnected
	<-bConnected

	return aConn, bConn
}

func onConnected() (func(ConnectionState), chan struct{}) {
	done := make(chan struct{})
	return func(state ConnectionState) {
		if state == ConnectionStateConnected {
			close(done)
		}
	}, done
}

func randomPort(t testing.TB) int {
	require := require.New(t)

	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(err, "Failed to pickPort")

	defer func() {
		_ = conn.Close()
	}()
	switch addr := conn.LocalAddr().(type) {
	case *net.UDPAddr:
		return addr.Port
	default:
		require.Failf("", "Unknown addr type %T", addr)
		return 0
	}
}

func TestConnStats(t *testing.T) {
	require := require.New(t)

	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 20).Stop()

	ca, cb := pipe(nil)
	_, err := ca.Write(make([]byte, 10))
	require.NoError(err, "Failed to write")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		buf := make([]byte, 10)
		if _, err := cb.Read(buf); err != nil {
			panic(errRead)
		}
		wg.Done()
	}()

	wg.Wait()

	require.Equal(ca.BytesSent(), uint64(10), "Bytes sent don't match")
	require.Equal(cb.BytesReceived(), uint64(10), "Bytes received don't match")

	require.NoError(ca.Close())
	require.NoError(cb.Close())
}
