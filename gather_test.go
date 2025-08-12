// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

package ice

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/pion/transport/v3/test"
	"github.com/pion/turn/v4"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/proxy"
)

func TestListenUDP(t *testing.T) {
	agent, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	_, localAddrs, err := localInterfaces(
		agent.net,
		agent.interfaceFilter,
		agent.ipFilter,
		[]NetworkType{NetworkTypeUDP4},
		false,
	)
	require.NotEqual(t, len(localAddrs), 0, "localInterfaces found no interfaces, unable to test")
	require.NoError(t, err)

	ip := localAddrs[0].AsSlice()

	conn, err := listenUDPInPortRange(agent.net, agent.log, 0, 0, udp, &net.UDPAddr{IP: ip, Port: 0})
	require.NoError(t, err, "listenUDP error with no port restriction")
	require.NotNil(t, conn, "listenUDP error with no port restriction return a nil conn")

	_, err = listenUDPInPortRange(agent.net, agent.log, 4999, 5000, udp, &net.UDPAddr{IP: ip, Port: 0})
	require.Equal(t, err, ErrPort, "listenUDP with invalid port range did not return ErrPort")

	conn, err = listenUDPInPortRange(agent.net, agent.log, 5000, 5000, udp, &net.UDPAddr{IP: ip, Port: 0})
	require.NoError(t, err, "listenUDP error with no port restriction")
	require.NotNil(t, conn, "listenUDP error with no port restriction return a nil conn")

	_, port, err := net.SplitHostPort(conn.LocalAddr().String())
	require.NoError(t, err)
	require.Equal(t, port, "5000", "listenUDP with port restriction of 5000 listened on incorrect port")

	portMin := 5100
	portMax := 5109
	total := portMax - portMin + 1
	result := make([]int, 0, total)
	portRange := make([]int, 0, total)
	for i := 0; i < total; i++ {
		conn, err = listenUDPInPortRange(agent.net, agent.log, portMax, portMin, udp, &net.UDPAddr{IP: ip, Port: 0})
		require.NoError(t, err, "listenUDP error with no port restriction")
		require.NotNil(t, conn, "listenUDP error with no port restriction return a nil conn")

		_, port, err = net.SplitHostPort(conn.LocalAddr().String())
		require.NoError(t, err)

		p, _ := strconv.Atoi(port)
		require.False(t, p < portMin || p > portMax)
		result = append(result, p)
		portRange = append(portRange, portMin+i)
	}
	require.False(t, sort.IntsAreSorted(result))
	sort.Ints(result)
	require.Equal(t, result, portRange)
	_, err = listenUDPInPortRange(agent.net, agent.log, portMax, portMin, udp, &net.UDPAddr{IP: ip, Port: 0})
	require.Equal(t, err, ErrPort, "listenUDP with port restriction [%d, %d], did not return ErrPort", portMin, portMax)
}

func TestGatherConcurrency(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:    []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
		IncludeLoopback: true,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
	require.NoError(t, agent.OnCandidate(func(Candidate) {
		candidateGatheredFunc()
	}))

	// Testing for panic
	for i := 0; i < 10; i++ {
		_ = agent.GatherCandidates()
	}

	<-candidateGathered.Done()
}

func TestLoopbackCandidate(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()
	type testCase struct {
		name        string
		agentConfig *AgentConfig
		loExpected  bool
	}
	mux, err := NewMultiUDPMuxFromPort(12500)
	require.NoError(t, err)
	muxWithLo, errlo := NewMultiUDPMuxFromPort(12501, UDPMuxFromPortWithLoopback())
	require.NoError(t, errlo)

	unspecConn, errconn := net.ListenPacket("udp", ":0")
	require.NoError(t, errconn)
	defer func() {
		_ = unspecConn.Close()
	}()
	muxUnspecDefault := NewUDPMuxDefault(UDPMuxParams{
		UDPConn: unspecConn,
	})

	testCases := []testCase{
		{
			name: "mux should not have loopback candidate",
			agentConfig: &AgentConfig{
				NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				UDPMux:       mux,
			},
			loExpected: false,
		},
		{
			name: "mux with loopback should not have loopback candidate",
			agentConfig: &AgentConfig{
				NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				UDPMux:       muxWithLo,
			},
			loExpected: true,
		},
		{
			name: "UDPMuxDefault with unspecified IP should not have loopback candidate",
			agentConfig: &AgentConfig{
				NetworkTypes: []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				UDPMux:       muxUnspecDefault,
			},
			loExpected: false,
		},
		{
			name: "UDPMuxDefault with unspecified IP should respect agent includeloopback",
			agentConfig: &AgentConfig{
				NetworkTypes:    []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				UDPMux:          muxUnspecDefault,
				IncludeLoopback: true,
			},
			loExpected: true,
		},
		{
			name: "includeloopback enabled",
			agentConfig: &AgentConfig{
				NetworkTypes:    []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				IncludeLoopback: true,
			},
			loExpected: true,
		},
		{
			name: "includeloopback disabled",
			agentConfig: &AgentConfig{
				NetworkTypes:    []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
				IncludeLoopback: false,
			},
			loExpected: false,
		},
	}

	for _, tc := range testCases {
		tcase := tc
		t.Run(tcase.name, func(t *testing.T) {
			agent, err := NewAgent(tc.agentConfig)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, agent.Close())
			}()

			candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
			var loopback int32
			require.NoError(t, agent.OnCandidate(func(c Candidate) {
				if c != nil {
					if net.ParseIP(c.Address()).IsLoopback() {
						atomic.StoreInt32(&loopback, 1)
					}
				} else {
					candidateGatheredFunc()

					return
				}
				t.Log(c.NetworkType(), c.Priority(), c)
			}))
			require.NoError(t, agent.GatherCandidates())

			<-candidateGathered.Done()

			require.Equal(t, tcase.loExpected, atomic.LoadInt32(&loopback) == 1)
		})
	}

	require.NoError(t, mux.Close())
	require.NoError(t, muxWithLo.Close())
	require.NoError(t, muxUnspecDefault.Close())
}

