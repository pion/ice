// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type localCandidatePacketConn struct {
	addr       net.Addr
	closeCount atomic.Int32
}

func (c *localCandidatePacketConn) ReadFrom([]byte) (int, net.Addr, error) {
	return 0, c.addr, io.EOF
}

func (c *localCandidatePacketConn) WriteTo(payload []byte, _ net.Addr) (int, error) {
	return len(payload), nil
}

func (c *localCandidatePacketConn) Close() error {
	c.closeCount.Add(1)

	return nil
}

func (c *localCandidatePacketConn) LocalAddr() net.Addr              { return c.addr }
func (c *localCandidatePacketConn) SetDeadline(time.Time) error      { return nil }
func (c *localCandidatePacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *localCandidatePacketConn) SetWriteDeadline(time.Time) error { return nil }

func TestAddLocalCandidateRegistersExternalRelay(t *testing.T) {
	agent, err := NewAgentWithOptions(
		WithCandidateTypes([]CandidateType{}),
		WithMulticastDNSMode(MulticastDNSModeDisabled),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	gatheringComplete := make(chan struct{})
	candidates := make(chan Candidate, 1)
	require.NoError(t, agent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			close(gatheringComplete)

			return
		}
		candidates <- candidate
	}))

	candidate, err := NewCandidateRelay(&CandidateRelayConfig{
		Network:       NetworkTypeUDP4.String(),
		Address:       "192.0.2.10",
		Port:          5000,
		Component:     ComponentRTP,
		RelayProtocol: "custom",
	})
	require.NoError(t, err)
	packetConn := &localCandidatePacketConn{
		addr: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 10), Port: 5000},
	}

	require.NoError(t, agent.AddLocalCandidate(candidate, packetConn))
	localCandidates, err := agent.GetLocalCandidates()
	require.NoError(t, err)
	require.Len(t, localCandidates, 1)
	require.Equal(t, "custom", localCandidates[0].(*CandidateRelay).RelayProtocol())

	select {
	case got := <-candidates:
		require.Equal(t, candidate, got)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for local candidate callback")
	}

	require.NoError(t, agent.GatherCandidates())
	select {
	case <-gatheringComplete:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for gathering completion")
	}
}

func TestAddLocalCandidateRejectsNilPacketConn(t *testing.T) {
	agent, err := NewAgentWithOptions(WithMulticastDNSMode(MulticastDNSModeDisabled))
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	candidate, err := NewCandidateRelay(&CandidateRelayConfig{
		Network:   NetworkTypeUDP4.String(),
		Address:   "192.0.2.11",
		Port:      5001,
		Component: ComponentRTP,
	})
	require.NoError(t, err)
	require.Error(t, agent.AddLocalCandidate(candidate, nil))
}

func TestAddLocalCandidateClosesDuplicateConnection(t *testing.T) {
	agent, err := NewAgentWithOptions(WithMulticastDNSMode(MulticastDNSModeDisabled))
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	require.NoError(t, agent.OnCandidate(func(Candidate) {}))
	config := CandidateRelayConfig{
		Network:   NetworkTypeUDP4.String(),
		Address:   "192.0.2.12",
		Port:      5002,
		Component: ComponentRTP,
	}
	first, err := NewCandidateRelay(&config)
	require.NoError(t, err)
	second, err := NewCandidateRelay(&config)
	require.NoError(t, err)
	firstConn := &localCandidatePacketConn{addr: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 12), Port: 5002}}
	secondConn := &localCandidatePacketConn{addr: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 12), Port: 5002}}

	require.NoError(t, agent.AddLocalCandidate(first, firstConn))
	require.NoError(t, agent.AddLocalCandidate(second, secondConn))
	require.Eventually(t, func() bool {
		return secondConn.closeCount.Load() == 1
	}, time.Second, time.Millisecond)

	localCandidates, err := agent.GetLocalCandidates()
	require.NoError(t, err)
	require.Len(t, localCandidates, 1)
}
