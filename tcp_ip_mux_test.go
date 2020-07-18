package ice

import (
	"net"
	"testing"

	"github.com/pion/logging"
	"github.com/pion/stun"
	"github.com/pion/transport/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTCP_Recv(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	loggerFactory := logging.NewDefaultLoggerFactory()

	tim := newTCPIPMux(tcpIPMuxParams{
		ListenPort:     8080,
		Logger:         loggerFactory.NewLogger("ice"),
		ReadBufferSize: 20,
	})

	tcpMux, err := tim.Listen(net.IP{127, 0, 0, 1})
	require.NoError(t, err, "error starting listener")
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

	pktConn, err := tcpMux.GetConn("myufrag")
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
