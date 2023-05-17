// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

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

var _ TCPMux = &TCPMuxDefault{}

func TestTCPMux_Recv(t *testing.T) {
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

			defer func() {
				_ = tcpMux.Close()
			}()

			require.NotNil(tcpMux.LocalAddr())

			conn, err := net.DialTCP("tcp", nil, tcpMux.LocalAddr().(*net.TCPAddr))
			require.NoError(err, "Failed to dial test TCP connection")

			msg := stun.New()
			msg.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}
			msg.Add(stun.AttrUsername, []byte("myufrag:otherufrag"))
			msg.Encode()

			n, err := writeStreamingPacket(conn, msg.Raw)
			require.NoError(err, "Failed to write TCP STUN packet")

			pktConn, err := tcpMux.GetConnByUfrag("myufrag", false, listener.Addr().(*net.TCPAddr).IP)
			require.NoError(err, "Failed to retrieve muxed connection for ufrag")
			defer func() {
				_ = pktConn.Close()
			}()

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
		})
	}
}

func TestTCPMux_NoDeadlockWhenClosingUnusedPacketConn(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	defer test.CheckRoutines(t)()

	loggerFactory := logging.NewDefaultLoggerFactory()

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

	_, err = tcpMux.GetConnByUfrag("test", false, listener.Addr().(*net.TCPAddr).IP)
	require.NoError(err, "Failed to get connection by ufrag")

	require.NoError(tcpMux.Close(), "Failed to close TCP mux")

	conn, err := tcpMux.GetConnByUfrag("test", false, listener.Addr().(*net.TCPAddr).IP)
	assert.Nil(conn, "Should receive nil because mux is closed")
	assert.Equal(io.ErrClosedPipe, err, "Should receive error because mux is closed")
}
