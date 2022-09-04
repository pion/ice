//go:build !js
// +build !js

package ice

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/transport/test"
	"github.com/stretchr/testify/require"
)

func TestMultiUDPMux(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	conn1, err := net.ListenUDP(udp, &net.UDPAddr{})
	require.NoError(t, err)

	conn2, err := net.ListenUDP(udp, &net.UDPAddr{})
	require.NoError(t, err)

	udpMuxMulti := NewMultiUDPMuxDefault(
		NewUDPMuxDefault(UDPMuxParams{UDPConn: conn1}),
		NewUDPMuxDefault(UDPMuxParams{UDPConn: conn2}),
	)
	defer func() {
		_ = udpMuxMulti.Close()
		_ = conn1.Close()
		_ = conn2.Close()
	}()

	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer wg.Done()
		testMultiUDPMuxConnections(t, udpMuxMulti, "ufrag1", udp)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		testMultiUDPMuxConnections(t, udpMuxMulti, "ufrag2", "udp4")
	}()

	// skip ipv6 test on i386
	const ptrSize = 32 << (^uintptr(0) >> 63)
	if ptrSize != 32 {
		testMultiUDPMuxConnections(t, udpMuxMulti, "ufrag3", "udp6")
	}

	wg.Wait()

	require.NoError(t, udpMuxMulti.Close())

	// can't create more connections
	_, err = udpMuxMulti.GetConn("failufrag", false)
	require.Error(t, err)
}

func testMultiUDPMuxConnections(t *testing.T, udpMuxMulti *MultiUDPMuxDefault, ufrag string, network string) {
	pktConns, err := udpMuxMulti.GetAllConns(ufrag, false)
	require.NoError(t, err, "error retrieving muxed connection for ufrag")
	defer func() {
		for _, c := range pktConns {
			_ = c.Close()
		}
	}()
	require.Len(t, pktConns, len(udpMuxMulti.muxs), "there should be a PacketConn for every mux")

	// Try talking with each PacketConn
	for _, pktConn := range pktConns {
		remoteConn, err := net.DialUDP(network, nil, &net.UDPAddr{
			Port: pktConn.LocalAddr().(*net.UDPAddr).Port,
		})
		require.NoError(t, err, "error dialing test udp connection")
		testMuxConnectionPair(t, pktConn, remoteConn, ufrag)
	}
}
