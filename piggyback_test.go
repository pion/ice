// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"context"
	"net"
	"testing"

	"github.com/pion/transport/v4/test"
	"github.com/stretchr/testify/require"
)

func TestSped(t *testing.T) {
	defer test.CheckRoutines(t)()

	newSPEDTestAgent := func(t *testing.T) *Agent {
		t.Helper()

		agent, err := NewAgent(&AgentConfig{
			NetworkTypes: supportedNetworkTypes(),
		})
		require.NoError(t, err)

		return agent
	}

	remoteAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 3478}
	type piggybackSnapshot struct {
		state        piggybackingState
		packets      []packetWithCrc
		packetsIndex int
		acks         []uint32
		hasCallback  bool
		newFlight    bool
	}
	snapshotPiggyback := func(agent *Agent) piggybackSnapshot {
		agent.piggyback.mu.Lock()
		defer agent.piggyback.mu.Unlock()

		packets := make([]packetWithCrc, len(agent.piggyback.packets))
		for i, packet := range agent.piggyback.packets {
			packets[i] = packetWithCrc{
				data: clonePiggybackPacket(packet.data),
				crc:  packet.crc,
			}
		}

		return piggybackSnapshot{
			state:        agent.piggyback.state,
			packets:      packets,
			packetsIndex: agent.piggyback.packetsIndex,
			acks:         clonePiggybackAcks(agent.piggyback.acks),
			hasCallback:  agent.piggyback.dtlsCallback != nil,
			newFlight:    agent.piggyback.newFlight,
		}
	}

	t.Run("Basic embedding", func(t *testing.T) {
		aNotifier, aConnected := onConnected()
		aAgent, err := NewAgent(&AgentConfig{
			NetworkTypes: supportedNetworkTypes(),
		})
		require.NoError(t, err)
		require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))

		var toA string
		fromA := "Hello from A"
		aAgent.SetDtlsCallback(func(packet []byte, rAddr net.Addr) {
			toA = string(packet)
		})
		require.True(t, aAgent.Piggyback([]byte(fromA), true))

		bNotifier, bConnected := onConnected()
		bAgent, err := NewAgent(&AgentConfig{
			NetworkTypes: supportedNetworkTypes(),
		})
		require.NoError(t, err)
		require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))

		var toB string
		fromB := "Hello from B"
		bAgent.SetDtlsCallback(func(packet []byte, rAddr net.Addr) {
			toB = string(packet)
		})
		require.True(t, bAgent.Piggyback([]byte(fromB), true))

		gatherAndExchangeCandidates(t, aAgent, bAgent)
		go func() {
			bUfrag, bPwd, err := bAgent.GetLocalUserCredentials()
			require.NoError(t, err)
			_, err = aAgent.Accept(context.TODO(), bUfrag, bPwd)
			require.NoError(t, err)
		}()

		go func() {
			aUfrag, aPwd, err := aAgent.GetLocalUserCredentials()
			require.NoError(t, err)
			_, err = bAgent.Dial(context.TODO(), aUfrag, aPwd)
			require.NoError(t, err)
		}()

		<-aConnected
		<-bConnected
		require.NoError(t, aAgent.Close())
		require.NoError(t, bAgent.Close())

		require.Equal(t, toA, fromB)
		require.Equal(t, toB, fromA)
	})

	t.Run("Confirmed ignores empty non-SPED messages", func(t *testing.T) {
		agent := newSPEDTestAgent(t)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		fromAgent := []byte("Hello from agent")
		agent.SetDtlsCallback(func([]byte, net.Addr) {})
		require.True(t, agent.Piggyback(fromAgent, true))

		agent.ReportPiggybacking([]byte("Hello from remote"), nil, remoteAddr)
		agent.ReportPiggybacking(nil, nil, remoteAddr)

		agent.piggyback.mu.Lock()
		require.Equal(t, piggybackingState(PiggybackingStateConfirmed), agent.piggyback.state)
		agent.piggyback.mu.Unlock()

		packet, acks := agent.GetPiggybackDataAndAcks()
		require.Equal(t, fromAgent, packet)
		require.NotNil(t, acks)
	})

	t.Run("Pending completes on empty non-SPED messages", func(t *testing.T) {
		agent := newSPEDTestAgent(t)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		agent.SetDtlsCallback(func([]byte, net.Addr) {})
		require.True(t, agent.Piggyback([]byte("Hello from agent"), true))
		agent.ReportPiggybacking([]byte("Hello from remote"), nil, remoteAddr)

		require.True(t, agent.Piggyback(nil, true))
		agent.ReportPiggybacking(nil, nil, remoteAddr)

		agent.piggyback.mu.Lock()
		require.Equal(t, piggybackingState(PiggybackingStateComplete), agent.piggyback.state)
		agent.piggyback.mu.Unlock()
	})

	t.Run("Fallback buffers packets until connected", func(t *testing.T) {
		agent := newSPEDTestAgent(t)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		agent.SetDtlsCallback(func([]byte, net.Addr) {})
		agent.ReportPiggybacking(nil, nil, remoteAddr)

		packet := []byte("clienthello")
		require.True(t, agent.Piggyback(packet, true))
		packet[0] = 'X'

		snapshot := snapshotPiggyback(agent)
		require.Equal(t, piggybackingState(PiggybackingStateOff), snapshot.state)
		require.Len(t, snapshot.packets, 1)
		require.Equal(t, []byte("clienthello"), snapshot.packets[0].data)

		local, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "127.0.0.1",
			Port:      12345,
			Component: 1,
		})
		require.NoError(t, err)
		mockConn := &mockPacketConnWithCapture{}
		local.conn = mockConn

		remote, err := NewCandidateHost(&CandidateHostConfig{
			Network:   "udp",
			Address:   "127.0.0.1",
			Port:      54321,
			Component: 1,
		})
		require.NoError(t, err)

		agent.setSelectedPair(agent.addPair(local, remote))
		agent.updateConnectionState(ConnectionStateConnected)

		require.Len(t, mockConn.sentPackets, 1)
		require.Equal(t, []byte("clienthello"), mockConn.sentPackets[0])
		require.False(t, agent.Piggyback([]byte("after connection"), true))
	})

	t.Run("SetDtlsCallback resets controller", func(t *testing.T) {
		agent := newSPEDTestAgent(t)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		cb := func([]byte, net.Addr) {}
		agent.SetDtlsCallback(cb)
		require.True(t, agent.Piggyback([]byte("Hello from agent"), true))
		agent.ReportDtlsPacket([]byte("Hello from remote"))

		agent.SetDtlsCallback(nil)

		snapshot := snapshotPiggyback(agent)
		require.Equal(t, piggybackingState(PiggybackingStateOff), snapshot.state)
		require.False(t, snapshot.hasCallback)
		require.Empty(t, snapshot.packets)
		require.Zero(t, snapshot.packetsIndex)
		require.NotNil(t, snapshot.acks)
		require.Empty(t, snapshot.acks)
		require.False(t, snapshot.newFlight)

		agent.SetDtlsCallback(cb)

		snapshot = snapshotPiggyback(agent)
		require.Equal(t, piggybackingState(PiggybackingStateTentative), snapshot.state)
		require.True(t, snapshot.hasCallback)
		require.Empty(t, snapshot.packets)
		require.NotNil(t, snapshot.acks)
		require.Empty(t, snapshot.acks)
	})

	t.Run("SetDtlsCallback nil preserves pending finish", func(t *testing.T) {
		agent := newSPEDTestAgent(t)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		agent.SetDtlsCallback(func([]byte, net.Addr) {})
		agent.ReportDtlsPacket([]byte("acked packet"))
		require.True(t, agent.Piggyback(nil, true))
		agent.SetDtlsCallback(nil)

		snapshot := snapshotPiggyback(agent)
		require.Equal(t, piggybackingState(PiggybackingStatePending), snapshot.state)
		require.False(t, snapshot.hasCallback)
		require.Empty(t, snapshot.packets)
		require.NotNil(t, snapshot.acks)

		packet, acks := agent.GetPiggybackDataAndAcks()
		require.Nil(t, packet)
		require.NotNil(t, acks)

		agent.ReportPiggybacking(nil, nil, remoteAddr)
		snapshot = snapshotPiggyback(agent)
		require.Equal(t, piggybackingState(PiggybackingStateComplete), snapshot.state)

		agent.connectionState = ConnectionStateConnected
		require.False(t, agent.Piggyback([]byte("after completion"), true))
	})

	t.Run("Packet and ACK snapshots are defensive copies", func(t *testing.T) {
		agent := newSPEDTestAgent(t)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		packet := []byte("immutable packet")
		agent.SetDtlsCallback(func([]byte, net.Addr) {})
		require.True(t, agent.Piggyback(packet, true))
		packet[0] = 'X'

		gotPacket, _ := agent.GetPiggybackDataAndAcks()
		require.Equal(t, []byte("immutable packet"), gotPacket)

		agent.ReportDtlsPacket([]byte("acked packet"))
		_, gotAcks := agent.GetPiggybackDataAndAcks()
		require.Len(t, gotAcks, 1)
		wantAck := gotAcks[0]

		gotAcks[0]++
		_, gotAcks = agent.GetPiggybackDataAndAcks()
		require.Len(t, gotAcks, 1)
		require.Equal(t, wantAck, gotAcks[0])
	})

	t.Run("New flight reset clears round-robin index", func(t *testing.T) {
		agent := newSPEDTestAgent(t)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		agent.SetDtlsCallback(func([]byte, net.Addr) {})
		require.True(t, agent.Piggyback([]byte("flight 1 packet 1"), false))
		require.True(t, agent.Piggyback([]byte("flight 1 packet 2"), true))

		packet, _ := agent.GetPiggybackDataAndAcks()
		require.Equal(t, []byte("flight 1 packet 1"), packet)

		require.True(t, agent.Piggyback([]byte("flight 2 packet 1"), true))
		require.NotPanics(t, func() {
			packet, _ = agent.GetPiggybackDataAndAcks()
		})
		require.Equal(t, []byte("flight 2 packet 1"), packet)
	})
}
