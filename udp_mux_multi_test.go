// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v3/stdnet"
	"github.com/pion/transport/v3/test"
	"github.com/stretchr/testify/require"
)

func TestMultiUDPMux(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

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

	testMultiUDPMuxConnections(t, udpMuxMulti, "ufrag3", udp6)

	wg.Wait()

	require.NoError(t, udpMuxMulti.Close())

	// Can't create more connections
	_, err = udpMuxMulti.GetConn("failufrag", conn1.LocalAddr())
	require.Error(t, err)
}

func testMultiUDPMuxConnections(t *testing.T, udpMuxMulti *MultiUDPMuxDefault, ufrag string, network string) {
	t.Helper()

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
		remoteConn, err := net.DialUDP(network, nil, pktConn.LocalAddr().(*net.UDPAddr)) // nolint
		require.NoError(t, err, "error dialing test UDP connection")
		testMuxConnectionPair(t, pktConn, remoteConn, ufrag)
	}
}

func TestUnspecifiedUDPMux(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	muxPort := 7778
	udpMuxMulti, err := NewMultiUDPMuxFromPort(muxPort, UDPMuxFromPortWithInterfaceFilter(problematicNetworkInterfaces))
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

	testMultiUDPMuxConnections(t, udpMuxMulti, "ufrag3", udp6)

	wg.Wait()

	require.NoError(t, udpMuxMulti.Close())
}

func TestMultiUDPMux_GetConn_NoUDPMuxAvailable(t *testing.T) {
	conn1, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer func() {
		_ = conn1.Close()
	}()

	conn2, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer func() {
		_ = conn2.Close()
	}()

	mux1 := NewUDPMuxDefault(UDPMuxParams{UDPConn: conn1})
	mux2 := NewUDPMuxDefault(UDPMuxParams{UDPConn: conn2})
	multi := NewMultiUDPMuxDefault(mux1, mux2)
	defer func() {
		_ = multi.Close()
	}()

	// Pick a port that is guaranteed not to match any listening address
	addrs := multi.GetListenAddresses()
	require.NotEmpty(t, addrs)

	udpAddr, ok := addrs[0].(*net.UDPAddr)
	require.True(t, ok, "expected *net.UDPAddr")

	// Build a set of in-use ports
	inUse := make(map[int]struct{}, len(addrs))
	for _, a := range addrs {
		if ua, ok := a.(*net.UDPAddr); ok {
			inUse[ua.Port] = struct{}{}
		}
	}

	// Find a nearby port not in use
	newPort := udpAddr.Port + 1
	for i := 0; i < 100; i++ {
		if _, exists := inUse[newPort]; !exists {
			break
		}
		newPort++
	}
	missing := &net.UDPAddr{IP: udpAddr.IP, Port: newPort, Zone: udpAddr.Zone}

	pc, getErr := multi.GetConn("missing-ufrag", missing)
	require.Nil(t, pc)
	require.ErrorIs(t, getErr, errNoUDPMuxAvailable)
}

type closeErrUDPMux struct {
	UDPMux
	ret error
}

func (w *closeErrUDPMux) Close() error {
	_ = w.UDPMux.Close() // ensure underlying resources are released

	return w.ret
}

var (
	errCloseBoom   = errors.New("close boom")
	errCloseFirst  = errors.New("first close failed")
	errCloseSecond = errors.New("second close failed")
)

func TestMultiUDPMux_Close_PropagatesError(t *testing.T) {
	udp1, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	udp2, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)

	mux1 := NewUDPMuxDefault(UDPMuxParams{UDPConn: udp1})
	mux2real := NewUDPMuxDefault(UDPMuxParams{UDPConn: udp2})

	mux2 := &closeErrUDPMux{UDPMux: mux2real, ret: errCloseBoom}

	multi := NewMultiUDPMuxDefault(mux1, mux2)
	got := multi.Close()

	require.ErrorIs(t, got, errCloseBoom)
}

func TestMultiUDPMux_Close_LastErrorWins(t *testing.T) {
	udpA, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	udpB, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)

	muxAReal := NewUDPMuxDefault(UDPMuxParams{UDPConn: udpA})
	muxBReal := NewUDPMuxDefault(UDPMuxParams{UDPConn: udpB})

	muxA := &closeErrUDPMux{UDPMux: muxAReal, ret: errCloseFirst}
	muxB := &closeErrUDPMux{UDPMux: muxBReal, ret: errCloseSecond}

	multi := NewMultiUDPMuxDefault(muxA, muxB)
	got := multi.Close()

	require.ErrorIs(t, got, errCloseSecond)
}

