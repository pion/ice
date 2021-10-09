//go:build !js
// +build !js

package ice

import (
	"net"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/test"
	"github.com/stretchr/testify/require"
)

// TestTCPMuxAgent is an end to end test over TCP mux, ensuring two agents could
// connect over TCP mux.
func TestTCPMuxAgent(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	const muxPort = 7686

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{
		Port: muxPort,
	})
	require.NoError(t, err, "error starting listener")
	defer func() {
		_ = listener.Close()
	}()

	loggerFactory := logging.NewDefaultLoggerFactory()
	tcpMux := NewTCPMuxDefault(TCPMuxParams{
		Listener:       listener,
		Logger:         loggerFactory.NewLogger("ice"),
		ReadBufferSize: 20,
	})

	defer func() {
		_ = tcpMux.Close()
	}()

	require.NotNil(t, tcpMux.LocalAddr(), "tcpMux.LocalAddr() is nil")

	muxedA, err := NewAgent(&AgentConfig{
		TCPMux:         tcpMux,
		CandidateTypes: []CandidateType{CandidateTypeHost},
		NetworkTypes: []NetworkType{
			NetworkTypeUDP4,
		},
	})
	require.NoError(t, err)

	a, err := NewAgent(&AgentConfig{
		CandidateTypes: []CandidateType{CandidateTypeHost},
		NetworkTypes:   supportedNetworkTypes(),
	})
	require.NoError(t, err)

	conn, muxedConn := connect(a, muxedA)

	pair := muxedA.getSelectedPair()
	require.NotNil(t, pair)
	require.Equal(t, muxPort, pair.Local.Port())

	// send a packet to Mux
	data := []byte("hello world")
	_, err = conn.Write(data)
	require.NoError(t, err)

	buffer := make([]byte, 1024)
	n, err := muxedConn.Read(buffer)
	require.NoError(t, err)
	require.Equal(t, data, buffer[:n])

	// send a packet from Mux
	_, err = muxedConn.Write(data)
	require.NoError(t, err)

	n, err = conn.Read(buffer)
	require.NoError(t, err)
	require.Equal(t, data, buffer[:n])

	// close it down
	require.NoError(t, conn.Close())
	require.NoError(t, muxedConn.Close())
	require.NoError(t, tcpMux.Close())
}
