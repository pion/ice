// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v4/test"
	"github.com/stretchr/testify/require"
)

type deadlineConn struct {
	readDeadline  time.Time
	writeDeadline time.Time
	deadline      time.Time
	lAddr         net.Addr
	rAddr         net.Addr
}

func (d *deadlineConn) Read([]byte) (int, error)  { return 0, io.EOF }
func (d *deadlineConn) Write([]byte) (int, error) { return 0, io.EOF }
func (d *deadlineConn) Close() error              { return nil }
func (d *deadlineConn) LocalAddr() net.Addr       { return d.lAddr }
func (d *deadlineConn) RemoteAddr() net.Addr      { return d.rAddr }
func (d *deadlineConn) SetDeadline(t time.Time) error {
	d.deadline = t

	return nil
}

func (d *deadlineConn) SetReadDeadline(t time.Time) error {
	d.readDeadline = t

	return nil
}

func (d *deadlineConn) SetWriteDeadline(t time.Time) error {
	d.writeDeadline = t

	return nil
}

func TestBufferedConn_Write_ErrorAfterClose(t *testing.T) {
	defer test.CheckRoutines(t)()

	logger := logging.NewDefaultLoggerFactory().NewLogger("ice")
	c1, c2 := net.Pipe()
	defer func() {
		_ = c2.Close()
	}()

	bc := newBufferedConn(c1, 0, logger)

	require.NoError(t, bc.Close())

	n, err := bc.Write([]byte("hello"))
	require.Error(t, err)
	require.Equal(t, 0, n)
}

type hookLogger struct {
	ch   chan struct{}
	once sync.Once
}

func newHookLogger() *hookLogger { return &hookLogger{ch: make(chan struct{})} }

func (l *hookLogger) Trace(msg string)      {}
func (l *hookLogger) Tracef(string, ...any) {}
func (l *hookLogger) Debug(msg string)      {}
func (l *hookLogger) Debugf(string, ...any) {}
func (l *hookLogger) Info(msg string)       {}
func (l *hookLogger) Infof(string, ...any)  {}
func (l *hookLogger) Warn(msg string)       { l.once.Do(func() { close(l.ch) }) }
func (l *hookLogger) Warnf(string, ...any)  { l.once.Do(func() { close(l.ch) }) }
func (l *hookLogger) Error(msg string)      {}
func (l *hookLogger) Errorf(string, ...any) {}

func TestBufferedConn_writeProcess_ReadError(t *testing.T) {
	c1, c2 := net.Pipe()
	t.Cleanup(func() { _ = c2.Close() })

	logger := newHookLogger()
	under := newBufferedConn(c1, 0, logger)

	bc, ok := under.(*bufferedConn)
	require.True(t, ok, "expected *bufferedConn")

	_ = bc.buf.SetReadDeadline(time.Unix(0, 0))

	select {
	case <-logger.ch:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "timed out waiting for read-error branch")
	}

	require.NoError(t, bc.Close())
}

func TestBufferedConn_writeProcess_WriteError(t *testing.T) {
	c1, c2 := net.Pipe()
	logger := newHookLogger()

	under := newBufferedConn(c1, 0, logger)
	bc, ok := under.(*bufferedConn)
	require.True(t, ok, "expected *bufferedConn")

	require.NoError(t, c2.Close())

	n, err := bc.Write([]byte("hello"))
	require.NoError(t, err)
	require.Equal(t, 5, n)

	select {
	case <-logger.ch:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "timed out waiting for write-error branch")
	}

	require.NoError(t, bc.Close())
}

func newTestTCPPC(t *testing.T, readBuf int) *tcpPacketConn {
	t.Helper()

	return newTCPPacketConn(tcpPacketParams{
		ReadBuffer:    readBuf,
		LocalAddr:     &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: 0},
		Logger:        logging.NewDefaultLoggerFactory().NewLogger("ice"),
		WriteBuffer:   0,
		AliveDuration: 0,
	})
}

func TestTCPPacketConn_AddConn_ReturnsClosed(t *testing.T) {
	defer test.CheckRoutines(t)()

	tpc := newTestTCPPC(t, 8)
	require.NoError(t, tpc.Close())

	c1, c2 := net.Pipe()
	defer func() {
		_ = c2.Close()
	}()

	err := tpc.AddConn(c1, nil)
	require.ErrorIs(t, err, io.ErrClosedPipe)
	_ = c1.Close()
}

func TestTCPPacketConn_AddConn_DuplicateRemoteAddr(t *testing.T) {
	defer test.CheckRoutines(t)()

	tpc := newTestTCPPC(t, 8)

	c1, c2 := net.Pipe()
	defer func() {
		_ = c2.Close()
	}()

	require.NoError(t, tpc.AddConn(c1, nil))

	err := tpc.AddConn(c1, nil)
	require.ErrorIs(t, err, errConnectionAddrAlreadyExist)

	require.NoError(t, tpc.Close())
	_ = c1.Close()
}

