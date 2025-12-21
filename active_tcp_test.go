// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v3/stdnet"
	"github.com/pion/transport/v3/test"
	"github.com/stretchr/testify/require"
)

func getLocalIPAddress(t *testing.T, networkType NetworkType) netip.Addr {
	t.Helper()

	net, err := stdnet.NewNet()
	require.NoError(t, err)
	_, localAddrs, err := localInterfaces(net, problematicNetworkInterfaces, nil, []NetworkType{networkType}, false)
	require.NoError(t, err)
	require.NotEmpty(t, localAddrs)

	if networkType.IsIPv6() && runtime.GOOS == "darwin" {
		for _, addr := range localAddrs {
			if !addr.addr.IsLinkLocalUnicast() {
				return addr.addr
			}
		}

		t.Skip("no non-link-local IPv6 address available")
	}

	return localAddrs[0].addr
}

func ipv6Available(t *testing.T) bool {
	t.Helper()

	net, err := stdnet.NewNet()
	require.NoError(t, err)
	_, localAddrs, err := localInterfaces(net, problematicNetworkInterfaces, nil, []NetworkType{NetworkTypeTCP6}, false)
	require.NoError(t, err)

	if runtime.GOOS == "darwin" {
		for _, addr := range localAddrs {
			if !addr.addr.IsLinkLocalUnicast() {
				return true
			}
		}

		return false
	}

	return len(localAddrs) > 0
}

func TestActiveTCP(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 5).Stop()

	const listenPort = 7686
	type testCase struct {
		name                    string
		networkTypes            []NetworkType
		listenIPAddress         netip.Addr
		selectedPairNetworkType string
		useMDNS                 bool
	}

	testCases := []testCase{
		{
			name:                    "TCP4 connection",
			networkTypes:            []NetworkType{NetworkTypeTCP4},
			listenIPAddress:         getLocalIPAddress(t, NetworkTypeTCP4),
			selectedPairNetworkType: tcp,
		},
		{
			name:                    "UDP is preferred over TCP4", // This fails some time
			networkTypes:            supportedNetworkTypes(),
			listenIPAddress:         getLocalIPAddress(t, NetworkTypeTCP4),
			selectedPairNetworkType: udp,
		},
	}

	if ipv6Available(t) {
		testCases = append(testCases,
			testCase{
				name:                    "TCP6 connection",
				networkTypes:            []NetworkType{NetworkTypeTCP6},
				listenIPAddress:         getLocalIPAddress(t, NetworkTypeTCP6),
				selectedPairNetworkType: tcp,
				// if we don't use mDNS, we will very likely be filtering out location tracked ips.
				useMDNS: true,
			},
			testCase{
				name:                    "UDP is preferred over TCP6",
				networkTypes:            supportedNetworkTypes(),
				listenIPAddress:         getLocalIPAddress(t, NetworkTypeTCP6),
				selectedPairNetworkType: udp,
				// if we don't use mDNS, we will very likely be filtering out location tracked ips.
				useMDNS: true,
			},
		)
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			req := require.New(t)

			listener, err := net.ListenTCP("tcp", &net.TCPAddr{
				IP:   testCase.listenIPAddress.AsSlice(),
				Port: listenPort,
				Zone: testCase.listenIPAddress.Zone(),
			})
			req.NoError(err)
			defer func() {
				_ = listener.Close()
			}()

			loggerFactory := logging.NewDefaultLoggerFactory()

			tcpMux := NewTCPMuxDefault(TCPMuxParams{
				Listener:       listener,
				Logger:         loggerFactory.NewLogger("passive-ice-tcp-mux"),
				ReadBufferSize: 20,
			})

			defer func() {
				_ = tcpMux.Close()
			}()

			req.NotNil(tcpMux.LocalAddr(), "tcpMux.LocalAddr() is nil")

			hostAcceptanceMinWait := 100 * time.Millisecond
			cfg := &AgentConfig{
				TCPMux:                tcpMux,
				CandidateTypes:        []CandidateType{CandidateTypeHost},
				NetworkTypes:          testCase.networkTypes,
				LoggerFactory:         loggerFactory,
				HostAcceptanceMinWait: &hostAcceptanceMinWait,
				InterfaceFilter:       problematicNetworkInterfaces,
				IncludeLoopback:       true,
			}
			if testCase.useMDNS {
				cfg.MulticastDNSMode = MulticastDNSModeQueryAndGather
			}
			passiveAgent, err := NewAgent(cfg)
			req.NoError(err)
			req.NotNil(passiveAgent)
			defer func() {
				req.NoError(passiveAgent.Close())
			}()

			activeAgent, err := NewAgent(&AgentConfig{
				CandidateTypes:        []CandidateType{CandidateTypeHost},
				NetworkTypes:          testCase.networkTypes,
				LoggerFactory:         loggerFactory,
				HostAcceptanceMinWait: &hostAcceptanceMinWait,
				InterfaceFilter:       problematicNetworkInterfaces,
				IncludeLoopback:       true,
			})

			req.NoError(err)
			req.NotNil(activeAgent)
			defer func() {
				req.NoError(activeAgent.Close())
			}()

			passiveAgentConn, activeAgenConn := connect(t, passiveAgent, activeAgent)
			req.NotNil(passiveAgentConn)
			req.NotNil(activeAgenConn)

			defer func() {
				req.NoError(activeAgenConn.Close())
				req.NoError(passiveAgentConn.Close())
			}()

			pair := passiveAgent.getSelectedPair()
			req.NotNil(pair)
			req.Equal(testCase.selectedPairNetworkType, pair.Local.NetworkType().NetworkShort())

			foo := []byte("foo")
			_, err = passiveAgentConn.Write(foo)
			req.NoError(err)

			buffer := make([]byte, 1024)
			n, err := activeAgenConn.Read(buffer)
			req.NoError(err)
			req.Equal(foo, buffer[:n])

			bar := []byte("bar")
			_, err = activeAgenConn.Write(bar)
			req.NoError(err)

			n, err = passiveAgentConn.Read(buffer)
			req.NoError(err)
			req.Equal(bar, buffer[:n])
		})
	}
}

