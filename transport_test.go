// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/pion/ice/v4/internal/taskloop"
	"github.com/pion/logging"
	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5/packetio"
	"github.com/pion/transport/v5/test"
	"github.com/pion/transport/v5/vnet"
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

	agent := &Agent{buf: buf, loop: loop}
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

func gatherAndExchangeCandidates(tb testing.TB, aAgent, bAgent *Agent, options ...[]GatherOption) {
	tb.Helper()
	var aOptions, bOptions []GatherOption
	if len(options) > 0 {
		aOptions = options[0]
		bOptions = options[1]
	}
	var wg sync.WaitGroup
	wg.Add(2)

	require.NoError(tb, aAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	}))
	require.NoError(tb, aAgent.Gather(aOptions...))

	require.NoError(tb, bAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	}))
	require.NoError(tb, bAgent.Gather(bOptions...))

	wg.Wait()
	require.NoError(tb, aAgent.OnCandidate(func(Candidate) {}))
	require.NoError(tb, bAgent.OnCandidate(func(Candidate) {}))

	candidates, err := aAgent.GetLocalCandidates()
	require.NoError(tb, err)

	for _, c := range candidates {
		if addr, parseErr := netip.ParseAddr(c.Address()); parseErr == nil {
			require.False(tb, shouldFilterLocationTrackedIP(addr))
		}
		candidateCopy, copyErr := c.copy()
		require.NoError(tb, copyErr)
		require.NoError(tb, bAgent.AddRemoteCandidate(candidateCopy))
	}

	candidates, err = bAgent.GetLocalCandidates()

	require.NoError(tb, err)
	for _, c := range candidates {
		candidateCopy, copyErr := c.copy()
		require.NoError(tb, copyErr)
		require.NoError(tb, aAgent.AddRemoteCandidate(candidateCopy))
	}
}

func connect(tb testing.TB, aAgent, bAgent *Agent, options ...[]GatherOption) (*Conn, *Conn) {
	tb.Helper()
	gatherAndExchangeCandidates(tb, aAgent, bAgent, options...)

	return connectGathered(tb, aAgent, bAgent)
}

func connectGathered(tb testing.TB, aAgent, bAgent *Agent) (*Conn, *Conn) {
	tb.Helper()
	accepted := make(chan struct{})
	var aConn *Conn

	go func() {
		var acceptErr error
		bUfrag, bPwd, acceptErr := bAgent.GetLocalUserCredentials()
		require.NoError(tb, acceptErr)
		aConn, acceptErr = aAgent.Accept(context.TODO(), bUfrag, bPwd)
		require.NoError(tb, acceptErr)
		close(accepted)
	}()
	aUfrag, aPwd, err := aAgent.GetLocalUserCredentials()
	require.NoError(tb, err)
	bConn, err := bAgent.Dial(context.TODO(), aUfrag, aPwd)
	require.NoError(tb, err)

	// Ensure accepted
	<-accepted

	return aConn, bConn
}

func pipe(tb testing.TB, defaultConfig []AgentOption, gatherOptions ...GatherOption) (*Conn, *Conn) {
	tb.Helper()
	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()

	cfg := defaultConfig
	cfgGatherOptions := gatherOptions

	aAgent, err := NewAgent(cfg...)
	require.NoError(tb, err)
	require.NoError(tb, aAgent.OnConnectionStateChange(aNotifier))
	tb.Cleanup(func() {
		require.NoError(tb, aAgent.Close())
	})

	bAgent, err := NewAgent(cfg...)
	require.NoError(tb, err)

	require.NoError(tb, bAgent.OnConnectionStateChange(bNotifier))
	tb.Cleanup(func() {
		require.NoError(tb, bAgent.Close())
	})

	aConn, bConn := connect(tb, aAgent, bAgent, cfgGatherOptions, cfgGatherOptions)

	// Ensure pair selected
	// Note: this assumes ConnectionStateConnected is thrown after selecting the final pair
	<-aConnected
	<-bConnected

	return aConn, bConn
}