// Assert that STUN gathering is done concurrently.
func TestSTUNConcurrency(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	serverPort := randomPort(t)
	serverListener, err := net.ListenPacket("udp4", localhostIPStr+":"+strconv.Itoa(serverPort))
	require.NoError(t, err)

	server, err := turn.NewServer(turn.ServerConfig{
		Realm:       "pion.ly",
		AuthHandler: optimisticAuthHandler,
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn:            serverListener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: localhostIPStr},
			},
		},
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, server.Close())
	}()

	urls := []*stun.URI{}
	for i := 0; i <= 10; i++ {
		urls = append(urls, &stun.URI{
			Scheme: stun.SchemeTypeSTUN,
			Host:   localhostIPStr,
			Port:   serverPort + 1,
		})
	}
	urls = append(urls, &stun.URI{
		Scheme: stun.SchemeTypeSTUN,
		Host:   localhostIPStr,
		Port:   serverPort,
	})

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{
		IP: net.IP{127, 0, 0, 1},
	})
	require.NoError(t, err)
	defer func() {
		_ = listener.Close()
	}()

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:   supportedNetworkTypes(),
		Urls:           urls,
		CandidateTypes: []CandidateType{CandidateTypeHost, CandidateTypeServerReflexive},
		TCPMux: NewTCPMuxDefault(
			TCPMuxParams{
				Listener:       listener,
				Logger:         logging.NewDefaultLoggerFactory().NewLogger("ice"),
				ReadBufferSize: 8,
			},
		),
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			candidateGatheredFunc()

			return
		}
		t.Log(c.NetworkType(), c.Priority(), c)
	}))
	require.NoError(t, agent.GatherCandidates())

	<-candidateGathered.Done()
}

// Assert that TURN gathering is done concurrently.
func TestTURNConcurrency(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	runTest := func(
		protocol stun.ProtoType,
		scheme stun.SchemeType,
		packetConn net.PacketConn,
		listener net.Listener,
		serverPort int,
	) {
		packetConnConfigs := []turn.PacketConnConfig{}
		if packetConn != nil {
			packetConnConfigs = append(packetConnConfigs, turn.PacketConnConfig{
				PacketConn:            packetConn,
				RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: localhostIPStr},
			})
		}

		listenerConfigs := []turn.ListenerConfig{}
		if listener != nil {
			listenerConfigs = append(listenerConfigs, turn.ListenerConfig{
				Listener:              listener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: localhostIPStr},
			})
		}

		server, err := turn.NewServer(turn.ServerConfig{
			Realm:             "pion.ly",
			AuthHandler:       optimisticAuthHandler,
			PacketConnConfigs: packetConnConfigs,
			ListenerConfigs:   listenerConfigs,
		})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, server.Close())
		}()

		urls := []*stun.URI{}
		for i := 0; i <= 10; i++ {
			urls = append(urls, &stun.URI{
				Scheme:   scheme,
				Host:     localhostIPStr,
				Username: "username",
				Password: "password",
				Proto:    protocol,
				Port:     serverPort + 1 + i,
			})
		}
		urls = append(urls, &stun.URI{
			Scheme:   scheme,
			Host:     localhostIPStr,
			Username: "username",
			Password: "password",
			Proto:    protocol,
			Port:     serverPort,
		})

		agent, err := NewAgent(&AgentConfig{
			CandidateTypes:     []CandidateType{CandidateTypeRelay},
			InsecureSkipVerify: true,
			NetworkTypes:       supportedNetworkTypes(),
			Urls:               urls,
		})
		require.NoError(t, err)
		defer func() {
			require.NoError(t, agent.Close())
		}()

		candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
		require.NoError(t, agent.OnCandidate(func(c Candidate) {
			if c != nil {
				candidateGatheredFunc()
			}
		}))
		require.NoError(t, agent.GatherCandidates())

		<-candidateGathered.Done()
	}

	t.Run("UDP Relay", func(t *testing.T) {
		serverPort := randomPort(t)
		serverListener, err := net.ListenPacket("udp", localhostIPStr+":"+strconv.Itoa(serverPort))
		require.NoError(t, err)

		runTest(stun.ProtoTypeUDP, stun.SchemeTypeTURN, serverListener, nil, serverPort)
	})

	t.Run("TCP Relay", func(t *testing.T) {
		serverPort := randomPort(t)
		serverListener, err := net.Listen("tcp", localhostIPStr+":"+strconv.Itoa(serverPort))
		require.NoError(t, err)

		runTest(stun.ProtoTypeTCP, stun.SchemeTypeTURN, nil, serverListener, serverPort)
	})

	t.Run("TLS Relay", func(t *testing.T) {
		certificate, genErr := selfsign.GenerateSelfSigned()
		require.NoError(t, genErr)

		serverPort := randomPort(t)
		serverListener, err := tls.Listen("tcp", localhostIPStr+":"+strconv.Itoa(serverPort), &tls.Config{ //nolint:gosec
			Certificates: []tls.Certificate{certificate},
		})
		require.NoError(t, err)

		runTest(stun.ProtoTypeTCP, stun.SchemeTypeTURNS, nil, serverListener, serverPort)
	})

	t.Run("DTLS Relay", func(t *testing.T) {
		certificate, genErr := selfsign.GenerateSelfSigned()
		require.NoError(t, genErr)

		serverPort := randomPort(t)
		serverListener, err := dtls.Listen(
			"udp",
			&net.UDPAddr{IP: net.ParseIP(localhostIPStr), Port: serverPort},
			&dtls.Config{
				Certificates: []tls.Certificate{certificate},
			},
		)
		require.NoError(t, err)

		runTest(stun.ProtoTypeUDP, stun.SchemeTypeTURNS, nil, serverListener, serverPort)
	})
}

