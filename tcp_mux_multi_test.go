// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"errors"
	"io"
	"net"
	"testing"

	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/pion/transport/v4/test"
	"github.com/stretchr/testify/require"
)

func TestMultiTCPMux_Recv(t *testing.T) {
	for name, bufSize := range map[string]int{
		"no buffer":    0,
		"buffered 4MB": 4 * 1024 * 1024,
	} {
		bufSize := bufSize
		t.Run(name, func(t *testing.T) {
			defer test.CheckRoutines(t)()

			loggerFactory := logging.NewDefaultLoggerFactory()

			var muxInstances []TCPMux
			for i := 0; i < 3; i++ {
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
				muxInstances = append(muxInstances, tcpMux)
				require.NotNil(t, tcpMux.LocalAddr(), "tcpMux.LocalAddr() is nil")
			}

			multiMux := NewMultiTCPMuxDefault(muxInstances...)
			defer func() {
				_ = multiMux.Close()
			}()

			pktConns, err := multiMux.GetAllConns("myufrag", false, net.IP{127, 0, 0, 1})
			require.NoError(t, err, "error retrieving muxed connection for ufrag")

			for _, pktConn := range pktConns {
				defer func() {
					_ = pktConn.Close()
				}()
				conn, err := net.DialTCP("tcp", nil, pktConn.LocalAddr().(*net.TCPAddr)) // nolint
				require.NoError(t, err, "error dialing test TCP connection")

				msg := stun.New()
				msg.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}
				msg.Add(stun.AttrUsername, []byte("myufrag:otherufrag"))
				msg.Encode()

				n, err := writeStreamingPacket(conn, msg.Raw)
				require.NoError(t, err, "error writing TCP STUN packet")

				recv := make([]byte, n)
				n2, rAddr, err := pktConn.ReadFrom(recv)
				require.NoError(t, err, "error receiving data")
				require.Equal(t, conn.LocalAddr(), rAddr, "remote TCP address mismatch")
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
			}
		})
	}
}

func TestMultiTCPMux_NoDeadlockWhenClosingUnusedPacketConn(t *testing.T) {
	defer test.CheckRoutines(t)()

	loggerFactory := logging.NewDefaultLoggerFactory()

	var tcpMuxInstances []TCPMux
	for i := 0; i < 3; i++ {
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
		tcpMuxInstances = append(tcpMuxInstances, tcpMux)
	}
	muxMulti := NewMultiTCPMuxDefault(tcpMuxInstances...)

	_, err := muxMulti.GetAllConns("test", false, net.IP{127, 0, 0, 1})
	require.NoError(t, err, "error getting conn by ufrag")

	require.NoError(t, muxMulti.Close(), "error closing tcpMux")

	conn, err := muxMulti.GetAllConns("test", false, net.IP{127, 0, 0, 1})
	require.Nil(t, conn, "should receive nil because mux is closed")
	require.Equal(t, io.ErrClosedPipe, err, "should receive error because mux is closed")
}

func TestMultiTCPMux_GetConnByUfrag_NoMuxes(t *testing.T) {
	multi := NewMultiTCPMuxDefault() // no muxes

	pc, err := multi.GetConnByUfrag("ufrag", false, net.IP{127, 0, 0, 1})
	require.Nil(t, pc)
	require.ErrorIs(t, err, errNoTCPMuxAvailable)
}