func pipeWithTimeout(t *testing.T, disconnectTimeout time.Duration, iceKeepalive time.Duration) (*Conn, *Conn) {
	t.Helper()

	aNotifier, aConnected := onConnected()
	bNotifier, bConnected := onConnected()
	cfgGatherOptions := []GatherOption{WithNetworkTypes(supportedNetworkTypes())}
	cfg := []AgentOption{WithDisconnectedTimeout(disconnectTimeout), WithKeepaliveInterval(iceKeepalive)}

	aAgent, err := NewAgent(cfg...)
	require.NoError(t, err)
	require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))
	t.Cleanup(func() {
		require.NoError(t, aAgent.Close())
	})

	bAgent, err := NewAgent(cfg...)
	require.NoError(t, err)
	require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))
	t.Cleanup(func() {
		require.NoError(t, bAgent.Close())
	})

	aConn, bConn := connect(t, aAgent, bAgent, cfgGatherOptions, cfgGatherOptions)

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

// portFromAddr returns the port of a bound socket address, so tests can bind
// to port 0 and read the assigned port back instead of guessing a free one.
func portFromAddr(tb testing.TB, addr net.Addr) int {
	tb.Helper()
	switch addr := addr.(type) {
	case *net.UDPAddr:
		return addr.Port
	case *net.TCPAddr:
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
	cfg := []AgentOption{}
	agent, err := NewAgent(cfg...)
	require.NoError(t, err)

	require.NoError(t, agent.Close())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// isControlling = true
	conn, cerr := agent.startConnect(true, "ufragX", "pwdX")
	require.Nil(t, conn)
	require.Error(t, cerr, "expected error from a.loop.Err() short-circuit")

	err2 := agent.AwaitConnect(ctx)
	require.Error(t, err2, "the agent is closed")
}

func TestConn_Write_RejectsSTUN(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(10 * time.Second).Stop()
	cfg := []AgentOption{WithMulticastDNSMode(MulticastDNSModeDisabled)}
	a, err := NewAgent(cfg...)
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

func TestStartDialConnWriteBeforeConnectReturnsError(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(10 * time.Second).Stop()
	cfg := []AgentOption{WithMulticastDNSMode(MulticastDNSModeDisabled)}
	agent, err := NewAgent(cfg...)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	b, err := NewAgent(cfg...)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, b.Close())
	}()

	gatherAndCollectCandidates(t, b, WithCandidateTypes(nil))
	bUfrag, bPwd, err := b.GetLocalUserCredentials()
	require.NoError(t, err)

	conn, err := agent.StartDial(bUfrag, bPwd)
	require.NoError(t, err)

	n, werr := conn.Write([]byte("early application data"))
	require.Zero(t, n)
	require.ErrorIs(t, werr, ErrNoCandidatePairs)
	require.Zero(t, conn.BytesSent())
}

func TestConn_GetCandidatePairsInfo(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(10 * time.Second).Stop()

	ca, cb := pipe(t, nil)
	defer closePipe(t, ca, cb)

	// Get pairs from conn A
	pairs := ca.GetCandidatePairsInfo()
	require.NotEmpty(t, pairs, "should have candidate pairs after connection")

	// Verify at least one pair is in Succeeded state
	hasSucceeded := false
	for _, info := range pairs {
		if info.State == CandidatePairStateSucceeded {
			hasSucceeded = true

			break
		}
	}
	require.True(t, hasSucceeded, "should have at least one succeeded pair")

	// Verify at least one pair is nominated
	hasNominated := false
	for _, info := range pairs {
		if info.Nominated {
			hasNominated = true

			break
		}
	}
	require.True(t, hasNominated, "should have at least one nominated pair")

	// Verify IDs are set
	for _, info := range pairs {
		require.NotZero(t, info.ID, "pair should have a non-zero ID")
	}
}

func TestConn_WriteToPair_InvalidID(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(10 * time.Second).Stop()
	cfg := []AgentOption{WithMulticastDNSMode(MulticastDNSModeDisabled)}
	agent, err := NewAgent(cfg...)
	require.NoError(t, err)
	defer func() {
		_ = agent.Close()
	}()

	conn := &Conn{agent: agent}

	// Try to write to a non-existent pair ID
	n, werr := conn.WriteToPair(99999, []byte("test"))
	require.Zero(t, n)
	require.ErrorIs(t, werr, ErrCandidatePairNotFound)
}