// Assert that STUN and TURN gathering are done concurrently.
func TestSTUNTURNConcurrency(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 8).Stop()

	serverPort := randomPort(t)
	serverListener, err := net.ListenPacket("udp4", localhostIPStr+":"+strconv.Itoa(serverPort))
	require.NoError(t, err)

	server, err := turn.NewServer(turn.ServerConfig{
		Realm:       "pion.ly",
		AuthHandler: optimisticAuthHandler,
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn:            serverListener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: localhostIPStr},
			},
		},
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, server.Close())
	}()

	urls := []*stun.URI{}
	for i := 0; i <= 10; i++ {
		urls = append(urls, &stun.URI{
			Scheme: stun.SchemeTypeSTUN,
			Host:   localhostIPStr,
			Port:   serverPort + 1,
		})
	}
	urls = append(urls, &stun.URI{
		Scheme:   stun.SchemeTypeTURN,
		Proto:    stun.ProtoTypeUDP,
		Host:     localhostIPStr,
		Port:     serverPort,
		Username: "username",
		Password: "password",
	})

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:   supportedNetworkTypes(),
		Urls:           urls,
		CandidateTypes: []CandidateType{CandidateTypeServerReflexive, CandidateTypeRelay},
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	{
		// As TURN and STUN should be checked in parallel, this should complete before the default STUN timeout (5s)
		gatherLim := test.TimeOut(time.Second * 3)
		candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
		require.NoError(t, agent.OnCandidate(func(c Candidate) {
			if c != nil {
				candidateGatheredFunc()
			}
		}))
		require.NoError(t, agent.GatherCandidates())

		<-candidateGathered.Done()
		gatherLim.Stop()
	}
}

// Assert that srflx candidates can be gathered from TURN servers
//
// When TURN servers are utilized, both types of candidates
// (i.e. srflx and relay) are obtained from the TURN server.
//
// https://tools.ietf.org/html/rfc5245#section-2.1
func TestTURNSrflx(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	serverPort := randomPort(t)
	serverListener, err := net.ListenPacket("udp4", localhostIPStr+":"+strconv.Itoa(serverPort))
	require.NoError(t, err)

	server, err := turn.NewServer(turn.ServerConfig{
		Realm:       "pion.ly",
		AuthHandler: optimisticAuthHandler,
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn:            serverListener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorNone{Address: localhostIPStr},
			},
		},
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, server.Close())
	}()

	urls := []*stun.URI{{
		Scheme:   stun.SchemeTypeTURN,
		Proto:    stun.ProtoTypeUDP,
		Host:     localhostIPStr,
		Port:     serverPort,
		Username: "username",
		Password: "password",
	}}

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:   supportedNetworkTypes(),
		Urls:           urls,
		CandidateTypes: []CandidateType{CandidateTypeServerReflexive, CandidateTypeRelay},
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c != nil && c.Type() == CandidateTypeServerReflexive {
			candidateGatheredFunc()
		}
	}))

	require.NoError(t, agent.GatherCandidates())

	<-candidateGathered.Done()
}

func TestCloseConnLog(t *testing.T) {
	a, err := NewAgent(&AgentConfig{})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, a.Close())
	}()

	closeConnAndLog(nil, a.log, "normal nil")

	var nc *net.UDPConn
	closeConnAndLog(nc, a.log, "nil ptr")
}

type mockProxy struct {
	proxyWasDialed func()
}

type mockConn struct{}

