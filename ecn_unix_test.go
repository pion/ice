// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build linux || darwin || freebsd

package ice

import (
	"net"
	"testing"
	"time"

	"github.com/pion/transport/v5/packetio"
	"github.com/pion/transport/v5/test"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func TestECN(t *testing.T) {
	defer test.TimeOut(10 * time.Second).Stop()
	_, ok := parseECN([]byte{1})
	require.False(t, ok, "short control messages must be ignored")
	reader, err := newECNPacketReader(&embeddingUDPConnWrapper{})
	require.NoError(t, err)
	require.Nil(t, reader, "embedded UDP methods must not bypass wrappers")

	sender, receiver := pipe(t, []AgentOption{WithNetworkTypes([]NetworkType{NetworkTypeUDP4}), WithIncludeLoopback(), WithIPFilter(net.IP.IsLoopback), WithMulticastDNSMode(MulticastDNSModeDisabled)})
	defer closePipe(t, sender, receiver)
	require.NoError(t, receiver.SetReadDeadline(time.Now().Add(5*time.Second)))
	local, ok := sender.agent.getSelectedPair().Local.(*CandidateHost)
	require.True(t, ok)
	udp := ipv4.NewPacketConn(local.conn)
	buf := make([]byte, 1)
	var attrs packetio.Attributes
	for codepoint := range uint8(4) {
		require.NoError(t, udp.SetTOS(0xb8|int(codepoint)))
		_, err = sender.Write([]byte{codepoint})
		require.NoError(t, err)
		_, attrs, err = receiver.ReadWithAttributes(buf, attrs)
		require.NoError(t, err)
		ecn, present := ECNFromAttributes(attrs)
		require.True(t, present)
		require.Equal(t, ECN(codepoint), ecn)
		require.Equal(t, codepoint, buf[0])

		ecn, present = parseECN((&ipv6.ControlMessage{TrafficClass: 0xb8 | int(codepoint)}).Marshal())
		require.True(t, present)
		require.Equal(t, ECN(codepoint), ecn)
	}
	_, err = receiver.agent.buf.Write(buf, nil)
	require.NoError(t, err)
	_, attrs, err = receiver.ReadWithAttributes(buf, attrs)
	require.NoError(t, err)
	require.Empty(t, attrs, "metadata must not leak between packets")
	require.Equal(t, uint64(5), receiver.BytesReceived())
}