func TestConnWritesRejectExpiredConsent(t *testing.T) {
	agent, err := NewAgent(WithMulticastDNSMode(MulticastDNSModeDisabled))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, agent.Close()) })
	packets := &mockPacketConnWithCapture{}
	var fresh, explicit, fallback *CandidatePair
	require.NoError(t, agent.loop.Run(agent.loop, func(context.Context) {
		local := newHostLocal(t)
		local.conn = packets
		fresh = agent.addPair(local, newPrflxRemote(t))
		explicit = agent.addPair(local, newHostRemote(t))
		fallback = agent.addPair(local, newRelayRemote(t))
		for _, pair := range []*CandidatePair{fresh, explicit, fallback} {
			pair.state = CandidatePairStateSucceeded
			pair.lastResponseReceivedAt.Store(time.Now().Add(-31 * time.Second))
		}
		fresh.UpdateRoundTripTime(time.Millisecond)
		fallback.setPriorityOverride(1 << 63) // Prefer this expired pair unless consent is checked.
		agent.setSelectedPair(fresh)
	}))
	conn := &Conn{agent: agent}
	payload := []byte("test")
	fresh.lastResponseReceivedAt.Store(time.Now().Add(-31 * time.Second))
	n, err := conn.Write(payload)
	require.ErrorIs(t, err, ErrNoCandidatePairs)
	require.Zero(t, n)
	fresh.UpdateRoundTripTime(time.Millisecond)
	n, err = conn.WriteToPair(explicit.id, payload)
	require.ErrorIs(t, err, ErrCandidatePairNotSucceeded)
	require.Zero(t, n)
	require.Empty(t, packets.sentPackets)
	require.NoError(t, agent.loop.Run(agent.loop, func(context.Context) { agent.setSelectedPair(nil) }))
	n, err = conn.Write(payload)
	require.NoError(t, err)
	require.Equal(t, len(payload), n)
	require.Equal(t, []net.Addr{fresh.Remote.addr()}, packets.sentAddrs)
	fresh.lastResponseReceivedAt.Store(time.Now().Add(-31 * time.Second))
	n, err = conn.Write(payload)
	require.ErrorIs(t, err, ErrNoCandidatePairs)
	require.Zero(t, n)
	require.Len(t, packets.sentPackets, 1)
}

func TestConn_WriteToPair_NotSucceeded(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(10 * time.Second).Stop()
	cfg := []AgentOption{WithMulticastDNSMode(MulticastDNSModeDisabled)}
	agent, err := NewAgent(cfg...)
	require.NoError(t, err)
	defer func() {
		_ = agent.Close()
	}()

	conn := &Conn{agent: agent}

	// Create a pair in Waiting state (default) and add to agent's map
	local, lerr := NewCandidateHost(&CandidateHostConfig{Network: "udp", Address: "192.168.1.1", Port: 1234, Component: ComponentRTP})
	require.NoError(t, lerr)

	remote, rerr := NewCandidateHost(&CandidateHostConfig{Network: "udp", Address: "192.168.1.2", Port: 5678, Component: ComponentRTP})
	require.NoError(t, rerr)

	// Add pair via agent.loop.Run to ensure thread safety
	var pairID uint64
	require.NoError(t, agent.loop.Run(agent.loop, func(_ context.Context) {
		pair := agent.addPair(local, remote)
		pairID = pair.id
		// pair.state is CandidatePairStateWaiting by default
	}))

	n, werr := conn.WriteToPair(pairID, []byte("test"))
	require.Zero(t, n)
	require.ErrorIs(t, werr, ErrCandidatePairNotSucceeded)
}

func TestConn_WriteToPair_RejectsSTUN(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(10 * time.Second).Stop()
	cfg := []AgentOption{WithMulticastDNSMode(MulticastDNSModeDisabled)}
	agent, err := NewAgent(cfg...)
	require.NoError(t, err)
	defer func() {
		_ = agent.Close()
	}()

	conn := &Conn{agent: agent}

	// Create a pair in Succeeded state and add to agent's map
	local, lerr := NewCandidateHost(&CandidateHostConfig{Network: "udp", Address: "192.168.1.1", Port: 1234, Component: ComponentRTP})
	require.NoError(t, lerr)

	remote, rerr := NewCandidateHost(&CandidateHostConfig{Network: "udp", Address: "192.168.1.2", Port: 5678, Component: ComponentRTP})
	require.NoError(t, rerr)

	// Add pair via agent.loop.Run to ensure thread safety
	var pairID uint64
	require.NoError(t, agent.loop.Run(agent.loop, func(_ context.Context) {
		pair := agent.addPair(local, remote)
		pair.state = CandidatePairStateSucceeded
		pairID = pair.id
	}))

	msg := stun.New()
	msg.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}
	msg.Encode()

	n, werr := conn.WriteToPair(pairID, msg.Raw)
	require.Zero(t, n)
	require.ErrorIs(t, werr, errWriteSTUNMessageToIceConn)
}