func TestUDPMuxFromPortOptions_Apply(t *testing.T) {
	t.Run("IPFilter", func(t *testing.T) {
		var p multiUDPMuxFromPortParam

		keepLoopbackV4 := func(ip net.IP) bool { return ip.IsLoopback() && ip.To4() != nil }
		opt := UDPMuxFromPortWithIPFilter(keepLoopbackV4)
		opt.apply(&p)

		require.NotNil(t, p.ipFilter)
		require.True(t, p.ipFilter(net.ParseIP("127.0.0.1")))
		require.False(t, p.ipFilter(net.ParseIP("8.8.8.8")))
	})

	t.Run("Networks single", func(t *testing.T) {
		var p multiUDPMuxFromPortParam

		opt := UDPMuxFromPortWithNetworks(NetworkTypeUDP4)
		opt.apply(&p)

		require.Len(t, p.networks, 1)
		require.Equal(t, NetworkTypeUDP4, p.networks[0])
	})

	t.Run("Networks multiple", func(t *testing.T) {
		var p multiUDPMuxFromPortParam

		opt := UDPMuxFromPortWithNetworks(NetworkTypeUDP4, NetworkTypeUDP6)
		opt.apply(&p)

		require.Len(t, p.networks, 2)
		require.ElementsMatch(t, []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6}, p.networks)
	})

	t.Run("ReadBufferSize", func(t *testing.T) {
		var p multiUDPMuxFromPortParam

		opt := UDPMuxFromPortWithReadBufferSize(4096)
		opt.apply(&p)

		require.Equal(t, 4096, p.readBufferSize)
	})

	t.Run("WriteBufferSize", func(t *testing.T) {
		var p multiUDPMuxFromPortParam

		opt := UDPMuxFromPortWithWriteBufferSize(8192)
		opt.apply(&p)

		require.Equal(t, 8192, p.writeBufferSize)
	})

	t.Run("Logger", func(t *testing.T) {
		var p multiUDPMuxFromPortParam

		logger := logging.NewDefaultLoggerFactory().NewLogger("ice-test")
		opt := UDPMuxFromPortWithLogger(logger)
		opt.apply(&p)

		require.NotNil(t, p.logger)
		require.Equal(t, logger, p.logger)
	})

	t.Run("Net", func(t *testing.T) {
		var p multiUDPMuxFromPortParam

		n, err := stdnet.NewNet()
		require.NoError(t, err)

		opt := UDPMuxFromPortWithNet(n)
		opt.apply(&p)

		require.NotNil(t, p.net)
		require.Equal(t, n, p.net)
	})
}

func TestNewMultiUDPMuxFromPort_PortInUse_ListenErrorAndCleanup(t *testing.T) {
	pre, err := net.ListenUDP(udp, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer func() {
		_ = pre.Close()
	}()

	srvAddr, ok := pre.LocalAddr().(*net.UDPAddr)
	require.True(t, ok, "pre.LocalAddr is not *net.UDPAddr")
	port := srvAddr.Port

	multi, buildErr := NewMultiUDPMuxFromPort(
		port,
		UDPMuxFromPortWithLoopback(),
		UDPMuxFromPortWithNetworks(NetworkTypeUDP4),
	)

	require.Nil(t, multi)
	require.Error(t, buildErr)
}

func TestNewMultiUDPMuxFromPort_Success_SetsBuffers(t *testing.T) {
	multi, err := NewMultiUDPMuxFromPort(
		0,
		UDPMuxFromPortWithLoopback(),
		UDPMuxFromPortWithNetworks(NetworkTypeUDP4),
		UDPMuxFromPortWithReadBufferSize(4096),
		UDPMuxFromPortWithWriteBufferSize(8192),
	)
	require.NoError(t, err)
	require.NotNil(t, multi)

	addrs := multi.GetListenAddresses()
	require.NotEmpty(t, addrs)

	require.NoError(t, multi.Close())
}

func TestNewMultiUDPMuxFromPort_CleanupClosesAll(t *testing.T) {
	stdNet, err := stdnet.NewNet()
	require.NoError(t, err)

	_, addrs, err := localInterfaces(stdNet, nil, nil, []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6}, true)
	require.NoError(t, err)
	if len(addrs) < 2 {
		t.Skip("need at least two local addresses to hit partial-success then failure")
	}

	second := addrs[1].addr
	l2, err := stdNet.ListenUDP("udp", &net.UDPAddr{
		IP:   second.AsSlice(),
		Port: 0,
		Zone: second.Zone(),
	})
	require.NoError(t, err)
	defer func() {
		_ = l2.Close()
	}()

	udpAddr2, ok := l2.LocalAddr().(*net.UDPAddr)
	require.True(t, ok, "LocalAddr is not *net.UDPAddr")
	picked := udpAddr2.Port

	preBinds := []net.PacketConn{l2}
	for i := 2; i < len(addrs); i++ {
		a := addrs[i].addr
		l, e := stdNet.ListenUDP("udp", &net.UDPAddr{
			IP:   a.AsSlice(),
			Port: picked,
			Zone: a.Zone(),
		})
		if e == nil {
			preBinds = append(preBinds, l)
		}
	}
	t.Cleanup(func() {
		for _, c := range preBinds {
			_ = c.Close()
		}
	})

	require.GreaterOrEqual(t, len(preBinds), 1, "need at least one prebound address after the first")

	multi, buildErr := NewMultiUDPMuxFromPort(
		picked,
		UDPMuxFromPortWithNet(stdNet),
		UDPMuxFromPortWithNetworks(NetworkTypeUDP4, NetworkTypeUDP6),
		UDPMuxFromPortWithLoopback(),
	)
	require.Nil(t, multi)
	require.Error(t, buildErr)

	first := addrs[0].addr
	rebind, err := stdNet.ListenUDP("udp", &net.UDPAddr{
		IP:   first.AsSlice(),
		Port: picked,
		Zone: first.Zone(),
	})
	require.NoError(t, err, "expected first address/port to be free after cleanup")
	_ = rebind.Close()
}
