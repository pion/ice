// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"io"
	"net"
	"testing"

	"github.com/pion/logging"
	"github.com/pion/stun"
	"github.com/pion/transport/v2/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMultiTCPMux_Recv(t *testing.T) {
	for name, bufSize := range map[string]int{
		"no buffer":    0,
		"buffered 4MB": 4 * 1024 * 1024,
	} {
		bufSize := bufSize
		t.Run(name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)

			report := test.CheckRoutines(t)
			defer report()

			loggerFactory := logging.NewDefaultLoggerFactory()

			var muxInstances []TCPMux
			for i := 0; i < 3; i++ {
				listener, err := net.ListenTCP("tcp", &net.TCPAddr{
					IP:   net.IP{127, 0, 0, 1},
					Port: 0,
				})
				require.NoError(err, "Failed to start listener")
				defer func() {
					_ = listener.Close()
				}()

				tcpMux := NewTCPMuxDefault(TCPMuxParams{
					Listener:        listener,
					Logger:          loggerFactory.NewLogger("ice"),
					ReadBufferSize:  20,
					WriteBufferSize: bufSize,
				})
				muxInstances = append(muxInstances, tcpMux)
				require.NotNil(tcpMux.LocalAddr())
			}

			multiMux := NewMultiTCPMuxDefault(muxInstances...)
			defer func() {
				_ = multiMux.Close()
			}()

			pktConns, err := multiMux.GetAllConns("myufrag", false, net.IP{127, 0, 0, 1})
			require.NoError(err, "Failed to retrieve muxed connection for ufrag")

			for _, pktConn := range pktConns {
				defer func() {
					_ = pktConn.Close()
				}()
				conn, err := net.DialTCP("tcp", nil, pktConn.LocalAddr().(*net.TCPAddr))
				require.NoError(err, "Failed to dial test TCP connection")

				msg := stun.New()
				msg.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}
				msg.Add(stun.AttrUsername, []byte("myufrag:otherufrag"))
				msg.Encode()

				n, err := writeStreamingPacket(conn, msg.Raw)
				require.NoError(err, "Failed to write TCP STUN packet")

				recv := make([]byte, n)
				n2, rAddr, err := pktConn.ReadFrom(recv)
				require.NoError(err, "Failed to receive data")
				assert.Equal(conn.LocalAddr(), rAddr, "Remote TCP address mismatch")
				assert.Equal(n, n2, "Received byte size mismatch")
				assert.Equal(msg.Raw, recv, "Received bytes mismatch")

				// Check echo response
				n, err = pktConn.WriteTo(recv, conn.LocalAddr())
				require.NoError(err, "Failed to write echo STUN packet")
				recvEcho := make([]byte, n)
				n3, err := readStreamingPacket(conn, recvEcho)
				require.NoError(err, "Failed to receive echo data")
				assert.Equal(n2, n3, "Received byte size mismatch")
				assert.Equal(msg.Raw, recvEcho, "Received bytes mismatch")
			}
		})
	}
}

func TestMultiTCPMux_NoDeadlockWhenClosingUnusedPacketConn(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	defer test.CheckRoutines(t)()

	loggerFactory := logging.NewDefaultLoggerFactory()

	var tcpMuxInstances []TCPMux
	for i := 0; i < 3; i++ {
		listener, err := net.ListenTCP("tcp", &net.TCPAddr{
			IP:   net.IP{127, 0, 0, 1},
			Port: 0,
		})
		require.NoError(err, "Failed to start listener")
		defer func() {
			_ = listener.Close()
		}()

		tcpMux := NewTCPMuxDefault(TCPMuxParams{
			Listener:       listener,
			Logger:         loggerFactory.NewLogger("ice"),
			ReadBufferSize: 20,
		})
		tcpMuxInstances = append(tcpMuxInstances, tcpMux)
	}
	muxMulti := NewMultiTCPMuxDefault(tcpMuxInstances...)

	_, err := muxMulti.GetAllConns("test", false, net.IP{127, 0, 0, 1})
	require.NoError(err, "Failed to get connection by ufrag")

	require.NoError(muxMulti.Close(), "Failed to close TCP mux")

	conn, err := muxMulti.GetAllConns("test", false, net.IP{127, 0, 0, 1})
	assert.Nil(conn, "Should receive nil because mux is closed")
	assert.Equal(io.ErrClosedPipe, err, "Should receive error because mux is closed")
}
