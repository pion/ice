// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package ice

import (
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/pion/transport/v3/test"
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
			defer test.CheckRoutines(t)()

			loggerFactory := logging.NewDefaultLoggerFactory()

			listener, err := net.ListenTCP("tcp", &net.TCPAddr{
				IP:   net.IP{127, 0, 0, 1},
				Port: 0,
			})
			require.NoError(t, err, "error starting listener")
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

			require.NotNil(t, tcpMux.LocalAddr(), "tcpMux.LocalAddr() is nil")

			conn, err := net.DialTCP("tcp", nil, tcpMux.LocalAddr().(*net.TCPAddr)) // nolint
			require.NoError(t, err, "error dialing test TCP connection")

			msg := stun.New()
			msg.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}
			msg.Add(stun.AttrUsername, []byte("myufrag:otherufrag"))
			msg.Encode()

			n, err := writeStreamingPacket(conn, msg.Raw)
			require.NoError(t, err, "error writing TCP STUN packet")

			listenerAddr, ok := listener.Addr().(*net.TCPAddr)
			require.True(t, ok)

			pktConn, err := tcpMux.GetConnByUfrag("myufrag", false, listenerAddr.IP)
			require.NoError(t, err, "error retrieving muxed connection for ufrag")
			defer func() {
				_ = pktConn.Close()
			}()

			recv := make([]byte, n)
			n2, rAddr, err := pktConn.ReadFrom(recv)
			require.NoError(t, err, "error receiving data")
			require.Equal(t, conn.LocalAddr(), rAddr, "remote tcp address mismatch")
			require.Equal(t, n, n2, "received byte size mismatch")
			require.Equal(t, msg.Raw, recv, "received bytes mismatch")

			// Check echo response
			n, err = pktConn.WriteTo(recv, conn.LocalAddr())
			require.NoError(t, err, "error writing echo STUN packet")
			recvEcho := make([]byte, n)
			n3, err := readStreamingPacket(conn, recvEcho)
			require.NoError(t, err, "error receiving echo data")
			require.Equal(t, n2, n3, "received byte size mismatch")
			require.Equal(t, msg.Raw, recvEcho, "received bytes mismatch")
		})
	}
}

func TestTCPMux_NoDeadlockWhenClosingUnusedPacketConn(t *testing.T) {
	defer test.CheckRoutines(t)()

	loggerFactory := logging.NewDefaultLoggerFactory()

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{
		IP:   net.IP{127, 0, 0, 1},
		Port: 0,
	})
	require.NoError(t, err, "error starting listener")
	defer func() {
		_ = listener.Close()
	}()

	tcpMux := NewTCPMuxDefault(TCPMuxParams{
		Listener:       listener,
		Logger:         loggerFactory.NewLogger("ice"),
		ReadBufferSize: 20,
	})

	defer func() {
		_ = tcpMux.Close()
	}()

	listenerAddr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)

	_, err = tcpMux.GetConnByUfrag("test", false, listenerAddr.IP)
	require.NoError(t, err, "error getting conn by ufrag")

	require.NoError(t, tcpMux.Close(), "error closing tcpMux")

	conn, err := tcpMux.GetConnByUfrag("test", false, listenerAddr.IP)
	require.Nil(t, conn, "should receive nil because mux is closed")
	require.Equal(t, io.ErrClosedPipe, err, "should receive error because mux is closed")
}

func TestTCPMux_FirstPacketTimeout(t *testing.T) {
	defer test.CheckRoutines(t)()

	loggerFactory := logging.NewDefaultLoggerFactory()

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{
		IP:   net.IP{127, 0, 0, 1},
		Port: 0,
	})
	require.NoError(t, err, "error starting listener")
	defer func() {
		_ = listener.Close()
	}()

	tcpMux := NewTCPMuxDefault(TCPMuxParams{
		Listener:             listener,
		Logger:               loggerFactory.NewLogger("ice"),
		ReadBufferSize:       20,
		FirstStunBindTimeout: time.Second,
	})
	defer func() {
		_ = tcpMux.Close()
	}()

	require.NotNil(t, tcpMux.LocalAddr(), "tcpMux.LocalAddr() is nil")

	conn, err := net.DialTCP("tcp", nil, tcpMux.LocalAddr().(*net.TCPAddr)) // nolint
	require.NoError(t, err, "error dialing test TCP connection")
	defer func() {
		_ = conn.Close()
	}()

	// Don't send any data, the mux should close the connection after the timeout
	time.Sleep(1500 * time.Millisecond)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	require.ErrorIs(t, err, io.EOF)
}