// Assert that Active TCP connectivity isn't established inside.
// the main thread of the Agent.
func TestActiveTCP_NonBlocking(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 5).Stop()

	cfg := &AgentConfig{
		NetworkTypes:    supportedNetworkTypes(),
		InterfaceFilter: problematicNetworkInterfaces,
	}

	aAgent, err := NewAgent(cfg)
	require.NoError(t, err)

	defer func() {
		require.NoError(t, aAgent.Close())
	}()

	bAgent, err := NewAgent(cfg)
	require.NoError(t, err)

	defer func() {
		require.NoError(t, bAgent.Close())
	}()

	isConnected := make(chan any)
	err = aAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateConnected {
			close(isConnected)
		}
	})
	require.NoError(t, err)

	// Add a invalid ice-tcp candidate to each
	invalidCandidate, err := UnmarshalCandidate("1052353102 1 tcp 1675624447 192.0.2.1 8080 typ host tcptype passive")
	require.NoError(t, err)
	require.NoError(t, aAgent.AddRemoteCandidate(invalidCandidate))
	require.NoError(t, bAgent.AddRemoteCandidate(invalidCandidate))

	connect(t, aAgent, bAgent)

	<-isConnected
}

// Assert that we ignore remote TCP candidates when running a UDP Only Agent.
func TestActiveTCP_Respect_NetworkTypes(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 5).Stop()

	tcpListener, err := net.Listen("tcp", "127.0.0.1:0") // nolint: noctx
	require.NoError(t, err)

	_, port, err := net.SplitHostPort(tcpListener.Addr().String())
	require.NoError(t, err)

	var incomingTCPCount uint64
	go func() {
		for {
			conn, listenErr := tcpListener.Accept()
			if listenErr != nil {
				return
			}

			require.NoError(t, conn.Close())
			atomic.AddUint64(&incomingTCPCount, ^uint64(0))
		}
	}()

	cfg := &AgentConfig{
		NetworkTypes:    []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6, NetworkTypeTCP6},
		InterfaceFilter: problematicNetworkInterfaces,
		IncludeLoopback: true,
	}

	aAgent, err := NewAgent(cfg)
	require.NoError(t, err)

	defer func() {
		require.NoError(t, aAgent.Close())
	}()

	bAgent, err := NewAgent(cfg)
	require.NoError(t, err)

	defer func() {
		require.NoError(t, bAgent.Close())
	}()

	isConnected := make(chan any)
	err = aAgent.OnConnectionStateChange(func(c ConnectionState) {
		if c == ConnectionStateConnected {
			close(isConnected)
		}
	})
	require.NoError(t, err)

	invalidCandidate, err := UnmarshalCandidate(
		fmt.Sprintf("1052353102 1 tcp 1675624447 127.0.0.1 %s typ host tcptype passive", port),
	)
	require.NoError(t, err)
	require.NoError(t, aAgent.AddRemoteCandidate(invalidCandidate))
	require.NoError(t, bAgent.AddRemoteCandidate(invalidCandidate))

	connect(t, aAgent, bAgent)

	<-isConnected
	require.NoError(t, tcpListener.Close())
	require.Equal(t, uint64(0), atomic.LoadUint64(&incomingTCPCount))
}

func TestNewActiveTCPConn_LocalAddrError_EarlyReturn(t *testing.T) {
	defer test.CheckRoutines(t)()

	logger := logging.NewDefaultLoggerFactory().NewLogger("ice")

	// an invalid local address so getTCPAddrOnInterface fails at ResolveTCPAddr.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ra := netip.MustParseAddrPort("127.0.0.1:1")

	a := newActiveTCPConn(ctx, "this_is_not_a_valid_addr", ra, logger)

	require.NotNil(t, a)
	require.True(t, a.closed.Load(), "should be closed on early return error")
	la := a.LocalAddr()
	require.NotNil(t, la)
}

