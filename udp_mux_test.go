// +build !js

package ice

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun"
	"github.com/pion/transport/test"
	"github.com/stretchr/testify/require"
)

func TestUDPMux(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	loggerFactory := logging.NewDefaultLoggerFactory()
	udpMux := NewUDPMuxDefault(UDPMuxParams{
		Logger:         loggerFactory.NewLogger("ice"),
		ReadBufferSize: 20,
	})
	err := udpMux.Start(7686)
	require.NoError(t, err)

	defer func() {
		_ = udpMux.Close()
	}()

	require.NotNil(t, udpMux.LocalAddr(), "tcpMux.LocalAddr() is nil")
	require.Equal(t, ":7686", udpMux.LocalAddr().String())

	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer wg.Done()
		testMuxConnection(t, udpMux, "ufrag1")
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		testMuxConnection(t, udpMux, "ufrag2")
	}()

	testMuxConnection(t, udpMux, "ufrag3")
	wg.Wait()

	require.NoError(t, udpMux.Close())

	// can't create more connections
	_, err = udpMux.GetConnByUfrag("failufrag")
	require.Error(t, err)
}

func testMuxConnection(t *testing.T, udpMux *UDPMuxDefault, ufrag string) {
	pktConn, err := udpMux.GetConnByUfrag(ufrag)
	require.NoError(t, err, "error retrieving muxed connection for ufrag")
	defer func() {
		_ = pktConn.Close()
	}()

	remoteConn, err := net.DialUDP(udp, nil, udpMux.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err, "error dialing test udp connection")

	// initial messages are dropped
	_, err = remoteConn.Write([]byte("dropped bytes"))
	require.NoError(t, err)
	// wait for packet to be consumed
	time.Sleep(time.Millisecond)

	// write out to establish connection
	msg := stun.New()
	msg.Type = stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}
	msg.Add(stun.AttrUsername, []byte(ufrag+":otherufrag"))
	msg.Encode()
	_, err = pktConn.WriteTo(msg.Raw, remoteConn.LocalAddr())
	require.NoError(t, err)

	// ensure received
	buf := make([]byte, receiveMTU)
	n, err := remoteConn.Read(buf)
	require.NoError(t, err)
	require.Equal(t, msg.Raw, buf[:n])

	// write a bunch of packets from remote to ensure proper receipt
	dataToSend := [][]byte{
		[]byte("hello world"),
		[]byte("test text"),
		msg.Raw,
	}

	buffer := make([]byte, receiveMTU)
	for _, data := range dataToSend {
		_, err := remoteConn.Write(data)
		require.NoError(t, err)

		n, _, err := pktConn.ReadFrom(buffer)
		require.NoError(t, err)
		require.Equal(t, data, buffer[:n])

		time.Sleep(10 * time.Millisecond)
	}
}