func TestTCPPacketConn_AddConn_FirstPacket_BailsOnClosed(t *testing.T) {
	defer test.CheckRoutines(t)()

	// unbuffered recvChan so send can't proceed until there is a receiver.
	tpc := newTestTCPPC(t, 0)

	c1, c2 := net.Pipe()
	defer func() {
		_ = c2.Close()
	}()

	// firstPacketData is non-nil, so the goroutine will try the select.
	require.NoError(t, tpc.AddConn(c1, []byte("hello")))
	require.NoError(t, tpc.Close())
	_ = c1.Close()
}

func TestTCPPacketConn_ReadFrom_ShortBuffer(t *testing.T) {
	logger := logging.NewDefaultLoggerFactory().NewLogger("ice")

	tpc := newTCPPacketConn(tcpPacketParams{
		ReadBuffer:    1, // buffered channel so we can enqueue a packet
		LocalAddr:     &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: 0},
		Logger:        logger,
		WriteBuffer:   0,
		AliveDuration: 0,
	})
	defer func() { _ = tpc.Close() }()

	raddr := &net.TCPAddr{IP: net.IP{10, 0, 0, 1}, Port: 4242}
	big := bytes.Repeat([]byte{0xAB}, 10) // packet larger than read buffer

	tpc.recvChan <- streamingPacket{Data: big, RAddr: raddr, Err: nil}

	smallBuf := make([]byte, 5) // cap=5 < len(big)=10
	n, addr, err := tpc.ReadFrom(smallBuf)

	require.ErrorIs(t, err, io.ErrShortBuffer)
	require.Equal(t, 0, n)
	require.Equal(t, raddr.String(), addr.String())
}

func TestTCPPacketConn_WriteTo_ErrorBranch_WithProvidedMock(t *testing.T) {
	logger := logging.NewDefaultLoggerFactory().NewLogger("ice")

	tpc := newTCPPacketConn(tcpPacketParams{
		ReadBuffer:    1,
		LocalAddr:     &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: 0},
		Logger:        logger,
		WriteBuffer:   0,
		AliveDuration: 0,
	})
	t.Cleanup(func() { _ = tpc.Close() })

	mc := &mockConn{}

	tpc.mu.Lock()
	tpc.conns[mc.RemoteAddr().String()] = mc
	tpc.mu.Unlock()

	n, err := tpc.WriteTo([]byte("hello"), mc.RemoteAddr())

	require.Equal(t, 0, n)
	require.ErrorIs(t, err, io.EOF)
}

func TestTCPPacketConn_SetDeadlines(t *testing.T) {
	logger := logging.NewDefaultLoggerFactory().NewLogger("ice")
	addr := &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: 12345}
	remoteAddr := &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: 23456}

	tpc := newTCPPacketConn(tcpPacketParams{
		ReadBuffer:    8,
		LocalAddr:     addr,
		Logger:        logger,
		WriteBuffer:   0,
		AliveDuration: 0,
	})
	observer := &deadlineConn{lAddr: addr, rAddr: remoteAddr}
	tpc.mu.Lock()
	tpc.conns[observer.RemoteAddr().String()] = observer
	tpc.mu.Unlock()

	readDeadline := time.Now().Add(200 * time.Millisecond)
	writeDeadline := readDeadline.Add(200 * time.Millisecond)
	combinedDeadline := writeDeadline.Add(200 * time.Millisecond)

	require.NoError(t, tpc.SetReadDeadline(readDeadline))
	require.NoError(t, tpc.SetWriteDeadline(writeDeadline))
	require.NoError(t, tpc.SetDeadline(combinedDeadline))

	require.Equal(t, readDeadline, observer.readDeadline)
	require.Equal(t, writeDeadline, observer.writeDeadline)
	require.Equal(t, combinedDeadline, observer.deadline)

	require.NoError(t, tpc.Close())
	require.NoError(t, tpc.SetReadDeadline(time.Now().Add(200*time.Millisecond)))
	require.NoError(t, tpc.SetWriteDeadline(time.Now().Add(200*time.Millisecond)))
}

func TestTCPPacketConn_String(t *testing.T) {
	logger := logging.NewDefaultLoggerFactory().NewLogger("ice")
	addr := &net.TCPAddr{IP: net.IP{10, 0, 0, 1}, Port: 54321}

	tpc := newTCPPacketConn(tcpPacketParams{
		ReadBuffer:    1,
		LocalAddr:     addr,
		Logger:        logger,
		WriteBuffer:   0,
		AliveDuration: 0,
	})

	got := tpc.String()
	want := fmt.Sprintf("tcpPacketConn{LocalAddr: %s}", addr)

	require.Equal(t, want, got)
	_ = tpc.Close()
}