func TestConn_WriteToPair_Success(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(10 * time.Second).Stop()

	ca, cb := pipe(t, nil)
	defer closePipe(t, ca, cb)

	// Get a succeeded pair from conn A
	pairs := ca.GetCandidatePairsInfo()
	require.NotEmpty(t, pairs)

	var succeededPairID uint64
	for _, info := range pairs {
		if info.State == CandidatePairStateSucceeded {
			succeededPairID = info.ID

			break
		}
	}
	require.NotZero(t, succeededPairID, "should have at least one succeeded pair")

	// Write using WriteToPair
	testData := []byte("test data via WriteToPair")
	n, err := ca.WriteToPair(succeededPairID, testData)
	require.NoError(t, err)
	require.Equal(t, len(testData), n)

	// Read on the other side
	buf := make([]byte, 100)
	n, err = cb.Read(buf)
	require.NoError(t, err)
	require.Equal(t, testData, buf[:n])
}

// TestUDPConnReadWriteDoesNotAllocate pins the data path at zero heap
// allocations per packet in both directions: Conn.Write on one agent through
// real UDP sockets to Conn.Read on the other.
func TestUDPConnReadWriteDoesNotAllocate(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(30 * time.Second).Stop()

	// AllocsPerRun counts allocations process-wide, so the agents are given
	// one candidate each and no extra keepalives. Measure between periodic
	// consent checks, which still run every two seconds.
	noKeepalive := time.Duration(0)
	ca, cb := pipe(t, []AgentOption{WithIncludeLoopback(), WithIPFilter(net.IP.IsLoopback), WithMulticastDNSMode(MulticastDNSModeDisabled), WithKeepaliveInterval(noKeepalive)}, WithNetworkTypes([]NetworkType{NetworkTypeUDP4}))
	defer closePipe(t, ca, cb)

	packet := make([]byte, 1200)
	readBuf := make([]byte, receiveMTU)

	// Note: the read must stay synchronous with the write so the receive path
	// runs exactly once per iteration; AllocsPerRun counts process-wide.
	var failure error
	roundTrip := func() {
		if _, err := ca.Write(packet); err != nil && failure == nil {
			failure = err
		}
		if _, err := cb.Read(readBuf); err != nil && failure == nil {
			failure = err
		}
	}

	// The first packets take slow paths that allocate (source validation and
	// address registration); warm up so the measured loop runs steady state.
	for range 100 {
		roundTrip()
	}
	require.NoError(t, failure)

	allocs := testing.AllocsPerRun(1000, roundTrip)
	require.NoError(t, failure)
	require.Zero(t, allocs)
	require.Positive(t, ca.BytesSent())
	require.Positive(t, cb.BytesReceived())
}

// discardPacketConn exercises the net.Addr fallback by implementing
// a simple black-hole writer that only supports WriteTo.
type discardPacketConn struct {
	deadlinePacketConn
}

func (*discardPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	return len(b), nil
}

// addrPortCapablePacketConn is a black-hole writer that implements
// support for the netip.AddrPort read/write variants.
type addrPortCapablePacketConn struct {
	deadlinePacketConn
	writeToCalled         bool
	writeToAddrPortCalled bool
}

func (c *addrPortCapablePacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	c.writeToCalled = true

	return len(b), nil
}

func (*addrPortCapablePacketConn) ReadFromAddrPort([]byte) (int, netip.AddrPort, error) {
	return 0, netip.AddrPort{}, io.EOF
}

func (c *addrPortCapablePacketConn) WriteToAddrPort(b []byte, _ netip.AddrPort) (int, error) {
	c.writeToAddrPortCalled = true

	return len(b), nil
}

// addrPortTCPMux simulates a custom TCPMux that returns PacketConns supporting
// AddrPortReaderWriter.
type addrPortTCPMux struct {
	conn net.PacketConn
}

func (*addrPortTCPMux) Close() error             { return nil }
func (*addrPortTCPMux) RemoveConnByUfrag(string) {}
func (m *addrPortTCPMux) GetConnByUfrag(string, bool, net.IP) (net.PacketConn, error) {
	return m.conn, nil
}

