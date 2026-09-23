// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"hash/crc32"
	"net"
	"strconv"
	"testing"

	"github.com/pion/dtls/v3/pkg/protocol"
	"github.com/pion/logging"
	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5/test"
	"github.com/stretchr/testify/require"
)

// fakeDtlsPacket prefixes the payload with a DTLS 1.2 handshake record header
// so it is recognized as a DTLS packet.
func fakeDtlsPacket(payload string) []byte {
	return append([]byte{22, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, []byte(payload)...)
}

func TestSped(t *testing.T) {
	defer test.CheckRoutines(t)()
	gatherOptions := []GatherOption{WithNetworkTypes(supportedNetworkTypes())}

	t.Run("Basic embedding", func(t *testing.T) {
		aAgent, err := NewAgent()
		require.NoError(t, err)

		var toA []byte
		fromA := fakeDtlsPacket("Hello from A")
		aAgent.SetDTLSCallback(func(packet []byte, rAddr net.Addr) {
			toA = packet
		})
		require.True(t, aAgent.Piggyback([][]byte{fromA}, nil))

		bAgent, err := NewAgent()
		require.NoError(t, err)

		var toB []byte
		fromB := fakeDtlsPacket("Hello from B")
		bAgent.SetDTLSCallback(func(packet []byte, rAddr net.Addr) {
			toB = packet
		})
		require.True(t, bAgent.Piggyback([][]byte{fromB}, nil))

		connect(t, aAgent, bAgent, gatherOptions, gatherOptions)
		require.NoError(t, aAgent.Close())
		require.NoError(t, bAgent.Close())

		require.Equal(t, toA, fromB)
		require.Equal(t, toB, fromA)
	})

	t.Run("Fallback to plain DTLS", func(t *testing.T) {
		aAgent, err := NewAgent()
		require.NoError(t, err)

		fromA := fakeDtlsPacket("Hello from A")
		aAgent.SetDTLSCallback(func([]byte, net.Addr) {})
		require.True(t, aAgent.Piggyback([][]byte{fromA}, nil))

		// bAgent does not support piggybacking.
		bAgent, err := NewAgent()
		require.NoError(t, err)

		aConn, bConn := connect(t, aAgent, bAgent, gatherOptions, gatherOptions)

		toB := make([]byte, len(fromA))
		_, err = bConn.Read(toB)
		require.NoError(t, err)
		require.Equal(t, fromA, toB)
		require.Equal(t, piggybackingStateOff, aAgent.piggyback.state)

		require.NoError(t, aConn.Close())
		require.NoError(t, bConn.Close())
	})
}

func newPiggybackAgent(t *testing.T) *Agent {
	t.Helper()

	agent := &Agent{log: logging.NewDefaultLoggerFactory().NewLogger("ice")}
	agent.SetDTLSCallback(func([]byte, net.Addr) {
		require.True(t, agent.piggyback.mu.TryLock(), "callback must run after unlocking")
		agent.piggyback.mu.Unlock()
	})

	return agent
}

func TestPiggybackingStateMachine(t *testing.T) {
	rAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4242}
	packet := fakeDtlsPacket("flight")
	packetCrc := crc32.ChecksumIEEE(packet)

	t.Run("Does not complete before the local handshake is done", func(t *testing.T) {
		agent := newPiggybackAgent(t)
		require.Equal(t, []stun.Setter{DTLSInSTUNAckAttribute{}}, agent.appendPiggybackAttributes(nil))
		agent.reportPiggybacking(packet, []uint32{}, rAddr)
		require.Equal(t, piggybackingStateConfirmed, agent.piggyback.state)
		require.Equal(t, []stun.Setter{DTLSInSTUNAckAttribute{packetCrc}, DTLSInSTUNAttribute{}}, agent.appendPiggybackAttributes(nil))

		agent.reportPiggybacking(nil, nil, rAddr)
		require.Equal(t, piggybackingStateConfirmed, agent.piggyback.state)
	})

	t.Run("Attributes own their buffers and rotate queued packets", func(t *testing.T) {
		agent := newPiggybackAgent(t)
		agent.ReportDTLSPacket(packet)
		other := fakeDtlsPacket("next flight")
		require.True(t, agent.Piggyback([][]byte{packet, other}, nil))
		attrs := agent.appendPiggybackAttributes(nil)
		agent.piggyback.packets[0].data[0] ^= 1
		agent.piggyback.acks[0] ^= 1
		require.Equal(t, []stun.Setter{DTLSInSTUNAckAttribute{packetCrc}, DTLSInSTUNAttribute(packet)}, attrs)
		require.Equal(t, DTLSInSTUNAttribute(other), agent.appendPiggybackAttributes(nil)[1])
	})

	t.Run("Completes when the peer stops sending acks", func(t *testing.T) {
		agent := newPiggybackAgent(t)
		agent.reportPiggybacking(packet, []uint32{}, rAddr)
		agent.SetDTLSHandshakeComplete(true, protocol.Version1_2)
		require.Equal(t, piggybackingStatePending, agent.piggyback.state)

		agent.reportPiggybacking(nil, nil, rAddr)
		require.Equal(t, piggybackingStateComplete, agent.piggyback.state)

		require.Empty(t, agent.appendPiggybackAttributes(nil))

		agent.SetDTLSHandshakeComplete(true, protocol.Version1_2)
		require.Equal(t, piggybackingStateComplete, agent.piggyback.state)
	})

	t.Run("Completes on the ack of the final flight", func(t *testing.T) {
		agent := newPiggybackAgent(t)
		require.True(t, agent.Piggyback([][]byte{packet}, nil))
		agent.reportPiggybacking(packet, []uint32{}, rAddr)
		// DTLS 1.2 server keeps the last flight until it is acknowledged.
		agent.SetDTLSHandshakeComplete(false, protocol.Version1_2)

		agent.reportPiggybacking(nil, []uint32{packetCrc}, rAddr)
		require.Equal(t, piggybackingStateComplete, agent.piggyback.state)
	})

	t.Run("Acks are kept when the peer sends no data", func(t *testing.T) {
		agent := newPiggybackAgent(t)
		agent.reportPiggybacking(packet, []uint32{}, rAddr)
		require.Equal(t, []uint32{packetCrc}, agent.piggyback.acks)

		agent.reportPiggybacking(nil, []uint32{}, rAddr)
		require.Equal(t, []uint32{packetCrc}, agent.piggyback.acks)
	})

	t.Run("Non-DTLS packets are not embedded", func(t *testing.T) {
		agent := newPiggybackAgent(t)
		require.False(t, agent.Piggyback([][]byte{[]byte("not a dtls packet")}, nil))
		require.Empty(t, agent.piggyback.packets)
	})

	t.Run("Non-DTLS data is dropped", func(t *testing.T) {
		agent := newPiggybackAgent(t)
		received := false
		agent.SetDTLSCallback(func([]byte, net.Addr) { received = true })

		agent.reportPiggybacking([]byte("not a dtls packet"), []uint32{}, rAddr)
		require.False(t, received)
		require.Empty(t, agent.piggyback.acks)
	})

	t.Run("The last flight is kept by the party that sends it", func(t *testing.T) {
		// The party sending the last flight keeps it around until it gets
		// acknowledged: the server in DTLS 1.2, the client in DTLS 1.3.
		for _, tc := range []struct {
			name        string
			isClient    bool
			version     protocol.Version
			wantPackets int
		}{
			{"DTLS 1.2 client", true, protocol.Version1_2, 0},
			{"DTLS 1.2 server", false, protocol.Version1_2, 1},
			{"DTLS 1.3 client", true, protocol.Version1_3, 1},
			{"DTLS 1.3 server", false, protocol.Version1_3, 0},
		} {
			t.Run(tc.name, func(t *testing.T) {
				agent := newPiggybackAgent(t)
				require.True(t, agent.Piggyback([][]byte{packet}, nil))
				agent.SetDTLSHandshakeComplete(tc.isClient, tc.version)

				require.Len(t, agent.piggyback.packets, tc.wantPackets)
			})
		}
	})

	t.Run("A failed DTLS handshake disables piggybacking", func(t *testing.T) {
		agent := newPiggybackAgent(t)
		agent.SetDTLSFailed()
		require.Equal(t, piggybackingStateOff, agent.piggyback.state)

		require.Empty(t, agent.appendPiggybackAttributes(nil))
	})

	t.Run("At most four packets are acked", func(t *testing.T) {
		agent := newPiggybackAgent(t)
		for i := range 6 {
			agent.reportPiggybacking(fakeDtlsPacket("in stun "+strconv.Itoa(i)), nil, rAddr)
			agent.ReportDTLSPacket(fakeDtlsPacket("plain " + strconv.Itoa(i)))
		}
		require.Len(t, agent.piggyback.acks, 4)

		agent.SetDTLSFailed()
		agent.ReportDTLSPacket(packet)
		require.Len(t, agent.piggyback.acks, 4)
	})

	t.Run("Packets are flushed when the peer does not support piggybacking", func(t *testing.T) {
		agent := &Agent{log: logging.NewDefaultLoggerFactory().NewLogger("ice")}

		require.True(t, agent.Piggyback([][]byte{packet}, nil))
		require.True(t, agent.Piggyback([][]byte{packet}, nil))
		require.Len(t, agent.piggyback.flushOnConnected(), 1)
		require.False(t, agent.Piggyback([][]byte{packet}, nil))
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
		require.Equal(t, piggybackingStateTentative, agent.piggyback.state)
	})
}