func TestActiveTCPConn_ReadLoop_BufferWriteError(t *testing.T) {
	defer test.CheckRoutines(t)()

	tcpListener, err := net.Listen("tcp", "127.0.0.1:0") // nolint: noctx
	require.NoError(t, err)
	defer func() { _ = tcpListener.Close() }()

	ra := netip.MustParseAddrPort(tcpListener.Addr().String())
	logger := logging.NewDefaultLoggerFactory().NewLogger("ice")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := newActiveTCPConn(ctx, "127.0.0.1:0", ra, logger)
	require.NotNil(t, a)

	srvConn, err := tcpListener.Accept()
	require.NoError(t, err)

	require.NoError(t, a.readBuffer.Close())

	_, err = writeStreamingPacket(srvConn, []byte("ping"))
	require.NoError(t, err)

	require.NoError(t, a.Close())
	require.NoError(t, srvConn.Close())
}

func TestActiveTCPConn_WriteLoop_WriteStreamingError(t *testing.T) {
	defer test.CheckRoutines(t)()

	tcpListener, err := net.Listen("tcp", "127.0.0.1:0") // nolint: noctx
	require.NoError(t, err)
	defer func() { _ = tcpListener.Close() }()

	ra := netip.MustParseAddrPort(tcpListener.Addr().String())
	logger := logging.NewDefaultLoggerFactory().NewLogger("ice")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := newActiveTCPConn(ctx, "127.0.0.1:0", ra, logger)
	require.NotNil(t, a)

	srvConn, err := tcpListener.Accept()
	require.NoError(t, err)

	require.NoError(t, srvConn.Close())

	n, err := a.WriteTo([]byte("data"), nil)
	require.NoError(t, err)
	require.Equal(t, len("data"), n)

	require.NoError(t, a.Close())
}

func TestActiveTCPConn_LocalAddr_DefaultWhenUnset(t *testing.T) {
	defer test.CheckRoutines(t)()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	invalidLocal := "127.0.0.1:65536"
	remote := netip.MustParseAddrPort("127.0.0.1:1")

	log := logging.NewDefaultLoggerFactory().NewLogger("ice")

	a := newActiveTCPConn(ctx, invalidLocal, remote, log)
	require.NotNil(t, a)
	require.True(t, a.closed.Load(), "expected early-return closed state")

	la := a.LocalAddr()
	ta, ok := la.(*net.TCPAddr)
	require.True(t, ok, "LocalAddr() should return *net.TCPAddr")
	require.Nil(t, ta.IP, "fallback *net.TCPAddr should be zero value (nil IP)")
	require.Equal(t, 0, ta.Port, "fallback *net.TCPAddr should be zero value (port 0)")
	require.Equal(t, "", ta.Zone, "fallback *net.TCPAddr should be zero value (empty zone)")
}

func TestActiveTCPConn_SetDeadlines_ReturnEOF(t *testing.T) {
	defer test.CheckRoutines(t)()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	invalidLocal := "127.0.0.1:65536"
	remote := netip.MustParseAddrPort("127.0.0.1:1")
	log := logging.NewDefaultLoggerFactory().NewLogger("ice")

	a := newActiveTCPConn(ctx, invalidLocal, remote, log)
	require.NotNil(t, a)
	require.True(t, a.closed.Load(), "expected early-return closed state")

	err := a.SetReadDeadline(time.Now())
	require.ErrorIs(t, err, io.EOF)

	err = a.SetWriteDeadline(time.Now())
	require.ErrorIs(t, err, io.EOF)
}

func TestActiveTCPConn_SetDeadlines_WhenConnected(t *testing.T) {
	defer test.CheckRoutines(t)()

	ln, err := net.Listen("tcp", "127.0.0.1:0") // nolint: noctx
	if err != nil {
		t.Skipf("tcp listen not permitted in this environment: %v", err)
	}
	defer func() { _ = ln.Close() }()

	remote := netip.MustParseAddrPort(ln.Addr().String())
	logger := logging.NewDefaultLoggerFactory().NewLogger("ice")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	active := newActiveTCPConn(ctx, "127.0.0.1:0", remote, logger)
	require.NotNil(t, active)

	acceptCh := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr == nil {
			acceptCh <- conn
		}
	}()

	require.Eventually(t, func() bool {
		return active.conn.Load() != nil || active.closed.Load()
	}, 2*time.Second, 10*time.Millisecond)

	connVal := active.conn.Load()
	if connVal == nil {
		t.Skip("tcp dial not permitted in this environment")
	}
	clientConn, ok := connVal.(net.Conn)
	require.True(t, ok)

	readDeadline := time.Now().Add(50 * time.Millisecond)
	writeDeadline := readDeadline.Add(50 * time.Millisecond)
	allDeadline := writeDeadline.Add(50 * time.Millisecond)

	require.NoError(t, active.SetReadDeadline(readDeadline))
	require.NoError(t, active.SetWriteDeadline(writeDeadline))
	require.NoError(t, active.SetDeadline(allDeadline))

	_ = active.Close()
	_ = clientConn.Close()
	select {
	case srvConn := <-acceptCh:
		_ = srvConn.Close()
	default:
	}
}