// TestConnWriteDoesNotAllocateOverStandardPacketConn pins Conn.Write at zero
// heap allocations per packet, at least in this package, when the candidate's
// PacketConn does not support netip.AddrPort writes. The write must reuse the
// candidate's cached net.Addr rather than synthesizing a *net.UDPAddr from a
// netip.AddrPort on every write.
// Note that the *net.UDPConn implementation of WriteTo does allocate internally,
// so a mock connection is used to isolate allocations to this package.
func TestConnWriteDoesNotAllocateOverStandardPacketConn(t *testing.T) {
	_, supportsAddrPort := any(&discardPacketConn{}).(AddrPortReaderWriter)
	require.False(t, supportsAddrPort, "discardPacketConn must be a standard-only PacketConn")

	agent, err := NewAgent()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, agent.Close())
	})

	newCandidate := func(port int) *CandidateHost {
		candidate, err := NewCandidateHost(&CandidateHostConfig{Network: "udp", Address: "192.0.2.1", Port: port, Component: ComponentRTP})
		require.NoError(t, err)

		return candidate
	}
	local, remote := newCandidate(19000), newCandidate(19001)
	local.conn = &discardPacketConn{}

	pair := newCandidatePair(local, remote, false)
	pair.consentStartedAt = time.Now()
	agent.selectedPair.Store(pair)

	conn := &Conn{agent: agent}
	packet := make([]byte, 1200)

	var writeErr error
	allocs := testing.AllocsPerRun(1000, func() {
		if _, err := conn.Write(packet); err != nil {
			writeErr = err
		}
	})

	require.NoError(t, writeErr)
	require.Zero(t, allocs)
	require.Positive(t, conn.BytesSent())
}

// TestCustomTCPMuxAddrPortCapability verifies that TCP candidates use
// netip.AddrPort methods exposed by a custom mux.
func TestCustomTCPMuxAddrPortCapability(t *testing.T) {
	packetConn := &addrPortCapablePacketConn{}
	mux := &addrPortTCPMux{conn: packetConn}
	conn, err := mux.GetConnByUfrag("ufrag", false, net.IPv4(127, 0, 0, 1))
	require.NoError(t, err)

	addrPortConn, ok := conn.(AddrPortReaderWriter)
	require.True(t, ok)

	local := &candidateBase{networkType: NetworkTypeTCP4, conn: conn, addrPortConn: addrPortConn}

	remote := &candidateBase{}
	remote.setResolvedAddr(&net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 5000})

	_, err = local.writeTo([]byte("framed"), remote)
	require.NoError(t, err)
	require.True(t, packetConn.writeToAddrPortCalled)
	require.False(t, packetConn.writeToCalled)
}

func BenchmarkUDPConnWriteRead(b *testing.B) {
	ca, cb := pipe(b, nil, WithNetworkTypes([]NetworkType{NetworkTypeUDP4}))
	defer closePipe(b, ca, cb)

	// Note: this loop needs to keep the writes and reads synchronous to keep
	// the allocation benchmark deterministic. Otherwise, if reads fall behind
	// writes and packets get dropped, the allocations could get underreported.
	packet := make([]byte, 1200)
	readBuf := make([]byte, 2000)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ca.Write(packet); err != nil {
			b.Fatal(err)
		}
		if _, err := cb.Read(readBuf); err != nil {
			b.Fatal(err)
		}
	}
}