func (m *mockConn) Read([]byte) (n int, err error)   { return 0, io.EOF }
func (m *mockConn) Write([]byte) (int, error)        { return 0, io.EOF }
func (m *mockConn) Close() error                     { return io.EOF }
func (m *mockConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (m *mockConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (m *mockConn) SetDeadline(time.Time) error      { return io.EOF }
func (m *mockConn) SetReadDeadline(time.Time) error  { return io.EOF }
func (m *mockConn) SetWriteDeadline(time.Time) error { return io.EOF }

func (m *mockProxy) Dial(string, string) (net.Conn, error) {
	m.proxyWasDialed()

	return &mockConn{}, nil
}

func TestTURNProxyDialer(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	proxyWasDialed, proxyWasDialedFunc := context.WithCancel(context.Background())
	proxy.RegisterDialerType("tcp", func(*url.URL, proxy.Dialer) (proxy.Dialer, error) {
		return &mockProxy{proxyWasDialedFunc}, nil
	})

	tcpProxyURI, err := url.Parse("tcp://fakeproxy:3128")
	require.NoError(t, err)

	proxyDialer, err := proxy.FromURL(tcpProxyURI, proxy.Direct)
	require.NoError(t, err)

	agent, err := NewAgent(&AgentConfig{
		CandidateTypes: []CandidateType{CandidateTypeRelay},
		NetworkTypes:   supportedNetworkTypes(),
		Urls: []*stun.URI{
			{
				Scheme:   stun.SchemeTypeTURN,
				Host:     localhostIPStr,
				Username: "username",
				Password: "password",
				Proto:    stun.ProtoTypeTCP,
				Port:     5000,
			},
		},
		ProxyDialer: proxyDialer,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	candidateGatherFinish, candidateGatherFinishFunc := context.WithCancel(context.Background())
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			candidateGatherFinishFunc()
		}
	}))

	require.NoError(t, agent.GatherCandidates())
	<-candidateGatherFinish.Done()
	<-proxyWasDialed.Done()
}

// TestUDPMuxDefaultWithNAT1To1IPsUsage requires that candidates
// are given and connections are valid when using UDPMuxDefault and NAT1To1IPs.
func TestUDPMuxDefaultWithNAT1To1IPsUsage(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	conn, err := net.ListenPacket("udp4", ":0")
	require.NoError(t, err)
	defer func() {
		_ = conn.Close()
	}()

	mux := NewUDPMuxDefault(UDPMuxParams{
		UDPConn: conn,
	})
	defer func() {
		_ = mux.Close()
	}()

	agent, err := NewAgent(&AgentConfig{
		NAT1To1IPs:             []string{"1.2.3.4"},
		NAT1To1IPCandidateType: CandidateTypeHost,
		UDPMux:                 mux,
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	gatherCandidateDone := make(chan struct{})
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(gatherCandidateDone)
		} else {
			require.Equal(t, "1.2.3.4", c.Address())
		}
	}))
	require.NoError(t, agent.GatherCandidates())
	<-gatherCandidateDone

	require.NotEqual(t, 0, len(mux.connsIPv4))
}

// Assert that candidates are given for each mux in a MultiUDPMux.
func TestMultiUDPMuxUsage(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	var expectedPorts []int
	var udpMuxInstances []UDPMux
	for i := 0; i < 3; i++ {
		port := randomPort(t)
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IP{127, 0, 0, 1}, Port: port})
		require.NoError(t, err)
		defer func() {
			_ = conn.Close()
		}()

		expectedPorts = append(expectedPorts, port)
		muxDefault := NewUDPMuxDefault(UDPMuxParams{UDPConn: conn})
		udpMuxInstances = append(udpMuxInstances, muxDefault)
		idx := i
		defer func() {
			_ = udpMuxInstances[idx].Close()
		}()
	}

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:   []NetworkType{NetworkTypeUDP4, NetworkTypeUDP6},
		CandidateTypes: []CandidateType{CandidateTypeHost},
		UDPMux:         NewMultiUDPMuxDefault(udpMuxInstances...),
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	candidateCh := make(chan Candidate)
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(candidateCh)

			return
		}
		candidateCh <- c
	}))
	require.NoError(t, agent.GatherCandidates())

	portFound := make(map[int]bool)
	for c := range candidateCh {
		portFound[c.Port()] = true
		require.True(t, c.NetworkType().IsUDP(), "All candidates should be UDP")
	}
	require.Len(t, portFound, len(expectedPorts))
	for _, port := range expectedPorts {
		require.True(t, portFound[port], "There should be a candidate for each UDP mux port")
	}
}

