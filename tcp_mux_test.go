package ice

import (
	"io"
	"net"
	"testing"

	"github.com/pion/logging"
	"github.com/pion/stun"
	"github.com/pion/transport/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ TCPMux = &TCPMuxDefault{}
var _ TCPMux = &invalidTCPMux{}

func TestTCPMux_Recv(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

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

	require.NotNil(t, tcpMux.LocalAddr(), "tcpMux.LocalAddr() is nil")

	conn, err := net.DialTCP("tcp", nil, tcpMux.LocalAddr().(*net.TCPAddr))
	require.NoError(t, err, "error dialing test tcp connection")

	msg := stun.New()
	msg.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}
	msg.Add(stun.AttrUsername, []byte("myufrag:otherufrag"))
	msg.Encode()

	n, err := writeStreamingPacket(conn, msg.Raw)
	require.NoError(t, err, "error writing tcp stun packet")

	pktConn, err := tcpMux.GetConnByUfrag("myufrag")
	require.NoError(t, err, "error retrieving muxed connection for ufrag")
	defer func() {
		_ = pktConn.Close()
	}()

	recv := make([]byte, n)
	n2, raddr, err := pktConn.ReadFrom(recv)
	require.NoError(t, err, "error receiving data")
	assert.Equal(t, conn.LocalAddr(), raddr, "remote tcp address mismatch")
	assert.Equal(t, n, n2, "received byte size mismatch")
	assert.Equal(t, msg.Raw, recv, "received bytes mismatch")
}

func TestTCPMux_NoDeadlockWhenClosingUnusedPacketConn(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

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

	_, err = tcpMux.GetConnByUfrag("test")
	require.NoError(t, err, "error getting conn by ufrag")

	require.NoError(t, tcpMux.Close(), "error closing tcpMux")

	conn, err := tcpMux.GetConnByUfrag("test")
	assert.Nil(t, conn, "should receive nil because mux is closed")
	assert.Equal(t, io.ErrClosedPipe, err, "should receive error because mux is closed")
}