func TestWriteUseValidPair(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 10).Stop()

	loggerFactory := logging.NewDefaultLoggerFactory()

	// Create a network with two interfaces
	wan, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "0.0.0.0/0", LoggerFactory: loggerFactory})
	require.NoError(t, err)

	wan.AddChunkFilter(func(c vnet.Chunk) bool {
		if stun.IsMessage(c.UserData()) {
			m := &stun.Message{Raw: c.UserData()}
			if decErr := m.Decode(); decErr != nil {
				return false
			} else if m.Contains(stun.AttrUseCandidate) {
				return false
			}
		}

		return true
	})

	net0, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{"192.168.0.1"}})
	require.NoError(t, err)
	require.NoError(t, wan.AddNet(net0))

	net1, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{"192.168.0.2"}})
	require.NoError(t, err)
	require.NoError(t, wan.AddNet(net1))

	require.NoError(t, wan.Start())
	// Create two agents and connect them
	controllingAgentGatherOptions := []GatherOption{WithNetworkTypes(supportedNetworkTypes())}
	controllingAgent, err := NewAgent(WithMulticastDNSMode(MulticastDNSModeDisabled), WithNet(net0))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, controllingAgent.Close())
	}()
	controlledAgentGatherOptions := []GatherOption{WithNetworkTypes(supportedNetworkTypes())}
	controlledAgent, err := NewAgent(WithMulticastDNSMode(MulticastDNSModeDisabled), WithNet(net1))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, controlledAgent.Close())
	}()

	gatherAndExchangeCandidates(t, controllingAgent, controlledAgent, controllingAgentGatherOptions, controlledAgentGatherOptions)

	controllingUfrag, controllingPwd, err := controllingAgent.GetLocalUserCredentials()
	require.NoError(t, err)

	controlledUfrag, controlledPwd, err := controlledAgent.GetLocalUserCredentials()
	require.NoError(t, err)

	require.NoError(t, controllingAgent.startConnectivityChecks(true, controlledUfrag, controlledPwd))
	require.NoError(t, controlledAgent.startConnectivityChecks(false, controllingUfrag, controllingPwd))

	testMessage := []byte("Test Message")
	go func() {
		for {
			if _, writeErr := (&Conn{agent: controllingAgent}).Write(testMessage); writeErr != nil {
				if !errors.Is(writeErr, ErrNoCandidatePairs) {
					return
				}
			}

			time.Sleep(20 * time.Millisecond)
		}
	}()

	readBuf := make([]byte, len(testMessage))
	_, err = (&Conn{agent: controlledAgent}).Read(readBuf)
	require.NoError(t, err)

	require.Equal(t, readBuf, testMessage)

	require.NoError(t, wan.Stop())
}

func TestRemoteLocalAddr(t *testing.T) {
	// Check for leaking routines
	defer test.CheckRoutines(t)()

	// Limit runtime in case of deadlocks
	defer test.TimeOut(time.Second * 20).Stop()

	// Agent0 is behind 1:1 NAT
	natType0 := &vnet.NATType{Mode: vnet.NATModeNAT1To1}
	// Agent1 is behind 1:1 NAT
	natType1 := &vnet.NATType{Mode: vnet.NATModeNAT1To1}

	builtVnet, errVnet := buildVNet(natType0, natType1)
	require.NoError(t, errVnet, "should succeed")
	defer builtVnet.close()

	stunServerURL := &stun.URI{Scheme: stun.SchemeTypeSTUN, Host: vnetSTUNServerIP, Port: vnetSTUNServerPort, Proto: stun.ProtoTypeUDP}

	t.Run("Disconnected Returns nil", func(t *testing.T) {
		disconnectedAgent, err := NewAgent()
		require.NoError(t, err)

		disconnectedConn := Conn{agent: disconnectedAgent}
		require.Nil(t, disconnectedConn.RemoteAddr())
		require.Nil(t, disconnectedConn.LocalAddr())

		require.NoError(t, disconnectedConn.Close())
	})

	t.Run("Remote/Local Pair Match between Agents", func(t *testing.T) {
		ca, cb := pipeWithVNet(t, builtVnet, &agentTestConfig{urls: []*stun.URI{stunServerURL}}, &agentTestConfig{urls: []*stun.URI{stunServerURL}})
		defer closePipe(t, ca, cb)

		aRAddr := ca.RemoteAddr()
		aLAddr := ca.LocalAddr()
		bRAddr := cb.RemoteAddr()
		bLAddr := cb.LocalAddr()

		// Assert that nothing is nil
		require.NotNil(t, aRAddr)
		require.NotNil(t, aLAddr)
		require.NotNil(t, bRAddr)
		require.NotNil(t, bLAddr)

		// Assert addresses
		require.Equal(t, aLAddr.String(),
			fmt.Sprintf("%s:%d", vnetLocalIPA, bRAddr.(*net.UDPAddr).Port), //nolint:forcetypeassert
		)
		require.Equal(t, bLAddr.String(),
			fmt.Sprintf("%s:%d", vnetLocalIPB, aRAddr.(*net.UDPAddr).Port), //nolint:forcetypeassert
		)
		require.Equal(t, aRAddr.String(),
			fmt.Sprintf("%s:%d", vnetGlobalIPB, bLAddr.(*net.UDPAddr).Port), //nolint:forcetypeassert
		)
		require.Equal(t, bRAddr.String(),
			fmt.Sprintf("%s:%d", vnetGlobalIPA, aLAddr.(*net.UDPAddr).Port), //nolint:forcetypeassert
		)
	})
}
