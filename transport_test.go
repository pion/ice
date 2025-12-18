// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/pion/ice/v4/internal/taskloop"
	"github.com/pion/stun/v3"
	"github.com/pion/transport/v3/packetio"
	"github.com/pion/transport/v3/test"
	"github.com/stretchr/testify/require"
)

type deadlineCandidate struct {
	candidateBase
}

type deadlinePacketConn struct {
	writeDeadline time.Time
}

func (d *deadlinePacketConn) ReadFrom([]byte) (n int, addr net.Addr, err error) {
	return 0, nil, nil
}

func (d *deadlinePacketConn) WriteTo([]byte, net.Addr) (n int, err error) {
	return 0, nil
}

func (d *deadlinePacketConn) Close() error {
	return nil
}

func (d *deadlinePacketConn) LocalAddr() net.Addr {
	return nil
}

func (d *deadlinePacketConn) SetDeadline(time.Time) error {
	return nil
}

func (d *deadlinePacketConn) SetReadDeadline(time.Time) error {
	return nil
}

func (d *deadlinePacketConn) SetWriteDeadline(t time.Time) error {
	d.writeDeadline = t

	return nil
}

func TestStressDuplex(t *testing.T) {
	// Check for leaking routines
	defer test.CheckRoutines(t)()

	// Limit runtime in case of deadlocks
	defer test.TimeOut(time.Second * 20).Stop()

	// Run the test
	stressDuplex(t)
}

func testTimeout(t *testing.T, conn *Conn, timeout time.Duration) {
	t.Helper()

	const pollRate = 100 * time.Millisecond
	const margin = 20 * time.Millisecond // Allow 20msec error in time
	ticker := time.NewTicker(pollRate)
	defer func() {
		ticker.Stop()
		require.NoError(t, conn.Close())
	}()

	startedAt := time.Now()

	for cnt := time.Duration(0); cnt <= timeout+defaultKeepaliveInterval+pollRate; cnt += pollRate {
		<-ticker.C

		var cs ConnectionState

		require.NoError(t, conn.agent.loop.Run(context.Background(), func(_ context.Context) {
			cs = conn.agent.connectionState
		}))

		if cs != ConnectionStateConnected {
			elapsed := time.Since(startedAt)
			require.Less(t, timeout, elapsed+margin)

			return
		}
	}
	t.Fatalf("Connection failed to time out in time. (expected timeout: %v)", timeout) //nolint
}

func TestTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	// Check for leaking routines
	defer test.CheckRoutines(t)()

	// Limit runtime in case of deadlocks
	defer test.TimeOut(time.Second * 20).Stop()

	t.Run("WithoutDisconnectTimeout", func(t *testing.T) {
		ca, cb := pipe(t, nil)
		require.NoError(t, cb.Close())
		testTimeout(t, ca, defaultDisconnectedTimeout)
	})

	t.Run("WithDisconnectTimeout", func(t *testing.T) {
		ca, cb := pipeWithTimeout(t, 5*time.Second, 3*time.Second)
		require.NoError(t, cb.Close())
		testTimeout(t, ca, 5*time.Second)
	})
}

func TestReadClosed(t *testing.T) {
	// Check for leaking routines
	defer test.CheckRoutines(t)()

	// Limit runtime in case of deadlocks
	defer test.TimeOut(time.Second * 20).Stop()

	ca, cb := pipe(t, nil)
	require.NoError(t, ca.Close())
	require.NoError(t, cb.Close())

	empty := make([]byte, 10)
	_, err := ca.Read(empty)
	require.Error(t, err)
}

func TestConnDeadlines(t *testing.T) {
	defer test.CheckRoutines(t)()

	loop := taskloop.New(func() {})
	defer loop.Close()

	buf := packetio.NewBuffer()
	pc := &deadlinePacketConn{}
	candidate := &deadlineCandidate{}
	candidate.conn = pc

	agent := &Agent{
		buf:  buf,
		loop: loop,
	}
	agent.selectedPair.Store(&CandidatePair{Local: candidate})

	conn := &Conn{agent: agent}

	writeDeadline := time.Now().Add(100 * time.Millisecond)
	require.NoError(t, conn.SetWriteDeadline(writeDeadline))
	require.WithinDuration(t, writeDeadline, pc.writeDeadline, time.Millisecond)

	readDeadline := time.Now().Add(-1 * time.Millisecond)
	require.NoError(t, conn.SetDeadline(readDeadline))

	_, err := conn.Read(make([]byte, 1))
	var netErr interface{ Timeout() bool }
	require.ErrorAs(t, err, &netErr)
	require.True(t, netErr.Timeout())
}

func stressDuplex(t *testing.T) {
	t.Helper()

	ca, cb := pipe(t, nil)

	defer func() {
		require.NoError(t, ca.Close())
		require.NoError(t, cb.Close())
	}()

	opt := test.Options{
		MsgSize:  10,
		MsgCount: 1, // Order not reliable due to UDP & potentially multiple candidate pairs.
	}

	require.NoError(t, test.StressDuplex(ca, cb, opt))
}

