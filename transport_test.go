// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

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
	"github.com/pion/transport/v4/packetio"
	"github.com/pion/transport/v4/test"
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

func gatherAndExchangeCandidates(tb testing.TB, aAgent, bAgent *Agent) {
	tb.Helper()
	var wg sync.WaitGroup
	wg.Add(2)

	require.NoError(tb, aAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	}))
	require.NoError(tb, aAgent.GatherCandidates())

	require.NoError(tb, bAgent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			wg.Done()
		}
	}))
	require.NoError(tb, bAgent.GatherCandidates())

	wg.Wait()

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

func connect(tb testing.TB, aAgent, bAgent *Agent) (*Conn, *Conn) {
	tb.Helper()
	gatherAndExchangeCandidates(tb, aAgent, bAgent)

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

func pipe(tb testing.TB, defaultConfig *AgentConfig) (*Conn, *Conn) {
	tb.Helper()
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
	require.NoError(tb, err)
	require.NoError(tb, aAgent.OnConnectionStateChange(aNotifier))
	tb.Cleanup(func() {
		require.NoError(tb, aAgent.Close())
	})

	bAgent, err := NewAgent(cfg)
	require.NoError(tb, err)

	require.NoError(tb, bAgent.OnConnectionStateChange(bNotifier))
	tb.Cleanup(func() {
		require.NoError(tb, bAgent.Close())
	})

	aConn, bConn := connect(tb, aAgent, bAgent)

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
	agent, err := NewAgent(cfg)
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

func TestStartDialConnWriteBeforeConnectReturnsError(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(10 * time.Second).Stop()

	cfg := &AgentConfig{
		NetworkTypes:     supportedNetworkTypes(),
		MulticastDNSMode: MulticastDNSModeDisabled,
	}
	agent, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	b, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, b.Close())
	}()

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

	cfg := &AgentConfig{
		NetworkTypes:     supportedNetworkTypes(),
		MulticastDNSMode: MulticastDNSModeDisabled,
	}
	agent, err := NewAgent(cfg)
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

func TestConn_WriteToPair_NotSucceeded(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(10 * time.Second).Stop()

	cfg := &AgentConfig{
		NetworkTypes:     supportedNetworkTypes(),
		MulticastDNSMode: MulticastDNSModeDisabled,
	}
	agent, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		_ = agent.Close()
	}()

	conn := &Conn{agent: agent}

	// Create a pair in Waiting state (default) and add to agent's map
	local, lerr := NewCandidateHost(&CandidateHostConfig{
		Network:   "udp",
		Address:   "192.168.1.1",
		Port:      1234,
		Component: ComponentRTP,
	})
	require.NoError(t, lerr)

	remote, rerr := NewCandidateHost(&CandidateHostConfig{
		Network:   "udp",
		Address:   "192.168.1.2",
		Port:      5678,
		Component: ComponentRTP,
	})
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

	cfg := &AgentConfig{
		NetworkTypes:     supportedNetworkTypes(),
		MulticastDNSMode: MulticastDNSModeDisabled,
	}
	agent, err := NewAgent(cfg)
	require.NoError(t, err)
	defer func() {
		_ = agent.Close()
	}()

	conn := &Conn{agent: agent}

	// Create a pair in Succeeded state and add to agent's map
	local, lerr := NewCandidateHost(&CandidateHostConfig{
		Network:   "udp",
		Address:   "192.168.1.1",
		Port:      1234,
		Component: ComponentRTP,
	})
	require.NoError(t, lerr)

	remote, rerr := NewCandidateHost(&CandidateHostConfig{
		Network:   "udp",
		Address:   "192.168.1.2",
		Port:      5678,
		Component: ComponentRTP,
	})
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

type discardPacketConn struct {
	deadlinePacketConn
}

func (*discardPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	return len(b), nil
}

// TestConnWriteDoesNotAllocateAbovePacketConn pins Write's fast path at zero
// heap allocations per packet, from the selected pair down to the candidate's
// net.PacketConn. The discarding conn excludes the socket write itself.
func TestConnWriteDoesNotAllocateAbovePacketConn(t *testing.T) {
	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, agent.Close())
	})

	newCandidate := func(port int) *CandidateHost {
		candidate, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "192.0.2.1",
			Port:      port,
			Component: ComponentRTP,
		})
		require.NoError(t, err)

		return candidate
	}
	local, remote := newCandidate(19000), newCandidate(19001)
	local.conn = &discardPacketConn{}

	agent.selectedPair.Store(newCandidatePair(local, remote, false))

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

func BenchmarkConnWriteRead(b *testing.B) {
	ca, cb := pipe(b, nil)

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
