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

// TestMuxAgent is an end to end test over UDP mux, ensuring two agents could connect over mux
func TestMuxAgent(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	const muxPort = 7686

	c, err := net.ListenUDP("udp4", &net.UDPAddr{
		Port: muxPort,
	})

	loggerFactory := logging.NewDefaultLoggerFactory()
	udpMux := NewUDPMuxDefault(UDPMuxParams{
		Logger:  loggerFactory.NewLogger("ice"),
		UDPConn: c,
	})

	require.NoError(t, err)
	defer func() {
		_ = udpMux.Close()
		_ = c.Close()
	}()

	muxedA, err := NewAgent(&AgentConfig{
		UDPMux:         udpMux,
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
	require.NoError(t, udpMux.Close())
}