// Assert that candidates are given for each mux in a MultiTCPMux.
func TestMultiTCPMuxUsage(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	var expectedPorts []int
	var tcpMuxInstances []TCPMux
	for i := 0; i < 3; i++ {
		port := randomPort(t)
		listener, err := net.ListenTCP("tcp", &net.TCPAddr{
			IP:   net.IP{127, 0, 0, 1},
			Port: port,
		})
		require.NoError(t, err)
		defer func() {
			_ = listener.Close()
		}()

		expectedPorts = append(expectedPorts, port)
		tcpMuxInstances = append(tcpMuxInstances, NewTCPMuxDefault(TCPMuxParams{
			Listener:       listener,
			ReadBufferSize: 8,
		}))
	}

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:   supportedNetworkTypes(),
		CandidateTypes: []CandidateType{CandidateTypeHost},
		TCPMux:         NewMultiTCPMuxDefault(tcpMuxInstances...),
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, agent.Close())
	}()

	candidateCh := make(chan Candidate)
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(candidateCh)

			return
		}
		candidateCh <- c
	}))
	require.NoError(t, agent.GatherCandidates())

	portFound := make(map[int]bool)
	for c := range candidateCh {
		activeCandidate := c.Port() == 0
		if c.NetworkType().IsTCP() && !activeCandidate {
			portFound[c.Port()] = true
		}
	}
	require.Len(t, portFound, len(expectedPorts))
	for _, port := range expectedPorts {
		require.True(t, portFound[port], "There should be a candidate for each TCP mux port")
	}
}

// Assert that UniversalUDPMux is used while gathering when configured in the Agent.
func TestUniversalUDPMuxUsage(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IP{127, 0, 0, 1}, Port: randomPort(t)})
	require.NoError(t, err)
	defer func() {
		_ = conn.Close()
	}()

	udpMuxSrflx := &universalUDPMuxMock{
		conn: conn,
	}

	numSTUNS := 3
	urls := []*stun.URI{}
	for i := 0; i < numSTUNS; i++ {
		urls = append(urls, &stun.URI{
			Scheme: SchemeTypeSTUN,
			Host:   localhostIPStr,
			Port:   3478 + i,
		})
	}

	agent, err := NewAgent(&AgentConfig{
		NetworkTypes:   supportedNetworkTypes(),
		Urls:           urls,
		CandidateTypes: []CandidateType{CandidateTypeServerReflexive},
		UDPMuxSrflx:    udpMuxSrflx,
	})
	require.NoError(t, err)
	var aClosed bool
	defer func() {
		if aClosed {
			return
		}
		require.NoError(t, agent.Close())
	}()

	candidateGathered, candidateGatheredFunc := context.WithCancel(context.Background())
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			candidateGatheredFunc()

			return
		}
		t.Log(c.NetworkType(), c.Priority(), c)
	}))
	require.NoError(t, agent.GatherCandidates())

	<-candidateGathered.Done()

	require.NoError(t, agent.Close())
	aClosed = true

	// Twice because of 2 STUN servers configured
	require.Equal(
		t,
		numSTUNS,
		udpMuxSrflx.getXORMappedAddrUsedTimes,
		"expected times that GetXORMappedAddr should be called",
	)
	// One for Restart() when agent has been initialized and one time when Close() the agent
	require.Equal(t, 2, udpMuxSrflx.removeConnByUfragTimes, "expected times that RemoveConnByUfrag should be called")
	// Twice because of 2 STUN servers configured
	require.Equal(t, numSTUNS, udpMuxSrflx.getConnForURLTimes, "expected times that GetConnForURL should be called")
}

// Verify advanced external IP mapper affects host candidates when using UDPMux.
func TestUDPMuxDefaultWithAdvancedMapperUsage(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	conn, err := net.ListenPacket("udp4", ":0")
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	udpMux := NewUDPMuxDefault(UDPMuxParams{UDPConn: conn})
	defer func() { _ = udpMux.Close() }()

	udpPort := conn.LocalAddr().(*net.UDPAddr).Port //nolint:forcetypeassert

	advIP1 := net.ParseIP("192.0.2.1")
	advPort1 := 45678
	advIP2 := net.ParseIP("198.51.100.2")

	agent, err := NewAgent(&AgentConfig{
		CandidateTypes:         []CandidateType{CandidateTypeHost},
		UDPMux:                 udpMux,
		NAT1To1IPCandidateType: CandidateTypeHost,
		HostUDPAdvertisedAddrsMapper: func(_ net.IP) []Endpoint {
			return []Endpoint{{IP: advIP1, Port: advPort1}, {IP: advIP2, Port: 0}}
		},
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	done := make(chan struct{})
	got := make(map[string]int)
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(done)

			return
		}
		t.Log(c.NetworkType(), c.Priority(), c)
		if c.Type() == CandidateTypeHost && c.NetworkType().IsUDP() {
			got[c.Address()] = c.Port()
		}
	}))

	require.NoError(t, agent.GatherCandidates())
	<-done

	require.Equal(t, advPort1, got[advIP1.String()])
	require.Equal(t, udpPort, got[advIP2.String()])
}

func TestUDPMuxDefaultWithAdvancedMapperSkip(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	conn, err := net.ListenPacket("udp4", ":0")
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	udpMux := NewUDPMuxDefault(UDPMuxParams{UDPConn: conn})
	defer func() { _ = udpMux.Close() }()

	agent, err := NewAgent(&AgentConfig{
		CandidateTypes:               []CandidateType{CandidateTypeHost},
		UDPMux:                       udpMux,
		NAT1To1IPCandidateType:       CandidateTypeHost,
		HostUDPAdvertisedAddrsMapper: func(_ net.IP) []Endpoint { return []Endpoint{} },
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	done := make(chan struct{})
	var hostCount int
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(done)

			return
		}
		if c.Type() == CandidateTypeHost && c.NetworkType().IsUDP() {
			hostCount++
		}
	}))

	require.NoError(t, agent.GatherCandidates())
	<-done

	require.Equal(t, 0, hostCount)
}

