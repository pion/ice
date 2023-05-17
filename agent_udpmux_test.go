// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"net"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v2/test"
	"github.com/stretchr/testify/require"
)

// TestMuxAgent is an end to end test over UDP mux, ensuring two agents could connect over mux
func TestMuxAgent(t *testing.T) {
	defer test.CheckRoutines(t)()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	const muxPort = 7686

	caseAddrs := map[string]*net.UDPAddr{
		"unspecified":  {Port: muxPort},
		"ipv4Loopback": {IP: net.IPv4(127, 0, 0, 1), Port: muxPort},
	}

	for subTest, addr := range caseAddrs {
		muxAddr := addr
		t.Run(subTest, func(t *testing.T) {
			require := require.New(t)

			c, err := net.ListenUDP("udp", muxAddr)
			require.NoError(err)

			loggerFactory := logging.NewDefaultLoggerFactory()
			udpMux := NewUDPMuxDefault(UDPMuxParams{
				Logger:  loggerFactory.NewLogger("ice"),
				UDPConn: c,
			})

			muxedA, err := NewAgent(&AgentConfig{
				UDPMux:         udpMux,
				CandidateTypes: []CandidateType{CandidateTypeHost},
				NetworkTypes: []NetworkType{
					NetworkTypeUDP4,
				},
			})
			require.NoError(err)

			a, err := NewAgent(&AgentConfig{
				CandidateTypes: []CandidateType{CandidateTypeHost},
				NetworkTypes:   supportedNetworkTypes(),
			})
			require.NoError(err)

			conn, muxedConn := connect(a, muxedA)

			pair := muxedA.getSelectedPair()
			require.NotNil(pair)
			require.Equal(muxPort, pair.Local.Port())

			// Send a packet to Mux
			data := []byte("hello world")
			_, err = conn.Write(data)
			require.NoError(err)

			buf := make([]byte, 1024)
			n, err := muxedConn.Read(buf)
			require.NoError(err)
			require.Equal(data, buf[:n])

			// Send a packet from Mux
			_, err = muxedConn.Write(data)
			require.NoError(err)

			n, err = conn.Read(buf)
			require.NoError(err)
			require.Equal(data, buf[:n])

			// Close it down
			require.NoError(conn.Close())
			require.NoError(muxedConn.Close())
			require.NoError(udpMux.Close())

			// Expect error when reading from closed mux
			_, err = muxedConn.Read(data)
			require.Error(err)

			// Expect error when writing to closed mux
			_, err = muxedConn.Write(data)
			require.Error(err)
		})
	}
}