func TestMultiTCPMux_GetConnByUfrag_FromAnyMux(t *testing.T) {
	logger := logging.NewDefaultLoggerFactory().NewLogger("ice")

	l1, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: 0})
	require.NoError(t, err)
	defer func() {
		_ = l1.Close()
	}()

	mux1 := NewTCPMuxDefault(TCPMuxParams{
		Listener:       l1,
		Logger:         logger,
		ReadBufferSize: 8,
	})
	defer func() {
		_ = mux1.Close()
	}()

	l2, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: 0})
	require.NoError(t, err)
	defer func() {
		_ = l2.Close()
	}()

	mux2 := NewTCPMuxDefault(TCPMuxParams{
		Listener:       l2,
		Logger:         logger,
		ReadBufferSize: 8,
	})
	defer func() {
		_ = mux2.Close()
	}()

	multi := NewMultiTCPMuxDefault(mux1, mux2)
	defer func() { _ = multi.Close() }()

	pc, err := multi.GetConnByUfrag("myufrag", false, net.IP{127, 0, 0, 1})
	require.NoError(t, err)
	require.NotNil(t, pc)

	pcAddr, ok := pc.LocalAddr().(*net.TCPAddr)
	require.True(t, ok, "packet conn addr should be *net.TCPAddr")

	m1Addr, ok := mux1.LocalAddr().(*net.TCPAddr)
	require.True(t, ok, "mux1 local addr should be *net.TCPAddr")

	m2Addr, ok := mux2.LocalAddr().(*net.TCPAddr)
	require.True(t, ok, "mux2 local addr should be *net.TCPAddr")

	isFromMux1 := pcAddr.Port == m1Addr.Port && pcAddr.IP.Equal(m1Addr.IP)
	isFromMux2 := pcAddr.Port == m2Addr.Port && pcAddr.IP.Equal(m2Addr.IP)
	require.True(t, isFromMux1 || isFromMux2, "conn must come from one of the underlying muxes")
}

func TestMultiTCPMux_GetAllConns_NoMuxes(t *testing.T) {
	multi := NewMultiTCPMuxDefault() // no underlying TCPMux instances

	conns, err := multi.GetAllConns("ufrag", false, net.IP{127, 0, 0, 1})

	require.Nil(t, conns)
	require.ErrorIs(t, err, errNoTCPMuxAvailable)
}

var (
	errTCPMuxCloseBoom   = errors.New("tcp mux close boom")
	errTCPMuxCloseFirst  = errors.New("first tcp mux close failed")
	errTCPMuxCloseSecond = errors.New("second tcp mux close failed")
)

type closeErrTCPMux struct {
	TCPMux
	ret error
}

func (w *closeErrTCPMux) Close() error {
	_ = w.TCPMux.Close()

	return w.ret
}

func TestMultiTCPMux_Close_PropagatesError_FromWrappedMux(t *testing.T) {
	logger := logging.NewDefaultLoggerFactory().NewLogger("ice")

	// first mux: normal close (nil)
	l1, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: 0})
	require.NoError(t, err)
	mux1 := NewTCPMuxDefault(TCPMuxParams{
		Listener:       l1,
		Logger:         logger,
		ReadBufferSize: 8,
	})
	defer func() {
		_ = mux1.Close()
	}()

	// second mux: Close() returns injected error
	l2, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: 0})
	require.NoError(t, err)
	mux2Real := NewTCPMuxDefault(TCPMuxParams{
		Listener:       l2,
		Logger:         logger,
		ReadBufferSize: 8,
	})
	defer func() {
		_ = mux2Real.Close()
	}()
	mux2 := &closeErrTCPMux{TCPMux: mux2Real, ret: errTCPMuxCloseBoom}

	multi := NewMultiTCPMuxDefault(mux1, mux2)

	got := multi.Close()
	require.ErrorIs(t, got, errTCPMuxCloseBoom)
}

func TestMultiTCPMux_Close_LastErrorWins_FromWrappedMuxes(t *testing.T) {
	logger := logging.NewDefaultLoggerFactory().NewLogger("ice")

	// first mux: error1
	la, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: 0})
	require.NoError(t, err)
	mux1Real := NewTCPMuxDefault(TCPMuxParams{
		Listener:       la,
		Logger:         logger,
		ReadBufferSize: 8,
	})
	defer func() {
		_ = mux1Real.Close()
	}()
	mux1 := &closeErrTCPMux{TCPMux: mux1Real, ret: errTCPMuxCloseFirst}

	// second mux: error2 (last error should be returned)
	lb, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: 0})
	require.NoError(t, err)
	mux2Real := NewTCPMuxDefault(TCPMuxParams{
		Listener:       lb,
		Logger:         logger,
		ReadBufferSize: 8,
	})
	defer func() {
		_ = mux2Real.Close()
	}()
	mux2 := &closeErrTCPMux{TCPMux: mux2Real, ret: errTCPMuxCloseSecond}

	multi := NewMultiTCPMuxDefault(mux1, mux2)

	got := multi.Close()
	require.ErrorIs(t, got, errTCPMuxCloseSecond)
}