// Verify gatherCandidatesSrflxMapped handles multiple endpoints and zero-port inheritance.
func TestSrflxAdvancedMapperMultipleEndpoints(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	advIP1 := net.ParseIP("203.0.113.10")
	advPort1 := 55001
	advIP2 := net.ParseIP("203.0.113.11")

	agent, err := NewAgent(&AgentConfig{
		CandidateTypes:         []CandidateType{CandidateTypeServerReflexive},
		NetworkTypes:           []NetworkType{NetworkTypeUDP4},
		NAT1To1IPCandidateType: CandidateTypeServerReflexive,
		HostUDPAdvertisedAddrsMapper: func(_ net.IP) []Endpoint {
			return []Endpoint{
				{IP: advIP1, Port: advPort1},
				{IP: advIP2, Port: 0},
			}
		},
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	done := make(chan struct{})
	var srflx []Candidate
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(done)

			return
		}
		if c.Type() == CandidateTypeServerReflexive {
			srflx = append(srflx, c)
		}
	}))

	require.NoError(t, agent.GatherCandidates())
	<-done

	require.Len(t, srflx, 2)

	m := map[string]Candidate{}
	for _, c := range srflx {
		m[c.Address()] = c
	}

	c1 := m[advIP1.String()]
	require.NotNil(t, c1)
	require.Equal(t, advPort1, c1.Port())
	require.NotNil(t, c1.RelatedAddress())
	require.Greater(t, c1.RelatedAddress().Port, 0)

	c2 := m[advIP2.String()]
	require.NotNil(t, c2)
	require.NotNil(t, c2.RelatedAddress())
	require.Equal(t, c2.RelatedAddress().Port, c2.Port())
}

// Verify that an empty endpoint list yields no srflx candidates.
func TestSrflxAdvancedMapperSkip(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	agent, err := NewAgent(&AgentConfig{
		CandidateTypes:               []CandidateType{CandidateTypeServerReflexive},
		NetworkTypes:                 []NetworkType{NetworkTypeUDP4},
		NAT1To1IPCandidateType:       CandidateTypeServerReflexive,
		HostUDPAdvertisedAddrsMapper: func(_ net.IP) []Endpoint { return []Endpoint{} },
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	done := make(chan struct{})
	var srflxCount int
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(done)

			return
		}
		if c.Type() == CandidateTypeServerReflexive {
			srflxCount++
		}
	}))

	require.NoError(t, agent.GatherCandidates())
	<-done

	require.Equal(t, 0, srflxCount)
}

// Verify gatherCandidatesLocal uses advanced mapper for UDP without UDPMux.
func TestGatherCandidatesLocalUDPAdvancedMapper(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	advIP1 := net.ParseIP("198.18.0.1")
	advPort1 := 40001
	advIP2 := net.ParseIP("198.18.0.2")

	agent, err := NewAgent(&AgentConfig{
		CandidateTypes:         []CandidateType{CandidateTypeHost},
		NetworkTypes:           []NetworkType{NetworkTypeUDP4},
		IncludeLoopback:        true,
		NAT1To1IPCandidateType: CandidateTypeHost,
		HostUDPAdvertisedAddrsMapper: func(_ net.IP) []Endpoint {
			return []Endpoint{
				{IP: advIP1, Port: advPort1},
				{IP: advIP2, Port: 0},
			}
		},
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	done := make(chan struct{})
	got := make(map[string]int)
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(done)

			return
		}
		if c.Type() == CandidateTypeHost && c.NetworkType().IsUDP() {
			got[c.Address()] = c.Port()
		}
	}))

	require.NoError(t, agent.GatherCandidates())
	<-done

	require.Equal(t, advPort1, got[advIP1.String()])
	require.NotZero(t, got[advIP2.String()])
}

// Verify gatherCandidatesLocal uses advanced mapper for TCP via TCPMux.
func TestGatherCandidatesLocalTCPAdvancedMapper(t *testing.T) {
	defer test.CheckRoutines(t)()

	defer test.TimeOut(time.Second * 30).Stop()

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: 0})
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()

	tcpPort := listener.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert
	tcpMux := NewTCPMuxDefault(TCPMuxParams{Listener: listener, ReadBufferSize: 8})

	advIP1 := net.ParseIP("198.51.100.10")
	advPort1 := 41001
	advIP2 := net.ParseIP("198.51.100.11")

	agent, err := NewAgent(&AgentConfig{
		CandidateTypes:         []CandidateType{CandidateTypeHost},
		NetworkTypes:           []NetworkType{NetworkTypeTCP4},
		IncludeLoopback:        true,
		TCPMux:                 tcpMux,
		NAT1To1IPCandidateType: CandidateTypeHost,
		HostTCPAdvertisedAddrsMapper: func(_ net.IP) []Endpoint {
			return []Endpoint{
				{IP: advIP1, Port: advPort1},
				{IP: advIP2, Port: 0},
			}
		},
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	done := make(chan struct{})
	got := make(map[string]int)
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(done)

			return
		}
		if c.Type() == CandidateTypeHost && c.NetworkType().IsTCP() && c.TCPType() == TCPTypePassive {
			got[c.Address()] = c.Port()
		}
	}))

	require.NoError(t, agent.GatherCandidates())
	<-done

	require.Equal(t, advPort1, got[advIP1.String()])
	require.Equal(t, tcpPort, got[advIP2.String()])
}

