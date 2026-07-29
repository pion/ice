// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type relayProviderTestPacketConn struct {
	addr       net.Addr
	closeCount atomic.Int32
}

func (c *relayProviderTestPacketConn) ReadFrom([]byte) (int, net.Addr, error) {
	return 0, c.addr, io.EOF
}

func (c *relayProviderTestPacketConn) WriteTo(payload []byte, _ net.Addr) (int, error) {
	return len(payload), nil
}

func (c *relayProviderTestPacketConn) Close() error {
	c.closeCount.Add(1)

	return nil
}

func (c *relayProviderTestPacketConn) LocalAddr() net.Addr {
	return c.addr
}

func (c *relayProviderTestPacketConn) SetDeadline(_ time.Time) error {
	return nil
}

func (c *relayProviderTestPacketConn) SetReadDeadline(_ time.Time) error {
	return nil
}

func (c *relayProviderTestPacketConn) SetWriteDeadline(_ time.Time) error {
	return nil
}

type relayProviderTestProvider struct {
	called          atomic.Int32
	packetConn      *relayProviderTestPacketConn
	relayProtocol   string
	localPreference uint16
	address         string
	port            int
}

func (p *relayProviderTestProvider) GatherCandidates(context.Context, string, string) ([]RelayCandidate, error) {
	p.called.Add(1)

	return []RelayCandidate{{
		Config: CandidateRelayConfig{
			Network:              NetworkTypeUDP4.String(),
			Address:              p.address,
			Port:                 p.port,
			Component:            ComponentRTP,
			RelayProtocol:        p.relayProtocol,
			RelayLocalPreference: p.localPreference,
		},
		Conn: p.packetConn,
	}}, nil
}

func TestNewCandidateRelayAcceptsExternalProtocolAndPreference(t *testing.T) {
	candidate, err := NewCandidateRelay(&CandidateRelayConfig{
		Network:              NetworkTypeUDP4.String(),
		Address:              "192.0.2.10",
		Port:                 5000,
		Component:            ComponentRTP,
		RelayProtocol:        "quic",
		RelayLocalPreference: 37,
	})
	require.NoError(t, err)

	require.Equal(t, "quic", candidate.RelayProtocol())
	require.Equal(t, uint16(37), candidate.LocalPreference())
	require.Equal(t, uint32(37*256+255), candidate.Priority())
}

func TestAgentGathersFromMultipleRelayProviders(t *testing.T) {
	first := &relayProviderTestProvider{
		packetConn:      &relayProviderTestPacketConn{addr: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 6001}},
		relayProtocol:   "websocket",
		localPreference: 21,
		address:         "192.0.2.1",
		port:            6001,
	}
	second := &relayProviderTestProvider{
		packetConn:      &relayProviderTestPacketConn{addr: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 2), Port: 6002}},
		relayProtocol:   "quic",
		localPreference: 42,
		address:         "192.0.2.2",
		port:            6002,
	}

	agent, err := NewAgentWithOptions(
		WithCandidateTypes([]CandidateType{CandidateTypeRelay}),
		WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
		WithRelayCandidateProviders(first, second),
		WithMulticastDNSMode(MulticastDNSModeDisabled),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()
	require.Equal(t, []CandidateType{CandidateTypeRelay}, agent.candidateTypes)
	require.Len(t, agent.relayCandidateProviders, 2)

	gathered := make(chan struct{})
	require.NoError(t, agent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			close(gathered)
		}
	}))
	require.NoError(t, agent.GatherCandidates())
	require.Eventually(t, func() bool {
		select {
		case <-gathered:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)

	require.Equal(t, int32(1), first.called.Load())
	require.Equal(t, int32(1), second.called.Load())

	candidates, err := agent.GetLocalCandidates()
	require.NoError(t, err)
	require.Len(t, candidates, 2)

	byProtocol := make(map[string]*CandidateRelay, len(candidates))
	for _, candidate := range candidates {
		relay, ok := candidate.(*CandidateRelay)
		require.True(t, ok)
		byProtocol[relay.RelayProtocol()] = relay
	}

	require.Equal(t, uint16(21), byProtocol["websocket"].LocalPreference())
	require.Equal(t, uint16(42), byProtocol["quic"].LocalPreference())
}

func TestAgentClosesInvalidExternalRelayCandidate(t *testing.T) {
	packetConn := &relayProviderTestPacketConn{
		addr: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 3), Port: 6003},
	}
	provider := &relayProviderTestProvider{
		packetConn:    packetConn,
		relayProtocol: "custom",
		address:       "not-an-ip-address",
		port:          6003,
	}

	agent, err := NewAgentWithOptions(
		WithCandidateTypes([]CandidateType{CandidateTypeRelay}),
		WithNetworkTypes([]NetworkType{NetworkTypeUDP4}),
		WithRelayCandidateProvider(provider),
		WithMulticastDNSMode(MulticastDNSModeDisabled),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	gathered := make(chan struct{})
	require.NoError(t, agent.OnCandidate(func(candidate Candidate) {
		if candidate == nil {
			close(gathered)
		}
	}))
	require.NoError(t, agent.GatherCandidates())
	require.Eventually(t, func() bool {
		select {
		case <-gathered:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)

	require.Equal(t, int32(1), provider.called.Load())
	require.Equal(t, int32(1), packetConn.closeCount.Load())
	candidates, err := agent.GetLocalCandidates()
	require.NoError(t, err)
	require.Empty(t, candidates)
}
