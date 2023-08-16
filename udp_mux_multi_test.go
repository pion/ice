// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/transport/v2/test"
	"github.com/stretchr/testify/require"
)

func TestMultiUDPMux(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	conn1, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)

	conn2, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)

	conn3, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv6loopback})
	if err != nil {
		// IPv6 is not supported on this machine
		t.Log("ipv6 is not supported on this machine")
	}

	muxes := []UDPMux{}
	muxV41 := NewUDPMuxDefault(UDPMuxParams{UDPConn: conn1})
	muxes = append(muxes, muxV41)
	muxV42 := NewUDPMuxDefault(UDPMuxParams{UDPConn: conn2})
	muxes = append(muxes, muxV42)
	if conn3 != nil {
		muxV6 := NewUDPMuxDefault(UDPMuxParams{UDPConn: conn3})
		muxes = append(muxes, muxV6)
	}

	udpMuxMulti := NewMultiUDPMuxDefault(muxes...)
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
		testMultiUDPMuxConnections(t, udpMuxMulti, "ufrag2", udp4)
	}()

	// Skip ipv6 test on i386
	const ptrSize = 32 << (^uintptr(0) >> 63)
	if ptrSize != 32 {
		testMultiUDPMuxConnections(t, udpMuxMulti, "ufrag3", udp6)
	}

	wg.Wait()

	require.NoError(t, udpMuxMulti.Close())

	// Can't create more connections
	_, err = udpMuxMulti.GetConn("failufrag", conn1.LocalAddr())
	require.Error(t, err)
}

func testMultiUDPMuxConnections(t *testing.T, udpMuxMulti *MultiUDPMuxDefault, ufrag string, network string) {
	addrs := udpMuxMulti.GetListenAddresses()
	pktConns := make([]net.PacketConn, 0, len(addrs))
	for _, addr := range addrs {
		udpAddr, ok := addr.(*net.UDPAddr)
		require.True(t, ok)
		if network == udp4 && udpAddr.IP.To4() == nil {
			continue
		} else if network == udp6 && udpAddr.IP.To4() != nil {
			continue
		}
		c, err := udpMuxMulti.GetConn(ufrag, addr)
		require.NoError(t, err, "error retrieving muxed connection for ufrag")
		pktConns = append(pktConns, c)
	}
	defer func() {
		for _, c := range pktConns {
			_ = c.Close()
		}
	}()

	// Try talking with each PacketConn
	for _, pktConn := range pktConns {
		remoteConn, err := net.DialUDP(network, nil, pktConn.LocalAddr().(*net.UDPAddr))
		require.NoError(t, err, "error dialing test UDP connection")
		testMuxConnectionPair(t, pktConn, remoteConn, ufrag)
	}
}

func TestUnspecifiedUDPMux(t *testing.T) {
	report := test.CheckRoutines(t)
	defer report()

	lim := test.TimeOut(time.Second * 30)
	defer lim.Stop()

	cases := map[string][]int{
		"single port": {7778},
		"multi ports": {7779, 7780, 7781},
	}

	for name, ports := range cases {
		cname, cports := name, ports
		t.Run(cname, func(t *testing.T) {
			udpMuxMulti, err := NewMultiUDPMuxFromPorts(cports, UDPMuxFromPortWithInterfaceFilter(func(s string) bool {
				defaultDockerBridgeNetwork := strings.Contains(s, "docker")
				customDockerBridgeNetwork := strings.Contains(s, "br-")
				return !defaultDockerBridgeNetwork && !customDockerBridgeNetwork
			}))
			require.NoError(t, err)

			require.GreaterOrEqual(t, len(udpMuxMulti.muxes), 1, "at least have 1 muxes")
			defer func() {
				_ = udpMuxMulti.Close()
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
				testMultiUDPMuxConnections(t, udpMuxMulti, "ufrag2", udp4)
			}()

			// Skip IPv6 test on i386
			const ptrSize = 32 << (^uintptr(0) >> 63)
			if ptrSize != 32 {
				testMultiUDPMuxConnections(t, udpMuxMulti, "ufrag3", udp6)
			}

			wg.Wait()

			// check port allocation is balanced
			if len(cports) > 1 {
				expectPorts := make(map[int]bool)
				for i := range cports {
					addr := udpMuxMulti.GetListenAddresses()[0]
					ufrag := fmt.Sprintf("ufragetest%d", i)
					conn, err := udpMuxMulti.GetConn(ufrag, addr)
					require.NoError(t, err)
					require.NotNil(t, conn)
					require.False(t, expectPorts[conn.LocalAddr().(*net.UDPAddr).Port], fmt.Sprint("port ", conn.LocalAddr().(*net.UDPAddr).Port, " is already used", expectPorts))
					expectPorts[conn.LocalAddr().(*net.UDPAddr).Port] = true

					conn2, err := udpMuxMulti.GetConn(ufrag, addr)
					require.NoError(t, err)
					require.Equal(t, conn, conn2)
				}
				require.Equal(t, len(cports), len(expectPorts))
			}

			require.NoError(t, udpMuxMulti.Close())
		})
	}
}