// Multiple advertised UDP endpoints sharing the same port but different IPs should yield multiple candidates.
func TestUDPMuxAdvancedMapperSamePortDifferentIPs(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	conn, err := net.ListenPacket("udp4", ":0")
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	udpMux := NewUDPMuxDefault(UDPMuxParams{UDPConn: conn})
	defer func() { _ = udpMux.Close() }()

	samePort := 46000
	ipA := net.ParseIP("198.51.100.50")
	ipB := net.ParseIP("198.51.100.51")

	agent, err := NewAgent(&AgentConfig{
		CandidateTypes:         []CandidateType{CandidateTypeHost},
		UDPMux:                 udpMux,
		NAT1To1IPCandidateType: CandidateTypeHost,
		HostUDPAdvertisedAddrsMapper: func(_ net.IP) []Endpoint {
			return []Endpoint{
				{IP: ipA, Port: samePort},
				{IP: ipB, Port: samePort},
			}
		},
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	done := make(chan struct{})
	got := map[string]int{}
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(done)

			return
		}
		if c.Type() == CandidateTypeHost && c.NetworkType().IsUDP() {
			got[c.Address()] = c.Port()
		}
	}))

	require.NoError(t, agent.GatherCandidates())
	<-done

	require.Equal(t, samePort, got[ipA.String()])
	require.Equal(t, samePort, got[ipB.String()])
}

// Multiple advertised UDP endpoints sharing the same IP but different ports should yield multiple candidates.
func TestUDPMuxAdvancedMapperSameIPDifferentPorts(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	conn, err := net.ListenPacket("udp4", ":0")
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	udpMux := NewUDPMuxDefault(UDPMuxParams{UDPConn: conn})
	defer func() { _ = udpMux.Close() }()

	ip := net.ParseIP("198.51.100.60")
	portA := 47001
	portB := 47002

	agent, err := NewAgent(&AgentConfig{
		CandidateTypes:         []CandidateType{CandidateTypeHost},
		UDPMux:                 udpMux,
		NAT1To1IPCandidateType: CandidateTypeHost,
		HostUDPAdvertisedAddrsMapper: func(_ net.IP) []Endpoint {
			return []Endpoint{
				{IP: ip, Port: portA},
				{IP: ip, Port: portB},
			}
		},
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	done := make(chan struct{})
	got := map[string]map[int]bool{}
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(done)

			return
		}
		if c.Type() == CandidateTypeHost && c.NetworkType().IsUDP() {
			if _, ok := got[c.Address()]; !ok {
				got[c.Address()] = map[int]bool{}
			}
			got[c.Address()][c.Port()] = true
		}
	}))

	require.NoError(t, agent.GatherCandidates())
	<-done

	ports := got[ip.String()]
	require.True(t, ports[portA])
	require.True(t, ports[portB])
}

// Mirror UDPMux tests for TCP using TCPMux.
func TestTCPMuxAdvancedMapperSamePortDifferentIPs(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: 0})
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()

	tcpMux := NewTCPMuxDefault(TCPMuxParams{Listener: listener, ReadBufferSize: 8})

	samePort := 48000
	ipA := net.ParseIP("203.0.113.50")
	ipB := net.ParseIP("203.0.113.51")

	agent, err := NewAgent(&AgentConfig{
		CandidateTypes:         []CandidateType{CandidateTypeHost},
		NetworkTypes:           []NetworkType{NetworkTypeTCP4},
		IncludeLoopback:        true,
		TCPMux:                 tcpMux,
		NAT1To1IPCandidateType: CandidateTypeHost,
		HostTCPAdvertisedAddrsMapper: func(_ net.IP) []Endpoint {
			return []Endpoint{
				{IP: ipA, Port: samePort},
				{IP: ipB, Port: samePort},
			}
		},
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	done := make(chan struct{})
	got := map[string]int{}
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(done)

			return
		}
		if c.Type() == CandidateTypeHost && c.NetworkType().IsTCP() && c.TCPType() == TCPTypePassive {
			got[c.Address()] = c.Port()
		}
	}))

	require.NoError(t, agent.GatherCandidates())
	<-done

	require.Equal(t, samePort, got[ipA.String()])
	require.Equal(t, samePort, got[ipB.String()])
}

