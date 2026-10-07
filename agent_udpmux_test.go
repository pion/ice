// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package ice

import (
	"net"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v5/test"
	"github.com/stretchr/testify/require"
)

// newMuxForAddr creates a UDPMuxDefault with the correct socket family for the given address.
// This fixes Windows dual-stack issues where IPv6 sockets don't receive IPv4 traffic by default.
func newMuxForAddr(t *testing.T, addr *net.UDPAddr, loggerFactory logging.LoggerFactory) *UDPMuxDefault {
	t.Helper()
	var (
		network string
		laddr   *net.UDPAddr
	)

	switch {
	case addr.IP == nil || addr.IP.IsUnspecified():
		network = "udp4"
		laddr = &net.UDPAddr{IP: net.IPv4zero, Port: addr.Port}
	case addr.IP.To4() != nil:
		network = "udp4"
		laddr = &net.UDPAddr{IP: net.IPv4zero, Port: addr.Port}
	default:
		network = "udp6"
		laddr = &net.UDPAddr{IP: net.IPv6unspecified, Port: addr.Port}
	}

	pc, err := net.ListenUDP(network, laddr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })

	return NewUDPMuxDefault(UDPMuxParams{Logger: loggerFactory.NewLogger("ice"), UDPConn: pc})
}

func TestMuxAgentPreservesPrecreatedConn(t *testing.T) {
	defer test.CheckRoutines(t)()

	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)

	mux := NewUDPMuxDefault(UDPMuxParams{UDPConn: udpConn})
	defer func() { require.NoError(t, mux.Close()) }()

	const ufrag = "precreated-ufrag"
	conn, err := mux.GetConn(ufrag, mux.LocalAddr())
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()

	wrapper, ok := conn.(*sharedAddrPortConn)
	require.True(t, ok)
	underlying, ok := wrapper.underlying.(*udpMuxedConn)
	require.True(t, ok)

	agent, err := NewAgent(&AgentConfig{
		LocalUfrag:       ufrag,
		UDPMux:           mux,
		NetworkTypes:     []NetworkType{NetworkTypeUDP4},
		CandidateTypes:   []CandidateType{CandidateTypeHost},
		MulticastDNSMode: MulticastDNSModeDisabled,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	require.False(t, underlying.isClosed(), "initialization must preserve the listener's connection")
	connection, err := mux.GetConn(ufrag, mux.LocalAddr())
	require.NoError(t, err)
	defer func() { require.NoError(t, connection.Close()) }()
	current, ok := connection.(*sharedAddrPortConn)
	require.True(t, ok)
	require.Same(t, underlying, current.underlying, "the ufrag must still route to the original connection")

	require.NoError(t, agent.Restart("restarted-ufrag", ""))
	mux.mu.Lock()
	_, exists := mux.getConn(ufrag, false)
	mux.mu.Unlock()
	require.False(t, exists, "an explicit restart must remove the old ufrag's routing")
}

// TestMuxAgent is an end to end test over UDP mux, ensuring two agents could connect over mux.
func TestMuxAgent(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	const muxPort = 7686

	caseAddrs := map[string]*net.UDPAddr{
		"unspecified":  {Port: muxPort},
		"ipv4Loopback": {IP: net.IPv4(127, 0, 0, 1), Port: muxPort},
	}

	for subTest, addr := range caseAddrs {
		muxAddr := addr
		t.Run(subTest, func(t *testing.T) {
			loggerFactory := logging.NewDefaultLoggerFactory()
			udpMux := newMuxForAddr(t, muxAddr, loggerFactory)

			muxedA, err := NewAgent(&AgentConfig{UDPMux: udpMux, CandidateTypes: []CandidateType{CandidateTypeHost}, NetworkTypes: []NetworkType{NetworkTypeUDP4}, IncludeLoopback: addr.IP.IsLoopback()})
			require.NoError(t, err)
			var muxedAClosed bool
			defer func() {
				if muxedAClosed {
					return
				}
				require.NoError(t, muxedA.Close())
			}()

			agent, err := NewAgent(&AgentConfig{CandidateTypes: []CandidateType{CandidateTypeHost}, NetworkTypes: supportedNetworkTypes()})
			require.NoError(t, err)
			var aClosed bool
			defer func() {
				if aClosed {
					return
				}
				require.NoError(t, agent.Close())
			}()

			conn, muxedConn := connect(t, agent, muxedA)

			pair := muxedA.getSelectedPair()
			require.NotNil(t, pair)
			require.Equal(t, muxPort, pair.Local.Port())

			// Send a packet to Mux
			data := []byte("hello world")
			_, err = conn.Write(data)
			require.NoError(t, err)

			buf := make([]byte, 1024)
			n, err := muxedConn.Read(buf)
			require.NoError(t, err)
			require.Equal(t, data, buf[:n])

			// Send a packet from Mux
			_, err = muxedConn.Write(data)
			require.NoError(t, err)

			n, err = conn.Read(buf)
			require.NoError(t, err)
			require.Equal(t, data, buf[:n])

			// Close it down
			require.NoError(t, conn.Close())
			aClosed = true
			require.NoError(t, muxedConn.Close())
			muxedAClosed = true
			require.NoError(t, udpMux.Close())

			// Expect error when reading from closed mux
			_, err = muxedConn.Read(data)
			require.Error(t, err)

			// Expect error when writing to closed mux
			_, err = muxedConn.Write(data)
			require.Error(t, err)
		})
	}
}