func TestTCPMux_NoLeakForConnectionFromStun(t *testing.T) {
	defer test.CheckRoutines(t)()

	loggerFactory := logging.NewDefaultLoggerFactory()

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{
		IP:   net.IP{127, 0, 0, 1},
		Port: 0,
	})
	require.NoError(t, err, "error starting listener")
	defer func() {
		_ = listener.Close()
	}()

	tcpMux := NewTCPMuxDefault(TCPMuxParams{
		Listener:                     listener,
		Logger:                       loggerFactory.NewLogger("ice"),
		ReadBufferSize:               20,
		AliveDurationForConnFromStun: time.Second,
	})

	defer func() {
		_ = tcpMux.Close()
	}()

	require.NotNil(t, tcpMux.LocalAddr(), "tcpMux.LocalAddr() is nil")

	t.Run("close connection from stun msg after timeout", func(t *testing.T) {
		conn, err := net.DialTCP("tcp", nil, tcpMux.LocalAddr().(*net.TCPAddr)) // nolint
		require.NoError(t, err, "error dialing test TCP connection")
		defer func() {
			_ = conn.Close()
		}()

		msg, err := stun.Build(stun.BindingRequest, stun.TransactionID,
			stun.NewUsername("myufrag:otherufrag"),
			stun.NewShortTermIntegrity("myufrag"),
			stun.Fingerprint,
		)
		require.NoError(t, err, "error building STUN packet")
		msg.Encode()

		_, err = writeStreamingPacket(conn, msg.Raw)
		require.NoError(t, err, "error writing TCP STUN packet")

		time.Sleep(1500 * time.Millisecond)
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
		buf := make([]byte, 1)
		_, err = conn.Read(buf)
		require.ErrorIs(t, err, io.EOF)
	})

	t.Run("connection keep alive if access by user", func(t *testing.T) {
		conn, err := net.DialTCP("tcp", nil, tcpMux.LocalAddr().(*net.TCPAddr)) // nolint
		require.NoError(t, err, "error dialing test TCP connection")
		defer func() {
			_ = conn.Close()
		}()

		msg, err := stun.Build(stun.BindingRequest, stun.TransactionID,
			stun.NewUsername("myufrag2:otherufrag2"),
			stun.NewShortTermIntegrity("myufrag2"),
			stun.Fingerprint,
		)
		require.NoError(t, err, "error building STUN packet")
		msg.Encode()

		n, err := writeStreamingPacket(conn, msg.Raw)
		require.NoError(t, err, "error writing TCP STUN packet")

		// wait for the connection to be created
		time.Sleep(100 * time.Millisecond)

		listenerAddr, ok := listener.Addr().(*net.TCPAddr)
		require.True(t, ok)

		pktConn, err := tcpMux.GetConnByUfrag("myufrag2", false, listenerAddr.IP)
		require.NoError(t, err, "error retrieving muxed connection for ufrag")
		defer func() {
			_ = pktConn.Close()
		}()

		time.Sleep(1500 * time.Millisecond)

		// timeout, not closed
		buf := make([]byte, 1024)
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(100*time.Millisecond)))
		_, err = conn.Read(buf)
		require.ErrorIs(t, err, os.ErrDeadlineExceeded)

		recv := make([]byte, n)
		n2, rAddr, err := pktConn.ReadFrom(recv)
		require.NoError(t, err, "error receiving data")
		require.Equal(t, conn.LocalAddr(), rAddr, "remote tcp address mismatch")
		require.Equal(t, n, n2, "received byte size mismatch")
		require.Equal(t, msg.Raw, recv, "received bytes mismatch")
	})
}