func TestTCPMuxAdvancedMapperSameIPDifferentPorts(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: 0})
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()

	tcpMux := NewTCPMuxDefault(TCPMuxParams{Listener: listener, ReadBufferSize: 8})

	ip := net.ParseIP("203.0.113.60")
	portA := 48001
	portB := 48002

	agent, err := NewAgent(&AgentConfig{
		CandidateTypes:         []CandidateType{CandidateTypeHost},
		NetworkTypes:           []NetworkType{NetworkTypeTCP4},
		IncludeLoopback:        true,
		TCPMux:                 tcpMux,
		NAT1To1IPCandidateType: CandidateTypeHost,
		HostTCPAdvertisedAddrsMapper: func(_ net.IP) []Endpoint {
			return []Endpoint{
				{IP: ip, Port: portA},
				{IP: ip, Port: portB},
			}
		},
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, agent.Close()) }()

	done := make(chan struct{})
	got := map[string]map[int]bool{}
	require.NoError(t, agent.OnCandidate(func(c Candidate) {
		if c == nil {
			close(done)

			return
		}
		if c.Type() == CandidateTypeHost && c.NetworkType().IsTCP() && c.TCPType() == TCPTypePassive {
			if _, ok := got[c.Address()]; !ok {
				got[c.Address()] = map[int]bool{}
			}
			got[c.Address()][c.Port()] = true
		}
	}))

	require.NoError(t, agent.GatherCandidates())
	<-done

	ports := got[ip.String()]
	require.True(t, ports[portA])
	require.True(t, ports[portB])
	require.True(t, len(got) == 1)
}

// Validate that control messages (STUN Binding Requests) can be sent from each advertised candidate
// sharing the same PacketConn and reach the peer without error.
func TestUDPMuxAdvancedMapperPerCandidateControlMessages(t *testing.T) {
	defer test.CheckRoutines(t)()
	defer test.TimeOut(time.Second * 30).Stop()

	bPC, err := net.ListenPacket("udp4", ":0")
	require.NoError(t, err)
	defer func() { _ = bPC.Close() }()
	bMux := NewUDPMuxDefault(UDPMuxParams{UDPConn: bPC})
	defer func() { _ = bMux.Close() }()

	advA := net.ParseIP("198.51.100.80")
	advB := net.ParseIP("198.51.100.81")
	portA := 49101
	portB := 49102

	aAgent, err := NewAgent(&AgentConfig{
		CandidateTypes:  []CandidateType{CandidateTypeHost},
		NetworkTypes:    []NetworkType{NetworkTypeUDP4},
		IncludeLoopback: true,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, aAgent.Close()) }()

	bAgent, err := NewAgent(&AgentConfig{
		CandidateTypes:         []CandidateType{CandidateTypeHost},
		NetworkTypes:           []NetworkType{NetworkTypeUDP4},
		IncludeLoopback:        true,
		UDPMux:                 bMux,
		NAT1To1IPCandidateType: CandidateTypeHost,
		HostUDPAdvertisedAddrsMapper: func(_ net.IP) []Endpoint {
			return []Endpoint{{IP: advA, Port: portA}, {IP: advB, Port: portB}}
		},
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, bAgent.Close()) }()

	_, _ = connect(t, aAgent, bAgent)

	msg, err := stun.Build(stun.BindingRequest, stun.TransactionID,
		stun.NewUsername(aAgent.localUfrag+":"+aAgent.remoteUfrag),
		UseCandidate(),
		stun.NewShortTermIntegrity(aAgent.localPwd),
		stun.Fingerprint,
	)
	require.NoError(t, err)

	bcands, err := bAgent.GetLocalCandidates()
	require.NoError(t, err)
	var used int
	for _, c := range bcands {
		if c.Type() != CandidateTypeHost || !c.NetworkType().IsUDP() {
			continue
		}
		if c.Address() != advA.String() && c.Address() != advB.String() {
			continue
		}
		_, err = c.writeTo(msg.Raw, bAgent.getSelectedPair().Remote)
		require.NoError(t, err)
		used++
	}
	require.Equal(t, 2, used)
}

type universalUDPMuxMock struct {
	UDPMux
	getXORMappedAddrUsedTimes int
	removeConnByUfragTimes    int
	getConnForURLTimes        int
	mu                        sync.Mutex
	conn                      *net.UDPConn
}

func (m *universalUDPMuxMock) GetRelayedAddr(net.Addr, time.Duration) (*net.Addr, error) {
	return nil, errNotImplemented
}

func (m *universalUDPMuxMock) GetConnForURL(string, string, net.Addr) (net.PacketConn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getConnForURLTimes++

	return m.conn, nil
}

func (m *universalUDPMuxMock) GetXORMappedAddr(net.Addr, time.Duration) (*stun.XORMappedAddress, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getXORMappedAddrUsedTimes++

	return &stun.XORMappedAddress{IP: net.IP{100, 64, 0, 1}, Port: 77878}, nil
}

func (m *universalUDPMuxMock) RemoveConnByUfrag(string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeConnByUfragTimes++
}

func (m *universalUDPMuxMock) GetListenAddresses() []net.Addr {
	return []net.Addr{m.conn.LocalAddr()}
}
