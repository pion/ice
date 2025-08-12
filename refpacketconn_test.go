package ice

import (
	"net"
	"testing"
	"time"

	fakenet "github.com/pion/ice/v4/internal/fakenet"
	"github.com/stretchr/testify/require"
)

func newPipePacketConn(t *testing.T) (parent net.PacketConn, remote net.Conn, cleanup func()) {
	t.Helper()
	c1, c2 := net.Pipe()
	pc := &fakenet.PacketConn{Conn: c1}
	cleanup = func() {
		_ = c1.Close()
		_ = c2.Close()
	}

	return pc, c2, cleanup
}

func TestSharedPacketConn_FanOut(t *testing.T) {
	parent, remote, cleanup := newPipePacketConn(t)
	defer cleanup()

	spc := newSharedPacketConn(parent)
	s1 := spc.Ref()
	s2 := spc.Ref()
	defer func() {
		_ = s1.Close()
		_ = s2.Close()
		spc.Release()
	}()

	// Set short deadlines to avoid hanging the test
	_ = s1.SetReadDeadline(time.Now().Add(time.Second))
	_ = s2.SetReadDeadline(time.Now().Add(time.Second))

	msg := []byte("hello")
	go func() { _, _ = remote.Write(msg) }()

	buf1 := make([]byte, 16)
	n1, _, err1 := s1.ReadFrom(buf1)
	require.NoError(t, err1)
	require.Equal(t, "hello", string(buf1[:n1]))

	buf2 := make([]byte, 16)
	n2, _, err2 := s2.ReadFrom(buf2)
	require.NoError(t, err2)
	require.Equal(t, "hello", string(buf2[:n2]))
}

func TestSharedPacketConn_HoldRelease(t *testing.T) {
	parent, remote, cleanup := newPipePacketConn(t)
	defer cleanup()

	spc := newSharedPacketConn(parent)
	// First ref then close it immediately
	s1 := spc.Ref()
	_ = s1.Close()

	// Parent should still be open due to hold
	s2 := spc.Ref()
	defer func() {
		_ = s2.Close()
	}()

	_ = s2.SetReadDeadline(time.Now().Add(time.Second))
	msg := []byte("x")
	go func() { _, _ = remote.Write(msg) }()
	buf := make([]byte, 4)
	n, _, err := s2.ReadFrom(buf)
	require.NoError(t, err)
	require.Equal(t, "x", string(buf[:n]))

	// Release and close last subscriber should close parent
	spc.Release()
	_ = s2.Close()
	time.Sleep(10 * time.Millisecond)
	_, werr := remote.Write([]byte("y"))
	require.Error(t, werr)
}

func TestSharedPacketConn_ReadDeadline(t *testing.T) {
	parent, _, cleanup := newPipePacketConn(t)
	defer cleanup()

	spc := newSharedPacketConn(parent)
	s := spc.Ref()
	defer func() {
		_ = s.Close()
		spc.Release()
	}()

	_ = s.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
	buf := make([]byte, 1)
	_, _, err := s.ReadFrom(buf)
	require.Error(t, err, "expected timeout error, got nil")
	require.IsType(t, &netTimeoutError{}, err, "expected netTimeoutError, got %T", err)
}

func TestSharedPacketConn_WriteDeadline(t *testing.T) {
	parent, _, cleanup := newPipePacketConn(t)
	defer cleanup()

	spc := newSharedPacketConn(parent)
	s := spc.Ref()
	defer func() {
		_ = s.Close()
		spc.Release()
	}()

	_ = s.SetWriteDeadline(time.Now().Add(-1 * time.Millisecond))
	_, err := s.WriteTo([]byte("w"), nil)
	require.Error(t, err, "expected timeout error on write, got nil")
}
