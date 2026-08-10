// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"context"
	"hash/crc32"
	"net"
	"strconv"
	"testing"

	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/pion/transport/v4/test"
	"github.com/stretchr/testify/require"
)

// fakeDtlsPacket prefixes the payload with a DTLS 1.2 handshake record header
// so it is recognized as a DTLS packet.
func fakeDtlsPacket(payload string) []byte {
	return append([]byte{22, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, []byte(payload)...)
}

func TestSped(t *testing.T) {
	defer test.CheckRoutines(t)()

	t.Run("Basic embedding", func(t *testing.T) {
		aNotifier, aConnected := onConnected()
		aAgent, err := NewAgent(&AgentConfig{
			NetworkTypes: supportedNetworkTypes(),
		})
		require.NoError(t, err)
		require.NoError(t, aAgent.OnConnectionStateChange(aNotifier))

		var toA []byte
		fromA := fakeDtlsPacket("Hello from A")
		aAgent.SetDtlsCallback(func(packet []byte, rAddr net.Addr) {
			toA = packet
		})
		require.True(t, aAgent.Piggyback(fromA, true))

		bNotifier, bConnected := onConnected()
		bAgent, err := NewAgent(&AgentConfig{
			NetworkTypes: supportedNetworkTypes(),
		})
		require.NoError(t, err)
		require.NoError(t, bAgent.OnConnectionStateChange(bNotifier))

		var toB []byte
		fromB := fakeDtlsPacket("Hello from B")
		bAgent.SetDtlsCallback(func(packet []byte, rAddr net.Addr) {
			toB = packet
		})
		require.True(t, bAgent.Piggyback(fromB, true))

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

	t.Run("Fallback to plain DTLS", func(t *testing.T) {
		aAgent, err := NewAgent(&AgentConfig{
			NetworkTypes: supportedNetworkTypes(),
		})
		require.NoError(t, err)

		fromA := fakeDtlsPacket("Hello from A")
		aAgent.SetDtlsCallback(func([]byte, net.Addr) {})
		require.True(t, aAgent.Piggyback(fromA, true))

		// bAgent does not support piggybacking.
		bAgent, err := NewAgent(&AgentConfig{
			NetworkTypes: supportedNetworkTypes(),
		})
		require.NoError(t, err)

		aConn, bConn := connect(t, aAgent, bAgent)

		toB := make([]byte, len(fromA))
		_, err = bConn.Read(toB)
		require.NoError(t, err)
		require.Equal(t, fromA, toB)
		require.Equal(t, PiggybackingStateOff, aAgent.piggyback.state)

		require.NoError(t, aConn.Close())
		require.NoError(t, bConn.Close())
	})
}

func newPiggybackAgent(t *testing.T) *Agent {
	t.Helper()

	agent := &Agent{log: logging.NewDefaultLoggerFactory().NewLogger("ice")}
	agent.piggyback.init()
	agent.SetDtlsCallback(func([]byte, net.Addr) {})

	return agent
}

func TestPiggybackingStateMachine(t *testing.T) {
	rAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4242}
	packet := fakeDtlsPacket("flight")
	packetCrc := crc32.ChecksumIEEE(packet)

	t.Run("Does not complete before the local handshake is done", func(t *testing.T) {
		agent := newPiggybackAgent(t)
		agent.ReportPiggybacking(packet, []uint32{}, rAddr)
		require.Equal(t, PiggybackingStateConfirmed, agent.piggyback.state)

		agent.ReportPiggybacking(nil, nil, rAddr)
		require.Equal(t, PiggybackingStateConfirmed, agent.piggyback.state)
	})

	t.Run("Completes when the peer stops sending acks", func(t *testing.T) {
		agent := newPiggybackAgent(t)
		agent.ReportPiggybacking(packet, []uint32{}, rAddr)
		require.True(t, agent.Piggyback(nil, true))
		require.Equal(t, PiggybackingStatePending, agent.piggyback.state)

		agent.ReportPiggybacking(nil, nil, rAddr)
		require.Equal(t, PiggybackingStateComplete, agent.piggyback.state)

		data, acks := agent.GetPiggybackDataAndAcks()
		require.Nil(t, data)
		require.Nil(t, acks)
	})

	t.Run("Completes on the ack of the final flight", func(t *testing.T) {
		agent := newPiggybackAgent(t)
		require.True(t, agent.Piggyback(packet, true))
		agent.ReportPiggybacking(packet, []uint32{}, rAddr)
		require.True(t, agent.Piggyback(nil, true))

		agent.ReportPiggybacking(nil, []uint32{packetCrc}, rAddr)
		require.Equal(t, PiggybackingStateComplete, agent.piggyback.state)
	})

	t.Run("Does not move back to pending once complete", func(t *testing.T) {
		agent := newPiggybackAgent(t)
		agent.ReportPiggybacking(packet, []uint32{}, rAddr)
		require.True(t, agent.Piggyback(nil, true))
		agent.ReportPiggybacking(nil, nil, rAddr)
		require.Equal(t, PiggybackingStateComplete, agent.piggyback.state)

		require.True(t, agent.Piggyback(nil, true))
		require.Equal(t, PiggybackingStateComplete, agent.piggyback.state)
	})

	t.Run("Acks are kept when the peer sends no data", func(t *testing.T) {
		agent := newPiggybackAgent(t)
		agent.ReportPiggybacking(packet, []uint32{}, rAddr)
		require.Equal(t, []uint32{packetCrc}, agent.piggyback.acks)

		agent.ReportPiggybacking(nil, []uint32{}, rAddr)
		require.Equal(t, []uint32{packetCrc}, agent.piggyback.acks)
	})

	t.Run("Non-DTLS packets are not embedded", func(t *testing.T) {
		agent := newPiggybackAgent(t)
		require.False(t, agent.Piggyback([]byte("not a dtls packet"), true))
		require.Empty(t, agent.piggyback.packets)
	})

	t.Run("Non-DTLS data is dropped", func(t *testing.T) {
		agent := newPiggybackAgent(t)
		received := false
		agent.SetDtlsCallback(func([]byte, net.Addr) { received = true })

		agent.ReportPiggybacking([]byte("not a dtls packet"), []uint32{}, rAddr)
		require.False(t, received)
		require.Empty(t, agent.piggyback.acks)
	})

	t.Run("The last flight is dropped by the DTLS client only", func(t *testing.T) {
		for _, isClient := range []bool{true, false} {
			agent := newPiggybackAgent(t)
			agent.SetDtlsRole(isClient)
			require.True(t, agent.Piggyback(packet, true))
			require.True(t, agent.Piggyback(nil, true))

			if isClient {
				require.Empty(t, agent.piggyback.packets)
			} else {
				require.Len(t, agent.piggyback.packets, 1)
			}
		}
	})

	t.Run("A failed DTLS handshake disables piggybacking", func(t *testing.T) {
		agent := newPiggybackAgent(t)
		agent.SetDtlsFailed()
		require.Equal(t, PiggybackingStateOff, agent.piggyback.state)

		data, acks := agent.GetPiggybackDataAndAcks()
		require.Nil(t, data)
		require.Nil(t, acks)
	})

	t.Run("At most four packets are acked", func(t *testing.T) {
		agent := newPiggybackAgent(t)
		for i := range 6 {
			agent.ReportPiggybacking(fakeDtlsPacket("in stun "+strconv.Itoa(i)), nil, rAddr)
			agent.ReportDtlsPacket(fakeDtlsPacket("plain " + strconv.Itoa(i)))
		}
		require.Len(t, agent.piggyback.acks, 4)

		agent.SetDtlsFailed()
		agent.ReportDtlsPacket(packet)
		require.Len(t, agent.piggyback.acks, 4)
	})

	t.Run("Packets are flushed when the peer does not support piggybacking", func(t *testing.T) {
		agent := &Agent{log: logging.NewDefaultLoggerFactory().NewLogger("ice")}
		agent.piggyback.init()

		require.True(t, agent.Piggyback(packet, true))
		require.True(t, agent.Piggyback(packet, true))
		require.Len(t, agent.piggyback.flushOnConnected(), 1)
		require.False(t, agent.Piggyback(packet, true))
	})

	t.Run("Malformed acks do not disable piggybacking", func(t *testing.T) {
		remote, err := NewCandidateHost(&CandidateHostConfig{
			Network: NetworkTypeUDP4.String(),
			Address: localhostIPStr,
			Port:    4242,
		})
		require.NoError(t, err)

		agent := newPiggybackAgent(t)
		message, err := stun.Build(stun.BindingRequest, stun.TransactionID)
		require.NoError(t, err)
		message.Add(stun.AttrDtlsInStunAck, []byte{0x01, 0x02, 0x03})

		agent.reportPiggybackingFromMessage(message, remote)
		require.Equal(t, PiggybackingStateTentative, agent.piggyback.state)
	})
}