func gatherAndExchangeCandidates(t *testing.T, aAgent, bAgent *Agent) {
	t.Helper()
	var wg sync.WaitGroup
	wg.Add(2)

	require.NoError(t, aAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	}))
	require.NoError(t, aAgent.GatherCandidates())

	require.NoError(t, bAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	}))
	require.NoError(t, bAgent.GatherCandidates())

	wg.Wait()

	candidates, err := aAgent.GetLocalCandidates()
	require.NoError(t, err)

	for _, c := range candidates {
		if addr, parseErr := netip.ParseAddr(c.Address()); parseErr == nil {
			require.False(t, shouldFilterLocationTrackedIP(addr))
		}
		candidateCopy, copyErr := c.copy()
		require.NoError(t, copyErr)
		require.NoError(t, bAgent.AddRemoteCandidate(candidateCopy))
	}

	candidates, err = bAgent.GetLocalCandidates()

	require.NoError(t, err)
	for _, c := range candidates {
		candidateCopy, copyErr := c.copy()
		require.NoError(t, copyErr)
		require.NoError(t, aAgent.AddRemoteCandidate(candidateCopy))
	}
}

func connect(t *testing.T, aAgent, bAgent *Agent) (*Conn, *Conn) {
	t.Helper()
	gatherAndExchangeCandidates(t, aAgent, bAgent)

	accepted := make(chan struct{})
	var aConn *Conn

	go func() {
		var acceptErr error
		bUfrag, bPwd, acceptErr := bAgent.GetLocalUserCredentials()
		require.NoError(t, acceptErr)
		aConn, acceptErr = aAgent.Accept(context.TODO(), bUfrag, bPwd)
		require.NoError(t, acceptErr)
		close(accepted)
	}()
	aUfrag, aPwd, err := aAgent.GetLocalUserCredentials()
	require.NoError(t, err)
	bConn, err := bAgent.Dial(context.TODO(), aUfrag, aPwd)
	require.NoError(t, err)

	// Ensure accepted
	<-accepted

	return aConn, bConn
}

func pipe(t *testing.T, defaultConfig *AgentConfig) (*Conn, *Conn) {
	t.Helper()
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
	require.NoError(t, err)
	require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))
	t.Cleanup(func() {
		require.NoError(t, aAgent.Close())
	})

	bAgent, err := NewAgent(cfg)
	require.NoError(t, err)

	require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))
	t.Cleanup(func() {
		require.NoError(t, bAgent.Close())
	})

	aConn, bConn := connect(t, aAgent, bAgent)

	// Ensure pair selected
	// Note: this assumes ConnectionStateConnected is thrown after selecting the final pair
	<-aConnected
	<-bConnected

	return aConn, bConn
}

func pipeWithTimeout(t *testing.T, disconnectTimeout time.Duration, iceKeepalive time.Duration) (*Conn, *Conn) {
	t.Helper()
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
	require.NoError(t, err)
	require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))
	t.Cleanup(func() {
		require.NoError(t, aAgent.Close())
	})

	bAgent, err := NewAgent(cfg)
	require.NoError(t, err)
	require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))
	t.Cleanup(func() {
		require.NoError(t, bAgent.Close())
	})

	aConn, bConn := connect(t, aAgent, bAgent)

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

func randomPort(tb testing.TB) int {
	tb.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0") // nolint: noctx
	if err != nil {
		tb.Fatalf("failed to pickPort: %v", err)
	}
	defer func() {
		_ = conn.Close()
	}()
	switch addr := conn.LocalAddr().(type) {
	case *net.UDPAddr:
		return addr.Port
	default:
		tb.Fatalf("unknown addr type %T", addr)

		return 0
	}
}

func TestConnStats(t *testing.T) {
	// Check for leaking routines
	defer test.CheckRoutines(t)()

	// Limit runtime in case of deadlocks
	defer test.TimeOut(time.Second * 20).Stop()

	ca, cb := pipe(t, nil)
	_, err := ca.Write(make([]byte, 10))
	require.NoError(t, err)
	defer closePipe(t, ca, cb)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		buf := make([]byte, 10)
		_, err := cb.Read(buf)
		require.NoError(t, err)
		wg.Done()
	}()

	wg.Wait()

	require.Equal(t, uint64(10), ca.BytesSent())
	require.Equal(t, uint64(10), cb.BytesReceived())
}

func TestAgent_connect_ErrEarly(t *testing.T) {
	defer test.CheckRoutines(t)()

	cfg := &AgentConfig{
		NetworkTypes: supportedNetworkTypes(),
	}
	a, err := NewAgent(cfg)
	require.NoError(t, err)

	require.NoError(t, a.Close())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// isControlling = true
	conn, cerr := a.connect(ctx, true, "ufragX", "pwdX")
	require.Nil(t, conn)
	require.Error(t, cerr, "expected error from a.loop.Err() short-circuit")
}

func TestConn_Write_RejectsSTUN(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(10 * time.Second).Stop()

	cfg := &AgentConfig{
		NetworkTypes:     supportedNetworkTypes(),
		MulticastDNSMode: MulticastDNSModeDisabled,
	}
	a, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		_ = a.Close()
	}()

	c := &Conn{agent: a}
	require.Nil(t, c.agent.getSelectedPair(), "precondition: no selected pair")

	msg := stun.New()
	msg.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}
	msg.Encode()

	n, werr := c.Write(msg.Raw)
	require.Zero(t, n)
	require.ErrorIs(t, werr, errWriteSTUNMessageToIceConn)
}
